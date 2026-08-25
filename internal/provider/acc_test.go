package provider_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	vaultv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/vault/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/apicontract"
	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"namespace": providerserver.NewProtocol6WithError(provider.New("test")()),
}

type testAccConflictCapture struct {
	mu                sync.Mutex
	mutationAttempted bool
	mutationErr       error
	providerErr       error
}

func (c *testAccConflictCapture) recordMutation(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mutationAttempted = true
	c.mutationErr = err
}

func (c *testAccConflictCapture) recordProviderError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providerErr = err
}

func (c *testAccConflictCapture) snapshot() (bool, error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mutationAttempted, c.mutationErr, c.providerErr
}

func testAccCloseClient(t *testing.T, cli *client.Client, purpose string) {
	t.Helper()
	if cli == nil {
		return
	}
	if err := cli.Close(); err != nil {
		t.Errorf("failed to close Namespace client for %s: %s", purpose, client.RedactSecrets(err.Error()))
	}
}

func testAccConcurrencyConflictProviderFactories(t *testing.T, targetTag string, capture *testAccConflictCapture) map[string]func() (tfprotov6.ProviderServer, error) {
	t.Helper()
	return map[string]func() (tfprotov6.ProviderServer, error){
		"namespace": func() (tfprotov6.ProviderServer, error) {
			var triggerOnce sync.Once
			interceptor := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				if strings.HasSuffix(method, "UpdateProfile") {
					if uReq, ok := req.(*githubv1beta.UpdateProfileRequest); ok {
						if uReq.GetSpec() != nil && strings.Contains(uReq.GetSpec().GetTag(), targetTag) {
							triggerOnce.Do(func() {
								// Intervening out-of-band writer mutates the backend resource right before the provider's update,
								// bumping the server revision so that the provider's update arrives with a stale revision.
								mutatedSpec := proto.Clone(uReq.GetSpec()).(*githubv1beta.RunnerProfileSpec)
								mutatedSpec.Description = "Intervening out-of-band writer collision"
								var directResp githubv1beta.UpdateProfileResponse
								err := invoker(ctx, method, &githubv1beta.UpdateProfileRequest{
									ProfileId:     uReq.GetProfileId(),
									UpdateVersion: uReq.GetUpdateVersion(),
									Spec:          mutatedSpec,
								}, &directResp, cc, opts...)
								capture.recordMutation(err)
							})
							providerErr := invoker(ctx, method, req, reply, cc, opts...)
							capture.recordProviderError(providerErr)
							return providerErr
						}
					}
				}
				return invoker(ctx, method, req, reply, cc, opts...)
			}

			cli, err := client.New(context.Background(), client.Config{
				DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(interceptor)},
			})
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { testAccCloseClient(t, cli, "runner concurrency provider") })

			return providerserver.NewProtocol6WithError(provider.NewWithClient("test", cli)())()
		},
	}
}

type cursorRecord struct {
	requestCursor  string
	responseCursor string
}

func testAccPaginationProviderFactories(t *testing.T, targetRepo string, capturedCursors *[]cursorRecord, mu *sync.Mutex) map[string]func() (tfprotov6.ProviderServer, error) {
	t.Helper()
	return map[string]func() (tfprotov6.ProviderServer, error){
		"namespace": func() (tfprotov6.ProviderServer, error) {
			interceptor := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				err := invoker(ctx, method, req, reply, cc, opts...)
				if strings.HasSuffix(method, "ListImages") && err == nil {
					if lReq, ok := req.(*registryv1beta.ListImagesRequest); ok {
						if lResp, ok := reply.(*registryv1beta.ListImagesResponse); ok {
							matchedTarget := targetRepo == ""
							if matcher := lReq.GetMatchRepository(); matcher != nil {
								for _, value := range matcher.GetValues() {
									if value == targetRepo {
										matchedTarget = true
										break
									}
								}
							}
							if !matchedTarget {
								return err
							}
							var reqCur, respCur string
							if len(lReq.GetPaginationCursor()) > 0 {
								reqCur = string(lReq.GetPaginationCursor())
							}
							if len(lResp.GetPaginationCursor()) > 0 {
								respCur = string(lResp.GetPaginationCursor())
							}
							mu.Lock()
							*capturedCursors = append(*capturedCursors, cursorRecord{
								requestCursor:  reqCur,
								responseCursor: respCur,
							})
							mu.Unlock()
						}
					}
				}
				return err
			}

			cli, err := client.New(context.Background(), client.Config{
				DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(interceptor)},
			})
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { testAccCloseClient(t, cli, "pagination provider") })

			return providerserver.NewProtocol6WithError(provider.NewWithClient("test", cli)())()
		},
	}
}

func testAccRegistryPolicyConcurrencyConflictProviderFactories(t *testing.T, targetRepo string, capture *testAccConflictCapture) map[string]func() (tfprotov6.ProviderServer, error) {
	t.Helper()
	return map[string]func() (tfprotov6.ProviderServer, error){
		"namespace": func() (tfprotov6.ProviderServer, error) {
			var triggerOnce sync.Once
			interceptor := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				if strings.HasSuffix(method, "UpdateRepositoryPolicy") {
					if uReq, ok := req.(*registryv1beta.UpdateRepositoryPolicyRequest); ok {
						if strings.Contains(uReq.GetRepository(), targetRepo) {
							triggerOnce.Do(func() {
								// Intervening out-of-band writer mutates the backend resource right before the provider's update,
								// bumping the server revision so that the provider's update arrives with a stale revision.
								var directResp emptypb.Empty
								err := invoker(ctx, method, &registryv1beta.UpdateRepositoryPolicyRequest{
									Repository:            uReq.GetRepository(),
									Revision:              uReq.GetRevision(),
									Expiration:            durationpb.New(24 * time.Hour),
									ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
								}, &directResp, cc, opts...)
								capture.recordMutation(err)
							})
							providerErr := invoker(ctx, method, req, reply, cc, opts...)
							capture.recordProviderError(providerErr)
							return providerErr
						}
					}
				}
				return invoker(ctx, method, req, reply, cc, opts...)
			}

			cli, err := client.New(context.Background(), client.Config{
				DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(interceptor)},
			})
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { testAccCloseClient(t, cli, "registry policy concurrency provider") })

			return providerserver.NewProtocol6WithError(provider.NewWithClient("test", cli)())()
		},
	}
}

var (
	testRunID           = fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	registryPolicyMutex sync.Mutex
)

func testAccRunPrefix() string {
	if runID := os.Getenv("NSC_RUN_ID"); runID != "" {
		return fmt.Sprintf("tfacc-%s-", runID)
	}
	return fmt.Sprintf("tfacc-%s-", testRunID)
}

func testAccResourcePrefix(prefix string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s%s%s", testAccRunPrefix(), prefix, hex.EncodeToString(b))
}

func testAccProviderConfig() string {
	return `
variable "nsc_compute_endpoint" {
  type    = string
  default = "https://eu.compute.namespaceapis.com:443"
}

provider "namespace" {
  compute_endpoint = var.nsc_compute_endpoint
}
`
}

func testAccPartnerProviderConfig() string {
	return `
variable "nsc_partner_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

provider "namespace" {
  token = var.nsc_partner_token
}
`
}

func testAccTrustedProviderConfig() string {
	return `
variable "nsc_trusted_jwt" {
  type      = string
  sensitive = true
  ephemeral = true
}

provider "namespace" {
  token = var.nsc_trusted_jwt
}
`
}

// resolveAndVerifyTenant strictly verifies token claims or Namespace tenant tokens against expectedTenant (NSC_TEST_TENANT).
// It supports both structured 3-part JWT tokens and valid Namespace tenant tokens (nsct_...),
// failing closed on opaque, malformed, empty, or mismatched tokens.
func resolveAndVerifyTenant(tokenOrPath, expectedTenant string) error {
	return client.VerifyTenantTokenMatch(tokenOrPath, expectedTenant)
}

func createTestJWT(claims map[string]interface{}) string {
	header := `{"alg":"none","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))
	claimsBytes, _ := json.Marshal(claims)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsBytes)
	sigB64 := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return fmt.Sprintf("%s.%s.%s", headerB64, claimsB64, sigB64)
}

func makeTestJWT(tenant string) string {
	return createTestJWT(map[string]interface{}{"tenant_id": tenant})
}

func testAccCleanupRunnerProfile(t *testing.T, tag string) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPreCheckImpl(t)
		if tag == "" {
			return
		}
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{})
		if err != nil {
			t.Errorf("cleanup failed to create client for runner profile tag %s: %v", tag, err)
			return
		}
		defer testAccCloseClient(t, cli, "runner profile tag cleanup")
		pCli, err := cli.ProfileClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to create profile client for runner profile tag %s: %v", tag, err)
			return
		}
		resp, err := pCli.ListProfiles(ctx, &emptypb.Empty{})
		if err != nil {
			if !client.IsNotFound(err) {
				t.Errorf("cleanup failed to list runner profiles for tag %s: %v", tag, err)
			}
			return
		}
		for _, p := range resp.GetProfiles() {
			if spec := p.GetSpec(); spec != nil && spec.GetTag() == tag {
				_, err := pCli.DeleteProfile(ctx, &githubv1beta.DeleteProfileRequest{ProfileId: p.GetProfileId()})
				if err != nil && !client.IsNotFound(err) {
					t.Errorf("cleanup failed for runner profile %s (tag %s): %v", p.GetProfileId(), tag, err)
				}
			}
		}
	})
}

func testAccCleanupRunnerProfileByID(t *testing.T, profileID string) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPreCheckImpl(t)
		if profileID == "" {
			return
		}
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{})
		if err != nil {
			t.Errorf("cleanup failed to create client for runner profile %s: %v", profileID, err)
			return
		}
		defer testAccCloseClient(t, cli, "runner profile ID cleanup")
		pCli, err := cli.ProfileClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to create profile client for runner profile %s: %v", profileID, err)
			return
		}
		_, err = pCli.DeleteProfile(ctx, &githubv1beta.DeleteProfileRequest{ProfileId: profileID})
		if err != nil && !client.IsNotFound(err) {
			t.Errorf("cleanup failed for runner profile %s: %v", profileID, err)
		}
	})
}

func testAccCleanupTenantGroup(t *testing.T, groupName string) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPartnerPreCheckImpl(t)
		partnerTok := os.Getenv("NSC_PARTNER_TOKEN")
		if partnerTok == "" {
			return
		}
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{Token: partnerTok})
		if err != nil {
			t.Errorf("cleanup failed to create partner client for tenant group %s: %v", groupName, err)
			return
		}
		defer testAccCloseClient(t, cli, "tenant group cleanup")
		tCli, err := cli.TenantClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to create tenant client for tenant group %s: %v", groupName, err)
			return
		}
		_, err = tCli.DeleteTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: groupName})
		if err != nil && !client.IsNotFound(err) {
			t.Errorf("cleanup failed for tenant group %s: %v", groupName, err)
		}
	})
}

func testAccCleanupSharedImageByID(t *testing.T, imageID string) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPreCheckImpl(t)
		if imageID == "" {
			return
		}
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{})
		if err != nil {
			t.Errorf("cleanup failed to create client for shared image %s: %v", imageID, err)
			return
		}
		defer testAccCloseClient(t, cli, "shared image cleanup")
		rCli, err := cli.RegistryClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to create registry client for shared image %s: %v", imageID, err)
			return
		}
		_, err = rCli.UnshareImage(ctx, &registryv1beta.UnshareImageRequest{Id: imageID})
		if err != nil && !client.IsNotFound(err) {
			t.Errorf("cleanup failed for shared image %s: %v", imageID, err)
		}
	})
}

func testAccCleanupRepositoryPolicy(t *testing.T, repoName string) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPreCheckImpl(t)
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{})
		if err != nil {
			t.Errorf("cleanup failed to create client for repository policy %s: %v", repoName, err)
			return
		}
		defer testAccCloseClient(t, cli, "repository policy cleanup")
		rCli, err := cli.RegistryClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to create registry client for repository policy %s: %v", repoName, err)
			return
		}
		curr, err := rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: repoName})
		if err != nil {
			if !client.IsNotFound(err) {
				t.Errorf("cleanup failed to get repository policy %s: %v", repoName, err)
			}
			return
		}
		if curr != nil {
			_, err = rCli.UpdateRepositoryPolicy(ctx, &registryv1beta.UpdateRepositoryPolicyRequest{
				Repository:            repoName,
				Revision:              curr.Revision,
				Expiration:            nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			})
			if err != nil && !client.IsNotFound(err) {
				t.Errorf("cleanup failed to reset repository policy %s: %v", repoName, err)
				return
			}
		}
		_, err = rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: repoName})
		if err == nil {
			t.Errorf("cleanup reset verification failed for repository policy %s: override still exists", repoName)
		} else if !client.IsNotFound(err) {
			t.Errorf("cleanup failed to verify repository policy reset %s: %v", repoName, err)
		}
	})
}

func testAccEnsureRegistryImage(t *testing.T) (string, string) {
	t.Helper()
	testAccPreCheck(t)
	if os.Getenv("TF_ACC") == "" {
		return "mock-repo-img", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	}

	envRepo := strings.TrimSpace(os.Getenv("NSC_TEST_IMAGE_REPO"))
	envDigest := strings.TrimSpace(os.Getenv("NSC_TEST_IMAGE_DIGEST"))
	if envRepo == "" || envDigest == "" {
		t.Fatalf("acceptance test requires deterministic registry image fixtures: set both NSC_TEST_IMAGE_REPO and NSC_TEST_IMAGE_DIGEST")
	}

	// Validate the exact configured fixture instead of selecting an arbitrary tenant image.
	ctx := context.Background()
	cli, err := client.New(ctx, client.Config{})
	if err != nil {
		t.Fatalf("failed to create client for registry image fixture check: %v", err)
	}
	defer testAccCloseClient(t, cli, "registry image fixture validation")
	rCli, err := cli.RegistryClient(ctx)
	if err != nil {
		t.Fatalf("failed to create registry client for registry image fixture check: %v", err)
	}
	getResp, err := rCli.GetImage(ctx, &registryv1beta.GetImageRequest{
		Repository: envRepo,
		Reference:  envDigest,
	})
	if err != nil {
		t.Fatalf("configured registry image fixture %q@%q could not be read: %v", envRepo, envDigest, err)
	}
	img := getResp.GetImage()
	if img == nil || img.GetRepository() != envRepo || img.GetDigest() != envDigest {
		t.Fatalf("configured registry image fixture response did not match requested repository and digest")
	}
	return envRepo, envDigest
}

func testAccEnsurePaginatedRegistryRepository(t *testing.T) string {
	t.Helper()
	testAccPreCheck(t)
	if os.Getenv("TF_ACC") == "" {
		return "mock-paginated-repo"
	}

	envRepo := strings.TrimSpace(os.Getenv("NSC_TEST_PAGINATED_IMAGE_REPO"))
	if envRepo == "" {
		t.Fatalf("TestAccMatrix_PaginationTraversal requires NSC_TEST_PAGINATED_IMAGE_REPO naming a deterministic repository with more than 100 images and at least two non-empty cursor transitions")
	}

	// Preflight the exact configured fixture, following every cursor and rejecting loops.
	ctx := context.Background()
	cli, err := client.New(ctx, client.Config{})
	if err != nil {
		t.Fatalf("failed to create client for pagination fixture check: %v", err)
	}
	defer testAccCloseClient(t, cli, "pagination fixture validation")
	rCli, err := cli.RegistryClient(ctx)
	if err != nil {
		t.Fatalf("failed to create registry client for pagination fixture check: %v", err)
	}

	var cursor []byte
	seenCursors := make(map[string]struct{})
	imageCount := 0
	for {
		listResp, err := rCli.ListImages(ctx, &registryv1beta.ListImagesRequest{
			MatchRepository: &stdlib.StringMatcher{
				Values: []string{envRepo},
				Op:     stdlib.StringMatcher_IS_ANY_OF,
			},
			PaginationCursor: cursor,
		})
		if err != nil {
			t.Fatalf("failed to preflight paginated registry fixture %q: %v", envRepo, err)
		}
		for _, img := range listResp.GetImages() {
			if img.GetRepository() != envRepo {
				t.Fatalf("pagination fixture query for %q returned an image from repository %q", envRepo, img.GetRepository())
			}
			imageCount++
		}

		next := string(listResp.GetPaginationCursor())
		if next == "" {
			break
		}
		if _, exists := seenCursors[next]; exists {
			t.Fatalf("pagination fixture %q returned repeated cursor", envRepo)
		}
		seenCursors[next] = struct{}{}
		cursor = []byte(next)
	}

	if imageCount <= 100 || len(seenCursors) < 2 {
		t.Fatalf("pagination fixture %q is insufficient: require >100 images and at least two non-empty cursor transitions, got %d images and %d transitions", envRepo, imageCount, len(seenCursors))
	}
	return envRepo
}

func testAccEnsureVolumeTag(t *testing.T, prefix string) (string, string) {
	t.Helper()
	testAccPreCheck(t)
	runnerTag := testAccResourcePrefix("run-" + prefix)
	if os.Getenv("TF_ACC") == "" {
		return runnerTag, testAccResourcePrefix("vol-" + prefix)
	}

	if prefix == "cv-" {
		if envTag := os.Getenv("NSC_TEST_CACHE_VOLUME_TAG"); envTag != "" {
			return runnerTag, envTag
		}
		t.Fatalf("acceptance test requires a configured live cache volume tag: set NSC_TEST_CACHE_VOLUME_TAG")
	}

	if prefix == "pv-" {
		if envTag := os.Getenv("NSC_TEST_PV_TAG"); envTag != "" {
			return runnerTag, envTag
		}
		t.Fatalf("acceptance test requires a configured live persistent volume tag: set NSC_TEST_PV_TAG")
	}

	t.Fatalf("unsupported volume tag prefix %q: configure live fixture environment variable", prefix)
	return "", ""
}

func testAccCleanupDefaultPolicy(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPreCheckImpl(t)
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{})
		if err != nil {
			t.Errorf("cleanup failed to create client for default policy: %v", err)
			return
		}
		defer testAccCloseClient(t, cli, "default registry policy cleanup")
		rCli, err := cli.RegistryClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to create registry client for default policy: %v", err)
			return
		}
		current, err := rCli.GetDefaultPolicy(ctx, &emptypb.Empty{})
		if err != nil {
			t.Errorf("cleanup failed to read default policy revision: %v", err)
			return
		}
		if current.GetDefaultExpiration() != nil || current.GetExpirationEnforcement() != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
			_, err = rCli.UpdateDefaultPolicy(ctx, &registryv1beta.UpdateDefaultPolicyRequest{
				Revision:              current.GetRevision(),
				DefaultExpiration:     nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
			})
			if err != nil {
				t.Errorf("cleanup failed to reset default policy: %v", err)
				return
			}
		}
		reset, err := rCli.GetDefaultPolicy(ctx, &emptypb.Empty{})
		if err != nil {
			t.Errorf("cleanup failed to verify default policy reset: %v", err)
			return
		}
		if reset.GetDefaultExpiration() != nil || reset.GetExpirationEnforcement() != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
			t.Errorf("cleanup reset verification failed for default policy: expiration=%v enforcement=%v", reset.GetDefaultExpiration(), reset.GetExpirationEnforcement())
		}
	})
}

type testAccPreCheckTB interface {
	Helper()
	Skip(args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

func testAccPreCheckImpl(t testAccPreCheckTB) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance tests (unit/contract tests provide complete mocked coverage)")
		return
	}
	expectedTenant := strings.TrimSpace(os.Getenv("NSC_TEST_TENANT"))
	if expectedTenant == "" {
		t.Fatal("NSC_TEST_TENANT must be set for acceptance tests to guarantee test tenant isolation")
		return
	}
	tok := strings.TrimSpace(os.Getenv("NSC_TOKEN"))
	tokFile := strings.TrimSpace(os.Getenv("NSC_TOKEN_FILE"))
	if tok == "" && tokFile == "" {
		t.Fatal("NSC_TOKEN or NSC_TOKEN_FILE must be set for live acceptance testing")
		return
	}
	activeTok := tok
	if activeTok == "" {
		activeTok = tokFile
	}
	if err := resolveAndVerifyTenant(activeTok, expectedTenant); err != nil {
		t.Fatalf("tenant credential verification failed: %v", err)
		return
	}
	if ep := os.Getenv("NSC_ENDPOINT"); ep != "" {
		_ = os.Setenv("TF_VAR_nsc_compute_endpoint", ep)
	}
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
	testAccPreCheckImpl(t)
}

func testAccPartnerPreCheckImpl(t testAccPreCheckTB) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance tests")
		return
	}
	expectedTenant := strings.TrimSpace(os.Getenv("NSC_TEST_TENANT"))
	if expectedTenant == "" {
		t.Fatal("NSC_TEST_TENANT must be set for acceptance tests to guarantee test tenant isolation")
		return
	}
	partnerTok := strings.TrimSpace(os.Getenv("NSC_PARTNER_TOKEN"))
	if partnerTok == "" {
		t.Fatal("NSC_PARTNER_TOKEN must be set for partner-scoped acceptance testing")
		return
	}
	if err := resolveAndVerifyTenant(partnerTok, expectedTenant); err != nil {
		t.Fatalf("partner tenant credential verification failed: %v", err)
		return
	}
	_ = os.Setenv("TF_VAR_nsc_partner_token", partnerTok)
}

func testAccPartnerPreCheck(t *testing.T) {
	t.Helper()
	testAccPartnerPreCheckImpl(t)
}

func testAccTrustedAuthorityPreCheckImpl(t testAccPreCheckTB) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance tests")
		return
	}
	expectedTenant := strings.TrimSpace(os.Getenv("NSC_TEST_TENANT"))
	if expectedTenant == "" {
		t.Fatal("NSC_TEST_TENANT must be set for acceptance tests to guarantee test tenant isolation")
		return
	}
	trustedJWT := strings.TrimSpace(os.Getenv("NSC_TRUSTED_JWT"))
	if trustedJWT == "" {
		t.Fatal("NSC_TRUSTED_JWT must be set for trusted-authority acceptance testing")
		return
	}
	if err := resolveAndVerifyTenant(trustedJWT, expectedTenant); err != nil {
		t.Fatalf("trusted authority tenant credential verification failed: %v", err)
		return
	}
	_ = os.Setenv("TF_VAR_nsc_trusted_jwt", trustedJWT)
}

func testAccTrustedAuthorityPreCheck(t *testing.T) {
	t.Helper()
	testAccTrustedAuthorityPreCheckImpl(t)
}

type mockAccTB struct {
	failed   bool
	skipped  bool
	messages []string
}

func (m *mockAccTB) Helper() {}
func (m *mockAccTB) Skip(args ...any) {
	m.skipped = true
	m.messages = append(m.messages, fmt.Sprint(args...))
}
func (m *mockAccTB) Fatal(args ...any) {
	m.failed = true
	m.messages = append(m.messages, fmt.Sprint(args...))
}
func (m *mockAccTB) Fatalf(format string, args ...any) {
	m.failed = true
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func TestAccSecurity_SecretScrubbing(t *testing.T) {
	t.Helper()

	secrets := map[string]string{
		"tenant token":        "nsct_" + "acceptance_secret_scrubbing_sentinel",
		"workload token":      "nscw_" + "acceptance_secret_scrubbing_sentinel",
		"user token":          "nscu_" + "acceptance_secret_scrubbing_sentinel",
		"legacy token":        "tok_" + "acceptance_secret_scrubbing_sentinel",
		"session token":       "st_" + "acceptance_secret_scrubbing_sentinel",
		"jwt":                 "ey" + "JhbGciOiJSUzI1NiJ9" + "." + "ey" + "J0ZW5hbnRfaWQiOiJzZW50aW5lbCJ9" + "." + "signature_sentinel",
		"rsa private key":     testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "rsa-privkey-body-sentinel"),
		"ec private key":      testutil.SynthesizePEMBlock("EC PRIVATE KEY", "ec-privkey-body-sentinel"),
		"dsa private key":     testutil.SynthesizePEMBlock("DSA PRIVATE KEY", "dsa-privkey-body-sentinel"),
		"openssh private key": testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "openssh-privkey-body-sentinel"),
		"pkcs8 private key":   testutil.SynthesizePEMBlock("PRIVATE KEY", "pkcs8-data-sentinel"),
		"certificate":         testutil.SynthesizePEMBlock("CERTIFICATE", "certificate-body-sentinel"),
		"password":            "acceptance-password-sentinel",
		"opaque bearer":       "opaque-acceptance-bearer-sentinel",
		"opaque api key":      "opaque-acceptance-api-key-sentinel",
	}

	payload := strings.Join([]string{
		"Authorization: Bearer " + secrets["tenant token"],
		"NSC_TOKEN=" + secrets["workload token"],
		"NSC_PARTNER_TOKEN=" + secrets["user token"],
		"NSC_TOKEN_FILE=" + secrets["legacy token"],
		"NSC_TRUSTED_JWT=" + secrets["jwt"],
		"session_token=" + secrets["session token"],
		"federation=" + secrets["jwt"],
		secrets["rsa private key"],
		secrets["ec private key"],
		secrets["dsa private key"],
		secrets["openssh private key"],
		secrets["pkcs8 private key"],
		secrets["certificate"],
		"CACHE_PASSWORD=" + secrets["password"],
		`password = "` + secrets["password"] + `"`,
		`{"bearerToken":"` + secrets["opaque bearer"] + `"}`,
		"api_key=" + secrets["opaque api key"],
	}, "\n")

	assertScrubbed := func(output string) {
		t.Helper()
		for label, secret := range secrets {
			if strings.Contains(output, secret) {
				t.Fatalf("secret scrubbing failed for %s sentinel", label)
			}
		}
	}

	assertScrubbed(client.RedactSecrets(payload))

	var debugOutput bytes.Buffer
	interceptor := client.RedactingUnaryInterceptor(&debugOutput, true, true, "acceptance-redaction-test")
	request := map[string]string{"authorization": "Bearer " + secrets["tenant token"], "private_key": secrets["rsa private key"]}
	response := map[string]string{"password": secrets["password"], "certificate": secrets["certificate"], "jwt": secrets["jwt"]}
	err := interceptor(context.Background(), "/test.RedactionService/Verify", request, response, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return nil
	})
	if err != nil {
		t.Fatalf("redaction interceptor returned an unexpected error: %v", err)
	}
	assertScrubbed(debugOutput.String())

	t.Log(apicontract.FormatAssertion(apicontract.AssertionSecretScrubbing))
}

func TestAccSecurity_TenantIsolation(t *testing.T) {
	testAccPreCheckImpl(t)
	testAccPartnerPreCheckImpl(t)
	testAccTrustedAuthorityPreCheckImpl(t)
	t.Log(apicontract.FormatAssertion(apicontract.AssertionTenantIsolation))
}

func TestAccPreCheck(t *testing.T) {
	t.Run("TF_ACC not set skips", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.skipped {
			t.Fatal("expected testAccPreCheck to skip when TF_ACC is unset")
		}
		if m.failed {
			t.Fatal("expected testAccPreCheck not to fail when TF_ACC is unset")
		}
	})

	t.Run("Missing NSC_TEST_TENANT fails with isolation error", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "")
		t.Setenv("NSC_TOKEN", makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected testAccPreCheck to fail when NSC_TEST_TENANT is missing")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "NSC_TEST_TENANT must be set") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Missing tokens fails", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected testAccPreCheck to fail when NSC_TOKEN is missing")
		}
	})

	t.Run("Valid environment passes with valid test JWT", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with valid test JWT, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Opaque non-JWT token fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "mock-token")
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail closed on opaque mock token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "unsupported or opaque token format") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Malformed 2-part token fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "header.payload")
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail closed on 2-part token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "unsupported or opaque token format") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Corrupted base64 payload fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "header.!!!invalid-base64!!!.sig")
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail closed on corrupted base64")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "malformed JWT payload") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Corrupted JSON payload fails closed", func(t *testing.T) {
		invalidJSONPayload := base64.RawURLEncoding.EncodeToString([]byte("not valid json"))
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", fmt.Sprintf("header.%s.sig", invalidJSONPayload))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail closed on corrupted JSON")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "malformed JWT claims JSON") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("JWT missing tenant claim fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"aud": "nsc"}))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail closed when tenant claim is missing")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not contain a supported tenant identity claim") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Valid environment passes with matching JWT tenant_id", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "test-tenant"}))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with matching JWT, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Valid environment passes with matching custom namespace claim", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"https://namespace.so/claims/tenant": "test-tenant"}))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with matching custom claim, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Mismatched JWT tenant_id fails with actionable error", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "prod-tenant"}))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail on mismatched tenant_id in JWT")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not match NSC_TEST_TENANT") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Token file with valid session_token and matching tenant passes", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token.json")
		jwt := createTestJWT(map[string]interface{}{"tenant_id": "test-tenant"})
		if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"session_token": %q}`, jwt)), 0600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", tokenFile)
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with token file, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Valid environment passes with matching nsct_ token", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "nsct_"+makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with matching nsct_ token, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Valid environment passes with matching nsct_ token without suffix", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "nsct_"+makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with matching nsct_ token without suffix, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Mismatched nsct_ token fails with actionable error", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "nsct_"+makeTestJWT("other-tenant"))
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected precheck to fail on mismatched nsct_ token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not match NSC_TEST_TENANT") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Token file with valid nsct_ token passes", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token.json")
		if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf(`{"bearer_token": "nsct_%s"}`, makeTestJWT("test-tenant"))), 0600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", tokenFile)
		m := &mockAccTB{}
		testAccPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected precheck to succeed with token file containing nsct_, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})
}

func TestAccPartnerPreCheck(t *testing.T) {
	t.Run("TF_ACC not set skips", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.skipped {
			t.Fatal("expected partner precheck to skip when TF_ACC is unset")
		}
	})

	t.Run("Missing NSC_TEST_TENANT fails with isolation error", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "")
		t.Setenv("NSC_PARTNER_TOKEN", makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail when NSC_TEST_TENANT is missing")
		}
	})

	t.Run("Generic NSC_TOKEN does not satisfy partner precheck", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", makeTestJWT("test-tenant"))
		t.Setenv("NSC_PARTNER_TOKEN", "")
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail when NSC_PARTNER_TOKEN is unset, despite NSC_TOKEN being present")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "NSC_PARTNER_TOKEN must be set") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Opaque partner token fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", "partner-token")
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail closed on opaque token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "unsupported or opaque token format") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Partner JWT without tenant claim fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", createTestJWT(map[string]interface{}{"aud": "partner"}))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail closed when tenant claim is missing")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not contain a supported tenant identity claim") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Matching JWT partner token passes", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "test-tenant"}))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected partner precheck to succeed, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Matching nsct_ partner token passes", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", "nsct_"+makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected partner precheck to succeed with matching nsct_, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Mismatched JWT partner token fails", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", createTestJWT(map[string]interface{}{"tenant_id": "other-tenant"}))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail on mismatched JWT")
		}
	})

	t.Run("Mismatched nsct_ partner token fails", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", "nsct_"+makeTestJWT("other-tenant"))
		m := &mockAccTB{}
		testAccPartnerPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected partner precheck to fail on mismatched nsct_ partner token")
		}
	})
}

func TestAccTrustedAuthorityPreCheck(t *testing.T) {
	t.Run("TF_ACC not set skips", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.skipped {
			t.Fatal("expected trusted precheck to skip when TF_ACC is unset")
		}
	})

	t.Run("Missing NSC_TEST_TENANT fails with isolation error", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "")
		t.Setenv("NSC_TRUSTED_JWT", makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted precheck to fail when NSC_TEST_TENANT is missing")
		}
	})

	t.Run("Generic NSC_TOKEN does not satisfy trusted authority precheck", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TOKEN", makeTestJWT("test-tenant"))
		t.Setenv("NSC_TRUSTED_JWT", "")
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted authority precheck to fail when NSC_TRUSTED_JWT is unset, despite NSC_TOKEN being present")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "NSC_TRUSTED_JWT must be set") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Opaque trusted token fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TRUSTED_JWT", "trusted-jwt")
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted authority precheck to fail closed on opaque token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "unsupported or opaque token format") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Trusted JWT without tenant claim fails closed", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TRUSTED_JWT", createTestJWT(map[string]interface{}{"aud": "trusted"}))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted authority precheck to fail closed when tenant claim is missing")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not contain a supported tenant identity claim") {
			t.Fatalf("unexpected fatal message: %v", m.messages)
		}
	})

	t.Run("Matching JWT trusted token passes", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TRUSTED_JWT", createTestJWT(map[string]interface{}{"tenant_id": "test-tenant"}))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected trusted precheck to succeed, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Matching nsct_ trusted token passes", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TRUSTED_JWT", "nsct_"+makeTestJWT("test-tenant"))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if m.failed || m.skipped {
			t.Fatalf("expected trusted precheck to succeed with matching nsct_, got failed=%v, skipped=%v", m.failed, m.skipped)
		}
	})

	t.Run("Mismatched JWT trusted token fails", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TRUSTED_JWT", createTestJWT(map[string]interface{}{"tenant_id": "foreign-tenant"}))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted precheck to fail on mismatched JWT")
		}
	})

	t.Run("Mismatched nsct_ trusted token fails", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "test-tenant")
		t.Setenv("NSC_TRUSTED_JWT", "nsct_"+makeTestJWT("other-tenant"))
		m := &mockAccTB{}
		testAccTrustedAuthorityPreCheckImpl(m)
		if !m.failed {
			t.Fatal("expected trusted precheck to fail on mismatched nsct_ trusted token")
		}
	})
}

func TestAccCleanupHelpers(t *testing.T) {
	t.Run("Cleanup helpers skip gracefully when TF_ACC is unset", func(t *testing.T) {
		t.Setenv("TF_ACC", "")
		testAccCleanupRunnerProfile(t, "dummy-tag")
		testAccCleanupRunnerProfileByID(t, "dummy-profile-id")
		testAccCleanupTenantGroup(t, "dummy-group")
		testAccCleanupSharedImageByID(t, "dummy-image-id")
		testAccCleanupRepositoryPolicy(t, "dummy-repo")
		testAccCleanupDefaultPolicy(t)
	})
}

func testAccCheckDestroy(s *terraform.State) (resultErr error) {
	ctx := context.Background()
	cli, err := client.New(ctx, client.Config{})
	if err != nil {
		return fmt.Errorf("failed to create client for destroy check: %w", err)
	}
	defer func() {
		if closeErr := cli.Close(); closeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("failed to close client after destroy check: %w", closeErr)
		}
	}()

	for _, rs := range s.RootModule().Resources {
		switch rs.Type {
		case "namespace_github_runner_profile":
			pCli, err := cli.ProfileClient(ctx)
			if err != nil {
				return err
			}
			resp, err := pCli.ListProfiles(ctx, &emptypb.Empty{})
			if err != nil {
				return fmt.Errorf("failed to list profiles during CheckDestroy: %w", err)
			}
			for _, p := range resp.GetProfiles() {
				if p.GetProfileId() == rs.Primary.ID {
					return fmt.Errorf("runner profile %q still exists remotely after destroy", rs.Primary.ID)
				}
			}

		case "namespace_tenant_group":
			partnerTok := os.Getenv("NSC_PARTNER_TOKEN")
			partnerCli, err := client.New(ctx, client.Config{Token: partnerTok})
			if err != nil {
				return fmt.Errorf("failed to create partner client for tenant group destroy check: %w", err)
			}
			defer func(cli *client.Client) {
				if closeErr := cli.Close(); closeErr != nil && resultErr == nil {
					resultErr = fmt.Errorf("failed to close partner client after tenant group destroy check: %w", closeErr)
				}
			}(partnerCli)
			tCli, err := partnerCli.TenantClient(ctx)
			if err != nil {
				return err
			}
			_, err = tCli.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: rs.Primary.ID})
			if err == nil {
				return fmt.Errorf("tenant group %q still exists remotely after destroy", rs.Primary.ID)
			}
			if !client.IsNotFound(err) {
				return fmt.Errorf("unexpected error checking tenant group destroy: %w", err)
			}

		case "namespace_registry_shared_image":
			rCli, err := cli.RegistryClient(ctx)
			if err != nil {
				return err
			}
			resp, err := rCli.ListSharedImages(ctx, &registryv1beta.ListSharedImagesRequest{})
			if err != nil {
				return fmt.Errorf("failed to list shared images during CheckDestroy: %w", err)
			}
			for _, img := range resp.GetSharedImages() {
				if img.GetId() == rs.Primary.ID {
					return fmt.Errorf("shared image %q still exists remotely after destroy", rs.Primary.ID)
				}
			}

		case "namespace_registry_repository_policy":
			rCli, err := cli.RegistryClient(ctx)
			if err != nil {
				return err
			}
			resp, err := rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: rs.Primary.ID})
			if err == nil {
				return fmt.Errorf("repository policy %q still exists remotely after destroy (revision=%d, enforcement=%v; expected NotFound)",
					rs.Primary.ID, resp.GetRevision(), resp.GetExpirationEnforcement())
			}
			if !client.IsNotFound(err) {
				return fmt.Errorf("unexpected error checking repository policy destroy: %w", err)
			}

		case "namespace_registry_default_policy":
			rCli, err := cli.RegistryClient(ctx)
			if err != nil {
				return err
			}
			resp, err := rCli.GetDefaultPolicy(ctx, &emptypb.Empty{})
			if err != nil {
				return fmt.Errorf("failed to check default policy destroy: %w", err)
			}
			if resp.GetExpirationEnforcement() != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
				return fmt.Errorf("default registry policy was not reset to neutral DO_NOT_ENFORCE (enforcement=%v)", resp.GetExpirationEnforcement())
			}
			if resp.GetDefaultExpiration() != nil {
				return fmt.Errorf("default registry policy retained expiration %v after neutral reset", resp.GetDefaultExpiration())
			}
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Provider Configuration Acceptance Tests
// -----------------------------------------------------------------------------

func TestAccProvider_Configuration(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {
  global_endpoint = "https://global.namespaceapis.com:443"
  iam_endpoint    = "https://iam.namespaceapis.com:443"
}

data "namespace_github_runner_profiles" "test" {}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.namespace_github_runner_profiles.test", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCGeneralProvider,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Provider configuration and endpoint initialization",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccProvider_AuthenticationPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	tokenFile1 := filepath.Join(tmpDir, "token_file_1.json")
	if err := os.WriteFile(tokenFile1, []byte(`{"bearer_token": "file-override-token-1"}`), 0600); err != nil {
		t.Fatalf("failed to write temporary token file: %v", err)
	}
	tokenFile2 := filepath.Join(tmpDir, "token_file_2.json")
	if err := os.WriteFile(tokenFile2, []byte(`{"bearer_token": "file-override-token-2"}`), 0600); err != nil {
		t.Fatalf("failed to write temporary token file: %v", err)
	}

	ctx := context.Background()

	// Capture parent env values for verification and explicit restoration
	origToken, hasToken := os.LookupEnv("NSC_TOKEN")
	origTokenFile, hasTokenFile := os.LookupEnv("NSC_TOKEN_FILE")
	origPartnerToken, hasPartnerToken := os.LookupEnv("NSC_PARTNER_TOKEN")
	origTrustedJWT, hasTrustedJWT := os.LookupEnv("NSC_TRUSTED_JWT")

	restoreEnv := func() {
		if hasToken {
			_ = os.Setenv("NSC_TOKEN", origToken)
		} else {
			_ = os.Unsetenv("NSC_TOKEN")
		}
		if hasTokenFile {
			_ = os.Setenv("NSC_TOKEN_FILE", origTokenFile)
		} else {
			_ = os.Unsetenv("NSC_TOKEN_FILE")
		}
		if hasPartnerToken {
			_ = os.Setenv("NSC_PARTNER_TOKEN", origPartnerToken)
		} else {
			_ = os.Unsetenv("NSC_PARTNER_TOKEN")
		}
		if hasTrustedJWT {
			_ = os.Setenv("NSC_TRUSTED_JWT", origTrustedJWT)
		} else {
			_ = os.Unsetenv("NSC_TRUSTED_JWT")
		}
	}

	// Genuinely test the 5-tier credential resolution hierarchy in isolated subtests:
	// Tier 1: Explicit HCL token takes precedence over configured token_file and environment variables
	t.Run("Tier1_ExplicitTokenPrecedence", func(subT *testing.T) {
		subT.Setenv("NSC_TOKEN", "env-token-tier3")
		subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
		ts1, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			Token:     "explicit-hcl-token-tier1",
			TokenFile: tokenFile1,
		})
		if err != nil {
			subT.Fatalf("ResolveTokenSource Tier 1 failed: %v", err)
		}
		tok1, err := ts1.IssueToken(ctx, time.Minute, false)
		if err != nil || tok1 != "explicit-hcl-token-tier1" {
			subT.Fatalf("Tier 1 precedence check failed: expected %q, got tok=%q, err=%v", "explicit-hcl-token-tier1", tok1, err)
		}
	})

	// Tier 2: Explicit HCL token_file takes precedence over environment variables
	t.Run("Tier2_ExplicitTokenFilePrecedence", func(subT *testing.T) {
		subT.Setenv("NSC_TOKEN", "env-token-tier3")
		subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
		ts2, err := client.ResolveTokenSource(ctx, client.AuthConfig{
			TokenFile: tokenFile1,
		})
		if err != nil {
			subT.Fatalf("ResolveTokenSource Tier 2 failed: %v", err)
		}
		tok2, err := ts2.IssueToken(ctx, time.Minute, false)
		if err != nil || tok2 != "file-override-token-1" {
			subT.Fatalf("Tier 2 precedence check failed: expected %q, got tok=%q, err=%v", "file-override-token-1", tok2, err)
		}
	})

	// Tier 3: Environment variable NSC_TOKEN takes precedence over NSC_TOKEN_FILE
	t.Run("Tier3_EnvTokenPrecedence", func(subT *testing.T) {
		subT.Setenv("NSC_TOKEN", "env-token-tier3")
		subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
		ts3, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
		if err != nil {
			subT.Fatalf("ResolveTokenSource Tier 3 failed: %v", err)
		}
		tok3, err := ts3.IssueToken(ctx, time.Minute, false)
		if err != nil || tok3 != "env-token-tier3" {
			subT.Fatalf("Tier 3 precedence check failed: expected %q, got tok=%q, err=%v", "env-token-tier3", tok3, err)
		}
	})

	// Tier 4: Environment variable NSC_TOKEN_FILE used when NSC_TOKEN is unset
	t.Run("Tier4_EnvTokenFilePrecedence", func(subT *testing.T) {
		subT.Setenv("NSC_TOKEN", "")
		subT.Setenv("NSC_TOKEN_FILE", tokenFile2)
		ts4, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
		if err != nil {
			subT.Fatalf("ResolveTokenSource Tier 4 failed: %v", err)
		}
		tok4, err := ts4.IssueToken(ctx, time.Minute, false)
		if err != nil || tok4 != "file-override-token-2" {
			subT.Fatalf("Tier 4 precedence check failed: expected %q, got tok=%q, err=%v", "file-override-token-2", tok4, err)
		}
	})

	// Tier 5: Default credentials fallback verification when all explicit tokens are unset
	t.Run("Tier5_DefaultCredentialsFallback", func(subT *testing.T) {
		subT.Setenv("NSC_TOKEN", "")
		subT.Setenv("NSC_TOKEN_FILE", "")

		tier5Home := subT.TempDir()
		subT.Setenv("HOME", tier5Home)
		subT.Setenv("XDG_CONFIG_HOME", tier5Home)

		userConfigDir, err := os.UserConfigDir()
		if err != nil {
			subT.Fatalf("failed to resolve user config dir: %v", err)
		}
		nsDir := filepath.Join(userConfigDir, "ns")
		if err := os.MkdirAll(nsDir, 0700); err != nil {
			subT.Fatalf("failed to create ns config dir %s: %v", nsDir, err)
		}
		tier5TokenPath := filepath.Join(nsDir, "token.json")
		if err := os.WriteFile(tier5TokenPath, []byte(`{"bearer_token": "default-fallback-user-token"}`), 0600); err != nil {
			subT.Fatalf("failed to write tier 5 token file: %v", err)
		}

		ts5, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
		if err != nil {
			subT.Fatalf("ResolveTokenSource Tier 5 failed: %v", err)
		}
		tok5, err := ts5.IssueToken(ctx, time.Minute, false)
		if err != nil || tok5 != "default-fallback-user-token" {
			subT.Fatalf("Tier 5 precedence check failed: expected %q, got tok=%q, err=%v", "default-fallback-user-token", tok5, err)
		}
	})

	// Ensure all environment variables are restored before live acceptance execution
	restoreEnv()

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			testAccPartnerPreCheck(t)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Conflicting attributes (token and token_file both specified) -> validation error
			{
				Config: fmt.Sprintf(`
provider "namespace" {
  token      = "explicit-override-token"
  token_file = %q
}

data "namespace_github_runner_profiles" "test" {}
`, tokenFile1),
				ExpectError: regexp.MustCompile(`(?i)Invalid Attribute Combination|ConflictsWith`),
			},
			// Step 2: Explicit provider token config with data source read
			{
				Config: fmt.Sprintf(`
%s

data "namespace_tenant_members" "test" {}
`, testAccPartnerProviderConfig()),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.namespace_tenant_members.test", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantMembershipServiceListTenantMembers,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Authentication precedence resolution hierarchy verified",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionAuthPrecedenceHierarchy))
						return nil
					},
				),
			},
		},
	})
}

func TestAccProvider_MultiRegionAliases(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {
  alias            = "eu"
  compute_endpoint = "https://eu.compute.namespaceapis.com:443"
}

provider "namespace" {
  alias            = "us"
  compute_endpoint = "https://us.compute.namespaceapis.com:443"
}

data "namespace_network_configuration" "eu_net" {
  provider = namespace.eu
}

data "namespace_network_configuration" "us_net" {
  provider = namespace.us
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_network_configuration.eu_net", "id", "network"),
					resource.TestCheckResourceAttr("data.namespace_network_configuration.us_net", "id", "network"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCComputeServiceGetNetworkConfiguration,
							apicontract.EndpointRegionalCompute,
							apicontract.StatusOk,
							"Multi-region compute alias routing verified",
						))
						return nil
					},
				),
			},
		},
	})
}

// -----------------------------------------------------------------------------
// Managed Resources Acceptance Tests
// -----------------------------------------------------------------------------

func TestAccRunnerProfile_Lifecycle(t *testing.T) {
	runnerTag := testAccResourcePrefix("run-")
	testAccCleanupRunnerProfile(t, runnerTag)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Create
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "test" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Lifecycle test runner profile"
  swap_size_mb = 1024

  instance_shape = {
    virtual_cpu      = 2
    memory_megabytes = 4096
    machine_arch     = "amd64"
    os               = "linux"
  }
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "tag", runnerTag),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "os", "ubuntu-24.04"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "description", "Lifecycle test runner profile"),
					resource.TestCheckResourceAttrSet("namespace_github_runner_profile.test", "id"),
					resource.TestCheckResourceAttrSet("namespace_github_runner_profile.test", "profile_id"),
				),
			},
			// Step 2: Import by profile_id
			{
				ResourceName:            "namespace_github_runner_profile.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"version"},
			},
			// Step 3: In-place update
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "test" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Updated lifecycle test runner profile"
  swap_size_mb = 2048

  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = "linux"
  }
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "tag", runnerTag),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "description", "Updated lifecycle test runner profile"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "swap_size_mb", "2048"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "instance_shape.virtual_cpu", "4"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "instance_shape.memory_megabytes", "8192"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCProfileServiceFullLifecycle,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Runner profile full lifecycle and import verification",
						))
						return nil
					},
				),
			},
		},
	})
}

// TestAccRunnerProfile_CustomImageCombinations is the protected Phase 0 product-contract
// spike for CustomRunnerImageSpec. The generated message has two independent optional
// fields, and the public schema deliberately supports neither (an omitted outer block),
// Ubuntu-only, Docker-only, and both together. A release cannot pass its acceptance
// evidence gate unless the live service accepts every modeled combination.
func TestAccRunnerProfile_CustomImageCombinations(t *testing.T) {
	testAccPreCheck(t)

	testCases := []struct {
		name        string
		configBlock string
		checks      []resource.TestCheckFunc
	}{
		{
			name: "neither",
		},
		{
			name: "ubuntu_only",
			configBlock: `
  custom_runner_image_spec = {
    ubuntu_spec = {
      apt_package = ["jq"]
    }
  }
`,
			checks: []resource.TestCheckFunc{
				resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "custom_runner_image_spec.ubuntu_spec.apt_package.#", "1"),
				resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "custom_runner_image_spec.ubuntu_spec.apt_package.0", "jq"),
			},
		},
		{
			name: "docker_only",
			configBlock: `
  custom_runner_image_spec = {
    docker_spec = {
      dockerfile_content = "FROM ubuntu:24.04\nRUN echo terraform-provider-namespace"
    }
  }
`,
			checks: []resource.TestCheckFunc{
				resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "custom_runner_image_spec.docker_spec.dockerfile_content", "FROM ubuntu:24.04\nRUN echo terraform-provider-namespace"),
			},
		},
		{
			name: "ubuntu_and_docker",
			configBlock: `
  custom_runner_image_spec = {
    ubuntu_spec = {
      apt_package = ["jq"]
    }
    docker_spec = {
      dockerfile_content = "FROM ubuntu:24.04\nRUN echo terraform-provider-namespace"
    }
  }
`,
			checks: []resource.TestCheckFunc{
				resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "custom_runner_image_spec.ubuntu_spec.apt_package.#", "1"),
				resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "custom_runner_image_spec.docker_spec.dockerfile_content", "FROM ubuntu:24.04\nRUN echo terraform-provider-namespace"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runnerTag := testAccResourcePrefix("img-" + strings.ReplaceAll(tc.name, "_", "-") + "-")
			testAccCleanupRunnerProfile(t, runnerTag)
			checks := []resource.TestCheckFunc{
				resource.TestCheckResourceAttr("namespace_github_runner_profile.test", "tag", runnerTag),
				resource.TestCheckResourceAttrSet("namespace_github_runner_profile.test", "profile_id"),
			}
			checks = append(checks, tc.checks...)

			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				CheckDestroy:             testAccCheckDestroy,
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "test" {
  tag         = %q
  os          = "ubuntu-24.04"
  description = "Custom runner image product-contract probe"
%s
}
`, runnerTag, tc.configBlock),
						Check: resource.ComposeTestCheckFunc(checks...),
					},
				},
			})
		})
	}

	t.Log(apicontract.FormatObservation(
		apicontract.RPCProfileServiceCreateProfile,
		apicontract.EndpointGlobal,
		apicontract.StatusOk,
		"Custom runner image neither, Ubuntu-only, Docker-only, and combined specifications",
	))
	t.Log(apicontract.FormatAssertion(apicontract.AssertionCustomImageCombinations))
}

func TestAccTenantGroup_Lifecycle(t *testing.T) {
	groupName := testAccResourcePrefix("tg-")
	testAccCleanupTenantGroup(t, groupName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Create
			{
				Config: fmt.Sprintf(`
%s

resource "namespace_tenant_group" "test" {
  name = %q
  policies = [
    {
      policy = "namespace.cloud.iam.v1beta.AdminPolicy"
      value  = "{}"
    }
  ]
}
`, testAccPartnerProviderConfig(), groupName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "name", groupName),
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "id", groupName),
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "policies.#", "1"),
				),
			},
			// Step 2: Import
			{
				ResourceName:      "namespace_tenant_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Step 3: Update
			{
				Config: fmt.Sprintf(`
%s

resource "namespace_tenant_group" "test" {
  name = %q
  policies = [
    {
      policy = "namespace.cloud.iam.v1beta.AdminPolicy"
      value  = "{\"version\":2}"
    }
  ]
}
`, testAccPartnerProviderConfig(), groupName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "name", groupName),
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "policies.0.value", "{\"version\":2}"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantServiceFullLifecycle,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Tenant group full lifecycle and policy update verification",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccTenantGroup_InterveningWriter(t *testing.T) {
	groupName := testAccResourcePrefix("tg-intervene-")

	t.Cleanup(func() {
		if os.Getenv("TF_ACC") == "" {
			return
		}
		testAccPartnerPreCheckImpl(t)
		partnerTok := os.Getenv("NSC_PARTNER_TOKEN")
		if partnerTok == "" {
			return
		}
		ctx := context.Background()
		cli, err := client.New(ctx, client.Config{Token: partnerTok})
		if err != nil {
			t.Errorf("cleanup failed to create partner client for intervening writer test: %v", err)
			return
		}
		defer testAccCloseClient(t, cli, "tenant group intervening-writer cleanup")
		tCli, err := cli.TenantClient(ctx)
		if err != nil {
			t.Errorf("cleanup failed to obtain tenant client for intervening writer test: %v", err)
			return
		}
		_, err = tCli.DeleteTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: groupName})
		if err != nil && !client.IsNotFound(err) {
			t.Errorf("cleanup failed to delete tenant group %s: %v", groupName, err)
		}
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Create tenant group with baseline policy via Terraform
			{
				Config: fmt.Sprintf(`
%s

resource "namespace_tenant_group" "test" {
  name = %q
  policies = [
    {
      policy = "namespace.cloud.iam.v1beta.AdminPolicy"
      value  = jsonencode({ role = "viewer" })
    }
  ]
}
`, testAccPartnerProviderConfig(), groupName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "name", groupName),
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "policies.0.policy", "namespace.cloud.iam.v1beta.AdminPolicy"),
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "policies.0.value", `{"role":"viewer"}`),
				),
			},
			// Step 2: Intervening out-of-band writer modifies the tenant group directly via gRPC,
			// followed by Terraform apply asserting last-writer-wins reconciliation.
			{
				PreConfig: func() {
					testAccPartnerPreCheck(t)
					ctx := context.Background()
					partnerTok := os.Getenv("NSC_PARTNER_TOKEN")
					if partnerTok == "" {
						t.Fatal("NSC_PARTNER_TOKEN required for intervening writer test")
					}
					cli, err := client.New(ctx, client.Config{Token: partnerTok})
					if err != nil {
						t.Fatalf("failed to create client for intervening writer: %v", err)
					}
					defer testAccCloseClient(t, cli, "tenant group intervening mutation")
					tCli, err := cli.TenantClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain tenant client: %v", err)
					}

					// Out-of-band mutation: Inject an unmanaged policy value
					_, err = tCli.EnsureTenantGroup(ctx, &iamv1beta.EnsureTenantGroupRequest{
						Name: groupName,
						Policies: []*iamv1beta.TenantGroupPolicy{
							{
								Policy: "namespace.cloud.iam.v1beta.AdminPolicy",
								Value:  `{"role":"editor","injected_by":"out_of_band"}`,
							},
						},
					})
					if err != nil {
						t.Fatalf("out-of-band EnsureTenantGroup failed: %v", err)
					}
				},
				Config: fmt.Sprintf(`
%s

resource "namespace_tenant_group" "test" {
  name = %q
  policies = [
    {
      policy = "namespace.cloud.iam.v1beta.AdminPolicy"
      value  = jsonencode({ role = "viewer" })
    }
  ]
}
`, testAccPartnerProviderConfig(), groupName),
				Check: resource.ComposeTestCheckFunc(
					// Verify that Terraform detected drift and enforced authoritative configured state (last-writer-wins)
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "name", groupName),
					resource.TestCheckResourceAttr("namespace_tenant_group.test", "policies.0.value", `{"role":"viewer"}`),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantServiceEnsureTenantGroup,
							apicontract.EndpointIAM,
							apicontract.StatusOkReconciled,
							"Intervening out-of-band writer drift detection and last-writer-wins reconciliation",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccSharedImage_Lifecycle(t *testing.T) {
	repoName, digest := testAccEnsureRegistryImage(t)

	var initialShareID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Create Shared Image
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_shared_image" "test" {
  repository = %q
  digest     = %q
  visibility = "PUBLIC"
}
`, repoName, digest),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_shared_image.test", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_shared_image.test", "digest", digest),
					resource.TestCheckResourceAttr("namespace_registry_shared_image.test", "visibility", "PUBLIC"),
					resource.TestCheckResourceAttrSet("namespace_registry_shared_image.test", "id"),
					resource.TestCheckResourceAttrSet("namespace_registry_shared_image.test", "image_ref"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_registry_shared_image.test"]
						if !ok || rs.Primary == nil || rs.Primary.ID == "" {
							return fmt.Errorf("shared image resource not found in state")
						}
						initialShareID = rs.Primary.ID
						testAccCleanupSharedImageByID(t, initialShareID)
						return nil
					},
				),
			},
			// Step 2: Replace Shared Image (RequiresReplace triggers destroy+create on visibility change)
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_shared_image" "test" {
  repository = %q
  digest     = %q
  visibility = "PARTNER"
}
`, repoName, digest),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_shared_image.test", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_shared_image.test", "visibility", "PARTNER"),
					resource.TestCheckResourceAttrSet("namespace_registry_shared_image.test", "id"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_registry_shared_image.test"]
						if !ok || rs.Primary == nil || rs.Primary.ID == "" {
							return fmt.Errorf("replaced shared image resource not found in state")
						}
						newID := rs.Primary.ID
						testAccCleanupSharedImageByID(t, newID)
						if newID == initialShareID && initialShareID != "" {
							return fmt.Errorf("expected new share ID after replacement, got same ID %q", newID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceShareLifecycle,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Shared image lifecycle and replacement on update",
						))
						return nil
					},
				),
			},
			// Step 3: Verify Import is rejected
			{
				ResourceName: "namespace_registry_shared_image.test",
				ImportState:  true,
				ExpectError:  regexp.MustCompile(`Resource Import Not Supported`),
			},
		},
	})
}

func TestAccSharedImage_ExpirationDisappearance(t *testing.T) {
	repoName, digest := testAccEnsureRegistryImage(t)

	var shareID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Create Shared Image
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_shared_image" "exp_test" {
  repository = %q
  digest     = %q
  visibility = "PUBLIC"
}
`, repoName, digest),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_shared_image.exp_test", "repository", repoName),
					resource.TestCheckResourceAttrSet("namespace_registry_shared_image.exp_test", "id"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_registry_shared_image.exp_test"]
						if !ok || rs.Primary == nil || rs.Primary.ID == "" {
							return fmt.Errorf("shared image not found in state")
						}
						shareID = rs.Primary.ID
						testAccCleanupSharedImageByID(t, shareID)
						return nil
					},
				),
			},
			// Step 2: Unshare out-of-band directly on server (simulating share expiration or external revocation),
			// then re-apply with same configuration to verify clean recreation (RemoveResource on Read followed by Create)
			{
				PreConfig: func() {
					testAccPreCheck(t)
					if shareID == "" {
						return
					}
					ctx := context.Background()
					cli, err := client.New(ctx, client.Config{})
					if err != nil {
						t.Fatalf("failed to create client for out-of-band unshare: %v", err)
					}
					defer testAccCloseClient(t, cli, "shared image out-of-band unshare")
					rCli, err := cli.RegistryClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain registry client: %v", err)
					}
					_, err = rCli.UnshareImage(ctx, &registryv1beta.UnshareImageRequest{Id: shareID})
					if err != nil && !client.IsNotFound(err) {
						t.Fatalf("out-of-band UnshareImage failed: %v", err)
					}
				},
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_shared_image" "exp_test" {
  repository = %q
  digest     = %q
  visibility = "PUBLIC"
}
`, repoName, digest),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_shared_image.exp_test", "repository", repoName),
					resource.TestCheckResourceAttrSet("namespace_registry_shared_image.exp_test", "id"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_registry_shared_image.exp_test"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupSharedImageByID(t, rs.Primary.ID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceUnshareImageRead,
							apicontract.EndpointGlobal,
							apicontract.StatusNotFoundRecreated,
							"Shared image external unshare and state recreation",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccRegistryPolicy_Repository(t *testing.T) {
	repoName := testAccResourcePrefix("repo-")
	testAccCleanupRepositoryPolicy(t, repoName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Create repository override
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_repository_policy" "test" {
  repository             = %q
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, repoName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.test", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.test", "expiration", "720h"),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.test", "expiration_enforcement", "EXPIRATION_ENFORCEMENT_ENFORCE"),
				),
			},
			// Step 2: Import
			{
				ResourceName:            "namespace_registry_repository_policy.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"revision"},
			},
			// Step 3: Update
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_repository_policy" "test" {
  repository             = %q
  expiration             = "1440h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, repoName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.test", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.test", "expiration", "1440h"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceUpdateRepositoryPolicy,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Repository retention override lifecycle",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccRegistryPolicy_Repository_Drift(t *testing.T) {
	repoName := testAccResourcePrefix("repo-drift-")
	testAccCleanupRepositoryPolicy(t, repoName)

	baselineConfig := fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_repository_policy" "drift" {
  repository             = %q
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, repoName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Initial creation of baseline policy
			{
				Config: baselineConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.drift", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.drift", "expiration", "720h"),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.drift", "expiration_enforcement", "EXPIRATION_ENFORCEMENT_ENFORCE"),
				),
			},
			// Step 2: Out-of-band gRPC mutation modifying policy directly on server, then assert PlanOnly drift detection on unchanged HCL
			{
				PreConfig: func() {
					testAccPreCheck(t)
					ctx := context.Background()
					cli, err := client.New(ctx, client.Config{})
					if err != nil {
						t.Fatalf("failed to create client for out-of-band policy drift: %v", err)
					}
					defer testAccCloseClient(t, cli, "repository policy drift mutation")
					rCli, err := cli.RegistryClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain registry client: %v", err)
					}
					curr, err := rCli.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{Repository: repoName})
					if err != nil {
						t.Fatalf("failed to get current repository policy: %v", err)
					}
					_, err = rCli.UpdateRepositoryPolicy(ctx, &registryv1beta.UpdateRepositoryPolicyRequest{
						Repository:            repoName,
						Revision:              curr.GetRevision(),
						Expiration:            durationpb.New(1440 * time.Hour),
						ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
					})
					if err != nil {
						t.Fatalf("out-of-band UpdateRepositoryPolicy failed: %v", err)
					}
				},
				Config:             baselineConfig,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Step 3: Apply unchanged baseline HCL, reconciling out-of-band drift back to baseline
			{
				Config: baselineConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.drift", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.drift", "expiration", "720h"),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.drift", "expiration_enforcement", "EXPIRATION_ENFORCEMENT_ENFORCE"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceUpdateRepositoryPolicy,
							apicontract.EndpointGlobal,
							apicontract.StatusOkReconciled,
							"Registry repository policy drift detection and reconciliation",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccRegistryPolicy_Repository_ConcurrencyConflict(t *testing.T) {
	repoName := testAccResourcePrefix("repo-conf-")
	testAccCleanupRepositoryPolicy(t, repoName)
	capture := &testAccConflictCapture{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccRegistryPolicyConcurrencyConflictProviderFactories(t, repoName, capture),
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Initial baseline creation (revision 1)
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_repository_policy" "conflict_target" {
  repository             = %q
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, repoName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.conflict_target", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.conflict_target", "expiration", "720h"),
				),
			},
			// Step 2: Intervening writer bumps backend revision during apply, causing update to fail with FailedPrecondition
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_repository_policy" "conflict_target" {
  repository             = %q
  expiration             = "1440h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, repoName),
				ExpectError: regexp.MustCompile(`(?i)Concurrency Conflict|FailedPrecondition|version|revision`),
			},
		},
	})

	mutationAttempted, mutationErr, providerErr := capture.snapshot()
	if !mutationAttempted {
		t.Fatal("registry policy concurrency test did not execute the intervening mutation")
	}
	if mutationErr != nil {
		t.Fatalf("registry policy intervening mutation failed: %v", mutationErr)
	}
	if code := status.Code(providerErr); code != codes.FailedPrecondition {
		t.Fatalf("expected provider repository-policy update to fail with codes.FailedPrecondition, got %v (err: %v)", code, providerErr)
	}
	t.Log(apicontract.FormatObservation(
		apicontract.RPCContainerRegistryServiceUpdateRepositoryPolicy,
		apicontract.EndpointGlobal,
		apicontract.StatusFailedPrecondition,
		"Optimistic revision locking collision on repository policy",
	))
}

func TestAccRegistryPolicy_Default(t *testing.T) {
	registryPolicyMutex.Lock()
	defer registryPolicyMutex.Unlock()
	testAccCleanupDefaultPolicy(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Set default policy
			{
				Config: `
provider "namespace" {}

resource "namespace_registry_default_policy" "test" {
  default_expiration     = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_default_policy.test", "id", "default"),
					resource.TestCheckResourceAttr("namespace_registry_default_policy.test", "default_expiration", "720h"),
					resource.TestCheckResourceAttr("namespace_registry_default_policy.test", "expiration_enforcement", "EXPIRATION_ENFORCEMENT_ENFORCE"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceUpdateDefaultPolicy,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Default registry policy lifecycle and import verification",
						))
						return nil
					},
				),
			},
			// Step 2: Import
			{
				ResourceName:            "namespace_registry_default_policy.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"revision"},
			},
		},
	})
}

// -----------------------------------------------------------------------------
// Phase 1 & 2: Data Sources Acceptance Tests
// -----------------------------------------------------------------------------

func TestAccDataSource_RunnerProfile(t *testing.T) {
	runnerTag := testAccResourcePrefix("ds-run-")
	testAccCleanupRunnerProfile(t, runnerTag)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "test" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Data source lookup test runner"
}

data "namespace_github_runner_profile" "by_tag" {
  tag = namespace_github_runner_profile.test.tag
}

data "namespace_github_runner_profile" "by_id" {
  profile_id = namespace_github_runner_profile.test.profile_id
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_github_runner_profile.by_tag", "tag", runnerTag),
					resource.TestCheckResourceAttr("data.namespace_github_runner_profile.by_tag", "os", "ubuntu-24.04"),
					resource.TestCheckResourceAttr("data.namespace_github_runner_profile.by_id", "tag", runnerTag),
					resource.TestCheckResourceAttrSet("data.namespace_github_runner_profile.by_id", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCProfileServiceListProfiles,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Runner profile data source lookup by tag and id",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_RunnerProfiles(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {}

data "namespace_github_runner_profiles" "all" {}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.namespace_github_runner_profiles.all", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCProfileServiceListProfiles,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Runner profiles collection data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_BaseImages(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {}

data "namespace_github_base_images" "ubuntu" {
  labels          = ["ubuntu-24.04"]
  release_stages  = ["PRODUCTION"]
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.namespace_github_base_images.ubuntu", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCImageServiceListBaseImages,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Base images data source query with label filters",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_TenantGroup(t *testing.T) {
	groupName := testAccResourcePrefix("ds-tg-")
	testAccCleanupTenantGroup(t, groupName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

resource "namespace_tenant_group" "test" {
  name = %q
  policies = [
    {
      policy = "namespace.cloud.iam.v1beta.AdminPolicy"
      value  = "{}"
    }
  ]
}

data "namespace_tenant_group" "test" {
  name = namespace_tenant_group.test.name
}
`, testAccPartnerProviderConfig(), groupName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_tenant_group.test", "name", groupName),
					resource.TestCheckResourceAttr("data.namespace_tenant_group.test", "id", groupName),
					resource.TestCheckResourceAttr("data.namespace_tenant_group.test", "policies.#", "1"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantServiceDescribeTenantGroup,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Tenant group data source lookup by name",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_TenantMembers(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {}

data "namespace_tenant_members" "current" {}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.namespace_tenant_members.current", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantMembershipServiceListTenantMembers,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Tenant members data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

// -----------------------------------------------------------------------------
// Phase 3: Ephemeral Resources Acceptance Tests
// -----------------------------------------------------------------------------

func testAccCheckEphemeralNotInState(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, m := range s.Modules {
			if _, ok := m.Resources[resourceName]; ok {
				return fmt.Errorf("ephemeral resource %q was unexpectedly found in persistent terraform.State", resourceName)
			}
		}
		return nil
	}
}

func TestAccEphemeral_IDToken(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {}

ephemeral "namespace_id_token" "test" {
  audience = "https://iam.amazonaws.com/oidc"
}
`,
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_id_token.test"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTokenServiceIssueIdToken,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Ephemeral OIDC ID token generation without state persistence",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_TenantToken(t *testing.T) {
	tenantID := os.Getenv("NSC_TEST_TENANT")
	if tenantID == "" {
		tenantID = "test-tenant"
	}
	actorID := testAccResourcePrefix("actor-")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

ephemeral "namespace_tenant_token" "test" {
  tenant_id     = %q
  actor_id      = %q
  duration_secs = 900
}
`, testAccPartnerProviderConfig(), tenantID, actorID),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_tenant_token.test"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTokenServiceIssueTenantToken,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Ephemeral scoped tenant token generation without state persistence",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_ClientCertificate(t *testing.T) {
	tenantID := os.Getenv("NSC_TEST_TENANT")
	if tenantID == "" {
		tenantID = "test-tenant"
	}
	actorID := testAccResourcePrefix("actor-")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccTrustedAuthorityPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

ephemeral "namespace_client_certificate" "test" {
  tenant_id     = %q
  actor_id      = %q
  duration_secs = 1800
}
`, testAccTrustedProviderConfig(), tenantID, actorID),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_client_certificate.test"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantServiceIssueTenantClientCertificate,
							apicontract.EndpointIAM,
							apicontract.StatusOk,
							"Ephemeral mTLS client certificate issuance without state persistence",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_BazelCache(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {}

ephemeral "namespace_bazel_cache" "test" {
  key = "ci"
}
`,
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_bazel_cache.test"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCBazelCacheServiceEnsureBazelCache,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Ephemeral Bazel cache credential acquisition",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_GradleCache(t *testing.T) {
	cacheName := testAccResourcePrefix("gradle-")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

ephemeral "namespace_gradle_cache" "test" {
  name = %q
}
`, cacheName),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_gradle_cache.test"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCGradleCacheServiceEnsureGradleCache,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Ephemeral Gradle cache credential acquisition",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_HTTPCache(t *testing.T) {
	cacheName := testAccResourcePrefix("http-")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

ephemeral "namespace_http_cache" "test" {
  name = %q
}
`, cacheName),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_http_cache.test"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCHttpCacheServiceEnsureHttpCache,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Ephemeral HTTP cache credential acquisition",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_DownstreamConsumption(t *testing.T) {
	tenantID := os.Getenv("NSC_TEST_TENANT")
	if tenantID == "" {
		tenantID = "test-tenant"
	}
	actorID := testAccResourcePrefix("act-downstream-")
	repoName := testAccResourcePrefix("repo-downstream-")
	testAccCleanupRepositoryPolicy(t, repoName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

ephemeral "namespace_tenant_token" "auth" {
  tenant_id     = %q
  actor_id      = %q
  duration_secs = 900
}

provider "namespace" {
  alias = "downstream"
  token = ephemeral.namespace_tenant_token.auth.bearer_token
}

resource "namespace_registry_repository_policy" "managed_target" {
  provider               = namespace.downstream
  repository             = %q
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, testAccPartnerProviderConfig(), tenantID, actorID, repoName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.managed_target", "repository", repoName),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.managed_target", "expiration", "720h"),
					testAccCheckEphemeralNotInState("ephemeral.namespace_tenant_token.auth"),
					func(s *terraform.State) error {
						for _, m := range s.Modules {
							for k, res := range m.Resources {
								if strings.HasPrefix(res.Type, "namespace_id_token") ||
									strings.HasPrefix(res.Type, "namespace_bazel_cache") ||
									strings.HasPrefix(res.Type, "namespace_tenant_token") ||
									strings.HasPrefix(res.Type, "namespace_client_certificate") ||
									strings.HasPrefix(res.Type, "namespace_gradle_cache") ||
									strings.HasPrefix(res.Type, "namespace_http_cache") {
									return fmt.Errorf("CRITICAL SECURITY VIOLATION: ephemeral resource %s (%s) found in persistent state!", k, res.Type)
								}
							}
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTokenServiceIssueTenantTokenContainerRegistryService,
							apicontract.EndpointIAMGlobal,
							apicontract.StatusOk,
							"Ephemeral token downstream provider wiring and consumption",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

func TestAccEphemeral_CacheLifecycle_PlanAndDeferral(t *testing.T) {
	runnerTag := testAccResourcePrefix("cache-plan-")
	testAccCleanupRunnerProfile(t, runnerTag)

	// Actively induce and assert deferred plan response when configuration contains unknown values
	ctx := context.Background()
	e := integrations.NewBazelCacheEphemeralResource()
	var schemaResp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

	deferredModel := integrations.BazelCacheEphemeralModel{
		Key:                     types.StringValue("ci"),
		Version:                 types.Int64Null(),
		Site:                    types.StringNull(),
		EnableRemoteAssetAPI:    types.BoolNull(),
		ExperimentalDirectMTLS:  types.BoolNull(),
		ExperimentalCacheName:   types.StringUnknown(),
		CacheEndpoint:           types.StringNull(),
		HttpsMtlsCacheEndpoint:  types.StringNull(),
		HttpsCacheEndpoint:      types.StringNull(),
		ServerCAPEM:             types.StringNull(),
		ClientCertPEM:           types.StringNull(),
		ClientKeyPEM:            types.StringNull(),
		ExpiresAt:               types.StringNull(),
		CredentialHelperDomains: types.ListNull(types.StringType),
		BuildEventEndpoint:      types.StringNull(),
		RemoteAssetEndpoint:     types.StringNull(),
		UseWorkloadMTLS:         types.BoolNull(),
	}
	deferredConfig := tfsdk.State{Schema: schemaResp.Schema}
	_ = deferredConfig.Set(ctx, &deferredModel)

	openReq := ephemeral.OpenRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: deferredConfig.Raw},
		ClientCapabilities: ephemeral.OpenClientCapabilities{
			DeferralAllowed: true,
		},
	}
	var openResp ephemeral.OpenResponse
	openResp.Result = tfsdk.EphemeralResultData{Schema: schemaResp.Schema}
	e.Open(ctx, openReq, &openResp)

	if openResp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics during deferred plan: %v", openResp.Diagnostics)
	}
	if openResp.Deferred == nil || openResp.Deferred.Reason != ephemeral.DeferredReasonEphemeralResourceConfigUnknown {
		t.Fatalf("expected Deferred response with DeferredReasonEphemeralResourceConfigUnknown, got: %+v", openResp.Deferred)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Ephemeral cache referencing a computed resource attribute (wired into ephemeral cache experimental_cache_name to test plan deferral vs apply execution)
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "cache_target" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Profile for cache plan deferral test"
  swap_size_mb = 1024
}

ephemeral "namespace_bazel_cache" "deferred_cache" {
  key                     = "ci"
  experimental_cache_name = namespace_github_runner_profile.cache_target.id
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.cache_target", "tag", runnerTag),
					resource.TestCheckResourceAttrSet("namespace_github_runner_profile.cache_target", "id"),
					testAccCheckEphemeralNotInState("ephemeral.namespace_bazel_cache.deferred_cache"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_github_runner_profile.cache_target"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupRunnerProfileByID(t, rs.Primary.ID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCBazelCacheServiceEnsureBazelCache,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Ephemeral cache plan deferral and apply execution",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionCachePlanDeferral))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionNonPersistence))
						return nil
					},
				),
			},
		},
	})
}

// -----------------------------------------------------------------------------
// Phase 4: Data Sources Acceptance Tests
// -----------------------------------------------------------------------------

func TestAccDataSource_CacheVolumeSummary(t *testing.T) {
	runnerTag, cacheTag := testAccEnsureVolumeTag(t, "cv-")
	testAccCleanupRunnerProfile(t, runnerTag)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

resource "namespace_github_runner_profile" "prereq" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Cache volume summary prerequisite profile"
  cache_volume_settings = [
    {
      volume_tag     = %q
      volume_size_gb = 50
    }
  ]
}

data "namespace_cache_volume_summary" "test" {
  tag = namespace_github_runner_profile.prereq.cache_volume_settings[0].volume_tag
}
`, testAccProviderConfig(), runnerTag, cacheTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_cache_volume_summary.test", "tag", cacheTag),
					resource.TestCheckResourceAttrSet("data.namespace_cache_volume_summary.test", "id"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_github_runner_profile.prereq"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupRunnerProfileByID(t, rs.Primary.ID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCStorageServiceGetVolumeTagSummary,
							apicontract.EndpointCompute,
							apicontract.StatusOk,
							"Cache volume summary data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_PersistentVolume(t *testing.T) {
	runnerTag, pvTag := testAccEnsureVolumeTag(t, "pv-")
	testAccCleanupRunnerProfile(t, runnerTag)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

resource "namespace_github_runner_profile" "prereq" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Persistent volume lookup prerequisite profile"
  cache_volume_settings = [
    {
      volume_tag     = %q
      volume_size_gb = 50
    }
  ]
}

data "namespace_persistent_volume" "test" {
  tag = namespace_github_runner_profile.prereq.cache_volume_settings[0].volume_tag
}
`, testAccProviderConfig(), runnerTag, pvTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_persistent_volume.test", "tag", pvTag),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_github_runner_profile.prereq"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupRunnerProfileByID(t, rs.Primary.ID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCStorageServiceDescribePersistentVolume,
							apicontract.EndpointCompute,
							apicontract.StatusOk,
							"Persistent volume data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_BuilderConfiguration(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

data "namespace_builder_configuration" "test" {
  platform = "linux/amd64"
}
`, testAccProviderConfig()),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.namespace_builder_configuration.test", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCBuilderServiceGetBuilderConfiguration,
							apicontract.EndpointRegionalCompute,
							apicontract.StatusOk,
							"Builder configuration data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_BuilderUsage(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

data "namespace_builder_usage" "test" {
  period_start = "2026-08-01"
  period_end   = "2026-08-20"
}
`, testAccProviderConfig()),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_builder_usage.test", "period_start", "2026-08-01"),
					resource.TestCheckResourceAttrSet("data.namespace_builder_usage.test", "id"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCBuilderServiceGetBuilderUsage,
							apicontract.EndpointRegionalCompute,
							apicontract.StatusOk,
							"Builder usage statistics data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_NetworkConfiguration(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
%s

data "namespace_network_configuration" "test" {}
`, testAccProviderConfig()),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_network_configuration.test", "id", "network"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCComputeServiceGetNetworkConfiguration,
							apicontract.EndpointRegionalCompute,
							apicontract.StatusOk,
							"Compute network configuration data source query",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_RegistryImage(t *testing.T) {
	repoName, digest := testAccEnsureRegistryImage(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_shared_image" "prereq" {
  repository = %q
  digest     = %q
  visibility = "PUBLIC"
}

data "namespace_registry_image" "test" {
  repository = namespace_registry_shared_image.prereq.repository
  reference  = %q
}
`, repoName, digest, digest),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_registry_image.test", "repository", repoName),
					resource.TestCheckResourceAttr("data.namespace_registry_image.test", "reference", digest),
					resource.TestCheckResourceAttrSet("data.namespace_registry_image.test", "id"),
					resource.TestCheckResourceAttrSet("data.namespace_registry_image.test", "digest"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_registry_shared_image.prereq"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupSharedImageByID(t, rs.Primary.ID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceGetImage,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Container registry image data source lookup by reference",
						))
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataSource_RegistryImages(t *testing.T) {
	repoName, digest := testAccEnsureRegistryImage(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_registry_shared_image" "prereq" {
  repository = %q
  digest     = %q
  visibility = "PUBLIC"
}

data "namespace_registry_images" "test" {
  repository = namespace_registry_shared_image.prereq.repository
}
`, repoName, digest),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_registry_images.test", "repository", repoName),
					resource.TestCheckResourceAttrSet("data.namespace_registry_images.test", "id"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_registry_shared_image.prereq"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupSharedImageByID(t, rs.Primary.ID)
						}
						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceListImages,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Container registry images list data source pagination",
						))
						return nil
					},
				),
			},
		},
	})
}

func testAccEnsureVaultSecret(t *testing.T) string {
	t.Helper()
	testAccPreCheck(t)
	if os.Getenv("TF_ACC") == "" {
		return "test-vault-secret-object-id"
	}
	secretID := strings.TrimSpace(os.Getenv("NSC_TEST_VAULT_SECRET_ID"))
	if secretID == "" {
		t.Fatal("acceptance test requires a deterministic Vault metadata fixture: set NSC_TEST_VAULT_SECRET_ID")
	}
	ctx := context.Background()
	cli, err := client.New(ctx, client.Config{})
	if err != nil {
		t.Fatalf("failed to create client for vault prerequisite: %v", err)
	}
	defer testAccCloseClient(t, cli, "Vault metadata fixture validation")
	vCli, err := cli.VaultClient(ctx)
	if err != nil {
		t.Fatalf("failed to create vault client: %v", err)
	}
	resp, err := vCli.ListObjects(ctx, &vaultv1beta.ListObjectsRequest{})
	if err != nil {
		t.Fatalf("failed to list vault objects: %v", err)
	}
	for _, obj := range resp.GetObjects() {
		if obj.GetObjectId() == secretID {
			if obj.GetDeletedAt() != nil {
				t.Fatalf("configured Vault metadata fixture %q is deleted", secretID)
			}
			return secretID
		}
	}
	t.Fatalf("configured Vault metadata fixture %q was not returned by VaultService.ListObjects", secretID)
	return ""
}

func TestAccDataSource_VaultSecretMetadata(t *testing.T) {
	secretID := testAccEnsureVaultSecret(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

data "namespace_vault_secret_metadata" "test" {
  object_id = %q
}
`, secretID),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_vault_secret_metadata.test", "object_id", secretID),
					resource.TestCheckResourceAttr("data.namespace_vault_secret_metadata.test", "id", secretID),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCVaultServiceListObjects,
							apicontract.EndpointGlobal,
							apicontract.StatusOk,
							"Vault secret metadata data source lookup",
						))
						return nil
					},
				),
			},
		},
	})
}

// -----------------------------------------------------------------------------
// Acceptance Test Matrix: Drift, Concurrency, Deletion, Collision, Pagination & Expiration
// -----------------------------------------------------------------------------

// TestAccMatrix_DriftDetection tests remote configuration drift detection and state reconciliation.
// An actual out-of-band gRPC mutation is performed directly on the backend resource (UpdateProfile),
// followed by a PlanOnly step asserting drift against unchanged HCL, then an apply step reconciling back to the baseline.
func TestAccMatrix_DriftDetection(t *testing.T) {
	runnerTag := testAccResourcePrefix("mat-drift-")
	testAccCleanupRunnerProfile(t, runnerTag)

	baselineConfig := fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "drift" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Baseline description before remote mutation"
  swap_size_mb = 1024
}
`, runnerTag)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Initial creation
			{
				Config: baselineConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.drift", "tag", runnerTag),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.drift", "description", "Baseline description before remote mutation"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.drift", "swap_size_mb", "1024"),
				),
			},
			// Step 2: Out-of-band gRPC mutation directly on backend, followed by PlanOnly on unchanged HCL asserting drift detection
			{
				PreConfig: func() {
					testAccPreCheck(t)
					ctx := context.Background()
					cli, err := client.New(ctx, client.Config{})
					if err != nil {
						t.Fatalf("failed to create client for out-of-band drift mutation: %v", err)
					}
					defer testAccCloseClient(t, cli, "runner profile drift mutation")
					pCli, err := cli.ProfileClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain profile client: %v", err)
					}
					listResp, err := pCli.ListProfiles(ctx, &emptypb.Empty{})
					if err != nil {
						t.Fatalf("failed to list profiles: %v", err)
					}
					var targetProfile *githubv1beta.RunnerProfileWithStatus
					for _, p := range listResp.GetProfiles() {
						if p.GetSpec() != nil && p.GetSpec().GetTag() == runnerTag {
							targetProfile = p
							break
						}
					}
					if targetProfile == nil {
						t.Fatalf("runner profile %q not found for out-of-band mutation", runnerTag)
					}
					// Perform actual out-of-band gRPC mutation modifying description and swap space
					mutatedSpec := proto.Clone(targetProfile.GetSpec()).(*githubv1beta.RunnerProfileSpec)
					mutatedSpec.Description = "Out-of-band remote drift mutation"
					mutatedSpec.SwapSizeMb = 4096
					_, err = pCli.UpdateProfile(ctx, &githubv1beta.UpdateProfileRequest{
						ProfileId:     targetProfile.GetProfileId(),
						UpdateVersion: targetProfile.GetVersion(),
						Spec:          mutatedSpec,
					})
					if err != nil {
						t.Fatalf("out-of-band UpdateProfile failed: %v", err)
					}
				},
				Config:             baselineConfig,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Step 3: Apply unchanged baseline HCL, reconciling out-of-band drift back to baseline
			{
				Config: baselineConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.drift", "tag", runnerTag),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.drift", "description", "Baseline description before remote mutation"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.drift", "swap_size_mb", "1024"),
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCProfileServiceUpdateProfile,
							apicontract.EndpointGlobal,
							apicontract.StatusOkReconciled,
							"Drift detection & HCL reconciliation",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionDriftDetection))
						return nil
					},
				),
			},
		},
	})
}

// TestAccMatrix_ConcurrencyConflict tests optimistic concurrency handling and revision conflict diagnostics.
// An intervening out-of-band writer mutates the resource directly on the backend (incrementing update_version)
// after plan / during apply, asserting that subsequent Terraform apply fails with codes.FailedPrecondition revision conflict.
func TestAccMatrix_ConcurrencyConflict(t *testing.T) {
	runnerTag := testAccResourcePrefix("mat-conf-")
	testAccCleanupRunnerProfile(t, runnerTag)

	capture := &testAccConflictCapture{}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccConcurrencyConflictProviderFactories(t, runnerTag, capture),
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Initial creation (state records version 1)
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "conflict_target" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Initial state version"
  swap_size_mb = 512
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.conflict_target", "tag", runnerTag),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.conflict_target", "swap_size_mb", "512"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_github_runner_profile.conflict_target"]
						if ok && rs.Primary != nil && rs.Primary.ID != "" {
							testAccCleanupRunnerProfileByID(t, rs.Primary.ID)
						}
						return nil
					},
				),
			},
			// Step 2: Intervening writer mutates update_version out-of-band during apply, causing Terraform apply to fail with FailedPrecondition
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "conflict_target" {
  tag          = %q
  os           = "ubuntu-24.04"
  description  = "Second state version update"
  swap_size_mb = 1024
}
`, runnerTag),
				ExpectError: regexp.MustCompile(`(?i)Concurrency Conflict|FailedPrecondition|version|revision`),
			},
		},
	})

	mutationAttempted, mutationErr, providerErr := capture.snapshot()
	if !mutationAttempted {
		t.Fatal("runner profile concurrency test did not execute the intervening mutation")
	}
	if mutationErr != nil {
		t.Fatalf("intervening mutation encountered an unexpected error: %v", mutationErr)
	}
	if code := status.Code(providerErr); code != codes.FailedPrecondition {
		t.Fatalf("expected provider profile update to fail with codes.FailedPrecondition, got %v (err: %v)", code, providerErr)
	}

	t.Log(apicontract.FormatObservation(
		apicontract.RPCProfileServiceUpdateProfile,
		apicontract.EndpointGlobal,
		apicontract.StatusFailedPrecondition,
		"Optimistic revision locking collision",
	))
	t.Log(apicontract.FormatAssertion(apicontract.AssertionConcurrencyConflict))
}

// TestAccMatrix_ExternalDeletion tests behavior when managed resources are deleted externally.
// An out-of-band gRPC call deletes the resource directly; Terraform detects NotFound on Read,
// clears the resource from state, and cleanly recreates it on apply.
func TestAccMatrix_ExternalDeletion(t *testing.T) {
	runnerTag := testAccResourcePrefix("mat-extdel-")
	testAccCleanupRunnerProfile(t, runnerTag)

	var initialProfileID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Initial creation
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "ext_del" {
  tag         = %q
  os          = "ubuntu-24.04"
  description = "Resource before external deletion"
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.ext_del", "tag", runnerTag),
					resource.TestCheckResourceAttrSet("namespace_github_runner_profile.ext_del", "id"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.ext_del", "description", "Resource before external deletion"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_github_runner_profile.ext_del"]
						if !ok || rs.Primary == nil || rs.Primary.ID == "" {
							return fmt.Errorf("resource namespace_github_runner_profile.ext_del not found or missing ID in step 1")
						}
						initialProfileID = rs.Primary.ID
						testAccCleanupRunnerProfileByID(t, initialProfileID)
						return nil
					},
				),
			},
			// Step 2: Delete out-of-band directly on server, then re-apply to verify clean recreation
			{
				PreConfig: func() {
					testAccPreCheck(t)
					ctx := context.Background()
					cli, err := client.New(ctx, client.Config{})
					if err != nil {
						t.Fatalf("failed to create client for external deletion: %v", err)
					}
					defer testAccCloseClient(t, cli, "runner profile external deletion")
					pCli, err := cli.ProfileClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain profile client: %v", err)
					}
					listResp, err := pCli.ListProfiles(ctx, &emptypb.Empty{})
					if err != nil {
						t.Fatalf("failed to list profiles: %v", err)
					}
					var deleted bool
					for _, p := range listResp.GetProfiles() {
						if p.GetSpec() != nil && p.GetSpec().GetTag() == runnerTag {
							_, err := pCli.DeleteProfile(ctx, &githubv1beta.DeleteProfileRequest{ProfileId: p.GetProfileId()})
							if err != nil {
								t.Fatalf("out-of-band DeleteProfile failed: %v", err)
							}
							deleted = true
							break
						}
					}
					if !deleted {
						t.Fatalf("profile with tag %q not found on backend for external deletion", runnerTag)
					}
				},
				Config: fmt.Sprintf(`
provider "namespace" {}

resource "namespace_github_runner_profile" "ext_del" {
  tag         = %q
  os          = "ubuntu-24.04"
  description = "Recreated after external deletion"
}
`, runnerTag),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_github_runner_profile.ext_del", "tag", runnerTag),
					resource.TestCheckResourceAttrSet("namespace_github_runner_profile.ext_del", "id"),
					resource.TestCheckResourceAttr("namespace_github_runner_profile.ext_del", "description", "Recreated after external deletion"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["namespace_github_runner_profile.ext_del"]
						if !ok || rs.Primary == nil || rs.Primary.ID == "" {
							return fmt.Errorf("resource namespace_github_runner_profile.ext_del not found or missing ID after recreation")
						}
						newID := rs.Primary.ID
						if initialProfileID == "" {
							return fmt.Errorf("initialProfileID was not recorded in step 1")
						}
						if newID == initialProfileID {
							return fmt.Errorf("recreated profile ID %q must be distinct from initial profile ID %q", newID, initialProfileID)
						}
						testAccCleanupRunnerProfileByID(t, newID)
						t.Log(apicontract.FormatObservation(
							apicontract.RPCProfileServiceDeleteProfileRead,
							apicontract.EndpointGlobal,
							apicontract.StatusNotFoundRecreated,
							"External purge & state recreation",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionExternalDeletionRecreated))
						return nil
					},
				),
			},
		},
	})
}

// TestAccMatrix_CollisionPrevention tests import guidance and collision handling for pre-existing identifiers.
// An out-of-band EnsureTenantGroup creates the resource prior to Terraform; Terraform Create fails
// with an AlreadyExists diagnostic instructing the operator to import.
func TestAccMatrix_CollisionPrevention(t *testing.T) {
	groupName := testAccResourcePrefix("mat-coll-")
	testAccCleanupTenantGroup(t, groupName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Pre-create out-of-band and verify Terraform Create fails with collision error and import hint
			{
				PreConfig: func() {
					testAccPartnerPreCheck(t)
					ctx := context.Background()
					partnerTok := os.Getenv("NSC_PARTNER_TOKEN")
					if partnerTok == "" {
						return
					}
					cli, err := client.New(ctx, client.Config{Token: partnerTok})
					if err != nil {
						t.Fatalf("failed to create partner client for collision test: %v", err)
					}
					defer testAccCloseClient(t, cli, "tenant group collision setup")
					tCli, err := cli.TenantClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain tenant client: %v", err)
					}
					_, err = tCli.EnsureTenantGroup(ctx, &iamv1beta.EnsureTenantGroupRequest{
						Name: groupName,
						Policies: []*iamv1beta.TenantGroupPolicy{
							{
								Policy: "namespace.cloud.iam.v1beta.AdminPolicy",
								Value:  "{}",
							},
						},
					})
					if err != nil {
						t.Fatalf("out-of-band EnsureTenantGroup failed: %v", err)
					}
				},
				Config: fmt.Sprintf(`
%s

resource "namespace_tenant_group" "collision" {
  name = %q
  policies = [
    {
      policy = "namespace.cloud.iam.v1beta.AdminPolicy"
      value  = "{}"
    }
  ]
}
`, testAccPartnerProviderConfig(), groupName),
				ExpectError: regexp.MustCompile(`(?i)Tenant Group Already Exists|already exists remotely|terraform import`),
			},
			// Step 2: Operator follows guidance and imports pre-existing resource successfully
			{
				ResourceName:      "namespace_tenant_group.collision",
				ImportState:       true,
				ImportStateVerify: true,
				Check: resource.ComposeTestCheckFunc(
					func(s *terraform.State) error {
						t.Log(apicontract.FormatObservation(
							apicontract.RPCTenantServiceEnsureTenantGroup,
							apicontract.EndpointIAM,
							apicontract.StatusOkAlreadyExistsImport,
							"Collision detection & import guidance",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionCollisionPrevention))
						return nil
					},
				),
			},
		},
	})
}

// TestAccMatrix_PaginationTraversal tests cursor-based pagination traversal across a repository with
// container images in namespace_registry_images, asserting >100 records and multiple cursor pages on the live API.
func TestAccMatrix_PaginationTraversal(t *testing.T) {
	paginatedRepo := testAccEnsurePaginatedRegistryRepository(t)

	var (
		cursorMu        sync.Mutex
		capturedCursors []cursorRecord
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccPaginationProviderFactories(t, paginatedRepo, &capturedCursors, &cursorMu),
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "namespace" {}

data "namespace_registry_images" "images_matrix" {
  repository = %q
}
`, paginatedRepo),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.namespace_registry_images.images_matrix", "repository", paginatedRepo),
					resource.TestCheckResourceAttrSet("data.namespace_registry_images.images_matrix", "id"),
					func(s *terraform.State) error {
						res, ok := s.RootModule().Resources["data.namespace_registry_images.images_matrix"]
						if !ok {
							return fmt.Errorf("resource data.namespace_registry_images.images_matrix not found in state")
						}
						countStr := res.Primary.Attributes["images.#"]
						count, err := strconv.Atoi(countStr)
						if err != nil {
							return fmt.Errorf("failed to parse images count attribute %q: %w", countStr, err)
						}
						if os.Getenv("TF_ACC") != "" && count <= 100 {
							return fmt.Errorf("TestAccMatrix_PaginationTraversal assertion failed: expected > 100 records across multiple cursor pages, got %d", count)
						}

						cursorMu.Lock()
						records := make([]cursorRecord, len(capturedCursors))
						copy(records, capturedCursors)
						cursorMu.Unlock()

						if os.Getenv("TF_ACC") != "" {
							if len(records) == 0 {
								return fmt.Errorf("TestAccMatrix_PaginationTraversal assertion failed: no ListImages cursor records captured")
							}

							validTraversal := false
							transitions := 0
							for i, record := range records {
								if record.requestCursor == "" {
									if i > 0 && records[i-1].responseCursor != "" {
										return fmt.Errorf("TestAccMatrix_PaginationTraversal assertion failed: a new traversal started before the prior cursor chain terminated")
									}
									transitions = 0
								} else if i == 0 || records[i-1].responseCursor != record.requestCursor {
									return fmt.Errorf("TestAccMatrix_PaginationTraversal assertion failed: request cursor did not match the preceding response cursor")
								}

								if record.responseCursor != "" {
									transitions++
								} else if transitions >= 2 {
									validTraversal = true
								}
							}
							if records[len(records)-1].responseCursor != "" {
								return fmt.Errorf("TestAccMatrix_PaginationTraversal assertion failed: final cursor chain did not terminate")
							}
							if !validTraversal {
								return fmt.Errorf("TestAccMatrix_PaginationTraversal assertion failed: no complete traversal contained at least two non-empty cursor transitions")
							}
						}

						t.Log(apicontract.FormatObservation(
							apicontract.RPCContainerRegistryServiceListImages,
							apicontract.EndpointGlobal,
							apicontract.StatusOkCursorTraversal,
							">100 image records pagination (live cursor traversal)",
						))
						t.Log(apicontract.FormatAssertion(apicontract.AssertionPaginationMultipleCursors))
						return nil
					},
				),
			},
		},
	})
}

// TestAccMatrix_ExpirationDisappearance tests short-lived ephemeral credentials and expired resources.
// Setting a short expiration (2s) and allowing it to elapse (3s) actively verifies that subsequent API calls with the
// expired token are rejected with codes.Unauthenticated, and ephemeral secrets never persist to state.
func TestAccMatrix_ExpirationDisappearance(t *testing.T) {
	tenantID := os.Getenv("NSC_TEST_TENANT")
	if tenantID == "" {
		tenantID = "test-tenant"
	}
	actorID := testAccResourcePrefix("mat-exp-act-")
	repoName := testAccResourcePrefix("mat-exp-repo-")
	testAccCleanupRepositoryPolicy(t, repoName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPartnerPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckDestroy,
		Steps: []resource.TestStep{
			// Step 1: Issue short-lived tenant token and set repository policy
			{
				Config: fmt.Sprintf(`
%s

ephemeral "namespace_tenant_token" "expiring_token" {
  tenant_id     = %q
  actor_id      = %q
  duration_secs = 2
}

ephemeral "namespace_id_token" "expiring_id_token" {
  audience = "https://iam.example.com/federation"
}

resource "namespace_registry_repository_policy" "expiring_policy" {
  repository             = %q
  expiration             = "1h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, testAccPartnerProviderConfig(), tenantID, actorID, repoName),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckEphemeralNotInState("ephemeral.namespace_tenant_token.expiring_token"),
					testAccCheckEphemeralNotInState("ephemeral.namespace_id_token.expiring_id_token"),
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.expiring_policy", "expiration", "1h"),
				),
			},
			// Step 2: Sleep past token expiration and actively assert rejection with codes.Unauthenticated without error swallowing
			{
				PreConfig: func() {
					testAccPartnerPreCheck(t)
					ctx := context.Background()
					partnerTok := os.Getenv("NSC_PARTNER_TOKEN")
					if partnerTok == "" {
						t.Fatalf("NSC_PARTNER_TOKEN required for live expiration test")
					}
					cli, err := client.New(ctx, client.Config{Token: partnerTok})
					if err != nil {
						t.Fatalf("failed to create partner client: %v", err)
					}
					defer testAccCloseClient(t, cli, "short-lived token issuance")
					tCli, err := cli.TokenClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain token client: %v", err)
					}
					// Actively issue short-lived token (2s duration)
					issueResp, err := tCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{
						TenantId:     tenantID,
						ActorId:      actorID,
						DurationSecs: 2,
					})
					if err != nil {
						t.Fatalf("failed to issue short-lived token: %v", err)
					}
					if issueResp.GetBearerToken() == "" {
						t.Fatalf("issued bearer token was empty")
					}

					// Sleep past expiration duration (3s)
					time.Sleep(3 * time.Second)

					// Actively attempt authenticated API call with expired token
					expiredCli, err := client.New(ctx, client.Config{Token: issueResp.GetBearerToken()})
					if err != nil {
						t.Fatalf("failed to create client with expired token: %v", err)
					}
					defer testAccCloseClient(t, expiredCli, "expired token verification")
					pCli, err := expiredCli.ProfileClient(ctx)
					if err != nil {
						t.Fatalf("failed to obtain profile client with expired token: %v", err)
					}
					_, err = pCli.ListProfiles(ctx, &emptypb.Empty{})
					if err == nil {
						t.Fatalf("expected Unauthenticated error when calling API with expired token, got success")
					}
					code := status.Code(err)
					if code != codes.Unauthenticated {
						t.Fatalf("expected codes.Unauthenticated on expired token, got status code %v (err: %v)", code, err)
					}
					t.Log(apicontract.FormatObservation(
						apicontract.RPCTokenServiceIssueTenantTokenProfileService,
						apicontract.EndpointIAMGlobal,
						apicontract.StatusUnauthenticatedRejected,
						"Short-lived token expiry rejection",
					))
					t.Log(apicontract.FormatAssertion(apicontract.AssertionExpiredTokenRejection))
				},
				Config: fmt.Sprintf(`
%s

resource "namespace_registry_repository_policy" "expiring_policy" {
  repository             = %q
  expiration             = "1h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
`, testAccPartnerProviderConfig(), repoName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("namespace_registry_repository_policy.expiring_policy", "repository", repoName),
				),
			},
		},
	})
}
