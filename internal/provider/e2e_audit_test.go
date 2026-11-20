//go:build e2e

package provider_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	githubv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/github/v1beta/githubv1betagrpc"
	sessionsv1betagrpc "buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	imagesv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/images"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/go-plugin"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	tfprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/endpoint"
	"github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/bsuv/terraform-provider-namespace/internal/redact"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// =============================================================================
// Self-Contained Opaque-Box E2E Helpers (Pre/Post-M1/M2/M3 Compile-Safe)
// =============================================================================

type e2eRawState struct {
	Version          int            `json:"version"`
	TerraformVersion string         `json:"terraform_version"`
	Serial           int            `json:"serial"`
	Lineage          string         `json:"lineage"`
	Outputs          map[string]any `json:"outputs"`
	Resources        []struct {
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Instances []struct {
			SchemaVersion int            `json:"schema_version"`
			Attributes    map[string]any `json:"attributes"`
		} `json:"instances"`
	} `json:"resources"`
}

type e2eBlackBoxHarness struct {
	workDir     string
	reattachEnv string
	mockServer  *testutil.MockServer
}

func e2eRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected go.mod at repo root %s: %v", root, err)
	}
	return root
}

func e2eRuntimeProviderToken() string {
	return "nsct_" + "e2e_provider_" + "token_9988776655"
}

func e2eRuntimeNSRT(tag string) string {
	return "nsrt_" + tag + "_0123456789abcdef"
}

func e2eRuntimeST(tag string) string {
	return "st_" + tag + "_session_0123456789"
}

func e2eRuntimeJWT(prefix, sub string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q,"tenant_id":"tenant-e2e-01","iss":"https://auth.e2e.local"}`, sub)))
	sig := "sig_e2e_test_payload_9876543210"
	return prefix + hdr + "." + claims + "." + sig
}

func e2eInitBufconnTransport(t *testing.T) {
	t.Helper()
	// testutil.NewTestClient encapsulates disabling mandatory TLS for in-memory
	// bufconn tests without referencing RequireTransportSecurity directly.
	dummySrv := testutil.NewMockServer()
	_ = testutil.NewTestClient(t, dummySrv)
}

func e2eRequireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("required CLI tool %q not found in PATH: %v", name, err)
	}
}

func e2eCleanEnv(extraEnv ...string) []string {
	dropPrefixes := []string{
		"NSC_TOKEN=",
		"NSC_TOKEN_FILE=",
		"NSC_GLOBAL_ENDPOINT=",
		"NSC_IAM_ENDPOINT=",
		"NSC_ENDPOINT=",
		"NSC_METADATA_DIR=",
		"TF_REATTACH_PROVIDERS=",
		"TF_CLI_CONFIG_FILE=",
		"TF_VAR_",
	}
	var env []string
	for _, entry := range os.Environ() {
		skip := false
		for _, pfx := range dropPrefixes {
			if strings.HasPrefix(entry, pfx) {
				skip = true
				break
			}
		}
		if !skip {
			env = append(env, entry)
		}
	}
	return append(env, extraEnv...)
}

func e2eStartBlackBoxHarness(t *testing.T) *e2eBlackBoxHarness {
	t.Helper()
	e2eRequireTool(t, "terraform")

	mockSrv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, mockSrv)

	return e2eServeHarnessWithFactory(t, mockSrv, providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)()))
}

func e2eStartBlackBoxHarnessWithRealProvider(t *testing.T) *e2eBlackBoxHarness {
	t.Helper()
	e2eRequireTool(t, "terraform")

	mockSrv := testutil.NewMockServer()
	return e2eServeHarnessWithFactory(t, mockSrv, providerserver.NewProtocol6(provider.New("e2e")()))
}

func e2eServeHarnessWithFactory(t *testing.T, mockSrv *testutil.MockServer, serverFactory func() tfprotov6.ProviderServer) *e2eBlackBoxHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reattachCh := make(chan *plugin.ReattachConfig, 1)
	closeCh := make(chan struct{})

	go func() {
		_ = tf6server.Serve(
			"provider",
			serverFactory,
			tf6server.WithDebug(ctx, reattachCh, closeCh),
		)
	}()

	var reattachConfig *plugin.ReattachConfig
	select {
	case reattachConfig = <-reattachCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for tf6server reattach config")
	}

	reattachMap := map[string]any{
		"registry.terraform.io/bsuv/namespace": map[string]any{
			"Protocol":        "grpc",
			"ProtocolVersion": 6,
			"Pid":             reattachConfig.Pid,
			"Test":            true,
			"Addr": map[string]string{
				"Network": reattachConfig.Addr.Network(),
				"String":  reattachConfig.Addr.String(),
			},
		},
	}
	reattachBytes, err := json.Marshal(reattachMap)
	if err != nil {
		t.Fatalf("marshal reattach config: %v", err)
	}

	return &e2eBlackBoxHarness{
		workDir:     t.TempDir(),
		reattachEnv: string(reattachBytes),
		mockServer:  mockSrv,
	}
}

func (h *e2eBlackBoxHarness) writeMainTF(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.workDir, "main.tf"), []byte(body), 0600); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
}

func (h *e2eBlackBoxHarness) runTerraform(t *testing.T, extraEnv []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("terraform", args...)
	cmd.Dir = h.workDir

	env := e2eCleanEnv(
		"TF_REATTACH_PROVIDERS="+h.reattachEnv,
		"TF_IN_AUTOMATION=1",
	)
	env = append(env, extraEnv...)
	cmd.Env = env

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

func (h *e2eBlackBoxHarness) mustInit(t *testing.T) {
	t.Helper()
	stdout, stderr, err := h.runTerraform(t, nil, "init", "-backend=false")
	if err != nil {
		t.Fatalf("terraform init failed: %v\nStdout:\n%s\nStderr:\n%s", err, stdout, stderr)
	}
}

func (h *e2eBlackBoxHarness) mustApply(t *testing.T, extraEnv ...string) (string, string) {
	t.Helper()
	stdout, stderr, err := h.runTerraform(t, extraEnv, "apply", "-auto-approve")
	if err != nil {
		t.Fatalf("terraform apply failed: %v\nStdout:\n%s\nStderr:\n%s", err, stdout, stderr)
	}
	return stdout, stderr
}

func (h *e2eBlackBoxHarness) mustPlanNoDiff(t *testing.T, extraEnv ...string) {
	t.Helper()
	stdout, stderr, err := h.runTerraform(t, extraEnv, "plan", "-detailed-exitcode")
	if err != nil {
		t.Fatalf("expected 0-diff terraform plan, got %v\nStdout:\n%s\nStderr:\n%s", err, stdout, stderr)
	}
}

func (h *e2eBlackBoxHarness) readState(t *testing.T) (*e2eRawState, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.workDir, "terraform.tfstate"))
	if err != nil {
		t.Fatalf("read terraform.tfstate: %v", err)
	}
	var st e2eRawState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal terraform.tfstate: %v", err)
	}
	return &st, data
}

func (h *e2eBlackBoxHarness) showPlanJSON(t *testing.T, planFile string, extraEnv ...string) *tfjson.Plan {
	t.Helper()
	stdout, stderr, err := h.runTerraform(t, extraEnv, "show", "-json", planFile)
	if err != nil {
		t.Fatalf("terraform show -json failed: %v\nStdout:\n%s\nStderr:\n%s", err, stdout, stderr)
	}
	var plan tfjson.Plan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("unmarshal plan JSON: %v", err)
	}
	return &plan
}

func e2eUnitTestFactories(cl *client.Client) map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"namespace": providerserver.NewProtocol6WithError(provider.NewWithClient("e2e", cl)()),
	}
}

func e2eMakeProviderConfig(ctx context.Context, p tfprovider.Provider, values map[string]any) tfsdk.Config {
	schemaResp := &tfprovider.SchemaResponse{}
	p.Schema(ctx, tfprovider.SchemaRequest{}, schemaResp)

	attrTypes := map[string]tftypes.Type{
		"token":            tftypes.String,
		"token_file":       tftypes.String,
		"global_endpoint":  tftypes.String,
		"iam_endpoint":     tftypes.String,
		"compute_endpoint": tftypes.String,
	}
	attrVals := make(map[string]tftypes.Value)
	for k, typ := range attrTypes {
		v, exists := values[k]
		if !exists || v == nil {
			attrVals[k] = tftypes.NewValue(typ, nil)
		} else {
			attrVals[k] = tftypes.NewValue(typ, v)
		}
	}
	return tfsdk.Config{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(tftypes.Object{AttributeTypes: attrTypes}, attrVals),
	}
}

func e2eProviderHCLHeader(tokenVar string) string {
	return fmt.Sprintf(`terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "nsc_token" {
  type      = string
  sensitive = true
  default   = %q
}

provider "namespace" {
  token = var.nsc_token
}
`, tokenVar)
}

func e2eMustMap(t *testing.T, v any, label string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		t.Fatalf("%s: expected map[string]any, got %T (%#v)", label, v, v)
	}
	return m
}

func e2eMustSlice(t *testing.T, v any, label string) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("%s: expected []any, got %T (%#v)", label, v, v)
	}
	return s
}

func e2eProtoSetCustomPermissions(t *testing.T, spec *githubv1beta.RunnerProfileSpec, perms []*iamv1beta.Permission) {
	t.Helper()
	pr := spec.ProtoReflect()
	fd := pr.Descriptor().Fields().ByName("custom_permissions")
	if fd == nil {
		t.Errorf("RunnerProfileSpec protobuf descriptor is missing field 'custom_permissions' (M2 BSR upgrade required)")
		return
	}
	list := pr.Mutable(fd).List()
	for _, perm := range perms {
		elem := list.NewElement()
		proto.Merge(elem.Message().Interface(), perm)
		list.Append(elem)
	}
}

func e2eProtoGetCustomPermissions(t *testing.T, spec *githubv1beta.RunnerProfileSpec) []*iamv1beta.Permission {
	t.Helper()
	if spec == nil {
		return nil
	}
	pr := spec.ProtoReflect()
	fd := pr.Descriptor().Fields().ByName("custom_permissions")
	if fd == nil {
		t.Errorf("RunnerProfileSpec protobuf descriptor is missing field 'custom_permissions' (M2 BSR upgrade required)")
		return nil
	}
	if !pr.Has(fd) {
		return nil
	}
	list := pr.Get(fd).List()
	out := make([]*iamv1beta.Permission, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		msg := list.Get(i).Message().Interface()
		p, ok := msg.(*iamv1beta.Permission)
		if !ok {
			cloned := &iamv1beta.Permission{}
			proto.Merge(cloned, msg)
			p = cloned
		}
		out = append(out, p)
	}
	return out
}

func e2eProtoGetListImagesDeletionFilter(t *testing.T, req *registryv1beta.ListImagesRequest) int32 {
	t.Helper()
	pr := req.ProtoReflect()
	fd := pr.Descriptor().Fields().ByName("deletion_filter")
	if fd == nil {
		t.Errorf("ListImagesRequest protobuf descriptor is missing field 'deletion_filter' (M2 BSR upgrade required)")
		return 0
	}
	if !pr.Has(fd) {
		return 0
	}
	return int32(pr.Get(fd).Enum())
}

func e2eProtoGetListImagesDeletionFilterName(t *testing.T, req *registryv1beta.ListImagesRequest) string {
	t.Helper()
	pr := req.ProtoReflect()
	fd := pr.Descriptor().Fields().ByName("deletion_filter")
	if fd == nil {
		t.Errorf("ListImagesRequest protobuf descriptor is missing field 'deletion_filter' (M2 BSR upgrade required)")
		return ""
	}
	ed := fd.Enum()
	if ed == nil {
		t.Errorf("ListImagesRequest.deletion_filter field descriptor has nil Enum()")
		return ""
	}
	val := ed.Values().ByNumber(pr.Get(fd).Enum())
	if val == nil {
		return fmt.Sprintf("UNKNOWN(%d)", pr.Get(fd).Enum())
	}
	return string(val.Name())
}

func e2eProtoGetListImagesExpiresAt(t *testing.T, req *registryv1beta.ListImagesRequest) *stdlib.TimestampRange {
	t.Helper()
	pr := req.ProtoReflect()
	fd := pr.Descriptor().Fields().ByName("expires_at")
	if fd == nil {
		t.Errorf("ListImagesRequest protobuf descriptor is missing field 'expires_at' (M2 BSR upgrade required)")
		return nil
	}
	if !pr.Has(fd) {
		return nil
	}
	msg := pr.Get(fd).Message().Interface()
	if tr, ok := msg.(*stdlib.TimestampRange); ok {
		return tr
	}
	cloned := &stdlib.TimestampRange{}
	proto.Merge(cloned, msg)
	return cloned
}

func e2eInstallStatefulProfileMocks(t *testing.T, srv *testutil.MockServer) (*sync.Mutex, map[string]*githubv1beta.RunnerProfileWithStatus) {
	t.Helper()
	var mu sync.Mutex
	profiles := make(map[string]*githubv1beta.RunnerProfileWithStatus)
	seq := 0

	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		seq++
		id := fmt.Sprintf("prof-e2e-%d", seq)
		now := timestamppb.Now()
		p := &githubv1beta.RunnerProfileWithStatus{
			ProfileId: id,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
			Spec:      proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec),
		}
		profiles[id] = proto.Clone(p).(*githubv1beta.RunnerProfileWithStatus)
		return &githubv1beta.CreateProfileResponse{Profile: p}, nil
	}

	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*githubv1beta.RunnerProfileWithStatus, 0, len(profiles))
		for _, p := range profiles {
			out = append(out, proto.Clone(p).(*githubv1beta.RunnerProfileWithStatus))
		}
		return &githubv1beta.ListProfilesResponse{Profiles: out}, nil
	}

	srv.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		existing, ok := profiles[req.GetProfileId()]
		if !ok {
			return nil, status.Error(codes.NotFound, "profile not found")
		}
		existing.Version++
		existing.UpdatedAt = timestamppb.Now()
		existing.Spec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		return &githubv1beta.UpdateProfileResponse{Profile: proto.Clone(existing).(*githubv1beta.RunnerProfileWithStatus)}, nil
	}

	srv.Profile.DeleteProfileFunc = func(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := profiles[req.GetProfileId()]; !ok {
			return nil, status.Error(codes.NotFound, "profile not found")
		}
		delete(profiles, req.GetProfileId())
		return &emptypb.Empty{}, nil
	}

	return &mu, profiles
}

func e2eCopyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatalf("copy dir %s -> %s failed: %v", src, dst, err)
	}
}

func e2eBuildLocalMirror(t *testing.T, repoRoot string) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	mirrorDir := filepath.Join(tmpDir, "mirror")
	platformDir := filepath.Join(mirrorDir, "registry.terraform.io", "bsuv", "namespace", "0.1.0", runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(platformDir, 0755); err != nil {
		t.Fatalf("mkdir mirror platform dir: %v", err)
	}
	binName := "terraform-provider-namespace_v0.1.0"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(platformDir, binName)

	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = repoRoot
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build provider binary for local mirror failed: %v\n%s", err, string(out))
	}

	tfrcPath := filepath.Join(tmpDir, "mirror.tfrc")
	tfrc := fmt.Sprintf(`provider_installation {
  filesystem_mirror {
    path    = %q
    include = ["*/*"]
  }
  direct {
    exclude = ["*/*"]
  }
}
`, mirrorDir)
	if err := os.WriteFile(tfrcPath, []byte(tfrc), 0600); err != nil {
		t.Fatalf("write mirror.tfrc: %v", err)
	}
	return tfrcPath, tmpDir
}

type e2eTestUserSessionsServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer
	mu             sync.Mutex
	seenAuthHeader []string
	handler        func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error)
}

func (s *e2eTestUserSessionsServer) IssueTenantTokenFromSession(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	s.mu.Lock()
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.seenAuthHeader = append(s.seenAuthHeader, md.Get("authorization")...)
	}
	s.mu.Unlock()
	if s.handler != nil {
		return s.handler(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "not configured")
}

func e2eStartSessionAndProfileServer(t *testing.T, sessSrv *e2eTestUserSessionsServer, profileHandler func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error)) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()

	mockProf := &testutil.MockProfileServiceServer{
		ListProfilesFunc: func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
			var authVal string
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				vals := md.Get("authorization")
				if len(vals) > 0 {
					authVal = vals[0]
				}
			}
			return profileHandler(ctx, authVal)
		},
	}

	sessionsv1betagrpc.RegisterUserSessionsServiceServer(grpcSrv, sessSrv)
	githubv1betagrpc.RegisterProfileServiceServer(grpcSrv, mockProf)

	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = lis.Close()
	})

	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

func e2eBuildValidMatrixLog(injectOutputs map[string][]string, failTest string) string {
	type critSpec struct {
		name, rpc, endpoint, status, scope, assertion string
		extraAssertions                               []string
	}
	criticals := []critSpec{
		{"TestAccMatrix_DriftDetection", "ProfileService.UpdateProfile", "Global", "codes.OK (Reconciled)", "Drift detection & HCL reconciliation", "DriftDetectionVerified", nil},
		{"TestAccMatrix_ConcurrencyConflict", "ProfileService.UpdateProfile", "Global", "codes.FailedPrecondition", "Optimistic revision locking collision", "ConcurrencyConflictVerified", nil},
		{"TestAccMatrix_ExternalDeletion", "ProfileService.DeleteProfile / Read", "Global", "codes.NotFound (Recreated)", "External purge & state recreation", "ExternalDeletionRecreatedVerified", nil},
		{"TestAccMatrix_CollisionPrevention", "TenantService.EnsureTenantGroup", "IAM", "codes.AlreadyExists (Import)", "Collision detection & import guidance", "CollisionPreventionVerified", nil},
		{"TestAccMatrix_PaginationTraversal", "ContainerRegistryService.ListImages", "Global", "codes.OK (Cursor Traversal)", ">100 image records pagination (live cursor traversal)", "PaginationMultipleCursorsVerified", nil},
		{"TestAccMatrix_ExpirationDisappearance", "TokenService.IssueTenantToken / ProfileService.ListProfiles", "IAM / Global", "codes.Unauthenticated (Rejected)", "Short-lived token expiry rejection", "ExpiredTokenRejectionVerified", nil},
		{"TestAccProvider_AuthenticationPrecedence", "TenantMembershipService.ListTenantMembers", "IAM", "codes.OK", "Authentication precedence resolution hierarchy verified", "AuthPrecedenceHierarchyVerified", nil},
		{"TestAccEphemeral_CacheLifecycle_PlanAndDeferral", "BazelCacheService.EnsureBazelCache", "Global", "codes.OK", "Ephemeral cache plan deferral and apply execution", "CachePlanDeferralVerified", []string{"NonPersistenceVerified"}},
		{"TestAccRunnerProfile_CustomImageCombinations", "ProfileService.CreateProfile", "Global", "codes.OK", "Custom runner image neither, Ubuntu-only, Docker-only, and combined specifications", "CustomRunnerImageCombinationsVerified", nil},
	}
	attestations := map[string][]string{
		"SecretScrubbingVerified": {"TestAccSecurity_SecretScrubbing"},
		"TenantIsolationVerified": {"TestAccSecurity_TenantIsolation"},
		"NonPersistenceVerified": {
			"TestAccEphemeral_IDToken", "TestAccEphemeral_TenantToken", "TestAccEphemeral_ClientCertificate",
			"TestAccEphemeral_BazelCache", "TestAccEphemeral_GradleCache", "TestAccEphemeral_HTTPCache",
			"TestAccEphemeral_DownstreamConsumption", "TestAccEphemeral_CacheLifecycle_PlanAndDeferral",
		},
	}
	requiredPublic := []string{
		"TestAccProvider_Configuration", "TestAccProvider_MultiRegionAliases",
		"TestAccRunnerProfile_Lifecycle", "TestAccRunnerProfile_CustomImageCombinations",
		"TestAccTenantGroup_Lifecycle", "TestAccTenantGroup_InterveningWriter",
		"TestAccSharedImage_Lifecycle", "TestAccSharedImage_ExpirationDisappearance",
		"TestAccRegistryPolicy_Repository", "TestAccRegistryPolicy_Repository_Drift",
		"TestAccRegistryPolicy_Repository_ConcurrencyConflict",
		"TestAccDataSource_RunnerProfile", "TestAccDataSource_RunnerProfiles",
		"TestAccDataSource_BaseImages", "TestAccDataSource_TenantGroup",
		"TestAccDataSource_TenantMembers", "TestAccDataSource_CacheVolumeSummary",
		"TestAccDataSource_PersistentVolume", "TestAccDataSource_BuilderConfiguration",
		"TestAccDataSource_BuilderUsage", "TestAccDataSource_NetworkConfiguration",
		"TestAccDataSource_RegistryImage", "TestAccDataSource_RegistryImages",
		"TestAccDataSource_VaultSecretMetadata",
		"TestAccEphemeral_IDToken", "TestAccEphemeral_TenantToken",
		"TestAccEphemeral_ClientCertificate", "TestAccEphemeral_BazelCache",
		"TestAccEphemeral_GradleCache", "TestAccEphemeral_HTTPCache",
		"TestAccEphemeral_DownstreamConsumption", "TestAccEphemeral_CacheLifecycle_PlanAndDeferral",
	}

	var lines []string
	emit := func(name, action string, outLines []string) {
		pkg := "github.com/bsuv/terraform-provider-namespace/internal/provider"
		bRun, _ := json.Marshal(map[string]any{"Action": "run", "Test": name, "Package": pkg})
		lines = append(lines, string(bRun))
		for _, ol := range outLines {
			bOut, _ := json.Marshal(map[string]any{"Action": "output", "Test": name, "Package": pkg, "Output": ol + "\n"})
			lines = append(lines, string(bOut))
		}
		for _, extra := range injectOutputs[name] {
			bOut, _ := json.Marshal(map[string]any{"Action": "output", "Test": name, "Package": pkg, "Output": extra + "\n"})
			lines = append(lines, string(bOut))
		}
		bEnd, _ := json.Marshal(map[string]any{"Action": action, "Test": name, "Package": pkg, "Elapsed": 0.12})
		lines = append(lines, string(bEnd))
	}

	emitted := map[string]bool{}
	for _, c := range criticals {
		out := []string{
			fmt.Sprintf("OBSERVATION: RPC=%s Endpoint=%s Status=%s Scope=%s", c.rpc, c.endpoint, c.status, c.scope),
			"ASSERTION: " + c.assertion,
		}
		for _, ea := range c.extraAssertions {
			out = append(out, "ASSERTION: "+ea)
		}
		act := "pass"
		if failTest == c.name {
			act = "fail"
		}
		emit(c.name, act, out)
		emitted[c.name] = true
	}
	for assertion, tests := range attestations {
		for _, tn := range tests {
			if emitted[tn] {
				continue
			}
			act := "pass"
			if failTest == tn {
				act = "fail"
			}
			emit(tn, act, []string{
				"OBSERVATION: RPC=TestFixture.LiveCall Endpoint=Global Status=codes.OK Scope=Authorized acceptance evidence fixture",
				"ASSERTION: " + assertion,
			})
			emitted[tn] = true
		}
	}
	for _, tn := range requiredPublic {
		if emitted[tn] {
			continue
		}
		act := "pass"
		if failTest == tn {
			act = "fail"
		}
		emit(tn, act, []string{
			"OBSERVATION: RPC=TestFixture.LiveCall Endpoint=Global Status=codes.OK Scope=First-release public surface live verification",
		})
		emitted[tn] = true
	}
	if failTest != "" && !emitted[failTest] {
		emit(failTest, "fail", []string{
			"OBSERVATION: RPC=TestFixture.FailingCall Endpoint=Global Status=codes.PermissionDenied Scope=Injected failure",
		})
	}
	return strings.Join(lines, "\n") + "\n"
}

// =============================================================================
// TIER 1: ISOLATED FEATURE COVERAGE (>= 5 TESTS PER AUDIT FEATURE GROUP)
// =============================================================================

// --- Group 1: R1A & R1-BSR Dependencies & CI ---

func TestE2E_T1_R1A_GoModNoIntegrations(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	content := string(raw)
	if strings.Contains(content, "namespacelabs.dev/integrations") {
		t.Errorf("go.mod must not depend on namespacelabs.dev/integrations")
	}
	if strings.Contains(content, "github.com/golang-jwt/jwt/v4") {
		t.Errorf("go.mod must not retain github.com/golang-jwt/jwt/v4")
	}
}

func TestE2E_T1_R1A_GoSumNoIntegrations(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}
	content := string(raw)
	if strings.Contains(content, "namespacelabs.dev/integrations") {
		t.Errorf("go.sum must not contain namespacelabs.dev/integrations entries")
	}
	if strings.Contains(content, "github.com/golang-jwt/jwt/v4") {
		t.Errorf("go.sum must not contain github.com/golang-jwt/jwt/v4 entries")
	}
}

func TestE2E_T1_R1A_GoListModulesNoIntegrations(t *testing.T) {
	root := e2eRepoRoot(t)
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Version}}", "all")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m all failed: %v\n%s", err, string(out))
	}
	s := string(out)
	if strings.Contains(s, "namespacelabs.dev/integrations") {
		t.Errorf("module graph still includes namespacelabs.dev/integrations:\n%s", s)
	}
	if strings.Contains(s, "github.com/golang-jwt/jwt/v4") {
		t.Errorf("module graph still includes github.com/golang-jwt/jwt/v4")
	}
}

func TestE2E_T1_R1A_BSRModulesUpgraded(t *testing.T) {
	root := e2eRepoRoot(t)
	tsRe := regexp.MustCompile(`-(\d{14})-`)
	for _, mod := range []string{
		"buf.build/gen/go/namespace/cloud/protocolbuffers/go",
		"buf.build/gen/go/namespace/cloud/grpc/go",
	} {
		cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", mod)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -m %s failed: %v\n%s", mod, err, string(out))
		}
		ver := strings.TrimSpace(string(out))
		m := tsRe.FindStringSubmatch(ver)
		if len(m) != 2 {
			t.Fatalf("unexpected BSR pseudo-version format for %s: %q", mod, ver)
		}
		if m[1] < "20261106090801" {
			t.Errorf("module %s version %q has BSR timestamp %s < 20261106090801", mod, ver, m[1])
		}
	}
}

func TestE2E_T1_R1A_GoModVerifySucceeds(t *testing.T) {
	root := e2eRepoRoot(t)
	cmd := exec.Command("go", "mod", "verify")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go mod verify failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "all modules verified") {
		t.Errorf("expected 'all modules verified', got: %s", string(out))
	}
}

// --- Group 2: R1B volume_tag Removal, Positional cache_volume_settings, v0->v1 UpgradeState ---

func TestE2E_T1_R1B_SingleCacheVolumeLifecycle(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	var seenSpec *githubv1beta.RunnerProfileSpec
	origCreate := h.mockServer.Profile.CreateProfileFunc
	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		seenSpec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		return origCreate(ctx, req)
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "runner" {
  tag = "r1b-single"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb          = 50
      volume_reserved_size_gb = 5
      bundled_container_cache = true
      allow_commit_from_branch = ["main"]
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	if seenSpec == nil || len(seenSpec.GetCacheVolumeSettings()) != 1 {
		t.Fatalf("expected 1 CacheVolumeSettings in CreateProfile, got %+v", seenSpec)
	}
	if seenSpec.GetCacheVolumeSettings()[0].GetVolumeSizeGb() != 50 {
		t.Errorf("expected VolumeSizeGb=50, got %d", seenSpec.GetCacheVolumeSettings()[0].GetVolumeSizeGb())
	}
	st, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("state must not contain volume_tag attribute")
	}
	if len(st.Resources) != 1 || st.Resources[0].Instances[0].SchemaVersion < 1 {
		t.Errorf("expected schema_version >= 1 in state, got %+v", st.Resources)
	}
}

func TestE2E_T1_R1B_MultiItemPositionalAllowCommitFromBranch(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "runner" {
  tag = "r1b-positional"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb           = 40
      allow_commit_from_branch = []
    },
    {
      volume_size_gb = 60
    },
    {
      volume_size_gb           = 80
      allow_commit_from_branch = ["main", "release/*"]
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, _ := h.readState(t)
	cvs, ok := st.Resources[0].Instances[0].Attributes["cache_volume_settings"].([]any)
	if !ok || len(cvs) != 3 {
		t.Fatalf("expected 3 cache_volume_settings entries, got %#v", st.Resources[0].Instances[0].Attributes["cache_volume_settings"])
	}
	item0 := e2eMustMap(t, cvs[0], "cvs[0]")
	item1 := e2eMustMap(t, cvs[1], "cvs[1]")
	item2 := e2eMustMap(t, cvs[2], "cvs[2]")

	b0, ok0 := item0["allow_commit_from_branch"].([]any)
	if !ok0 || len(b0) != 0 {
		t.Errorf("index 0 expected empty slice [], got %#v", item0["allow_commit_from_branch"])
	}
	if item1["allow_commit_from_branch"] != nil {
		t.Errorf("index 1 expected null allow_commit_from_branch, got %#v", item1["allow_commit_from_branch"])
	}
	b2, ok2 := item2["allow_commit_from_branch"].([]any)
	if !ok2 || len(b2) != 2 || b2[0] != "main" || b2[1] != "release/*" {
		t.Errorf("index 2 expected [main release/*], got %#v", item2["allow_commit_from_branch"])
	}
}

func TestE2E_T1_R1B_ReorderAndUpdateCacheVolumesPositionally(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	step1 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_github_runner_profile" "runner" {
  tag = "r1b-reorder"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb           = 100
      allow_commit_from_branch = []
    },
    {
      volume_size_gb           = 200
      allow_commit_from_branch = ["main"]
    }
  ]
}
`
	step2 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_github_runner_profile" "runner" {
  tag = "r1b-reorder"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb           = 200
      allow_commit_from_branch = ["main", "dev"]
    },
    {
      volume_size_gb           = 100
      allow_commit_from_branch = []
    }
  ]
}
`
	h.writeMainTF(t, step1)
	h.mustInit(t)
	h.mustApply(t)

	h.writeMainTF(t, step2)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T1_R1B_DataSourceRunnerProfileOmitsVolumeTag(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "prof-ds-1",
					Version:   1,
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag: "ds-tag",
						Os:  "ubuntu-24.04",
						CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
							{VolumeSizeGb: 75, BundledBuildkitCache: true, AllowCommitFromBranch: []string{"main"}},
							{VolumeSizeGb: 150},
						},
					},
				},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_runner_profile" "one" {
  tag = "ds-tag"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	_, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("data.namespace_github_runner_profile state must not contain volume_tag")
	}
}

func TestE2E_T1_R1B_DataSourceRunnerProfilesOmitsVolumeTag(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "prof-list-b",
					Version:   1,
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag:                 "list-b",
						Os:                  "ubuntu-24.04",
						CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{{VolumeSizeGb: 40}},
					},
				},
				{
					ProfileId: "prof-list-a",
					Version:   1,
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag:                 "list-a",
						Os:                  "ubuntu-24.04",
						CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{{VolumeSizeGb: 80}},
					},
				},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_runner_profiles" "all" {}
`)
	h.mustInit(t)
	h.mustApply(t)
	_, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("data.namespace_github_runner_profiles state must not contain volume_tag")
	}
}

func TestE2E_T1_R1B_StateUpgradeV0ToV1StripsVolumeTag(t *testing.T) {
	// 1. Direct tfprotov6 UpgradeResourceState check
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)
	ps := providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)())()
	ctx := context.Background()

	schemaResp, err := ps.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	resSchema := schemaResp.ResourceSchemas["namespace_github_runner_profile"]
	if resSchema == nil || resSchema.Version < 1 {
		t.Fatalf("expected namespace_github_runner_profile schema version >= 1, got %+v", resSchema)
	}

	v0Payload := []byte(`{
		"id": "prof-v0-1",
		"profile_id": "prof-v0-1",
		"version": 1,
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"tag": "legacy-v0-tag",
		"os": "ubuntu-24.04",
		"description": "legacy",
		"emoji": "",
		"swap_size_mb": 0,
		"builder_mode": "BUILDER_MODE_UNSPECIFIED",
		"access_level": "ACCESS_LEVEL_LIMITED",
		"experimental_features": null,
		"instance_shape": null,
		"cache_volume_settings": [
			{
				"volume_tag": "legacy-v0",
				"volume_size_gb": 80,
				"volume_reserved_size_gb": 0,
				"bundled_container_cache": true,
				"bundled_buildkit_cache": false,
				"bundled_git_mirror": false,
				"bundled_runner_tools_cache": false,
				"bundled_runner_actions_cache": false,
				"allow_commit_from_branch": ["main"]
			}
		],
		"custom_runner_image_spec": null,
		"network_policy": null,
		"custom_runner_images": null
	}`)

	upResp, err := ps.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "namespace_github_runner_profile",
		Version:  0,
		RawState: &tfprotov6.RawState{JSON: v0Payload},
	})
	if err != nil {
		t.Fatalf("UpgradeResourceState RPC error: %v", err)
	}
	for _, d := range upResp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("UpgradeResourceState error diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}
	val, err := upResp.UpgradedState.Unmarshal(resSchema.ValueType())
	if err != nil {
		t.Fatalf("unmarshal upgraded state: %v", err)
	}
	var root map[string]tftypes.Value
	if err := val.As(&root); err != nil {
		t.Fatalf("extract root object: %v", err)
	}
	var gotTag string
	if err := root["tag"].As(&gotTag); err != nil || gotTag != "legacy-v0-tag" {
		t.Errorf("upgraded state tag=%q (err=%v), want %q", gotTag, err, "legacy-v0-tag")
	}
	var cvsVals []tftypes.Value
	if err := root["cache_volume_settings"].As(&cvsVals); err != nil || len(cvsVals) != 1 {
		t.Fatalf("extract upgraded cache_volume_settings: %v (len=%d)", err, len(cvsVals))
	}
	var cvMap map[string]tftypes.Value
	if err := cvsVals[0].As(&cvMap); err != nil {
		t.Fatalf("extract cv item: %v", err)
	}
	if _, hasVolTag := cvMap["volume_tag"]; hasVolTag {
		t.Errorf("upgraded state must not contain volume_tag")
	}
	var gotVolSizeBig *big.Float
	if err := cvMap["volume_size_gb"].As(&gotVolSizeBig); err != nil || gotVolSizeBig == nil {
		t.Errorf("upgraded volume_size_gb=%v (err=%v), want 80", gotVolSizeBig, err)
	} else if iv, acc := gotVolSizeBig.Int64(); acc != big.Exact || iv != 80 {
		t.Errorf("upgraded volume_size_gb=%s (int64=%d acc=%v), want 80", gotVolSizeBig.String(), iv, acc)
	}
	var gotReservedBig *big.Float
	if err := cvMap["volume_reserved_size_gb"].As(&gotReservedBig); err != nil || gotReservedBig == nil {
		t.Errorf("upgraded volume_reserved_size_gb=%v (err=%v), want 0", gotReservedBig, err)
	} else if iv, acc := gotReservedBig.Int64(); acc != big.Exact || iv != 0 {
		t.Errorf("upgraded volume_reserved_size_gb=%s (int64=%d acc=%v), want 0", gotReservedBig.String(), iv, acc)
	}
	var gotBundledContainer bool
	if err := cvMap["bundled_container_cache"].As(&gotBundledContainer); err != nil || !gotBundledContainer {
		t.Errorf("upgraded bundled_container_cache=%v (err=%v), want true", gotBundledContainer, err)
	}
	var gotBranches []tftypes.Value
	if err := cvMap["allow_commit_from_branch"].As(&gotBranches); err != nil || len(gotBranches) != 1 {
		t.Fatalf("upgraded allow_commit_from_branch len=%d (err=%v), want 1", len(gotBranches), err)
	}
	var gotBranch0 string
	if err := gotBranches[0].As(&gotBranch0); err != nil || gotBranch0 != "main" {
		t.Errorf("upgraded allow_commit_from_branch[0]=%q (err=%v), want %q", gotBranch0, err, "main")
	}

	// 2. Real Terraform CLI v0 state plan + refresh check
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "prof-v0-1",
					Version:   1,
					CreatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
					UpdatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag:         "legacy-v0-tag",
						Os:          "ubuntu-24.04",
						Description: "legacy",
						BuilderMode: githubv1beta.BuilderMode_BUILDER_MODE_UNSPECIFIED,
						AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_LIMITED,
						CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
							{
								VolumeSizeGb:          80,
								BundledContainerCache: true,
								AllowCommitFromBranch: []string{"main"},
							},
						},
					},
				},
			},
		}, nil
	}

	v0StateFile := fmt.Sprintf(`{
  "version": 4,
  "terraform_version": "1.15.6",
  "serial": 1,
  "lineage": "e2e-v0-state-migration",
  "outputs": {},
  "resources": [
    {
      "mode": "managed",
      "type": "namespace_github_runner_profile",
      "name": "runner",
      "provider": "provider[\"registry.terraform.io/bsuv/namespace\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": %s
        }
      ]
    }
  ]
}`, string(v0Payload))
	if err := os.WriteFile(filepath.Join(h.workDir, "terraform.tfstate"), []byte(v0StateFile), 0600); err != nil {
		t.Fatalf("write v0 terraform.tfstate: %v", err)
	}
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "runner" {
  tag          = "legacy-v0-tag"
  os           = "ubuntu-24.04"
  description  = "legacy"
  access_level = "ACCESS_LEVEL_LIMITED"
  cache_volume_settings = [
    {
      volume_size_gb           = 80
      bundled_container_cache  = true
      allow_commit_from_branch = ["main"]
    }
  ]
}
`)
	h.mustInit(t)
	if out, serr, err := h.runTerraform(t, nil, "plan", "-refresh=false", "-detailed-exitcode"); err != nil {
		t.Fatalf("expected 0-diff terraform plan -refresh=false after v0->v1 upgrade, got %v\nStdout:\n%s\nStderr:\n%s", err, out, serr)
	}
	h.mustPlanNoDiff(t)
	if out, serr, err := h.runTerraform(t, nil, "refresh"); err != nil {
		t.Fatalf("terraform refresh failed: %v\nStdout:\n%s\nStderr:\n%s", err, out, serr)
	}
	st, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("refreshed state still contains volume_tag")
	}
	if st.Resources[0].Instances[0].SchemaVersion != 1 {
		t.Errorf("expected schema_version 1 after refresh, got %d", st.Resources[0].Instances[0].SchemaVersion)
	}
}

// --- Group 3: R1C ACCESS_LEVEL_CUSTOM & custom_permissions ---

func TestE2E_T1_R1C_CustomAccessLevelAndPermissionsLifecycle(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	var capturedSpec *githubv1beta.RunnerProfileSpec
	origCreate := h.mockServer.Profile.CreateProfileFunc
	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		capturedSpec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		return origCreate(ctx, req)
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "custom" {
  tag          = "r1c-custom"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "sec_1"
      actions       = ["read"]
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	if capturedSpec == nil {
		t.Fatal("CreateProfile was not called")
	}
	if int32(capturedSpec.GetAccessLevel()) != 4 {
		t.Errorf("expected AccessLevel enum 4 (ACCESS_LEVEL_CUSTOM), got %d", int32(capturedSpec.GetAccessLevel()))
	}
	perms := e2eProtoGetCustomPermissions(t, capturedSpec)
	if len(perms) != 1 || perms[0].GetResourceType() != "vault/object" || perms[0].GetResourceId() != "sec_1" || len(perms[0].GetActions()) != 1 || perms[0].GetActions()[0] != "read" {
		t.Errorf("unexpected custom_permissions forwarded: %+v", perms)
	}

	st, _ := h.readState(t)
	attrs := st.Resources[0].Instances[0].Attributes
	if attrs["access_level"] != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("expected state access_level=ACCESS_LEVEL_CUSTOM, got %v", attrs["access_level"])
	}
}

func TestE2E_T1_R1C_TransitionAccessLevelsAndPermissions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	var lastUpdateSpec *githubv1beta.RunnerProfileSpec
	origUpdate := h.mockServer.Profile.UpdateProfileFunc
	h.mockServer.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		lastUpdateSpec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		return origUpdate(ctx, req)
	}

	step1 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_github_runner_profile" "custom" {
  tag          = "r1c-trans"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_LIMITED"
}
`
	step2 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_github_runner_profile" "custom" {
  tag          = "r1c-trans"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "cache/bazel"
      resource_id   = "ci"
      actions       = ["read", "write"]
    }
  ]
}
`
	step3 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_github_runner_profile" "custom" {
  tag          = "r1c-trans"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_RESTRICTED"
}
`
	h.writeMainTF(t, step1)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	h.writeMainTF(t, step2)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
	if len(e2eProtoGetCustomPermissions(t, lastUpdateSpec)) != 1 {
		t.Errorf("expected 1 custom_permission in UpdateProfileRequest after step 2, got %+v", lastUpdateSpec)
	}

	h.writeMainTF(t, step3)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	if lastUpdateSpec == nil || lastUpdateSpec.GetAccessLevel() != githubv1beta.AccessLevel_ACCESS_LEVEL_RESTRICTED {
		t.Errorf("expected UpdateProfileRequest access_level=ACCESS_LEVEL_RESTRICTED after step 3, got %+v", lastUpdateSpec)
	}
	if cleared := e2eProtoGetCustomPermissions(t, lastUpdateSpec); len(cleared) != 0 {
		t.Errorf("expected custom_permissions cleared in UpdateProfileRequest after step 3, got %+v", cleared)
	}

	st, _ := h.readState(t)
	attrs := st.Resources[0].Instances[0].Attributes
	if attrs["access_level"] != "ACCESS_LEVEL_RESTRICTED" {
		t.Errorf("expected ACCESS_LEVEL_RESTRICTED after step 3, got %v", attrs["access_level"])
	}
	if cp := attrs["custom_permissions"]; cp != nil {
		if slice, ok := cp.([]any); !ok || len(slice) != 0 {
			t.Errorf("expected custom_permissions cleared (nil or []) in state after step 3, got %#v", cp)
		}
	}
}

func TestE2E_T1_R1C_MultiEntryCustomPermissionsEmptyResourceID(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "custom" {
  tag          = "r1c-multi"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "containerregistry/repository"
      resource_id   = ""
      actions       = ["list", "pull"]
    },
    {
      resource_type = "cache/bazel"
      resource_id   = "ci"
      actions       = ["read", "write"]
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, _ := h.readState(t)
	cp := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["custom_permissions"], "custom_permissions")
	if len(cp) != 2 {
		t.Fatalf("expected 2 custom_permissions entries, got %d", len(cp))
	}
	first := e2eMustMap(t, cp[0], "custom_permissions[0]")
	if first["resource_id"] != "" {
		t.Errorf("expected empty resource_id preserved, got %#v", first["resource_id"])
	}
}

func TestE2E_T1_R1C_DataSourceRunnerProfileCustomPermissions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		spec := &githubv1beta.RunnerProfileSpec{
			Tag:         "ds-custom-tag",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel(4),
		}
		e2eProtoSetCustomPermissions(t, spec, []*iamv1beta.Permission{
			{ResourceType: "vault/object", ResourceId: "secret-a", Actions: []string{"read"}},
		})
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{ProfileId: "prof-custom-ds", Version: 1, Spec: spec},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_runner_profile" "custom" {
  tag = "ds-custom-tag"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	attrs := st.Resources[0].Instances[0].Attributes
	if attrs["access_level"] != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("expected ACCESS_LEVEL_CUSTOM, got %v", attrs["access_level"])
	}
	cp, ok := attrs["custom_permissions"].([]any)
	if !ok || len(cp) != 1 {
		t.Fatalf("expected 1 custom_permissions entry, got %#v", attrs["custom_permissions"])
	}
}

func TestE2E_T1_R1C_DataSourceRunnerProfilesCustomPermissions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		spec1 := &githubv1beta.RunnerProfileSpec{
			Tag:         "custom-one",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel(4),
		}
		e2eProtoSetCustomPermissions(t, spec1, []*iamv1beta.Permission{
			{ResourceType: "github/runner-profile", ResourceId: "", Actions: []string{"read"}},
		})
		spec2 := &githubv1beta.RunnerProfileSpec{
			Tag:         "perm-two",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_PERMISSIVE,
		}
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{ProfileId: "prof-1", Version: 1, Spec: spec1},
				{ProfileId: "prof-2", Version: 1, Spec: spec2},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_runner_profiles" "all" {}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	profiles := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["profiles"], "profiles")
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	p0 := e2eMustMap(t, profiles[0], "profiles[0]")
	if p0["access_level"] != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("expected first profile ACCESS_LEVEL_CUSTOM, got %v", p0["access_level"])
	}
}

func TestE2E_T1_R1C_ImportPreservesAccessLevelCustomAndPermissions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		spec := &githubv1beta.RunnerProfileSpec{
			Tag:         "imported-custom",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel(4),
		}
		e2eProtoSetCustomPermissions(t, spec, []*iamv1beta.Permission{
			{ResourceType: "vault/object", ResourceId: "k1", Actions: []string{"read"}},
		})
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{ProfileId: "prof-import-4", Version: 2, Spec: spec},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "imported" {
  tag          = "imported-custom"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "k1"
      actions       = ["read"]
    }
  ]
}
`)
	h.mustInit(t)
	if out, serr, err := h.runTerraform(t, nil, "import", "namespace_github_runner_profile.imported", "prof-import-4"); err != nil {
		t.Fatalf("terraform import failed: %v\nStdout:\n%s\nStderr:\n%s", err, out, serr)
	}
	st, _ := h.readState(t)
	attrs := st.Resources[0].Instances[0].Attributes
	if attrs["access_level"] != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("imported access_level expected ACCESS_LEVEL_CUSTOM, got %v", attrs["access_level"])
	}
	cp := e2eMustSlice(t, attrs["custom_permissions"], "imported custom_permissions")
	if len(cp) != 1 {
		t.Errorf("expected 1 imported custom_permissions item, got %#v", cp)
	}
	h.mustPlanNoDiff(t)
}

// --- Group 4: R2-OS Windows OS & Descriptions ---

func TestE2E_T1_R2OS_RunnerProfileWindowsOSLifecycle(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	var seenOS string
	origCreate := h.mockServer.Profile.CreateProfileFunc
	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		seenOS = req.GetSpec().GetInstanceShape().GetOs()
		return origCreate(ctx, req)
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "win" {
  tag = "win-runner"
  os  = "windows-2022"
  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 16384
    machine_arch     = "amd64"
    os               = "windows"
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
	if seenOS != "windows" {
		t.Errorf("expected InstanceShape.Os='windows', got %q", seenOS)
	}
}

func TestE2E_T1_R2OS_RunnerProfileOSTransitions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	tmpl := func(osVal string) string {
		return e2eProviderHCLHeader(e2eRuntimeProviderToken()) + fmt.Sprintf(`
resource "namespace_github_runner_profile" "os_trans" {
  tag = "os-trans"
  os  = "ubuntu-24.04"
  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = %q
  }
}
`, osVal)
	}

	h.writeMainTF(t, tmpl("linux"))
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	h.writeMainTF(t, tmpl("windows"))
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	h.writeMainTF(t, tmpl(""))
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T1_R2OS_DataSourceBaseImagesWindows(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{
					Label:    "windows-2022",
					ImageRef: "nscr.io/base/windows-2022:latest",
					Title:    "Windows Server 2022",
					Spec: &imagesv1beta.BaseImage{
						Id:           "win-2022-id",
						Os:           "windows",
						OsName:       "Windows Server",
						OsVersion:    "2022",
						ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
					},
				},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_base_images" "win" {
  labels = ["windows-2022"]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	imgs := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["images"], "base images")
	img0 := e2eMustMap(t, imgs[0], "images[0]")
	spec := e2eMustMap(t, img0["spec"], "images[0].spec")
	if spec["os"] != "windows" {
		t.Errorf("expected images[0].spec.os='windows', got %v", spec["os"])
	}
}

func TestE2E_T1_R2OS_DataSourceBuilderConfigurationWindows(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		return &builderv1beta.GetBuilderConfigurationResponse{
			BuildkitEndpoint:     "buildkit.namespaceapis.com:443",
			FullBuildkitEndpoint: "grpcs://buildkit.namespaceapis.com:443",
			CacheSizeMb:          51200,
			Shape: &computev1beta.InstanceShape{
				VirtualCpu:      8,
				MemoryMegabytes: 32768,
				MachineArch:     "amd64",
				Os:              "windows",
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_builder_configuration" "win" {
  platform = "windows/amd64"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	shape := e2eMustMap(t, st.Resources[0].Instances[0].Attributes["shape"], "builder shape")
	if shape["os"] != "windows" {
		t.Errorf("expected builder shape.os='windows', got %v", shape["os"])
	}
}

func TestE2E_T1_R2OS_SchemaDescriptionsIncludeWindows(t *testing.T) {
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)
	ps := providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)())()
	resp, err := ps.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	findAttr := func(attrs []*tfprotov6.SchemaAttribute, name string) *tfprotov6.SchemaAttribute {
		for _, a := range attrs {
			if a.Name == name {
				return a
			}
		}
		return nil
	}
	mustNestedAttrs := func(attrs []*tfprotov6.SchemaAttribute, name, path string) []*tfprotov6.SchemaAttribute {
		a := findAttr(attrs, name)
		if a == nil {
			t.Fatalf("missing schema attribute %s.%s", path, name)
		}
		if a.NestedType == nil {
			t.Fatalf("schema attribute %s.%s has nil NestedType", path, name)
		}
		return a.NestedType.Attributes
	}
	mustResAttrs := func(name string) []*tfprotov6.SchemaAttribute {
		s := resp.ResourceSchemas[name]
		if s == nil || s.Block == nil {
			t.Fatalf("missing resource schema %q", name)
		}
		return s.Block.Attributes
	}
	mustDSAttrs := func(name string) []*tfprotov6.SchemaAttribute {
		s := resp.DataSourceSchemas[name]
		if s == nil || s.Block == nil {
			t.Fatalf("missing data source schema %q", name)
		}
		return s.Block.Attributes
	}

	checks := []struct {
		label string
		attr  *tfprotov6.SchemaAttribute
	}{
		{
			"resource.namespace_github_runner_profile.instance_shape.os",
			findAttr(mustNestedAttrs(mustResAttrs("namespace_github_runner_profile"), "instance_shape", "resource.namespace_github_runner_profile"), "os"),
		},
		{
			"data.namespace_github_runner_profile.instance_shape.os",
			findAttr(mustNestedAttrs(mustDSAttrs("namespace_github_runner_profile"), "instance_shape", "data.namespace_github_runner_profile"), "os"),
		},
		{
			"data.namespace_github_runner_profiles.profiles.instance_shape.os",
			findAttr(mustNestedAttrs(mustNestedAttrs(mustDSAttrs("namespace_github_runner_profiles"), "profiles", "data.namespace_github_runner_profiles"), "instance_shape", "data.namespace_github_runner_profiles.profiles"), "os"),
		},
		{
			"data.namespace_github_base_images.images.spec.os",
			findAttr(mustNestedAttrs(mustNestedAttrs(mustDSAttrs("namespace_github_base_images"), "images", "data.namespace_github_base_images"), "spec", "data.namespace_github_base_images.images"), "os"),
		},
		{
			"data.namespace_builder_configuration.shape.os",
			findAttr(mustNestedAttrs(mustDSAttrs("namespace_builder_configuration"), "shape", "data.namespace_builder_configuration"), "os"),
		},
	}

	for _, c := range checks {
		if c.attr == nil {
			t.Errorf("missing schema attribute for %s", c.label)
			continue
		}
		if !strings.Contains(strings.ToLower(c.attr.Description), "windows") {
			t.Errorf("%s description %q does not document 'windows'", c.label, c.attr.Description)
		}
	}
}

// --- Group 5: R2-Redact & Secret Scan / Report Scripts ---

func TestE2E_T1_R2Redact_OpaqueNSRT(t *testing.T) {
	tok := e2eRuntimeNSRT("opaque_revokable")
	cases := []string{
		"Bearer " + tok,
		"token=" + tok,
		tok,
	}
	for _, in := range cases {
		out := redact.Secrets(in)
		if strings.Contains(out, tok) || strings.Contains(out, "opaque_revokable") {
			t.Errorf("redact.Secrets(%q) leaked nsrt_ token: %q", in, out)
		}
		if !strings.Contains(out, "[REDACTED TOKEN]") {
			t.Errorf("expected [REDACTED TOKEN] in %q", out)
		}
		if redact.Secrets(out) != out {
			t.Errorf("redact.Secrets not idempotent on %q", out)
		}
	}
}

func TestE2E_T1_R2Redact_NSCTokenSpecEnvVars(t *testing.T) {
	in := `NSC_TOKEN_SPEC=raw_spec_secret_val NSC_TOKEN_SPEC='{"secret":"val"}' NSC_TOKEN_SPEC_FILE=/tmp/spec.json NSC_ENDPOINT=https://iam.namespaceapis.com`
	out := redact.Secrets(in)
	if strings.Contains(out, "raw_spec_secret_val") || strings.Contains(out, `"secret":"val"`) || strings.Contains(out, "/tmp/spec.json") {
		t.Errorf("NSC_TOKEN_SPEC* not redacted: %q", out)
	}
	if strings.Count(out, "NSC_TOKEN_SPEC=[REDACTED]") != 2 {
		t.Errorf("expected both unquoted and quoted NSC_TOKEN_SPEC=[REDACTED], got %q", out)
	}
	if !strings.Contains(out, "NSC_TOKEN_SPEC_FILE=[REDACTED]") {
		t.Errorf("expected NSC_TOKEN_SPEC_FILE=[REDACTED], got %q", out)
	}
	if !strings.Contains(out, "NSC_ENDPOINT=https://iam.namespaceapis.com") {
		t.Errorf("non-secret NSC_ENDPOINT was altered: %q", out)
	}
}

func TestE2E_T1_R2Redact_PrefixedJWT_OIDC(t *testing.T) {
	jwt := e2eRuntimeJWT("oidc_", "sub-oidc-1")
	for _, in := range []string{
		jwt,
		"rpc failed with token " + jwt,
		fmt.Sprintf(`{"id_token":%q}`, jwt),
	} {
		out := redact.Secrets(in)
		if strings.Contains(out, "sig_e2e_test_payload_9876543210") {
			t.Errorf("prefixed OIDC JWT leaked in %q -> %q", in, out)
		}
	}
}

func TestE2E_T1_R2Redact_PrefixedJWT_CognitoAndCustom(t *testing.T) {
	cognito := e2eRuntimeJWT("cognito_", "sub-cog-1")
	custom := e2eRuntimeJWT("custom_idp_", "sub-cust-1")
	for _, tok := range []string{cognito, custom} {
		out := redact.Secrets("header " + tok + " trailer")
		if strings.Contains(out, "sig_e2e_test_payload_9876543210") {
			t.Errorf("prefixed JWT leaked: %q", out)
		}
		if redact.Secrets(out) != out {
			t.Errorf("not idempotent on %q", out)
		}
	}
}

func TestE2E_T1_R2Redact_TerraformCLIDiagnosticsScrubsSecrets(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	nsrt := e2eRuntimeNSRT("cli_diag")
	oidc := e2eRuntimeJWT("oidc_", "cli-diag-sub")

	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		msg := fmt.Sprintf("denied using %s and %s with NSC_TOKEN_SPEC=raw_spec_secret_99", nsrt, oidc)
		return nil, status.Error(codes.PermissionDenied, msg)
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "fail" {
  tag = "redact-cli"
  os  = "ubuntu-24.04"
}
`)
	h.mustInit(t)
	stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
	if err == nil {
		t.Fatal("expected terraform apply to fail with PermissionDenied")
	}
	combined := stdout + "\n" + stderr
	for _, secret := range []string{nsrt, oidc, "raw_spec_secret_99", "sig_e2e_test_payload_9876543210"} {
		if strings.Contains(combined, secret) {
			t.Errorf("Terraform CLI output leaked raw secret %q:\n%s", secret, combined)
		}
	}
}

func TestE2E_T1_R2Redact_ConcurrencySafety(t *testing.T) {
	nsrt := e2eRuntimeNSRT("conc")
	oidc := e2eRuntimeJWT("oidc_", "conc-1")
	cog := e2eRuntimeJWT("cognito_", "conc-2")
	samples := []string{
		"tok=" + nsrt + " NSC_TOKEN_SPEC=abc123 " + oidc,
		"NSC_TOKEN_SPEC_FILE=/var/run/spec.json " + cog,
		"benign status=ok region=eu-central",
	}

	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				in := samples[(id+i)%len(samples)]
				out := redact.Secrets(in)
				if strings.Contains(out, nsrt) || strings.Contains(out, "abc123") || strings.Contains(out, "sig_e2e_test_payload") {
					t.Errorf("leaked secret under concurrency: %q", out)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestE2E_T1_R2Scan_RepoSecretScanCleanPass(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "gitleaks")
	cmd := exec.Command("bash", "./scripts/secret-scan.sh")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scripts/secret-scan.sh failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "Secret scan passed.") {
		t.Errorf("expected 'Secret scan passed.' in output, got: %s", string(out))
	}
}

func TestE2E_T1_R2Scan_ReportAdversarialSuitePasses(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "python3")
	cmd := exec.Command("bash", "./scripts/test_report_adversarial.sh")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scripts/test_report_adversarial.sh failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "OK") {
		t.Errorf("expected OK summary from test_report_adversarial.sh, got: %s", string(out))
	}
}

// --- Group 6: R2-Errors PermissionDeniedError & detailsStr Preservation ---

func TestE2E_T1_R2Err_PermissionDeniedMissingAccessCLI(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		st, err := status.New(codes.PermissionDenied, "caller lacks access").WithDetails(&iamv1beta.PermissionDeniedError{
			MissingAccessPermissions: []*iamv1beta.Access{
				{ResourceType: "github/runner-profile", ResourceId: "prof_1", Action: "create"},
			},
		})
		if err != nil {
			return nil, err
		}
		return nil, st.Err()
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "denied" {
  tag = "err-denied"
  os  = "ubuntu-24.04"
}
`)
	h.mustInit(t)
	stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
	if err == nil {
		t.Fatal("expected PermissionDenied apply error")
	}
	combined := stdout + "\n" + stderr
	for _, want := range []string{"Authorization Failure", "github/runner-profile", "prof_1", "create"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in CLI error output, got:\n%s", want, combined)
		}
	}
}

func TestE2E_T1_R2Err_UnauthenticatedPreservesErrorInfoDetails(t *testing.T) {
	st, err := status.New(codes.Unauthenticated, "token expired").WithDetails(&errdetails.ErrorInfo{
		Reason: "TOKEN_EXPIRED",
		Domain: "iam.namespaceapis.com",
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "read", "namespace_github_runner_profile")
	if !diags.HasError() {
		t.Fatal("expected error diagnostic")
	}
	d := diags[0]
	if d.Summary() != "Authorization Failure during read namespace_github_runner_profile" {
		t.Errorf("unexpected summary: %q", d.Summary())
	}
	for _, want := range []string{"TOKEN_EXPIRED", "iam.namespaceapis.com"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("expected %q in detail %q", want, d.Detail())
		}
	}
}

func TestE2E_T1_R2Err_FailedPreconditionRevisionConflictPreservesDetails(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "revision mismatch on profile").WithDetails(&errdetails.ErrorInfo{
		Reason: "REVISION_MISMATCH",
		Domain: "github.namespaceapis.com",
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "update", "namespace_github_runner_profile")
	if !diags.HasError() {
		t.Fatal("expected error diagnostic")
	}
	d := diags[0]
	if !strings.Contains(d.Summary(), "Concurrency Conflict") {
		t.Errorf("expected Concurrency Conflict summary, got %q", d.Summary())
	}
	for _, want := range []string{"terraform refresh", "REVISION_MISMATCH", "github.namespaceapis.com"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("expected %q in detail %q", want, d.Detail())
		}
	}
}

func TestE2E_T1_R2Err_AbortedVersionConflictPreservesDetails(t *testing.T) {
	st, err := status.New(codes.Aborted, "version conflict").WithDetails(&errdetails.ErrorInfo{
		Reason:   "VERSION_MISMATCH",
		Domain:   "registry.namespaceapis.com",
		Metadata: map[string]string{"expected": "3", "actual": "2"},
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "update", "namespace_registry_repository_policy")
	if !diags.HasError() {
		t.Fatal("expected error diagnostic")
	}
	d := diags[0]
	if !strings.Contains(d.Summary(), "Concurrency Conflict") {
		t.Errorf("expected Concurrency Conflict summary, got %q", d.Summary())
	}
	for _, want := range []string{"VERSION_MISMATCH", "expected=3", "actual=2"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("expected %q in detail %q", want, d.Detail())
		}
	}
}

func TestE2E_T1_R2Err_AlreadyExistsStandardAndSharedImagePreservesDetails(t *testing.T) {
	st, err := status.New(codes.AlreadyExists, "duplicate tag").WithDetails(&errdetails.ErrorInfo{
		Reason: "DUPLICATE_TAG",
		Domain: "namespace.cloud",
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	for _, resName := range []string{"namespace_github_runner_profile", "namespace_registry_shared_image"} {
		var diags diag.Diagnostics
		client.HandleGRPCError(context.Background(), st.Err(), &diags, "create", resName)
		if !diags.HasError() {
			t.Fatalf("%s: expected error diagnostic", resName)
		}
		if !strings.Contains(diags[0].Detail(), "DUPLICATE_TAG") {
			t.Errorf("%s: expected DUPLICATE_TAG detailsStr preserved in %q", resName, diags[0].Detail())
		}
	}
}

func TestE2E_T1_R2Err_MultipleMissingAccessPermissions(t *testing.T) {
	st, err := status.New(codes.PermissionDenied, "multi denied").WithDetails(&iamv1beta.PermissionDeniedError{
		MissingAccessPermissions: []*iamv1beta.Access{
			{ResourceType: "github/runner-profile", ResourceId: "prof-a", Action: "create"},
			{ResourceType: "vault/object", ResourceId: "", Action: "read"},
			{ResourceType: "cache/bazel", ResourceId: "ci", Action: "ensure"},
		},
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "create", "namespace_github_runner_profile")
	if !diags.HasError() {
		t.Fatal("expected error diagnostic")
	}
	detail := diags[0].Detail()
	for _, want := range []string{"github/runner-profile", "prof-a", "vault/object", "cache/bazel", "ci", "ensure"} {
		if !strings.Contains(detail, want) {
			t.Errorf("expected %q in detail %q", want, detail)
		}
	}
}

// --- Group 7: R2-Auth Windows Workload Creds, Unix Path, nsrt_, st_ Exchange ---

func TestE2E_T1_R2Auth_NSRTInlineTokenNoStatProbeAndBearerAuth(t *testing.T) {
	e2eInitBufconnTransport(t)
	tmp := t.TempDir()
	trapName := e2eRuntimeNSRT("file_trap")
	trapPath := filepath.Join(tmp, trapName)
	validJWT := e2eRuntimeJWT("nsct_", "trap-tenant")
	if err := os.WriteFile(trapPath, []byte(validJWT), 0600); err != nil {
		t.Fatalf("write trap file: %v", err)
	}
	t.Chdir(tmp)

	// Must recognize nsrt_ as inline token (never read trap file) and fail closed as opaque.
	if _, err := client.ResolveTenantFromToken(trapName); err == nil {
		t.Fatalf("expected opaque nsrt_ token %q to fail closed rather than probing filesystem trap", trapName)
	}

	// Passing nsrt_ to client.New sends Authorization: Bearer nsrt_...
	srv := testutil.NewMockServer()
	var seenAuth string
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get("authorization"); len(vals) > 0 {
				seenAuth = vals[0]
			}
		}
		return &githubv1beta.ListProfilesResponse{}, nil
	}
	srv.Start()
	defer srv.Stop()

	cl, err := client.New(context.Background(), client.Config{
		Token:           trapName,
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(srv.Dialer())},
	})
	if err != nil {
		t.Fatalf("client.New with nsrt_ failed: %v", err)
	}
	defer func() { _ = cl.Close() }()

	pc, err := cl.ProfileClient(context.Background())
	if err != nil {
		t.Fatalf("ProfileClient: %v", err)
	}
	if _, err := pc.ListProfiles(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if seenAuth != "Bearer "+trapName {
		t.Errorf("expected Bearer %s, got %q", trapName, seenAuth)
	}
}

func TestE2E_T1_R2Auth_InlineSTTokenInConfigExchangesViaUserSessions(t *testing.T) {
	e2eInitBufconnTransport(t)
	stTok := e2eRuntimeST("cfg")
	exchanged := e2eRuntimeJWT("nsct_", "exchanged-cfg")

	sessSrv := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchanged}, nil
		},
	}
	var downstreamAuth string
	dialer := e2eStartSessionAndProfileServer(t, sessSrv, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
		downstreamAuth = authHeader
		return &githubv1beta.ListProfilesResponse{}, nil
	})

	cl, err := client.New(context.Background(), client.Config{
		Token:           stTok,
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(dialer)},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer func() { _ = cl.Close() }()

	pc, err := cl.ProfileClient(context.Background())
	if err != nil {
		t.Fatalf("ProfileClient: %v", err)
	}
	if _, err := pc.ListProfiles(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if downstreamAuth != "Bearer "+exchanged {
		t.Errorf("expected downstream Bearer %s after inline st_ exchange, got %q", exchanged, downstreamAuth)
	}
}

func TestE2E_T1_R2Auth_InlineSTTokenInEnvExchangesViaUserSessions(t *testing.T) {
	e2eInitBufconnTransport(t)
	stTok := e2eRuntimeST("env")
	exchanged := e2eRuntimeJWT("nsct_", "exchanged-env")

	t.Setenv("NSC_TOKEN", stTok)
	t.Setenv("NSC_TOKEN_FILE", "")

	sessSrv := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchanged}, nil
		},
	}
	var downstreamAuth string
	dialer := e2eStartSessionAndProfileServer(t, sessSrv, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
		downstreamAuth = authHeader
		return &githubv1beta.ListProfilesResponse{}, nil
	})

	cl, err := client.New(context.Background(), client.Config{
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(dialer)},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer func() { _ = cl.Close() }()

	pc, err := cl.ProfileClient(context.Background())
	if err != nil {
		t.Fatalf("ProfileClient: %v", err)
	}
	if _, err := pc.ListProfiles(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if downstreamAuth != "Bearer "+exchanged {
		t.Errorf("expected downstream Bearer %s after NSC_TOKEN st_ exchange, got %q", exchanged, downstreamAuth)
	}
}

func TestE2E_T1_R2Auth_RawSTFileInTokenFileAndEnvExchangesViaUserSessions(t *testing.T) {
	e2eInitBufconnTransport(t)
	stTok := e2eRuntimeST("rawfile")
	exchanged := e2eRuntimeJWT("nsct_", "exchanged-rawfile")

	tokPath := filepath.Join(t.TempDir(), "raw_st_token")
	if err := os.WriteFile(tokPath, []byte(stTok+"\n"), 0600); err != nil {
		t.Fatalf("write raw st_ file: %v", err)
	}

	for _, mode := range []string{"TokenFile", "NSC_TOKEN_FILE"} {
		t.Run(mode, func(t *testing.T) {
			sessSrv := &e2eTestUserSessionsServer{
				handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
					return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchanged}, nil
				},
			}
			var downstreamAuth string
			dialer := e2eStartSessionAndProfileServer(t, sessSrv, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
				downstreamAuth = authHeader
				return &githubv1beta.ListProfilesResponse{}, nil
			})

			cfg := client.Config{
				GlobalEndpoint:  "https://global.namespaceapis.com:443",
				IAMEndpoint:     "https://iam.namespaceapis.com:443",
				ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
				Creds:           insecure.NewCredentials(),
				DialOptions:     []grpc.DialOption{grpc.WithContextDialer(dialer)},
			}
			t.Setenv("NSC_TOKEN", "")
			if mode == "TokenFile" {
				t.Setenv("NSC_TOKEN_FILE", "")
				cfg.TokenFile = tokPath
			} else {
				t.Setenv("NSC_TOKEN_FILE", tokPath)
			}

			cl, err := client.New(context.Background(), cfg)
			if err != nil {
				t.Fatalf("client.New (%s) failed on raw st_ file: %v", mode, err)
			}
			defer func() { _ = cl.Close() }()

			pc, err := cl.ProfileClient(context.Background())
			if err != nil {
				t.Fatalf("ProfileClient: %v", err)
			}
			if _, err := pc.ListProfiles(context.Background(), &emptypb.Empty{}); err != nil {
				t.Fatalf("ListProfiles: %v", err)
			}
			if downstreamAuth != "Bearer "+exchanged {
				t.Errorf("%s: expected Bearer %s, got %q", mode, exchanged, downstreamAuth)
			}
		})
	}
}

func TestE2E_T1_R2Auth_UnixDefaultUserConfigResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix default credential path test only applies to non-Windows hosts")
	}
	if _, err := os.Stat("/var/run/nsc/token.json"); err == nil {
		t.Skip("/var/run/nsc/token.json exists on host; skipping isolated user config fallback test")
	}

	fakeHome := t.TempDir()
	fakeXDG := filepath.Join(fakeHome, ".config")
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", fakeXDG)
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")

	userCfgDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir: %v", err)
	}
	nsDir := filepath.Join(userCfgDir, "ns")
	if err := os.MkdirAll(nsDir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", nsDir, err)
	}
	expectedTok := e2eRuntimeJWT("nsct_", "unix-fallback")
	payload, _ := json.Marshal(map[string]string{"bearer_token": expectedTok})
	if err := os.WriteFile(filepath.Join(nsDir, "token.json"), payload, 0600); err != nil {
		t.Fatalf("write token.json: %v", err)
	}

	ts, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{})
	if err != nil {
		t.Fatalf("ResolveTokenSource failed: %v", err)
	}
	gotTok, err := ts.IssueToken(context.Background(), time.Minute, false)
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}
	if gotTok != expectedTok {
		t.Errorf("expected %q, got %q", expectedTok, gotTok)
	}
}

func TestE2E_T1_R2Auth_WindowsMetadataDirAndCrossBuild(t *testing.T) {
	root := e2eRepoRoot(t)
	// Verify Windows target cross-builds cleanly from any host OS.
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GOOS=windows go build ./... failed: %v\n%s", err, string(out))
	}

	if runtime.GOOS != "windows" {
		return
	}

	metaDir := t.TempDir()
	progData := t.TempDir()
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")
	t.Setenv("NSC_METADATA_DIR", metaDir)
	t.Setenv("ProgramData", progData)

	metaTok := e2eRuntimeJWT("nsct_", "win-meta")
	metaJSON, _ := json.Marshal(map[string]string{"bearer_token": metaTok})
	if err := os.WriteFile(filepath.Join(metaDir, "token.json"), metaJSON, 0600); err != nil {
		t.Fatalf("write NSC_METADATA_DIR token.json: %v", err)
	}
	progPath := filepath.Join(progData, "Namespace", "GuestAgent", "metadata", "token.json")
	if err := os.MkdirAll(filepath.Dir(progPath), 0700); err != nil {
		t.Fatalf("mkdir ProgramData metadata dir: %v", err)
	}
	progTok := e2eRuntimeJWT("nsct_", "win-progdata")
	progJSON, _ := json.Marshal(map[string]string{"bearer_token": progTok})
	if err := os.WriteFile(progPath, progJSON, 0600); err != nil {
		t.Fatalf("write ProgramData token.json: %v", err)
	}

	ts, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{})
	if err != nil {
		t.Fatalf("ResolveTokenSource on Windows failed: %v", err)
	}
	got, err := ts.IssueToken(context.Background(), time.Minute, false)
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}
	if got != metaTok {
		t.Errorf("expected NSC_METADATA_DIR precedence %q, got %q", metaTok, got)
	}
}

// --- Group 8: R3-Registry deletion_filter, expires_at, deleted_at, labels ---

func TestE2E_T1_R3Reg_DefaultOmitsDeletionFilter(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var seenFilter int32 = -1
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		seenFilter = e2eProtoGetListImagesDeletionFilter(t, req)
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "app/web",
					Digest:     "sha256:aaa111",
					CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
					Tags:       []string{"v1"},
				},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "imgs" {
  repository = "app/web"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if seenFilter != 0 {
		t.Errorf("expected default deletion_filter=0 (UNSPECIFIED), got %d", seenFilter)
	}
}

func TestE2E_T1_R3Reg_ExplicitExcludeAndIncludeDeleted(t *testing.T) {
	for _, tc := range []struct {
		filterStr   string
		wantNonZero bool
	}{
		{"INCLUDE_DELETED", true},
		{"EXCLUDE_DELETED", true},
		{"DELETION_FILTER_UNSPECIFIED", false},
	} {
		t.Run(tc.filterStr, func(t *testing.T) {
			h := e2eStartBlackBoxHarness(t)
			var seen int32
			var seenName string
			var lastReq *registryv1beta.ListImagesRequest
			deletedAt := timestamppb.New(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
			h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
				lastReq = proto.Clone(req).(*registryv1beta.ListImagesRequest)
				seen = e2eProtoGetListImagesDeletionFilter(t, req)
				seenName = e2eProtoGetListImagesDeletionFilterName(t, req)
				imgs := []*registryv1beta.Image{
					{Repository: "app/web", Digest: "sha256:active", CreatedAt: timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))},
				}
				if seenName == "INCLUDE_DELETED" {
					imgs = append(imgs, &registryv1beta.Image{
						Repository: "app/web",
						Digest:     "sha256:deleted",
						CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
						DeletedAt:  deletedAt,
					})
				}
				return &registryv1beta.ListImagesResponse{Images: imgs}, nil
			}

			h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+fmt.Sprintf(`
data "namespace_registry_images" "imgs" {
  repository      = "app/web"
  deletion_filter = %q
}
`, tc.filterStr))
			h.mustInit(t)
			h.mustApply(t)
			if lastReq == nil {
				t.Fatal("ListImages was not called")
			}
			fd := lastReq.ProtoReflect().Descriptor().Fields().ByName("deletion_filter")
			if fd == nil || fd.Enum() == nil {
				t.Fatalf("ListImagesRequest protobuf descriptor missing enum field 'deletion_filter'")
			}
			wantValDesc := fd.Enum().Values().ByName(protoreflect.Name(tc.filterStr))
			if wantValDesc == nil {
				t.Fatalf("deletion_filter enum descriptor missing value %q", tc.filterStr)
			}
			wantEnum := int32(wantValDesc.Number())
			if seen != wantEnum {
				t.Errorf("deletion_filter numeric enum for %s: got %d, want %d", tc.filterStr, seen, wantEnum)
			}
			if seenName != tc.filterStr {
				t.Errorf("deletion_filter enum name for %s: got %q, want %q", tc.filterStr, seenName, tc.filterStr)
			}
			if tc.wantNonZero && seen == 0 {
				t.Errorf("expected non-zero deletion_filter enum for %s, got 0", tc.filterStr)
			}
			if !tc.wantNonZero && seen != 0 {
				t.Errorf("expected 0 deletion_filter enum for %s, got %d", tc.filterStr, seen)
			}
			st, _ := h.readState(t)
			imgs := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["images"], "registry images")
			img0 := e2eMustMap(t, imgs[0], "images[0]")
			if img0["deleted_at"] != nil {
				t.Errorf("expected active image deleted_at=null, got %v", img0["deleted_at"])
			}
			if tc.filterStr == "INCLUDE_DELETED" {
				if len(imgs) != 2 {
					t.Fatalf("expected 2 images, got %d", len(imgs))
				}
				img1 := e2eMustMap(t, imgs[1], "images[1]")
				if img1["deleted_at"] == nil || img1["deleted_at"] == "" {
					t.Errorf("expected non-empty deleted_at on soft-deleted image, got %v", img1["deleted_at"])
				}
			} else if len(imgs) != 1 {
				t.Fatalf("expected 1 image for %s, got %d", tc.filterStr, len(imgs))
			}
		})
	}
}

func TestE2E_T1_R3Reg_ExpiresAtAfterAndBeforeFilter(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var seenRange *stdlib.TimestampRange
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		seenRange = e2eProtoGetListImagesExpiresAt(t, req)
		return &registryv1beta.ListImagesResponse{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "imgs" {
  repository = "app/web"
  expires_at = {
    after  = "2026-11-01T00:00:00Z"
    before = "2026-12-01T00:00:00Z"
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if seenRange == nil || seenRange.GetAfter() == nil || seenRange.GetBefore() == nil {
		t.Fatalf("expected both After and Before timestamps in ListImagesRequest.expires_at, got %+v", seenRange)
	}
	if !seenRange.GetAfter().AsTime().Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unexpected After: %v", seenRange.GetAfter().AsTime())
	}
	if !seenRange.GetBefore().AsTime().Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unexpected Before: %v", seenRange.GetBefore().AsTime())
	}
}

func TestE2E_T1_R3Reg_ImagesExposesDeletedAtAndLabels(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "app/web",
					Digest:     "sha256:lbl1",
					CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
					DeletedAt:  timestamppb.New(time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)),
					Labels: []*stdlib.Label{
						{Name: "env", Value: "prod"},
						{Name: "commit", Value: "abc123"},
					},
				},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "imgs" {
  repository = "app/web"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	imgs := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["images"], "registry images")
	img := e2eMustMap(t, imgs[0], "images[0]")
	if img["deleted_at"] == nil || img["deleted_at"] == "" {
		t.Errorf("expected populated deleted_at, got %v", img["deleted_at"])
	}
	lbls := e2eMustMap(t, img["labels"], "images[0].labels")
	if lbls["env"] != "prod" || lbls["commit"] != "abc123" {
		t.Errorf("unexpected labels map: %#v", lbls)
	}
}

func TestE2E_T1_R3Reg_SingularImageExposesDeletedAtAndLabels(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return &registryv1beta.GetImageResponse{
			Image: &registryv1beta.Image{
				Repository: req.GetRepository(),
				Digest:     "sha256:single1",
				CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
				ExpiresAt:  timestamppb.New(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)),
				DeletedAt:  timestamppb.New(time.Date(2026, 11, 8, 9, 10, 11, 0, time.UTC)),
				Labels: []*stdlib.Label{
					{Name: "team", Value: "infra"},
				},
				Tags:  []string{"latest"},
				Sizes: &registryv1beta.Image_Sizes{Total: 4096},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_image" "one" {
  repository = "app/web"
  reference  = "latest"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	attrs := st.Resources[0].Instances[0].Attributes
	if attrs["deleted_at"] == nil || attrs["deleted_at"] == "" {
		t.Errorf("expected deleted_at in singular data source, got %v", attrs["deleted_at"])
	}
	lbls := e2eMustMap(t, attrs["labels"], "singular image labels")
	if lbls["team"] != "infra" {
		t.Errorf("expected labels.team=infra, got %#v", lbls)
	}
}

func TestE2E_T1_R3Reg_ExpiresAtPartialBounds(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var ranges []*stdlib.TimestampRange
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		ranges = append(ranges, e2eProtoGetListImagesExpiresAt(t, req))
		return &registryv1beta.ListImagesResponse{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "after_only" {
  repository = "app/after"
  expires_at = {
    after = "2026-11-01T00:00:00Z"
  }
}

data "namespace_registry_images" "before_only" {
  repository = "app/before"
  expires_at = {
    before = "2026-12-01T00:00:00Z"
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if len(ranges) != 2 {
		t.Fatalf("expected 2 ListImages calls, got %d", len(ranges))
	}
	var sawAfterOnly, sawBeforeOnly bool
	for _, r := range ranges {
		if r != nil && r.GetAfter() != nil && r.GetBefore() == nil {
			sawAfterOnly = true
		}
		if r != nil && r.GetAfter() == nil && r.GetBefore() != nil {
			sawBeforeOnly = true
		}
	}
	if !sawAfterOnly || !sawBeforeOnly {
		t.Errorf("expected both after-only and before-only TimestampRange calls, got %+v", ranges)
	}
}

// --- Group 9: R3-Endpoints namespaceapi.com Trusted Domain ---

func TestE2E_T1_R3End_IsTrustedHostAcceptsNamespaceAPICom(t *testing.T) {
	for _, host := range []string{
		"https://namespaceapi.com",
		"https://compute.namespaceapi.com:443",
		"https://turbo.cache.namespaceapi.com",
		"COMPUTE.NAMESPACEAPI.COM:443",
	} {
		if !endpoint.IsTrustedHost(host) {
			t.Errorf("expected IsTrustedHost(%q) == true", host)
		}
	}
}

func TestE2E_T1_R3End_ProviderConfigureComputeNamespaceAPIComZeroWarnings(t *testing.T) {
	t.Setenv("NSC_GLOBAL_ENDPOINT", "")
	t.Setenv("NSC_IAM_ENDPOINT", "")
	t.Setenv("NSC_ENDPOINT", "")

	ctx := context.Background()
	p := provider.New("e2e")()
	resp := &tfprovider.ConfigureResponse{}
	p.Configure(ctx, tfprovider.ConfigureRequest{
		Config: e2eMakeProviderConfig(ctx, p, map[string]any{
			"token":            e2eRuntimeProviderToken(),
			"compute_endpoint": "https://compute.namespaceapi.com:443",
		}),
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected Configure errors: %v", resp.Diagnostics.Errors())
	}
	if len(resp.Diagnostics.Warnings()) != 0 {
		t.Errorf("expected 0 warnings for compute.namespaceapi.com, got: %v", resp.Diagnostics.Warnings())
	}
}

func TestE2E_T1_R3End_ProviderConfigureGlobalAndIAMNamespaceAPIComZeroWarnings(t *testing.T) {
	t.Setenv("NSC_GLOBAL_ENDPOINT", "")
	t.Setenv("NSC_IAM_ENDPOINT", "")
	t.Setenv("NSC_ENDPOINT", "")

	ctx := context.Background()
	p := provider.New("e2e")()
	resp := &tfprovider.ConfigureResponse{}
	p.Configure(ctx, tfprovider.ConfigureRequest{
		Config: e2eMakeProviderConfig(ctx, p, map[string]any{
			"token":           e2eRuntimeProviderToken(),
			"global_endpoint": "https://global.namespaceapi.com:443",
			"iam_endpoint":    "https://iam.namespaceapi.com:443",
		}),
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected Configure errors: %v", resp.Diagnostics.Errors())
	}
	if len(resp.Diagnostics.Warnings()) != 0 {
		t.Errorf("expected 0 warnings for global/iam.namespaceapi.com, got: %v", resp.Diagnostics.Warnings())
	}
}

func TestE2E_T1_R3End_EnvNSCEndpointNamespaceAPIComZeroWarnings(t *testing.T) {
	t.Setenv("NSC_GLOBAL_ENDPOINT", "")
	t.Setenv("NSC_IAM_ENDPOINT", "")
	t.Setenv("NSC_ENDPOINT", "https://turbo.cache.namespaceapi.com:443")

	ctx := context.Background()
	p := provider.New("e2e")()
	resp := &tfprovider.ConfigureResponse{}
	p.Configure(ctx, tfprovider.ConfigureRequest{
		Config: e2eMakeProviderConfig(ctx, p, map[string]any{
			"token": e2eRuntimeProviderToken(),
		}),
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected Configure errors: %v", resp.Diagnostics.Errors())
	}
	if len(resp.Diagnostics.Warnings()) != 0 {
		t.Errorf("expected 0 warnings for env NSC_ENDPOINT turbo.cache.namespaceapi.com, got: %v", resp.Diagnostics.Warnings())
	}
}

func TestE2E_T1_R3End_TerraformPlanNamespaceAPIComNoWarning(t *testing.T) {
	h := e2eStartBlackBoxHarnessWithRealProvider(t)
	h.writeMainTF(t, fmt.Sprintf(`terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

provider "namespace" {
  token            = %q
  compute_endpoint = "https://compute.namespaceapi.com:443"
  global_endpoint  = "https://global.namespaceapi.com:443"
  iam_endpoint     = "https://iam.namespaceapi.com:443"
}

resource "namespace_github_runner_profile" "planned" {
  tag = "ep-plan"
  os  = "ubuntu-24.04"
}
`, e2eRuntimeProviderToken()))
	h.mustInit(t)
	stdout, stderr, err := h.runTerraform(t, nil, "plan")
	if err != nil {
		t.Fatalf("terraform plan failed: %v\nStdout:\n%s\nStderr:\n%s", err, stdout, stderr)
	}
	combined := stdout + "\n" + stderr
	if strings.Contains(combined, "Custom Untrusted Endpoint Configured") {
		t.Errorf("unexpected untrusted endpoint warning for namespaceapi.com:\n%s", combined)
	}
}

// --- Group 10: R3-Limits UsagePolicy Valid limits.pb.go Fields ---

func TestE2E_T1_R3Lim_TenantGroupConcurrencyAndUsageLimits(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var mu sync.Mutex
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{
			Name:     req.GetName(),
			Policies: req.GetPolicies(),
		}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(groups, req.GetName())
		return &emptypb.Empty{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "grp" {
  name = "e2e-limits-grp"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        concurrency_limits = {
          max_cpu            = 64
          max_memory_mb      = 131072
          max_instance_count = 16
        }
        usage_limits = {
          unit_minutes = 10000
          builds       = 500
          wall_seconds = 360000
        }
      })
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T1_R3Lim_TenantGroupEnabledAndPerPlatformLimits(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var mu sync.Mutex
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{
			Name:     req.GetName(),
			Policies: req.GetPolicies(),
		}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "grp" {
  name = "e2e-platform-grp"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        enabled_platforms = ["linux/amd64", "linux/arm64", "windows/amd64"]
        per_platform_limits = {
          "linux/amd64" = {
            concurrency_limits = {
              max_cpu       = 32
              max_memory_mb = 65536
            }
            shape_limits = {
              largest_acceptable_shape = {
                virtual_cpu      = 16
                memory_megabytes = 32768
              }
            }
          }
        }
      })
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T1_R3Lim_TenantGroupDevboxLimits(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var seenJSON string
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		if len(req.GetPolicies()) > 0 {
			seenJSON = req.GetPolicies()[0].GetValue()
		}
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{
			Name:     req.GetName(),
			Policies: req.GetPolicies(),
		}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "grp" {
  name = "e2e-devbox-grp"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        concurrency_limits = { max_cpu = 16 }
        devbox_limits      = { max_count = 5 }
      })
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	var decoded map[string]any
	if err := json.Unmarshal([]byte(seenJSON), &decoded); err != nil {
		t.Fatalf("invalid UsagePolicy JSON: %v", err)
	}
	if _, ok := decoded["devbox_limits"]; !ok {
		t.Errorf("expected devbox_limits in forwarded JSON: %s", seenJSON)
	}
}

func TestE2E_T1_R3Lim_EphemeralTenantTokenUsagePolicy(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	ephemeralTok := e2eRuntimeJWT("nsct_", "eph-lim")
	var seenPolicies []*iamv1beta.TenantTokenPolicy
	h.mockServer.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		seenPolicies = req.GetPolicies()
		return &iamv1beta.IssueTenantTokenResponse{BearerToken: ephemeralTok}, nil
	}

	modDir := filepath.Join(h.workDir, "modules", "tok")
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatalf("mkdir mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "main.tf"), []byte(`
variable "tok" {
  type      = string
  sensitive = true
  ephemeral = true
}
`), 0600); err != nil {
		t.Fatalf("write mod main.tf: %v", err)
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
ephemeral "namespace_tenant_token" "scoped" {
  tenant_id = "tenant-e2e-lim"
  actor_id  = "actor-e2e-lim"
  policies = [
    {
      policy_type = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        concurrency_limits = { max_cpu = 8, max_memory_mb = 16384 }
        usage_limits       = { builds = 50 }
      })
    }
  ]
}

module "consumer" {
  source = "./modules/tok"
  tok    = ephemeral.namespace_tenant_token.scoped.bearer_token
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if len(seenPolicies) != 1 || seenPolicies[0].GetPolicyType() != "namespace.cloud.compute.v1beta.UsagePolicy" {
		t.Fatalf("unexpected IssueTenantToken policies: %+v", seenPolicies)
	}
	_, rawState := h.readState(t)
	if strings.Contains(string(rawState), ephemeralTok) {
		t.Errorf("ephemeral bearer_token leaked into terraform.tfstate")
	}
}

func TestE2E_T1_R3Lim_TenantGroupUpdateUsagePolicyInPlace(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{
			Name:     req.GetName(),
			Policies: req.GetPolicies(),
		}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	step1 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_tenant_group" "grp" {
  name = "e2e-update-lim"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value  = jsonencode({ concurrency_limits = { max_instance_count = 8 } })
    }
  ]
}
`
	step2 := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
resource "namespace_tenant_group" "grp" {
  name = "e2e-update-lim"
  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        concurrency_limits = { max_instance_count = 16 }
        devbox_limits      = { max_count = 3 }
      })
    }
  ]
}
`
	h.writeMainTF(t, step1)
	h.mustInit(t)
	h.mustApply(t)

	h.writeMainTF(t, step2)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

// --- Group 11: R3-Inventory, Examples, Templates, Docs ---

func TestE2E_T1_R3Docs_InventoryMDCorrectedRBACTuples(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "apicontract", "inventory.md"))
	if err != nil {
		t.Fatalf("read inventory.md: %v", err)
	}
	inv := string(raw)
	requiredPatterns := []struct {
		label string
		re    string
	}{
		{`{"github/runner-profile", "manage"}`, `"github/runner-profile"[^\n]*"manage"`},
		{`{"github/runner-profile", "read"}`, `"github/runner-profile"[^\n]*"read"`},
		{`{"tenant/member", "list"}`, `"tenant/member"[^\n]*"list"`},
		{`{"vault/object", "read"}`, `"vault/object"[^\n]*"read"`},
		{`containerregistry/repository (list)`, `"containerregistry/repository"[^\n]*list`},
		{`containerregistry/repository (share)`, `"containerregistry/repository"[^\n]*share`},
		{`containerregistry/repository (configure)`, `"containerregistry/repository"[^\n]*configure`},
		{`{"cache/bazel", "ensure"}`, `"cache/bazel"[^\n]*"ensure"`},
		{`{"cache/gradle", "ensure"}`, `"cache/gradle"[^\n]*"ensure"`},
		{`{"cache/httpcache", "ensure"}`, `"cache/httpcache"[^\n]*"ensure"`},
		{`{"builder", "read"}`, `"builder"[^\n]*"read"`},
		{`{"iam/tenant-group", "manage"}`, `"iam/tenant-group"[^\n]*"manage"`},
		{`{"iam/tenant-group", "read"}`, `"iam/tenant-group"[^\n]*"read"`},
		{`{"iam/tenant-token", "issue"}`, `"iam/tenant-token"[^\n]*"issue"`},
		{`{"iam/client-certificate", "issue"}`, `"iam/client-certificate"[^\n]*"issue"`},
	}
	for _, want := range requiredPatterns {
		if !regexp.MustCompile(want.re).MatchString(inv) {
			t.Errorf("inventory.md missing corrected RBAC tuple/action pairing %s", want.label)
		}
	}
}

func TestE2E_T1_R3Docs_InventoryMDCorrectedServiceNamesAndSchemas(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "apicontract", "inventory.md"))
	if err != nil {
		t.Fatalf("read inventory.md: %v", err)
	}
	inv := string(raw)
	for _, want := range []string{
		"namespace.cloud.builder.v1beta.BuilderService",
		"ACCESS_LEVEL_CUSTOM",
		"custom_permissions",
		"windows",
		"deletion_filter",
		"deleted_at",
		"labels",
	} {
		if !strings.Contains(inv, want) {
			t.Errorf("inventory.md missing updated service/schema entry %q", want)
		}
	}
}

func TestE2E_T1_R3Docs_InventoryMDOmitsRemovedAndDriftedStrings(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "apicontract", "inventory.md"))
	if err != nil {
		t.Fatalf("read inventory.md: %v", err)
	}
	inv := string(raw)
	forbidden := []string{
		"volume_tag",
		"integrations@v0.0.10",
		"namespacelabs.dev/integrations",
		"namespace.cloud.compute.v1beta.BuilderService",
		"github/profile",
		"iam/member",
		`{"vault", "read"}`,
		`{"registry", "read"}`,
		"integrations/bazel",
		"integrations/gradle",
		"integrations/httpcache",
		"compute/builder",
	}
	for _, bad := range forbidden {
		if strings.Contains(inv, bad) {
			t.Errorf("inventory.md still contains stale/drifted string %q", bad)
		}
	}
}

func TestE2E_T1_R3Docs_ExamplesTerraformFmtCheck(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "terraform")
	cmd := exec.Command("terraform", "fmt", "-check", "-recursive", "examples/")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("terraform fmt -check -recursive examples/ failed: %v\n%s", err, string(out))
	}
}

func TestE2E_T1_R3Docs_SecurityMDHasNSRTAndNamespaceAPICom(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "SECURITY.md"))
	if err != nil {
		t.Fatalf("read SECURITY.md: %v", err)
	}
	sec := string(raw)
	if !strings.Contains(sec, "nsrt_") {
		t.Errorf("SECURITY.md does not document nsrt_ opaque revokable tokens")
	}
	if !strings.Contains(sec, "namespaceapi.com") {
		t.Errorf("SECURITY.md does not document namespaceapi.com trusted domain")
	}
	if strings.Contains(sec, "namespacelabs.dev/integrations") {
		t.Errorf("SECURITY.md still references removed namespacelabs.dev/integrations")
	}
}

// =============================================================================
// TIER 2: BOUNDARY, CORNER & NEGATIVE CASES (>= 5 TESTS PER FEATURE GROUP)
// =============================================================================

// --- Group 1 (R1A) Tier 2 ---

func TestE2E_T2_R1A_SourceTreeNoIntegrationsOrJWTImports(t *testing.T) {
	root := e2eRepoRoot(t)
	bannedIntegrations := "namespacelabs.dev/" + "integrations"
	bannedJWT := "github.com/golang-jwt/" + "jwt"
	fset := token.NewFileSet()
	var hits []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == ".agents" || base == "dist" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(impPath, bannedIntegrations) || strings.HasPrefix(impPath, bannedJWT) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, fmt.Sprintf("%s (%s)", rel, impPath))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	if len(hits) > 0 {
		t.Errorf("Go files still import %s or %s: %v", bannedIntegrations, bannedJWT, hits)
	}
}

func TestE2E_T2_R1A_CIWorkflowNoIntegrationsPinWorkaround(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "test.yml"))
	if err != nil {
		t.Fatalf("read test.yml: %v", err)
	}
	s := string(raw)
	for _, stale := range []string{
		"namespacelabs.dev/" + "integrations",
		"integrations_before",
		"integrations_after",
	} {
		if strings.Contains(s, stale) {
			t.Errorf(".github/workflows/test.yml still contains %q", stale)
		}
	}
}

func TestE2E_T2_R1A_DependabotNoIntegrationsGroupOrBSRIgnore(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "dependabot.yml"))
	if err != nil {
		t.Fatalf("read dependabot.yml: %v", err)
	}
	s := string(raw)
	for _, stale := range []string{
		"namespacelabs.dev/" + "integrations",
		"VolumeTag",
		"buf.build/gen/go/namespace/cloud/*",
	} {
		if strings.Contains(s, stale) {
			t.Errorf(".github/dependabot.yml still contains %q", stale)
		}
	}
}

func TestE2E_T2_R1A_GoModTidyDiffClean(t *testing.T) {
	root := e2eRepoRoot(t)
	cmd := exec.Command("go", "mod", "tidy", "-diff")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go mod tidy -diff failed: %v\n%s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("go mod tidy -diff is non-empty:\n%s", string(out))
	}
}

func TestE2E_T2_R1A_ClientUserAgentAndEndpointsCompileClean(t *testing.T) {
	e2eInitBufconnTransport(t)
	srv := testutil.NewMockServer()
	var seenUA []string
	srv.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			seenUA = md.Get("user-agent")
		}
		return &githubv1beta.ListProfilesResponse{}, nil
	}
	srv.Start()
	defer srv.Stop()

	cl, err := client.New(context.Background(), client.Config{
		Token:           e2eRuntimeProviderToken(),
		GlobalEndpoint:  "https://global.namespaceapis.com:443",
		IAMEndpoint:     "https://iam.namespaceapis.com:443",
		ComputeEndpoint: "https://eu.compute.namespaceapis.com:443",
		Version:         "e2e-test",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(srv.Dialer())},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer func() { _ = cl.Close() }()

	pc, err := cl.ProfileClient(context.Background())
	if err != nil {
		t.Fatalf("ProfileClient: %v", err)
	}
	if _, err := pc.ListProfiles(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	joinedUA := strings.Join(seenUA, " ")
	if !strings.Contains(joinedUA, "terraform-provider-namespace/e2e-test") {
		t.Errorf("expected terraform-provider-namespace/e2e-test user-agent, got %q", joinedUA)
	}
}

// --- Group 2 (R1B) Tier 2 ---

func TestE2E_T2_R1B_RejectVolumeTagInHCL(t *testing.T) {
	// (a) Direct tfprotov6 GetProviderSchema verification: volume_tag must be absent from
	// cache_volume_settings on the resource and both runner profile data sources.
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)
	ps := providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)())()
	schemaResp, err := ps.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	findProtoAttr := func(attrs []*tfprotov6.SchemaAttribute, name string) *tfprotov6.SchemaAttribute {
		for _, a := range attrs {
			if a.Name == name {
				return a
			}
		}
		return nil
	}
	assertProtoNoVolumeTag := func(label string, attrs []*tfprotov6.SchemaAttribute) {
		cvs := findProtoAttr(attrs, "cache_volume_settings")
		if cvs == nil || cvs.NestedType == nil {
			t.Fatalf("%s: missing cache_volume_settings NestedType", label)
		}
		if findProtoAttr(cvs.NestedType.Attributes, "volume_tag") != nil {
			t.Errorf("%s: cache_volume_settings still contains removed volume_tag attribute", label)
		}
		if findProtoAttr(cvs.NestedType.Attributes, "volume_size_gb") == nil {
			t.Errorf("%s: cache_volume_settings missing expected volume_size_gb attribute", label)
		}
	}

	resSchema := schemaResp.ResourceSchemas["namespace_github_runner_profile"]
	if resSchema == nil || resSchema.Block == nil {
		t.Fatal("missing resource schema namespace_github_runner_profile")
	}
	assertProtoNoVolumeTag("resource.namespace_github_runner_profile", resSchema.Block.Attributes)

	dsSingle := schemaResp.DataSourceSchemas["namespace_github_runner_profile"]
	if dsSingle == nil || dsSingle.Block == nil {
		t.Fatal("missing data source schema namespace_github_runner_profile")
	}
	assertProtoNoVolumeTag("data.namespace_github_runner_profile", dsSingle.Block.Attributes)

	dsPlural := schemaResp.DataSourceSchemas["namespace_github_runner_profiles"]
	if dsPlural == nil || dsPlural.Block == nil {
		t.Fatal("missing data source schema namespace_github_runner_profiles")
	}
	profilesAttr := findProtoAttr(dsPlural.Block.Attributes, "profiles")
	if profilesAttr == nil || profilesAttr.NestedType == nil {
		t.Fatal("data.namespace_github_runner_profiles missing profiles NestedType")
	}
	assertProtoNoVolumeTag("data.namespace_github_runner_profiles.profiles", profilesAttr.NestedType.Attributes)

	// (b) CLI opaque-box schema verification via `terraform providers schema -json`.
	h := e2eStartBlackBoxHarness(t)
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "bad" {
  tag = "reject-voltag"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_tag     = "legacy"
      volume_size_gb = 50
    }
  ]
}
`)
	h.mustInit(t)

	schemaStdout, schemaStderr, err := h.runTerraform(t, nil, "providers", "schema", "-json")
	if err != nil {
		t.Fatalf("terraform providers schema -json failed: %v\nStdout:\n%s\nStderr:\n%s", err, schemaStdout, schemaStderr)
	}
	var cliSchemas tfjson.ProviderSchemas
	if err := json.Unmarshal([]byte(schemaStdout), &cliSchemas); err != nil {
		t.Fatalf("unmarshal providers schema JSON: %v", err)
	}
	provSchema := cliSchemas.Schemas["registry.terraform.io/bsuv/namespace"]
	if provSchema == nil {
		t.Fatalf("missing provider schema for registry.terraform.io/bsuv/namespace in CLI output")
	}
	assertCLISchemaNoVolumeTag := func(label string, attrs map[string]*tfjson.SchemaAttribute) {
		cvs, ok := attrs["cache_volume_settings"]
		if !ok || cvs == nil || cvs.AttributeNestedType == nil {
			t.Fatalf("%s: CLI schema missing cache_volume_settings nested_type", label)
		}
		if _, has := cvs.AttributeNestedType.Attributes["volume_tag"]; has {
			t.Errorf("%s: CLI schema cache_volume_settings still exposes volume_tag", label)
		}
		if _, has := cvs.AttributeNestedType.Attributes["volume_size_gb"]; !has {
			t.Errorf("%s: CLI schema cache_volume_settings missing volume_size_gb", label)
		}
	}
	assertCLISchemaNoVolumeTag("cli.resource.namespace_github_runner_profile", provSchema.ResourceSchemas["namespace_github_runner_profile"].Block.Attributes)
	assertCLISchemaNoVolumeTag("cli.data.namespace_github_runner_profile", provSchema.DataSourceSchemas["namespace_github_runner_profile"].Block.Attributes)
	assertCLISchemaNoVolumeTag(
		"cli.data.namespace_github_runner_profiles.profiles",
		provSchema.DataSourceSchemas["namespace_github_runner_profiles"].Block.Attributes["profiles"].AttributeNestedType.Attributes,
	)

	// (c) Because cache_volume_settings is a ListNestedAttribute (List(Object)), Terraform Core's
	// cty.Convert coerces inline object literals by stripping unknown keys before ValidateResourceConfig.
	// Verify via `terraform plan -out=tfplan` + `terraform show -json tfplan` that volume_tag is absent
	// from both planned_values and resource_changes (never persisted to state or sent to the API).
	var createCalled atomic.Bool
	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		createCalled.Store(true)
		return nil, status.Error(codes.Internal, "CreateProfile must not be called during plan")
	}
	planOut, planErrOut, err := h.runTerraform(t, nil, "plan", "-out=tfplan")
	if err != nil {
		t.Fatalf("terraform plan -out=tfplan failed: %v\nStdout:\n%s\nStderr:\n%s", err, planOut, planErrOut)
	}
	if createCalled.Load() {
		t.Errorf("CreateProfile should not be called during terraform plan")
	}
	plan := h.showPlanJSON(t, "tfplan")
	if plan.PlannedValues == nil || plan.PlannedValues.RootModule == nil || len(plan.PlannedValues.RootModule.Resources) != 1 {
		t.Fatalf("expected 1 planned resource in tfplan, got %+v", plan.PlannedValues)
	}
	plannedCVS := e2eMustSlice(t, plan.PlannedValues.RootModule.Resources[0].AttributeValues["cache_volume_settings"], "planned_values.cache_volume_settings")
	if len(plannedCVS) != 1 {
		t.Fatalf("expected 1 planned cache_volume_settings item, got %d", len(plannedCVS))
	}
	plannedItem := e2eMustMap(t, plannedCVS[0], "planned_values.cache_volume_settings[0]")
	if _, hasVolTag := plannedItem["volume_tag"]; hasVolTag {
		t.Errorf("planned_values.cache_volume_settings[0] unexpectedly contains volume_tag: %#v", plannedItem)
	}
	if plannedItem["volume_size_gb"] != float64(50) {
		t.Errorf("planned_values.cache_volume_settings[0].volume_size_gb=%v, want 50", plannedItem["volume_size_gb"])
	}

	if len(plan.ResourceChanges) != 1 || plan.ResourceChanges[0].Change == nil {
		t.Fatalf("expected 1 resource_change in tfplan, got %+v", plan.ResourceChanges)
	}
	afterMap := e2eMustMap(t, plan.ResourceChanges[0].Change.After, "resource_changes[0].change.after")
	afterCVS := e2eMustSlice(t, afterMap["cache_volume_settings"], "resource_changes[0].change.after.cache_volume_settings")
	if len(afterCVS) != 1 {
		t.Fatalf("expected 1 resource_change cache_volume_settings item, got %d", len(afterCVS))
	}
	afterItem := e2eMustMap(t, afterCVS[0], "resource_changes[0].change.after.cache_volume_settings[0]")
	if _, hasVolTag := afterItem["volume_tag"]; hasVolTag {
		t.Errorf("resource_changes.after.cache_volume_settings[0] unexpectedly contains volume_tag: %#v", afterItem)
	}
	if afterItem["volume_size_gb"] != float64(50) {
		t.Errorf("resource_changes.after.cache_volume_settings[0].volume_size_gb=%v, want 50", afterItem["volume_size_gb"])
	}
}

func TestE2E_T2_R1B_CrossFieldValidatorReservedSizeWithoutVolumeTag(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "bad_res" {
  tag = "bad-reserved"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb          = 50
      volume_reserved_size_gb = 50
    }
  ]
}
`)
	h.mustInit(t)
	stdout, stderr, err := h.runTerraform(t, nil, "plan")
	if err == nil {
		t.Fatal("expected plan error when volume_reserved_size_gb >= volume_size_gb")
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "Invalid Cache Volume Reserved Size") {
		t.Errorf("expected Invalid Cache Volume Reserved Size error, got:\n%s", combined)
	}
}

func TestE2E_T2_R1B_EmptyToPopulatedToEmptyToOmittedTransitions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	base := e2eProviderHCLHeader(e2eRuntimeProviderToken())
	emptyHCL := base + `
resource "namespace_github_runner_profile" "cv" {
  tag                   = "cv-transitions"
  os                    = "ubuntu-24.04"
  cache_volume_settings = []
}
`
	popHCL := base + `
resource "namespace_github_runner_profile" "cv" {
  tag = "cv-transitions"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    {
      volume_size_gb           = 60
      allow_commit_from_branch = []
    }
  ]
}
`
	omittedHCL := base + `
resource "namespace_github_runner_profile" "cv" {
  tag = "cv-transitions"
  os  = "ubuntu-24.04"
}
`
	h.writeMainTF(t, emptyHCL)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	h.writeMainTF(t, popHCL)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	h.writeMainTF(t, emptyHCL)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	h.writeMainTF(t, omittedHCL)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T2_R1B_StateUpgradeV0ToV1PreservesEmptyVsNullBranches(t *testing.T) {
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)
	ps := providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)())()
	ctx := context.Background()

	schemaResp, err := ps.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	resSchema := schemaResp.ResourceSchemas["namespace_github_runner_profile"]
	if resSchema == nil || resSchema.Version < 1 {
		t.Fatalf("expected namespace_github_runner_profile schema version >= 1, got %+v", resSchema)
	}

	v0 := []byte(`{
		"profile_id": "prof-v0-multi",
		"version": 1,
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"tag": "v0-multi",
		"os": "ubuntu-24.04",
		"description": "",
		"emoji": "",
		"swap_size_mb": 0,
		"builder_mode": "BUILDER_MODE_UNSPECIFIED",
		"access_level": "ACCESS_LEVEL_LIMITED",
		"experimental_features": null,
		"instance_shape": null,
		"cache_volume_settings": [
			{
				"volume_tag": "vol-empty",
				"volume_size_gb": 50,
				"volume_reserved_size_gb": 0,
				"bundled_container_cache": false,
				"bundled_buildkit_cache": false,
				"bundled_git_mirror": false,
				"bundled_runner_tools_cache": false,
				"bundled_runner_actions_cache": false,
				"allow_commit_from_branch": []
			},
			{
				"volume_tag": "vol-null",
				"volume_size_gb": 70,
				"volume_reserved_size_gb": 0,
				"bundled_container_cache": false,
				"bundled_buildkit_cache": false,
				"bundled_git_mirror": false,
				"bundled_runner_tools_cache": false,
				"bundled_runner_actions_cache": false,
				"allow_commit_from_branch": null
			},
			{
				"volume_tag": "vol-main",
				"volume_size_gb": 90,
				"volume_reserved_size_gb": 0,
				"bundled_container_cache": false,
				"bundled_buildkit_cache": false,
				"bundled_git_mirror": false,
				"bundled_runner_tools_cache": false,
				"bundled_runner_actions_cache": false,
				"allow_commit_from_branch": ["main"]
			}
		],
		"custom_runner_image_spec": null,
		"network_policy": null,
		"custom_runner_images": null
	}`)

	upResp, err := ps.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "namespace_github_runner_profile",
		Version:  0,
		RawState: &tfprotov6.RawState{JSON: v0},
	})
	if err != nil {
		t.Fatalf("UpgradeResourceState: %v", err)
	}
	if len(upResp.Diagnostics) > 0 {
		for _, d := range upResp.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatalf("UpgradeResourceState error: %s: %s", d.Summary, d.Detail)
			}
		}
	}
	val, err := upResp.UpgradedState.Unmarshal(resSchema.ValueType())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var root map[string]tftypes.Value
	_ = val.As(&root)
	var items []tftypes.Value
	_ = root["cache_volume_settings"].As(&items)
	if len(items) != 3 {
		t.Fatalf("expected 3 upgraded items, got %d", len(items))
	}
	var m0, m1, m2 map[string]tftypes.Value
	_ = items[0].As(&m0)
	_ = items[1].As(&m1)
	_ = items[2].As(&m2)
	for idx, m := range []map[string]tftypes.Value{m0, m1, m2} {
		if _, hasVolTag := m["volume_tag"]; hasVolTag {
			t.Errorf("idx %d upgraded cache_volume_settings must not contain volume_tag", idx)
		}
	}

	var b0, b2 []tftypes.Value
	_ = m0["allow_commit_from_branch"].As(&b0)
	if m0["allow_commit_from_branch"].IsNull() || len(b0) != 0 {
		t.Errorf("idx 0 expected non-null empty list, got null=%v len=%d", m0["allow_commit_from_branch"].IsNull(), len(b0))
	}
	if !m1["allow_commit_from_branch"].IsNull() {
		t.Errorf("idx 1 expected null allow_commit_from_branch, got %v", m1["allow_commit_from_branch"])
	}
	_ = m2["allow_commit_from_branch"].As(&b2)
	if len(b2) != 1 {
		t.Errorf("idx 2 expected 1 branch, got %d", len(b2))
	}
}

func TestE2E_T2_R1B_StateUpgradeV0ToV1NullAndEmptyCacheVolumes(t *testing.T) {
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)
	ps := providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)())()
	ctx := context.Background()

	schemaResp, err := ps.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	resSchema := schemaResp.ResourceSchemas["namespace_github_runner_profile"]
	if resSchema == nil || resSchema.Version < 1 {
		t.Fatalf("expected namespace_github_runner_profile schema version >= 1, got %+v", resSchema)
	}

	for _, tc := range []struct {
		name string
		cvs  string
	}{
		{"null", "null"},
		{"empty", "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{
				"id": "prof-v0-%s",
				"profile_id": "prof-v0-%s",
				"version": 1,
				"created_at": "2026-01-01T00:00:00Z",
				"updated_at": "2026-01-01T00:00:00Z",
				"tag": "v0-%s",
				"os": "ubuntu-24.04",
				"description": "",
				"emoji": "",
				"swap_size_mb": 0,
				"builder_mode": "BUILDER_MODE_UNSPECIFIED",
				"access_level": "ACCESS_LEVEL_LIMITED",
				"experimental_features": null,
				"instance_shape": null,
				"cache_volume_settings": %s,
				"custom_runner_image_spec": null,
				"network_policy": null,
				"custom_runner_images": null
			}`, tc.name, tc.name, tc.name, tc.cvs)
			upResp, err := ps.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
				TypeName: "namespace_github_runner_profile",
				Version:  0,
				RawState: &tfprotov6.RawState{JSON: []byte(raw)},
			})
			if err != nil {
				t.Fatalf("UpgradeResourceState(%s): %v", tc.name, err)
			}
			for _, d := range upResp.Diagnostics {
				if d.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatalf("unexpected error diagnostic (%s): %s: %s", tc.name, d.Summary, d.Detail)
				}
			}
			val, err := upResp.UpgradedState.Unmarshal(resSchema.ValueType())
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", tc.name, err)
			}
			var root map[string]tftypes.Value
			if err := val.As(&root); err != nil {
				t.Fatalf("extract root(%s): %v", tc.name, err)
			}
			cvsVal, ok := root["cache_volume_settings"]
			if !ok {
				t.Fatalf("missing cache_volume_settings in upgraded state(%s)", tc.name)
			}
			if listTyp, ok := cvsVal.Type().(tftypes.List); ok {
				if objTyp, ok := listTyp.ElementType.(tftypes.Object); ok {
					if _, hasVolTag := objTyp.AttributeTypes["volume_tag"]; hasVolTag {
						t.Errorf("upgraded cache_volume_settings object attributes still contain volume_tag")
					}
				}
			}
			if tc.name == "null" {
				if !cvsVal.IsNull() {
					t.Errorf("expected null cache_volume_settings to remain null, got %v", cvsVal)
				}
			} else {
				if cvsVal.IsNull() {
					t.Fatalf("expected empty cache_volume_settings [] not to become null")
				}
				var items []tftypes.Value
				if err := cvsVal.As(&items); err != nil {
					t.Fatalf("extract empty cache_volume_settings list: %v", err)
				}
				if len(items) != 0 {
					t.Errorf("expected 0 items in empty cache_volume_settings, got %d", len(items))
				}
			}
		})
	}
}

// --- Group 3 (R1C) Tier 2 ---

func TestE2E_T2_R1C_RejectInvalidAccessLevelInHCL(t *testing.T) {
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)

	for _, badLevel := range []string{"ACCESS_LEVEL_UNKNOWN", "4"} {
		t.Run(badLevel, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: e2eUnitTestFactories(cl),
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
provider "namespace" {}
resource "namespace_github_runner_profile" "bad" {
  tag          = "bad-lvl"
  os           = "ubuntu-24.04"
  access_level = %q
}
`, badLevel),
						ExpectError: regexp.MustCompile(`(?i)value must be one of|Invalid Attribute Value`),
					},
				},
			})
		})
	}
}

func TestE2E_T2_R1C_RejectEmptyResourceTypeOrMissingActions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)

	for _, tc := range []struct {
		name string
		hcl  string
	}{
		{
			"empty_resource_type",
			`custom_permissions = [{ resource_type = "", resource_id = "id1", actions = ["read"] }]`,
		},
		{
			"missing_actions",
			`custom_permissions = [{ resource_type = "vault/object", resource_id = "id1" }]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+fmt.Sprintf(`
resource "namespace_github_runner_profile" "bad" {
  tag          = "bad-cp"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  %s
}
`, tc.hcl))
			h.mustInit(t)
			stdout, stderr, err := h.runTerraform(t, nil, "validate")
			if err == nil {
				t.Fatalf("expected terraform validate error for %s", tc.name)
			}
			combined := stdout + "\n" + stderr
			if strings.Contains(combined, "Unsupported argument") {
				t.Fatalf("terraform validate failed with Unsupported argument (M2 schema missing) instead of %s validation:\n%s", tc.name, combined)
			}
			if strings.Contains(combined, "access_level") {
				t.Fatalf("terraform validate failed on access_level instead of %s:\n%s", tc.name, combined)
			}
			lower := strings.ToLower(combined)
			switch tc.name {
			case "empty_resource_type":
				if !strings.Contains(combined, "resource_type") || (!strings.Contains(lower, "empty") && !strings.Contains(lower, "non-empty")) {
					t.Errorf("expected diagnostic referencing resource_type non-empty constraint, got:\n%s", combined)
				}
			case "missing_actions":
				if !strings.Contains(combined, "actions") || !strings.Contains(lower, "required") {
					t.Errorf("expected diagnostic referencing required actions attribute, got:\n%s", combined)
				}
			}
		})
	}
}

func TestE2E_T2_R1C_EmptyCustomPermissionsListVsOmitted(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	base := e2eProviderHCLHeader(e2eRuntimeProviderToken())
	h.writeMainTF(t, base+`
resource "namespace_github_runner_profile" "empty_cp" {
  tag                = "empty-cp"
  os                 = "ubuntu-24.04"
  access_level       = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = []
}

resource "namespace_github_runner_profile" "omitted_cp" {
  tag          = "omitted-cp"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
}

resource "namespace_github_runner_profile" "null_cp" {
  tag                = "null-cp"
  os                 = "ubuntu-24.04"
  access_level       = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = null
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, _ := h.readState(t)
	findResAttrs := func(name string) map[string]any {
		for _, r := range st.Resources {
			if r.Type == "namespace_github_runner_profile" && r.Name == name && len(r.Instances) == 1 {
				return r.Instances[0].Attributes
			}
		}
		t.Fatalf("resource namespace_github_runner_profile.%s not found in state", name)
		return nil
	}

	emptyAttrs := findResAttrs("empty_cp")
	cpEmpty, okEmpty := emptyAttrs["custom_permissions"].([]any)
	if !okEmpty || len(cpEmpty) != 0 {
		t.Errorf("expected explicit empty custom_permissions [], got %#v", emptyAttrs["custom_permissions"])
	}

	omittedAttrs := findResAttrs("omitted_cp")
	if omittedAttrs["custom_permissions"] != nil {
		t.Errorf("expected omitted custom_permissions to be null, got %#v", omittedAttrs["custom_permissions"])
	}

	nullAttrs := findResAttrs("null_cp")
	if nullAttrs["custom_permissions"] != nil {
		t.Errorf("expected custom_permissions = null to be null, got %#v", nullAttrs["custom_permissions"])
	}
}

func TestE2E_T2_R1C_EmptyResourceIDAndEmptyActionsAccepted(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "wildcard" {
  tag          = "wildcard-cp"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = ""
      actions       = []
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T2_R1C_NonCustomClearsPermissionsAndUnknownEnumDoesNotLeak4(t *testing.T) {
	// Part A: Transitioning a managed runner profile away from ACCESS_LEVEL_CUSTOM clears custom_permissions.
	h1 := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h1.mockServer)

	var lastUpdateSpec *githubv1beta.RunnerProfileSpec
	origUpdate := h1.mockServer.Profile.UpdateProfileFunc
	h1.mockServer.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		lastUpdateSpec = proto.Clone(req.GetSpec()).(*githubv1beta.RunnerProfileSpec)
		return origUpdate(ctx, req)
	}

	h1.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "clearing" {
  tag          = "r1c-clear"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "sec_clear"
      actions       = ["read"]
    }
  ]
}
`)
	h1.mustInit(t)
	h1.mustApply(t)

	h1.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "clearing" {
  tag          = "r1c-clear"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_LIMITED"
}
`)
	h1.mustApply(t)
	h1.mustPlanNoDiff(t)

	if lastUpdateSpec == nil || lastUpdateSpec.GetAccessLevel() != githubv1beta.AccessLevel_ACCESS_LEVEL_LIMITED {
		t.Errorf("expected UpdateProfileRequest access_level=ACCESS_LEVEL_LIMITED, got %+v", lastUpdateSpec)
	}
	if gotPerms := e2eProtoGetCustomPermissions(t, lastUpdateSpec); len(gotPerms) != 0 {
		t.Errorf("expected custom_permissions cleared in UpdateProfileRequest, got %+v", gotPerms)
	}
	st1, _ := h1.readState(t)
	attrs1 := st1.Resources[0].Instances[0].Attributes
	if attrs1["access_level"] != "ACCESS_LEVEL_LIMITED" {
		t.Errorf("expected ACCESS_LEVEL_LIMITED in state, got %v", attrs1["access_level"])
	}
	if cp := attrs1["custom_permissions"]; cp != nil {
		if slice, ok := cp.([]any); !ok || len(slice) != 0 {
			t.Errorf("expected custom_permissions nil/empty in state after non-custom transition, got %#v", cp)
		}
	}

	// Part B: Verify resource_runner_profile Read and data sources handle enum 4 (ACCESS_LEVEL_CUSTOM) and unknown enum 99.
	h2 := e2eStartBlackBoxHarness(t)
	spec4 := &githubv1beta.RunnerProfileSpec{
		Tag:         "raw-4",
		Os:          "ubuntu-24.04",
		AccessLevel: githubv1beta.AccessLevel(4),
	}
	e2eProtoSetCustomPermissions(t, spec4, []*iamv1beta.Permission{
		{ResourceType: "vault/object", ResourceId: "raw-sec", Actions: []string{"read"}},
	})
	h2.mockServer.Profile.ListProfilesFunc = func(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{
			Profiles: []*githubv1beta.RunnerProfileWithStatus{
				{
					ProfileId: "prof-4-raw",
					Version:   1,
					Spec:      spec4,
				},
				{
					ProfileId: "prof-99-unknown",
					Version:   1,
					Spec: &githubv1beta.RunnerProfileSpec{
						Tag:         "raw-99",
						Os:          "ubuntu-24.04",
						AccessLevel: githubv1beta.AccessLevel(99),
					},
				},
			},
		}, nil
	}

	h2.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_runner_profiles" "all" {}

data "namespace_github_runner_profile" "raw4" {
  profile_id = "prof-4-raw"
}

resource "namespace_github_runner_profile" "imported_raw4" {
  tag          = "raw-4"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "raw-sec"
      actions       = ["read"]
    }
  ]
}
`)
	h2.mustInit(t)
	if out, serr, err := h2.runTerraform(t, nil, "import", "namespace_github_runner_profile.imported_raw4", "prof-4-raw"); err != nil {
		t.Fatalf("terraform import prof-4-raw failed: %v\nStdout:\n%s\nStderr:\n%s", err, out, serr)
	}
	h2.mustApply(t)
	h2.mustPlanNoDiff(t)
	st2, _ := h2.readState(t)

	var seenProfilesDS, seenSingleDS, seenImportedRes bool
	for _, r := range st2.Resources {
		switch {
		case r.Mode == "data" && r.Type == "namespace_github_runner_profiles":
			seenProfilesDS = true
			profiles := e2eMustSlice(t, r.Instances[0].Attributes["profiles"], "profiles")
			byTag := map[string]map[string]any{}
			for _, rawP := range profiles {
				m := e2eMustMap(t, rawP, "profile")
				byTag[m["tag"].(string)] = m
			}
			if byTag["raw-4"]["access_level"] != "ACCESS_LEVEL_CUSTOM" {
				t.Errorf("enum 4 must resolve to ACCESS_LEVEL_CUSTOM in data.namespace_github_runner_profiles, got %v", byTag["raw-4"]["access_level"])
			}
			if byTag["raw-99"]["access_level"] == "ACCESS_LEVEL_CUSTOM" || byTag["raw-99"]["access_level"] == "4" {
				t.Errorf("unknown enum 99 leaked invalid value %v", byTag["raw-99"]["access_level"])
			}
		case r.Mode == "data" && r.Type == "namespace_github_runner_profile":
			seenSingleDS = true
			attrs := r.Instances[0].Attributes
			if attrs["access_level"] != "ACCESS_LEVEL_CUSTOM" {
				t.Errorf("enum 4 must resolve to ACCESS_LEVEL_CUSTOM in data.namespace_github_runner_profile, got %v", attrs["access_level"])
			}
			cps := e2eMustSlice(t, attrs["custom_permissions"], "raw4 custom_permissions")
			if len(cps) != 1 {
				t.Errorf("expected 1 custom_permission on raw4 data source, got %d", len(cps))
			}
		case r.Mode == "managed" && r.Type == "namespace_github_runner_profile":
			seenImportedRes = true
			attrs := r.Instances[0].Attributes
			if attrs["access_level"] != "ACCESS_LEVEL_CUSTOM" {
				t.Errorf("enum 4 must resolve to ACCESS_LEVEL_CUSTOM on resource Read/Import, got %v", attrs["access_level"])
			}
			cps := e2eMustSlice(t, attrs["custom_permissions"], "imported_raw4 custom_permissions")
			if len(cps) != 1 {
				t.Errorf("expected 1 custom_permission on imported resource, got %d", len(cps))
			}
		}
	}
	if !seenProfilesDS || !seenSingleDS || !seenImportedRes {
		t.Errorf("missing expected resources in state: profilesDS=%v singleDS=%v importedRes=%v", seenProfilesDS, seenSingleDS, seenImportedRes)
	}
}

// --- Group 4 (R2-OS) Tier 2 ---

func TestE2E_T2_R2OS_RejectInvalidOSAndCaseMismatch(t *testing.T) {
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)

	for _, badOS := range []string{"freebsd", "Windows"} {
		t.Run(badOS, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: e2eUnitTestFactories(cl),
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
provider "namespace" {}
resource "namespace_github_runner_profile" "bad_os" {
  tag = "bad-os"
  os  = "ubuntu-24.04"
  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = %q
  }
}
`, badOS),
						ExpectError: regexp.MustCompile(`(?i)value must be one of|Invalid Attribute Value`),
					},
				},
			})
		})
	}
}

func TestE2E_T2_R2OS_AcceptExplicitEmptyOS(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "empty_os" {
  tag = "empty-os"
  os  = "ubuntu-24.04"
  instance_shape = {
    virtual_cpu      = 4
    memory_megabytes = 8192
    machine_arch     = "amd64"
    os               = ""
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T2_R2OS_WindowsWithARM64AndEmptySelectors(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "win_arm" {
  tag = "win-arm64"
  os  = "windows-11-arm"
  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 16384
    machine_arch     = "arm64"
    os               = "windows"
    selectors        = []
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
	st, _ := h.readState(t)
	shape := e2eMustMap(t, st.Resources[0].Instances[0].Attributes["instance_shape"], "instance_shape")
	sels, ok := shape["selectors"].([]any)
	if !ok || len(sels) != 0 {
		t.Errorf("expected explicit empty selectors [], got %#v", shape["selectors"])
	}
}

func TestE2E_T2_R2OS_BaseImagesMixedOSDeterministicSort(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{Label: "windows-2022", Spec: &imagesv1beta.BaseImage{Id: "w", Os: "windows", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
				{Label: "macos-15", Spec: &imagesv1beta.BaseImage{Id: "m", Os: "macos", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
				{Label: "ubuntu-24.04", Spec: &imagesv1beta.BaseImage{Id: "l", Os: "linux", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_base_images" "all" {}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	imgs := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["images"], "base images")
	if len(imgs) != 3 {
		t.Fatalf("expected 3 images, got %d", len(imgs))
	}
	gotLabels := []string{
		e2eMustMap(t, imgs[0], "imgs[0]")["label"].(string),
		e2eMustMap(t, imgs[1], "imgs[1]")["label"].(string),
		e2eMustMap(t, imgs[2], "imgs[2]")["label"].(string),
	}
	if gotLabels[0] != "macos-15" || gotLabels[1] != "ubuntu-24.04" || gotLabels[2] != "windows-2022" {
		t.Errorf("expected sorted [macos-15 ubuntu-24.04 windows-2022], got %v", gotLabels)
	}
}

func TestE2E_T2_R2OS_BuilderConfigurationNilShapeVsWindows(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		if req.GetPlatform() == "windows/amd64" {
			return &builderv1beta.GetBuilderConfigurationResponse{
				BuildkitEndpoint: "bk-win:443",
				Shape:            &computev1beta.InstanceShape{VirtualCpu: 4, MemoryMegabytes: 8192, MachineArch: "amd64", Os: "windows"},
			}, nil
		}
		return &builderv1beta.GetBuilderConfigurationResponse{
			BuildkitEndpoint: "bk-nil:443",
			Shape:            nil,
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_builder_configuration" "nilshape" {
  platform = "linux/amd64"
}
data "namespace_builder_configuration" "winshape" {
  platform = "windows/amd64"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	for _, r := range st.Resources {
		attrs := r.Instances[0].Attributes
		if r.Name == "nilshape" && attrs["shape"] != nil {
			t.Errorf("expected nil shape for nilshape, got %#v", attrs["shape"])
		}
		if r.Name == "winshape" {
			shape := e2eMustMap(t, attrs["shape"], "winshape.shape")
			if shape["os"] != "windows" {
				t.Errorf("expected shape.os=windows for winshape, got %#v", attrs["shape"])
			}
		}
	}
}

// --- Group 5 (R2-Redact & Scan) Tier 2 ---

func TestE2E_T2_R2Redact_NSRTBoundariesAndBenignWords(t *testing.T) {
	validPunct := "nsrt_" + "a.b-c_1234567890"
	benign := "runner_profile_linux status=ok"
	out := redact.Secrets("val=" + validPunct + " " + benign)
	if strings.Contains(out, validPunct) || strings.Contains(out, "1234567890") {
		t.Errorf("valid nsrt_ with dots/hyphens/underscores leaked: %q", out)
	}
	if !strings.Contains(out, benign) {
		t.Errorf("benign text was corrupted: %q", out)
	}
}

func TestE2E_T2_R2Redact_NSCTokenSpecQuotedJSONAndSpaces(t *testing.T) {
	in := `NSC_TOKEN_SPEC="{\"token\":\"my_secret_spec_val\",\"nested\":true}" other=ok`
	out := redact.Secrets(in)
	if strings.Contains(out, "my_secret_spec_val") {
		t.Errorf("escaped-quote NSC_TOKEN_SPEC leaked: %q", out)
	}
	if !strings.Contains(out, "NSC_TOKEN_SPEC=[REDACTED]") || !strings.Contains(out, "other=ok") {
		t.Errorf("unexpected NSC_TOKEN_SPEC redaction output: %q", out)
	}
}

func TestE2E_T2_R2Redact_NSCTokenSpecFileCaseInsensitiveAndDelimiters(t *testing.T) {
	in := "nsc_token_spec_file=/tmp/secret-spec.json; status=ready, ok=1"
	out := redact.Secrets(in)
	if strings.Contains(out, "/tmp/secret-spec.json") {
		t.Errorf("lowercase nsc_token_spec_file leaked: %q", out)
	}
	if !strings.Contains(out, "=[REDACTED]; status=ready, ok=1") {
		t.Errorf("trailing delimiters not preserved cleanly: %q", out)
	}
}

func TestE2E_T2_R2Redact_PrefixedJWTMalformedAndHyphenPrefixes(t *testing.T) {
	hyphenJWT := e2eRuntimeJWT("my-idp_", "hyphen-sub")
	out := redact.Secrets("auth=" + hyphenJWT)
	if strings.Contains(out, "sig_e2e_test_payload_9876543210") {
		t.Errorf("hyphen-prefixed JWT leaked: %q", out)
	}
	malformed := "oidc_" + "ey" + "J0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.onlytwoparts"
	_ = redact.Secrets(malformed) // must not panic or hang
}

func TestE2E_T2_R2Redact_PrefixedJWTInJSONEscapedError(t *testing.T) {
	jwt := e2eRuntimeJWT("cognito_", "json-esc")
	in := fmt.Sprintf(`{"detail":"rpc failed: \"authorization\": [\"Bearer %s\"]"}`, jwt)
	out := redact.Secrets(in)
	if strings.Contains(out, "sig_e2e_test_payload_9876543210") {
		t.Errorf("JSON-escaped prefixed JWT leaked: %q", out)
	}
	if redact.Secrets(out) != out {
		t.Errorf("redaction not idempotent on %q", out)
	}
}

func TestE2E_T2_R2Scan_GitleaksDetectsOpaqueNSRTBoundary(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "gitleaks")
	cfgPath := filepath.Join(root, ".gitleaks.toml")

	tmp := t.TempDir()
	posFile1 := filepath.Join(tmp, "positive_16.txt")
	posFile2 := filepath.Join(tmp, "positive_32.txt")
	negFile := filepath.Join(tmp, "negative_15.txt")

	posSecret1 := "nsrt_" + "AbCdEf0123456789"
	posSecret2 := "nsrt_" + "revokable.token_value-1234567890"
	negBoundary := "nsrt_" + "AbCdEf012345678" // 15 chars after nsrt_

	if err := os.WriteFile(posFile1, []byte("val="+posSecret1+"\n"), 0600); err != nil {
		t.Fatalf("write positive_16.txt: %v", err)
	}
	if err := os.WriteFile(posFile2, []byte("val="+posSecret2+"\n"), 0600); err != nil {
		t.Fatalf("write positive_32.txt: %v", err)
	}
	if err := os.WriteFile(negFile, []byte("val="+negBoundary+"\n"), 0600); err != nil {
		t.Fatalf("write negative_15.txt: %v", err)
	}

	type gitleaksFinding struct {
		RuleID string `json:"RuleID"`
		Secret string `json:"Secret"`
	}

	for _, tc := range []struct {
		name   string
		path   string
		secret string
	}{
		{"16_chars", posFile1, posSecret1},
		{"32_chars", posFile2, posSecret2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reportPath := filepath.Join(tmp, "report_"+tc.name+".json")
			posCmd := exec.Command("gitleaks", "dir", "--config", cfgPath, "--no-banner", "--redact=100", "--report-format", "json", "--report-path", reportPath, tc.path)
			posOut, posErr := posCmd.CombinedOutput()
			if posErr == nil {
				t.Fatalf("expected gitleaks to detect %s nsrt_ token in %s, but exited 0:\n%s", tc.name, tc.path, string(posOut))
			}
			if strings.Contains(string(posOut), tc.secret) {
				t.Errorf("gitleaks --redact=100 output leaked raw nsrt_ secret:\n%s", string(posOut))
			}
			repRaw, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatalf("read gitleaks JSON report %s: %v", reportPath, err)
			}
			var findings []gitleaksFinding
			if err := json.Unmarshal(repRaw, &findings); err != nil {
				t.Fatalf("unmarshal gitleaks JSON report: %v\n%s", err, string(repRaw))
			}
			matchedRule := false
			for _, f := range findings {
				if f.RuleID == "namespace-cloud-revokable-token" {
					matchedRule = true
					break
				}
			}
			if !matchedRule {
				t.Errorf("expected RuleID=namespace-cloud-revokable-token in findings, got %+v", findings)
			}
		})
	}

	negCmd := exec.Command("gitleaks", "dir", "--config", cfgPath, "--no-banner", "--redact=100", negFile)
	if negOut, negErr := negCmd.CombinedOutput(); negErr != nil {
		t.Errorf("expected 15-char nsrt_ boundary (%q) not to trigger >=16-char rule, got err=%v:\n%s", "val=...", negErr, string(negOut))
	}
}

func TestE2E_T2_R2Scan_SecretScanDefenseInDepthTempRepo(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "gitleaks")

	// Verify scripts/secret-scan.sh itself includes an nsrt_ defense-in-depth grep rule.
	scanScriptRaw, err := os.ReadFile(filepath.Join(root, "scripts", "secret-scan.sh"))
	if err != nil {
		t.Fatalf("read secret-scan.sh: %v", err)
	}
	if !strings.Contains(string(scanScriptRaw), "nsrt_") {
		t.Errorf("scripts/secret-scan.sh is missing defense-in-depth nsrt_ pattern in scan_working_tree")
	}

	tmpRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpRepo, "scripts"), 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	for _, rel := range []string{".gitleaks.toml", ".gitleaksignore", filepath.Join("scripts", "secret-scan.sh")} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(tmpRepo, rel), data, 0755); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmpRepo, "README.md"), []byte("clean\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "e2e@example.invalid")
	runGit("config", "user.name", "E2E")
	runGit("add", ".")
	runGit("commit", "-q", "-m", "clean baseline")

	// Drop untracked file containing runtime-concatenated nsrt_ secret
	leakTok := e2eRuntimeNSRT("untracked")
	if err := os.WriteFile(filepath.Join(tmpRepo, "leaked.txt"), []byte("token="+leakTok+"\n"), 0600); err != nil {
		t.Fatalf("write leaked.txt: %v", err)
	}

	scanCmd := exec.Command("bash", filepath.Join(tmpRepo, "scripts", "secret-scan.sh"))
	scanCmd.Dir = tmpRepo
	out, err := scanCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected secret-scan.sh to fail closed on untracked nsrt_ secret, exited 0:\n%s", string(out))
	}
}

func TestE2E_T2_R2Scan_GenerateAcceptanceReportScrubsNewFormats(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "python3")

	nsrt := e2eRuntimeNSRT("rep_matrix")
	oidc := e2eRuntimeJWT("oidc_", "rep-oidc")
	cog := e2eRuntimeJWT("cognito_", "rep-cog")
	specVal := "super_secret_spec_token_987"
	specFile := "/tmp/e2e-secret-spec-fixture.json"

	logContent := e2eBuildValidMatrixLog(map[string][]string{
		"TestAccSecurity_SecretScrubbing": {
			"nsrt=" + nsrt,
			"NSC_TOKEN_SPEC=" + specVal,
			fmt.Sprintf(`NSC_TOKEN_SPEC={"token":%q}`, specVal),
			"NSC_TOKEN_SPEC_FILE=" + specFile,
			"oidc=" + oidc,
			"cognito=" + cog,
		},
	}, "")

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "matrix.json")
	outDir := filepath.Join(tmp, "out")
	if err := os.WriteFile(logPath, []byte(logContent), 0600); err != nil {
		t.Fatalf("write matrix.json: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "generate-acceptance-report.sh"),
		"--input", logPath,
		"--output-dir", outDir,
		"--commit-sha", "0123456789abcdef0123456789abcdef01234567",
		"--run-id", "99999",
		"--run-link", "https://github.com/bsuv/terraform-provider-namespace/actions/runs/99999",
		"--suite", "matrix",
	)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate-acceptance-report.sh failed: %v\n%s", err, string(out))
	}

	for _, artifact := range []string{"sanitized-acceptance.log", "acceptance-report.md", "junit.xml"} {
		data, err := os.ReadFile(filepath.Join(outDir, artifact))
		if err != nil {
			t.Fatalf("read %s: %v", artifact, err)
		}
		text := string(data)
		for _, secret := range []string{nsrt, oidc, cog, specVal, specFile, "sig_e2e_test_payload_9876543210"} {
			if strings.Contains(text, secret) {
				t.Errorf("artifact %s leaked secret %q", artifact, secret)
			}
		}
	}

	// Every line in sanitized-acceptance.log must remain valid JSON.
	sanitizedBytes, _ := os.ReadFile(filepath.Join(outDir, "sanitized-acceptance.log"))
	for i, line := range strings.Split(strings.TrimSpace(string(sanitizedBytes)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("sanitized-acceptance.log line %d invalid JSON: %v\nline: %s", i+1, err, line)
		}
	}
}

// --- Group 6 (R2-Errors) Tier 2 ---

func TestE2E_T2_R2Err_PermissionDeniedErrorScrubsEmbeddedSecrets(t *testing.T) {
	nsrt := e2eRuntimeNSRT("err_leak")
	oidc := e2eRuntimeJWT("oidc_", "err-sub")
	st, err := status.New(codes.PermissionDenied, "denied for "+nsrt).WithDetails(&iamv1beta.PermissionDeniedError{
		MissingAccessPermissions: []*iamv1beta.Access{
			{ResourceType: "vault/object", ResourceId: oidc, Action: "read"},
		},
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "read", "namespace_vault_secret_metadata")
	detail := diags[0].Detail()
	if strings.Contains(detail, nsrt) || strings.Contains(detail, oidc) || strings.Contains(detail, "sig_e2e_test_payload") {
		t.Errorf("HandleGRPCError leaked embedded secrets in detail: %q", detail)
	}
	if !strings.Contains(detail, "vault/object") {
		t.Errorf("expected resource_type preserved after redaction: %q", detail)
	}
}

func TestE2E_T2_R2Err_PermissionDeniedEmptyErrorNoTrailingBlankDetails(t *testing.T) {
	st, err := status.New(codes.PermissionDenied, "forbidden").WithDetails(&iamv1beta.PermissionDeniedError{})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "create", "namespace_github_runner_profile")
	if !diags.HasError() {
		t.Fatal("expected diagnostic")
	}
	detail := diags[0].Detail()
	if strings.Contains(detail, "Details:\n- \n") || strings.HasSuffix(detail, "Details:") {
		t.Errorf("unexpected empty Details trailer in %q", detail)
	}
}

func TestE2E_T2_R2Err_FailedPreconditionNonRevisionNotMisclassified(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "tenant group still has enrolled tenants").WithDetails(&errdetails.PreconditionFailure{
		Violations: []*errdetails.PreconditionFailure_Violation{
			{Type: "ENROLLED_TENANTS", Subject: "grp-1", Description: "unenroll tenants first"},
		},
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "delete", "namespace_tenant_group")
	if strings.Contains(diags[0].Summary(), "Concurrency Conflict") {
		t.Errorf("non-revision FailedPrecondition misclassified as Concurrency Conflict: %q", diags[0].Summary())
	}
	if !strings.Contains(diags[0].Detail(), "ENROLLED_TENANTS") {
		t.Errorf("expected PreconditionFailure details preserved, got %q", diags[0].Detail())
	}
}

func TestE2E_T2_R2Err_PermissionDeniedCombinedDetails(t *testing.T) {
	st, err := status.New(codes.PermissionDenied, "denied").WithDetails(
		&iamv1beta.PermissionDeniedError{
			MissingAccessPermissions: []*iamv1beta.Access{
				{ResourceType: "github/runner-profile", ResourceId: "p1", Action: "update"},
			},
		},
		&errdetails.ErrorInfo{Reason: "IAM_DENIED", Domain: "iam.namespaceapis.com"},
		&errdetails.BadRequest{
			FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: "spec.access_level", Description: "not permitted"}},
		},
	)
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), st.Err(), &diags, "update", "namespace_github_runner_profile")
	detail := diags[0].Detail()
	for _, want := range []string{"github/runner-profile", "IAM_DENIED", "spec.access_level"} {
		if !strings.Contains(detail, want) {
			t.Errorf("expected combined detail %q in %q", want, detail)
		}
	}
}

func TestE2E_T2_R2Err_NonGRPCErrorRedactsTokenSpec(t *testing.T) {
	var diags diag.Diagnostics
	client.HandleGRPCError(context.Background(), errors.New("transport failed with NSC_TOKEN_SPEC=secret_payload_abc"), &diags, "read", "namespace_tenant_group")
	if !diags.HasError() {
		t.Fatal("expected error diagnostic")
	}
	if strings.Contains(diags[0].Detail(), "secret_payload_abc") {
		t.Errorf("non-gRPC error leaked NSC_TOKEN_SPEC: %q", diags[0].Detail())
	}
}

// --- Group 7 (R2-Auth) Tier 2 ---

func TestE2E_T2_R2Auth_MalformedNonJSONStillRejected(t *testing.T) {
	tokPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(tokPath, []byte("not-json"), 0600); err != nil {
		t.Fatalf("write bad.json: %v", err)
	}
	_, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{TokenFile: tokPath})
	if err == nil || !strings.Contains(err.Error(), "contains malformed JSON") {
		t.Errorf("expected 'contains malformed JSON', got %v", err)
	}
}

func TestE2E_T2_R2Auth_EmptyAndWhitespaceTokenFileRejected(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{"empty", ""},
		{"whitespace", "   \n\t  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "empty.json")
			if err := os.WriteFile(p, []byte(tc.content), 0600); err != nil {
				t.Fatalf("write file: %v", err)
			}
			_, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{TokenFile: p})
			if err == nil || !strings.Contains(err.Error(), "is empty") {
				t.Errorf("expected 'is empty', got %v", err)
			}
		})
	}
}

func TestE2E_T2_R2Auth_JSONMissingTokensRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.json")
	if err := os.WriteFile(p, []byte(`{"foo":"bar"}`), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{TokenFile: p})
	if err == nil || !strings.Contains(err.Error(), "missing or has empty bearer_token or session_token") {
		t.Errorf("expected missing bearer_token/session_token error, got %v", err)
	}
}

func TestE2E_T2_R2Auth_PrecedenceChainWithSTExchange(t *testing.T) {
	e2eInitBufconnTransport(t)
	cfgST := e2eRuntimeST("prec_cfg")
	envTok := e2eRuntimeJWT("nsct_", "prec-env")
	fileST := e2eRuntimeST("prec_file")
	exchangedCfg := e2eRuntimeJWT("nsct_", "ex-cfg")
	exchangedFile := e2eRuntimeJWT("nsct_", "ex-file")

	filePath := filepath.Join(t.TempDir(), "raw_st")
	if err := os.WriteFile(filePath, []byte(fileST), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Case 1: cfg.Token (st_) overrides NSC_TOKEN & NSC_TOKEN_FILE
	t.Setenv("NSC_TOKEN", envTok)
	t.Setenv("NSC_TOKEN_FILE", filePath)
	sess1 := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchangedCfg}, nil
		},
	}
	dialer1 := e2eStartSessionAndProfileServer(t, sess1, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{}, nil
	})
	ts1, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{
		Token:       cfgST,
		IAMEndpoint: "https://iam.namespaceapis.com:443",
		Creds:       insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(dialer1)},
	})
	if err != nil {
		t.Fatalf("ResolveTokenSource case 1: %v", err)
	}
	got1, err := ts1.IssueToken(context.Background(), time.Minute, false)
	if err != nil || got1 != exchangedCfg {
		t.Errorf("case 1 expected %q, got %q (err=%v)", exchangedCfg, got1, err)
	}

	// Case 2: cfg.TokenFile (raw st_) overrides NSC_TOKEN
	sess2 := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchangedFile}, nil
		},
	}
	dialer2 := e2eStartSessionAndProfileServer(t, sess2, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{}, nil
	})
	ts2, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{
		TokenFile:   filePath,
		IAMEndpoint: "https://iam.namespaceapis.com:443",
		Creds:       insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(dialer2)},
	})
	if err != nil {
		t.Fatalf("ResolveTokenSource case 2: %v", err)
	}
	got2, err := ts2.IssueToken(context.Background(), time.Minute, false)
	if err != nil || got2 != exchangedFile {
		t.Errorf("case 2 expected %q, got %q (err=%v)", exchangedFile, got2, err)
	}
}

func TestE2E_T2_R2Auth_WindowsMetadataDirEmptyFallbackAndDirError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows runtime metadata fallback test skipped on non-Windows host")
	}
	progData := t.TempDir()
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")
	t.Setenv("NSC_METADATA_DIR", "   ")
	t.Setenv("ProgramData", progData)

	progDir := filepath.Join(progData, "Namespace", "GuestAgent", "metadata")
	if err := os.MkdirAll(progDir, 0700); err != nil {
		t.Fatalf("mkdir progDir: %v", err)
	}
	// Put a directory at token.json to verify fail-closed "is a directory" diagnostic
	if err := os.Mkdir(filepath.Join(progDir, "token.json"), 0700); err != nil {
		t.Fatalf("mkdir token.json dir: %v", err)
	}
	_, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{})
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("expected 'is a directory' error when ProgramData token.json is a dir, got %v", err)
	}
}

func TestE2E_T2_R2Auth_STExchangeFailureScrubsSessionToken(t *testing.T) {
	e2eInitBufconnTransport(t)
	stTok := e2eRuntimeST("fail_scrub")

	sessSrv := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return nil, status.Error(codes.Unauthenticated, "expired session "+stTok)
		},
	}
	dialer := e2eStartSessionAndProfileServer(t, sessSrv, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
		return &githubv1beta.ListProfilesResponse{}, nil
	})
	ts, err := client.ResolveTokenSource(context.Background(), client.AuthConfig{
		Token:       stTok,
		IAMEndpoint: "https://iam.namespaceapis.com:443",
		Creds:       insecure.NewCredentials(),
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(dialer)},
	})
	if err != nil {
		t.Fatalf("ResolveTokenSource: %v", err)
	}
	_, issueErr := ts.IssueToken(context.Background(), time.Minute, false)
	if issueErr == nil {
		t.Fatal("expected IssueToken failure on Unauthenticated")
	}
	scrubbed := client.RedactSecrets(issueErr.Error())
	if strings.Contains(scrubbed, stTok) {
		t.Errorf("RedactSecrets leaked raw st_ token in exchange error: %q", scrubbed)
	}
}

// --- Group 8 (R3-Registry) Tier 2 ---

func TestE2E_T2_R3Reg_RejectInvalidDeletionFilter(t *testing.T) {
	srv := testutil.NewMockServer()
	cl := testutil.NewTestClient(t, srv)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: e2eUnitTestFactories(cl),
		Steps: []resource.TestStep{
			{
				Config: `
provider "namespace" {}
data "namespace_registry_images" "bad" {
  repository      = "app/web"
  deletion_filter = "ONLY_DELETED"
}
`,
				ExpectError: regexp.MustCompile(`(?i)value must be one of|Invalid Attribute Value`),
			},
		},
	})
}

func TestE2E_T2_R3Reg_RejectInvalidExpiresAtTimestamp(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	for _, ts := range []string{"not-a-timestamp", "2026-13-99T00:00:00Z"} {
		t.Run(ts, func(t *testing.T) {
			h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+fmt.Sprintf(`
data "namespace_registry_images" "bad_ts" {
  repository = "app/web"
  expires_at = {
    after = %q
  }
}
`, ts))
			h.mustInit(t)
			stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
			if err == nil {
				t.Fatalf("expected RFC3339 diagnostic for invalid expires_at %q", ts)
			}
			combined := stdout + "\n" + stderr
			if strings.Contains(combined, "Unsupported argument") {
				t.Fatalf("terraform apply failed with Unsupported argument (M3 schema missing) instead of RFC3339 validation:\n%s", combined)
			}
			lower := strings.ToLower(combined)
			if !strings.Contains(combined, "expires_at") || (!strings.Contains(combined, "RFC3339") && !strings.Contains(lower, "timestamp")) {
				t.Errorf("expected diagnostic referencing expires_at RFC3339/timestamp validation for %q, got:\n%s", ts, combined)
			}
		})
	}
}

func TestE2E_T2_R3Reg_InvertedExpiresAtWindowDeterministic(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{}, nil
	}
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "inverted" {
  repository = "app/web"
  expires_at = {
    after  = "2026-12-01T00:00:00Z"
    before = "2026-11-01T00:00:00Z"
  }
}
`)
	h.mustInit(t)
	// Must not panic or fail on missing schema: either succeeds cleanly with 0 images or rejects with clean expires_at/time-range diagnostic.
	stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
	combined := stdout + "\n" + stderr
	if err == nil {
		st, _ := h.readState(t)
		imgs := e2eMustSlice(t, st.Resources[0].Instances[0].Attributes["images"], "inverted window images")
		if len(imgs) != 0 {
			t.Errorf("expected 0 images for inverted expires_at window when apply succeeds, got %d", len(imgs))
		}
		return
	}
	if strings.Contains(combined, "Unsupported argument") || strings.Contains(combined, "Unimplemented") || strings.Contains(combined, "panic") {
		t.Fatalf("invalid/unexpected failure on inverted expires_at window:\n%s", combined)
	}
	lower := strings.ToLower(combined)
	if !strings.Contains(combined, "expires_at") || (!strings.Contains(lower, "after") && !strings.Contains(lower, "before") && !strings.Contains(lower, "range") && !strings.Contains(lower, "window") && !strings.Contains(lower, "timestamp")) {
		t.Errorf("expected diagnostic referencing expires_at / time range on inverted window, got:\n%s", combined)
	}
}

func TestE2E_T2_R3Reg_PaginationForwardsFiltersOnEveryPage(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	type pageCall struct {
		cursor           string
		deletionFiltName string
		hasExpiresAt     bool
	}
	var calls []pageCall
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		cursor := string(req.GetPaginationCursor())
		calls = append(calls, pageCall{
			cursor:           cursor,
			deletionFiltName: e2eProtoGetListImagesDeletionFilterName(t, req),
			hasExpiresAt:     e2eProtoGetListImagesExpiresAt(t, req) != nil,
		})
		switch cursor {
		case "":
			return &registryv1beta.ListImagesResponse{
				Images:           []*registryv1beta.Image{{Repository: "app/pg", Digest: "sha256:p1", CreatedAt: timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))}},
				PaginationCursor: []byte("page-2"),
			}, nil
		case "page-2":
			return &registryv1beta.ListImagesResponse{
				Images:           []*registryv1beta.Image{{Repository: "app/pg", Digest: "sha256:p2", CreatedAt: timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))}},
				PaginationCursor: []byte("page-3"),
			}, nil
		default:
			return &registryv1beta.ListImagesResponse{
				Images: []*registryv1beta.Image{{Repository: "app/pg", Digest: "sha256:p3", CreatedAt: timestamppb.New(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))}},
			}, nil
		}
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "paged" {
  repository      = "app/pg"
  deletion_filter = "EXCLUDE_DELETED"
  expires_at = {
    after  = "2026-11-01T00:00:00Z"
    before = "2026-12-01T00:00:00Z"
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if len(calls) != 3 {
		t.Fatalf("expected 3 paginated ListImages calls, got %d", len(calls))
	}
	for i, c := range calls {
		if c.deletionFiltName != "EXCLUDE_DELETED" || !c.hasExpiresAt {
			t.Errorf("page %d (cursor=%q) lost/mismatched filter: deletion_filter=%q (want EXCLUDE_DELETED) hasExpiresAt=%v", i, c.cursor, c.deletionFiltName, c.hasExpiresAt)
		}
	}
}

func TestE2E_T2_R3Reg_EmptyLabelsAndNullDeletedAtEdgeCases(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.mockServer.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return &registryv1beta.GetImageResponse{
			Image: &registryv1beta.Image{
				Repository: req.GetRepository(),
				Digest:     "sha256:empty",
				CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
				Tags:       []string{"z", "a"},
			},
		}, nil
	}
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		return &registryv1beta.ListImagesResponse{
			Images: []*registryv1beta.Image{
				{
					Repository: "app/empty",
					Digest:     "sha256:empty",
					CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
					Tags:       []string{"z", "a"},
				},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_image" "one" {
  repository = "app/empty"
  reference  = "a"
}
data "namespace_registry_images" "list" {
  repository = "app/empty"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	st, _ := h.readState(t)
	var seenSingle, seenList bool
	for _, r := range st.Resources {
		attrs := r.Instances[0].Attributes
		switch r.Type {
		case "namespace_registry_image":
			seenSingle = true
			if attrs["deleted_at"] != nil {
				t.Errorf("single image expected null deleted_at, got %v", attrs["deleted_at"])
			}
			lbls, ok := attrs["labels"].(map[string]any)
			if !ok || len(lbls) != 0 {
				t.Errorf("single image expected empty labels map {}, got %#v", attrs["labels"])
			}
		case "namespace_registry_images":
			seenList = true
			imgs := e2eMustSlice(t, attrs["images"], "registry images")
			if len(imgs) != 1 {
				t.Fatalf("expected 1 image in namespace_registry_images, got %d", len(imgs))
			}
			img0 := e2eMustMap(t, imgs[0], "images[0]")
			if img0["deleted_at"] != nil {
				t.Errorf("images[0] expected null deleted_at, got %v", img0["deleted_at"])
			}
			lbls, ok := img0["labels"].(map[string]any)
			if !ok || len(lbls) != 0 {
				t.Errorf("images[0] expected empty labels map {}, got %#v", img0["labels"])
			}
		}
	}
	if !seenSingle || !seenList {
		t.Errorf("missing expected resources in state: seenSingle=%v seenList=%v", seenSingle, seenList)
	}
}

// --- Group 9 (R3-Endpoints) Tier 2 ---

func TestE2E_T2_R3End_LookalikeDomainsRemainUntrusted(t *testing.T) {
	t.Setenv("NSC_GLOBAL_ENDPOINT", "")
	t.Setenv("NSC_IAM_ENDPOINT", "")
	t.Setenv("NSC_ENDPOINT", "")
	ctx := context.Background()
	p := provider.New("e2e")()

	for _, evil := range []string{
		"https://evil-namespaceapi.com",
		"https://namespaceapi.com.evil.com",
		"https://notnamespaceapi.com",
	} {
		if endpoint.IsTrustedHost(evil) {
			t.Errorf("lookalike host %q must NOT be trusted", evil)
		}
		resp := &tfprovider.ConfigureResponse{}
		p.Configure(ctx, tfprovider.ConfigureRequest{
			Config: e2eMakeProviderConfig(ctx, p, map[string]any{
				"token":            e2eRuntimeProviderToken(),
				"compute_endpoint": evil,
			}),
		}, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("expected non-blocking warning (not error) for %q: %v", evil, resp.Diagnostics.Errors())
		}
		if len(resp.Diagnostics.Warnings()) != 1 {
			t.Errorf("expected 1 untrusted endpoint warning for %q, got %v", evil, resp.Diagnostics.Warnings())
		}
	}
}

func TestE2E_T2_R3End_UntrustedEnvOverriddenByNamespaceAPIComHCL(t *testing.T) {
	t.Setenv("NSC_GLOBAL_ENDPOINT", "")
	t.Setenv("NSC_IAM_ENDPOINT", "")
	t.Setenv("NSC_ENDPOINT", "https://untrusted.corp.example:443")

	ctx := context.Background()
	p := provider.New("e2e")()
	resp := &tfprovider.ConfigureResponse{}
	p.Configure(ctx, tfprovider.ConfigureRequest{
		Config: e2eMakeProviderConfig(ctx, p, map[string]any{
			"token":            e2eRuntimeProviderToken(),
			"compute_endpoint": "https://compute.namespaceapi.com:443",
		}),
	}, resp)
	if resp.Diagnostics.HasError() || len(resp.Diagnostics.Warnings()) != 0 {
		t.Errorf("HCL override with compute.namespaceapi.com should suppress warning, got errors=%v warnings=%v", resp.Diagnostics.Errors(), resp.Diagnostics.Warnings())
	}
}

func TestE2E_T2_R3End_RejectInvalidSchemeAndUserinfoOnNamespaceAPICom(t *testing.T) {
	ctx := context.Background()
	p := provider.New("e2e")()
	for _, bad := range []string{
		"http://compute.namespaceapi.com",
		"https://user:pass@compute.namespaceapi.com",
		"https://compute.namespaceapi.com/path",
		"https://compute.namespaceapi.com?q=1",
	} {
		if err := endpoint.Validate(bad); err == nil {
			t.Errorf("endpoint.Validate(%q) should fail", bad)
		}
		resp := &tfprovider.ConfigureResponse{}
		p.Configure(ctx, tfprovider.ConfigureRequest{
			Config: e2eMakeProviderConfig(ctx, p, map[string]any{
				"token":            e2eRuntimeProviderToken(),
				"compute_endpoint": bad,
			}),
		}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("provider.Configure should fail on %q", bad)
		}
	}
}

func TestE2E_T2_R3End_RejectInvalidPortsAndAcceptTrailingDot(t *testing.T) {
	for _, bad := range []string{
		"https://compute.namespaceapi.com:0",
		"https://compute.namespaceapi.com:70000",
		"https://compute.namespaceapi.com:",
	} {
		if err := endpoint.Validate(bad); err == nil {
			t.Errorf("expected Validate(%q) to fail", bad)
		}
	}
	if err := endpoint.Validate("https://compute.namespaceapi.com.:443"); err != nil {
		t.Errorf("expected trailing dot endpoint to validate cleanly, got %v", err)
	}
}

func TestE2E_T2_R3End_SSRFAndMetadataHostsRemainBlocked(t *testing.T) {
	for _, ssrf := range []string{
		"https://169.254.169.254",
		"https://metadata.google.internal",
		"https://0.0.0.0:443",
		"https://[fe80::1]:443",
	} {
		if err := endpoint.Validate(ssrf); err == nil {
			t.Errorf("SSRF host %q must be rejected", ssrf)
		}
	}
}

// --- Group 10 (R3-Limits) Tier 2 ---

func TestE2E_T2_R3Lim_TenantGroupEmptyAndOmittedPoliciesTransitions(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{Name: req.GetName(), Policies: req.GetPolicies()}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	base := e2eProviderHCLHeader(e2eRuntimeProviderToken())
	emptyHCL := base + `
resource "namespace_tenant_group" "grp" {
  name     = "lim-trans"
  policies = []
}
`
	popHCL := base + `
resource "namespace_tenant_group" "grp" {
  name = "lim-trans"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = jsonencode({ concurrency_limits = { max_cpu = 16 } })
  }]
}
`
	omitHCL := base + `
resource "namespace_tenant_group" "grp" {
  name = "lim-trans"
}
`
	for _, hcl := range []string{emptyHCL, popHCL, emptyHCL, omitHCL} {
		h.writeMainTF(t, hcl)
		h.mustInit(t)
		h.mustApply(t)
		h.mustPlanNoDiff(t)
	}
}

func TestE2E_T2_R3Lim_RejectEmptyPolicyAndSurfaceInvalidJSONError(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "bad" {
  name = "lim-bad-policy"
  policies = [{
    policy = ""
    value  = "{}"
  }]
}
`)
	h.mustInit(t)
	if _, _, err := h.runTerraform(t, nil, "validate"); err == nil {
		t.Error("expected validate error for empty policy identifier")
	}

	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid UsagePolicy JSON payload")
	}
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "bad_json" {
  name = "lim-bad-json"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = "not-json"
  }]
}
`)
	stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
	if err == nil {
		t.Fatal("expected InvalidArgument error from EnsureTenantGroup")
	}
	if !strings.Contains(stdout+stderr, "invalid UsagePolicy JSON payload") {
		t.Errorf("expected error message preserved, got:\n%s\n%s", stdout, stderr)
	}
}

func TestE2E_T2_R3Lim_CamelCaseAndSnakeCaseRoundTrip(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{Name: req.GetName(), Policies: req.GetPolicies()}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "snake" {
  name = "lim-snake"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = jsonencode({ concurrency_limits = { max_memory_mb = 32768 } })
  }]
}
resource "namespace_tenant_group" "camel" {
  name = "lim-camel"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = jsonencode({ concurrencyLimits = { maxMemoryMb = 32768 } })
  }]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T2_R3Lim_EphemeralTenantTokenPoliciesCombinedWithAccessGrants(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var seenReq *iamv1beta.IssueTenantTokenRequest
	h.mockServer.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		seenReq = proto.Clone(req).(*iamv1beta.IssueTenantTokenRequest)
		return &iamv1beta.IssueTenantTokenResponse{BearerToken: e2eRuntimeJWT("nsct_", "combined")}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
ephemeral "namespace_tenant_token" "combo" {
  tenant_id = "tenant-combo"
  actor_id  = "actor-combo"
  policies = [{
    policy_type = "namespace.cloud.compute.v1beta.UsagePolicy"
    value       = jsonencode({ concurrency_limits = { max_cpu = 4 } })
  }]
  access = {
    grants = [{
      resource_type = "github/runner-profile"
      resource_id   = "*"
      actions       = ["read"]
    }]
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if seenReq == nil || len(seenReq.GetPolicies()) != 1 || seenReq.GetAccess() == nil || len(seenReq.GetAccess().GetGrants()) != 1 {
		t.Fatalf("expected both Policies and Access.Grants in IssueTenantTokenRequest, got %+v", seenReq)
	}
}

func TestE2E_T2_R3Lim_ZeroValueLimitsPreserved(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var seenVal string
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		if len(req.GetPolicies()) > 0 {
			seenVal = req.GetPolicies()[0].GetValue()
		}
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{Name: req.GetName(), Policies: req.GetPolicies()}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "zero" {
  name = "lim-zero"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = jsonencode({ concurrency_limits = { max_cpu = 0 }, devbox_limits = { max_count = 0 } })
  }]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
	if !strings.Contains(seenVal, `"max_cpu":0`) || !strings.Contains(seenVal, `"max_count":0`) {
		t.Errorf("expected zero-value limits preserved in JSON, got %s", seenVal)
	}
}

// --- Group 11 (R3-Docs/Examples/Inventory) Tier 2 ---

func TestE2E_T2_R3Docs_ExamplesOfflineTerraformValidateAll(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "terraform")
	tfrcPath, tmpDir := e2eBuildLocalMirror(t, root)

	var exampleDirs []string
	for _, sub := range []string{"data-sources/*", "ephemeral-resources/*", "resources/*", "provider"} {
		matches, err := filepath.Glob(filepath.Join(root, "examples", sub))
		if err != nil {
			t.Fatalf("glob examples/%s: %v", sub, err)
		}
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.IsDir() {
				exampleDirs = append(exampleDirs, m)
			}
		}
	}
	if len(exampleDirs) == 0 {
		t.Fatal("no example directories found")
	}

	for _, exDir := range exampleDirs {
		rel, _ := filepath.Rel(root, exDir)
		t.Run(rel, func(t *testing.T) {
			staged := filepath.Join(tmpDir, "staged", rel)
			e2eCopyDir(t, exDir, staged)

			initCmd := exec.Command("terraform", "init", "-backend=false")
			initCmd.Dir = staged
			initCmd.Env = e2eCleanEnv("TF_CLI_CONFIG_FILE="+tfrcPath, "TF_IN_AUTOMATION=1")
			if out, err := initCmd.CombinedOutput(); err != nil {
				t.Fatalf("terraform init failed for %s: %v\n%s", rel, err, string(out))
			}

			valCmd := exec.Command("terraform", "validate")
			valCmd.Dir = staged
			valCmd.Env = e2eCleanEnv("TF_CLI_CONFIG_FILE="+tfrcPath, "TF_IN_AUTOMATION=1")
			if out, err := valCmd.CombinedOutput(); err != nil {
				t.Fatalf("terraform validate failed for %s: %v\n%s", rel, err, string(out))
			}
		})
	}
}

func TestE2E_T2_R3Docs_ExamplesNoStaleVolumeTagOrPolicyKeys(t *testing.T) {
	root := e2eRepoRoot(t)
	stalePatterns := []*regexp.Regexp{
		regexp.MustCompile(`\bvolume_tag\b`),
		regexp.MustCompile(`\bAdminPolicy\b`),
		regexp.MustCompile(`\bRunnerProfilePolicy\b`),
		regexp.MustCompile(`\bmax_cpus\b`),
		regexp.MustCompile(`\bmax_memory\b`),
		regexp.MustCompile(`\bmax_instances\b`),
		regexp.MustCompile(`\bmax_concurrent_instances\b`),
	}
	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".tf") {
			return walkErr
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(raw)
		for _, re := range stalePatterns {
			if re.MatchString(s) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s still contains stale symbol matching %s", rel, re.String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk examples: %v", err)
	}
}

func TestE2E_T2_R3Docs_TemplatesAndDocsNoStaleStringsAndHaveNSRT(t *testing.T) {
	root := e2eRepoRoot(t)
	tmplRaw, err := os.ReadFile(filepath.Join(root, "templates", "index.md.tmpl"))
	if err != nil {
		t.Fatalf("read templates/index.md.tmpl: %v", err)
	}
	if !strings.Contains(string(tmplRaw), "nsrt_") {
		t.Errorf("templates/index.md.tmpl does not document nsrt_")
	}
	idxRaw, err := os.ReadFile(filepath.Join(root, "docs", "index.md"))
	if err != nil {
		t.Fatalf("read docs/index.md: %v", err)
	}
	if !strings.Contains(string(idxRaw), "nsrt_") {
		t.Errorf("docs/index.md does not document nsrt_")
	}

	stalePatterns := []*regexp.Regexp{
		regexp.MustCompile(`\bvolume_tag\b`),
		regexp.MustCompile(`\bAdminPolicy\b`),
		regexp.MustCompile(`\bRunnerProfilePolicy\b`),
		regexp.MustCompile(`\bmax_cpus\b`),
		regexp.MustCompile(`\bmax_memory\b`),
		regexp.MustCompile(`\bmax_instances\b`),
		regexp.MustCompile(`\bmax_concurrent_instances\b`),
	}
	for _, dir := range []string{"templates", "docs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			s := string(raw)
			for _, re := range stalePatterns {
				if re.MatchString(s) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s still contains stale symbol matching %s", rel, re.String())
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestE2E_T2_R3Docs_TenantGroupAndRunnerProfileExamplesContent(t *testing.T) {
	root := e2eRepoRoot(t)
	tgRaw, err := os.ReadFile(filepath.Join(root, "examples", "resources", "namespace_tenant_group", "resource.tf"))
	if err != nil {
		t.Fatalf("read tenant_group example: %v", err)
	}
	tg := string(tgRaw)
	if !strings.Contains(tg, "UsagePolicy") || !strings.Contains(tg, "concurrency_limits") {
		t.Errorf("namespace_tenant_group example must use UsagePolicy with concurrency_limits")
	}

	rpRaw, err := os.ReadFile(filepath.Join(root, "examples", "resources", "namespace_github_runner_profile", "resource.tf"))
	if err != nil {
		t.Fatalf("read runner_profile example: %v", err)
	}
	if strings.Contains(string(rpRaw), "volume_tag") {
		t.Errorf("namespace_github_runner_profile example still contains volume_tag")
	}
}

func TestE2E_T2_R3Docs_InventoryMDHasBSRRevAndNoIntegrations(t *testing.T) {
	root := e2eRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "apicontract", "inventory.md"))
	if err != nil {
		t.Fatalf("read inventory.md: %v", err)
	}
	inv := string(raw)

	goModRaw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	goModRe := regexp.MustCompile(`buf\.build/gen/go/namespace/cloud/(protocolbuffers|grpc)/go\s+v[^-\s]+-(\d{14})-`)
	goModTS := map[string]string{}
	for _, m := range goModRe.FindAllStringSubmatch(string(goModRaw), -1) {
		goModTS[m[1]] = m[2]
	}
	if len(goModTS) != 2 {
		t.Fatalf("expected 2 BSR module timestamps in go.mod, got %v", goModTS)
	}

	invRe := regexp.MustCompile(`buf\.build/gen/go/namespace/cloud/(protocolbuffers|grpc)/go[@\s]+v[^-\s]+-(\d{14})-`)
	invTS := map[string]string{}
	for _, m := range invRe.FindAllStringSubmatch(inv, -1) {
		invTS[m[1]] = m[2]
	}
	for _, kind := range []string{"protocolbuffers", "grpc"} {
		ts, ok := invTS[kind]
		if !ok {
			t.Errorf("inventory.md missing BSR %s pseudo-version timestamp", kind)
			continue
		}
		if ts < "20261106090801" {
			t.Errorf("inventory.md BSR %s timestamp %s < 20261106090801", kind, ts)
		}
		if ts != goModTS[kind] {
			t.Errorf("inventory.md BSR %s timestamp %s != go.mod timestamp %s", kind, ts, goModTS[kind])
		}
	}
	if strings.Contains(inv, "20260820164744") {
		t.Errorf("inventory.md still references old BSR revision 20260820164744")
	}
	if strings.Contains(inv, "namespacelabs.dev/"+"integrations") {
		t.Errorf("inventory.md still references removed integrations SDK")
	}
}

func TestE2E_T2_R3Docs_TestsAndContractNoStalePoliciesOrVolumeTag(t *testing.T) {
	root := e2eRepoRoot(t)
	targets := []string{
		filepath.Join("internal", "apicontract", "compile_test.go"),
		filepath.Join("internal", "services", "iam", "resource_tenant_group_test.go"),
		filepath.Join("internal", "provider", "acc_test.go"),
	}
	stalePatterns := []*regexp.Regexp{
		regexp.MustCompile(`\bAdminPolicy\b`),
		regexp.MustCompile(`\bRunnerProfilePolicy\b`),
		regexp.MustCompile(`\bmax_cpus\b`),
		regexp.MustCompile(`\bmax_memory\b`),
		regexp.MustCompile(`\bmax_instances\b`),
		regexp.MustCompile(`\bmax_concurrent_instances\b`),
		regexp.MustCompile(`\bVolumeTag\b`),
		regexp.MustCompile(`\bvolume_tag\b`),
	}
	for _, rel := range targets {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		s := string(raw)
		for _, re := range stalePatterns {
			if re.MatchString(s) {
				t.Errorf("%s still contains stale symbol matching %s", rel, re.String())
			}
		}
	}
}

// =============================================================================
// TIER 3: CROSS-FEATURE PAIRWISE COMBINATIONS & TOOLING INTEGRATION
// =============================================================================

func TestE2E_T3_01_RunnerProfile_PositionalCache_CustomAccess_WindowsOS(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "combo" {
  tag          = "t3-win-custom-cache"
  os           = "windows-2022"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "signing-cert"
      actions       = ["read"]
    },
    {
      resource_type = "cache/bazel"
      resource_id   = ""
      actions       = ["read", "write"]
    }
  ]
  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 32768
    machine_arch     = "amd64"
    os               = "windows"
  }
  cache_volume_settings = [
    {
      volume_size_gb           = 100
      allow_commit_from_branch = []
    },
    {
      volume_size_gb           = 200
      allow_commit_from_branch = ["main"]
    }
  ]
}

data "namespace_github_runner_profile" "readback" {
  profile_id = namespace_github_runner_profile.combo.profile_id
}

data "namespace_github_runner_profiles" "all" {
  depends_on = [namespace_github_runner_profile.combo]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("state unexpectedly contains volume_tag")
	}
	if len(st.Resources) != 3 {
		t.Fatalf("expected 3 state resources (1 managed + 2 data), got %d", len(st.Resources))
	}
	for _, r := range st.Resources {
		if r.Type == "namespace_github_runner_profile" && r.Mode == "data" {
			attrs := r.Instances[0].Attributes
			if attrs["access_level"] != "ACCESS_LEVEL_CUSTOM" {
				t.Errorf("data source access_level=%v, want ACCESS_LEVEL_CUSTOM", attrs["access_level"])
			}
			shape := e2eMustMap(t, attrs["instance_shape"], "instance_shape")
			if shape["os"] != "windows" {
				t.Errorf("data source instance_shape.os=%v, want windows", shape["os"])
			}
		}
	}
}

func TestE2E_T3_02_StateUpgradeV0ToV1_WithAccessLevelCustomMigration(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, profiles := e2eInstallStatefulProfileMocks(t, h.mockServer)
	profiles["prof-t3-mig"] = &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof-t3-mig",
		Version:   1,
		CreatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		UpdatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag:         "t3-mig",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_LIMITED,
			CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
				{VolumeSizeGb: 100, AllowCommitFromBranch: []string{"main"}},
			},
		},
	}

	v0State := `{
  "version": 4,
  "terraform_version": "1.15.6",
  "serial": 1,
  "lineage": "t3-02-mig",
  "outputs": {},
  "resources": [
    {
      "mode": "managed",
      "type": "namespace_github_runner_profile",
      "name": "mig",
      "provider": "provider[\"registry.terraform.io/bsuv/namespace\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "prof-t3-mig",
            "profile_id": "prof-t3-mig",
            "version": 1,
            "created_at": "2026-01-01T00:00:00Z",
            "updated_at": "2026-01-01T00:00:00Z",
            "tag": "t3-mig",
            "os": "ubuntu-24.04",
            "description": "",
            "emoji": "",
            "swap_size_mb": 0,
            "builder_mode": "BUILDER_MODE_UNSPECIFIED",
            "access_level": "ACCESS_LEVEL_LIMITED",
            "experimental_features": null,
            "instance_shape": null,
            "cache_volume_settings": [
              {
                "volume_tag": "old-vol-tag",
                "volume_size_gb": 100,
                "volume_reserved_size_gb": 0,
                "bundled_container_cache": false,
                "bundled_buildkit_cache": false,
                "bundled_git_mirror": false,
                "bundled_runner_tools_cache": false,
                "bundled_runner_actions_cache": false,
                "allow_commit_from_branch": ["main"]
              }
            ],
            "custom_runner_image_spec": null,
            "network_policy": null,
            "custom_runner_images": null
          }
        }
      ]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(h.workDir, "terraform.tfstate"), []byte(v0State), 0600); err != nil {
		t.Fatalf("write v0 tfstate: %v", err)
	}
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "mig" {
  tag          = "t3-mig"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    {
      resource_type = "vault/object"
      resource_id   = "deploy-key"
      actions       = ["read"]
    }
  ]
  cache_volume_settings = [
    {
      volume_size_gb           = 100
      allow_commit_from_branch = ["main"]
    }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("migrated state must not retain volume_tag")
	}
	if st.Resources[0].Instances[0].Attributes["access_level"] != "ACCESS_LEVEL_CUSTOM" {
		t.Errorf("expected migrated access_level=ACCESS_LEVEL_CUSTOM")
	}
}

func TestE2E_T3_03_Registry_PaginatedFilters_Labels_DeletedAt_CrossCheck(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	delAt := timestamppb.New(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	imgA := &registryv1beta.Image{
		Repository: "org/svc",
		Digest:     "sha256:aaa",
		CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
		ExpiresAt:  timestamppb.New(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)),
		Labels:     []*stdlib.Label{{Name: "stage", Value: "prod"}},
		Tags:       []string{"v1"},
	}
	imgB := &registryv1beta.Image{
		Repository: "org/svc",
		Digest:     "sha256:bbb",
		CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
		ExpiresAt:  timestamppb.New(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)),
		DeletedAt:  delAt,
		Labels:     []*stdlib.Label{{Name: "stage", Value: "gc"}},
		Tags:       []string{"v2-del"},
	}
	imgC := &registryv1beta.Image{
		Repository: "org/svc",
		Digest:     "sha256:ccc",
		CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
		Labels:     []*stdlib.Label{{Name: "arch", Value: "arm64"}},
		Tags:       []string{"v3"},
	}

	pageCalls := 0
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		pageCalls++
		if gotFilter := e2eProtoGetListImagesDeletionFilterName(t, req); gotFilter != "INCLUDE_DELETED" {
			t.Errorf("page %d deletion_filter=%q, want INCLUDE_DELETED", pageCalls, gotFilter)
		}
		if e2eProtoGetListImagesExpiresAt(t, req) == nil {
			t.Errorf("page %d missing expires_at", pageCalls)
		}
		switch string(req.GetPaginationCursor()) {
		case "":
			return &registryv1beta.ListImagesResponse{Images: []*registryv1beta.Image{imgC}, PaginationCursor: []byte("c2")}, nil
		case "c2":
			return &registryv1beta.ListImagesResponse{Images: []*registryv1beta.Image{imgB}, PaginationCursor: []byte("c3")}, nil
		default:
			return &registryv1beta.ListImagesResponse{Images: []*registryv1beta.Image{imgA}}, nil
		}
	}
	h.mockServer.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return &registryv1beta.GetImageResponse{Image: imgB}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_registry_images" "all" {
  repository      = "org/svc"
  deletion_filter = "INCLUDE_DELETED"
  expires_at = {
    after  = "2026-11-15T00:00:00Z"
    before = "2026-12-01T00:00:00Z"
  }
}

data "namespace_registry_image" "deleted" {
  repository = "org/svc"
  reference  = "v2-del"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	if pageCalls != 3 {
		t.Errorf("expected 3 ListImages page calls, got %d", pageCalls)
	}
	st, _ := h.readState(t)
	var listImgs []any
	var singleDeletedAt any
	for _, r := range st.Resources {
		if r.Type == "namespace_registry_images" {
			listImgs = e2eMustSlice(t, r.Instances[0].Attributes["images"], "registry images")
		}
		if r.Type == "namespace_registry_image" {
			singleDeletedAt = r.Instances[0].Attributes["deleted_at"]
		}
	}
	if len(listImgs) != 3 {
		t.Fatalf("expected 3 sorted images, got %d", len(listImgs))
	}
	// Sorted by (created_at, digest): aaa, bbb, ccc
	i0 := e2eMustMap(t, listImgs[0], "listImgs[0]")
	i1 := e2eMustMap(t, listImgs[1], "listImgs[1]")
	i2 := e2eMustMap(t, listImgs[2], "listImgs[2]")
	if i0["digest"] != "sha256:aaa" ||
		i1["digest"] != "sha256:bbb" ||
		i2["digest"] != "sha256:ccc" {
		t.Errorf("unexpected deterministic sort order: %#v", listImgs)
	}
	if i1["deleted_at"] == nil || i1["deleted_at"] == "" {
		t.Errorf("expected non-empty deleted_at on soft-deleted image bbb, got %v", i1["deleted_at"])
	}
	if i1["deleted_at"] != singleDeletedAt {
		t.Errorf("cross-check mismatch deleted_at: list=%v single=%v", i1["deleted_at"], singleDeletedAt)
	}
}

func TestE2E_T3_04_Errors_PermissionDenied_RedactsEmbeddedSecretsCLI(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	nsrt := e2eRuntimeNSRT("t3_04")
	oidc := e2eRuntimeJWT("oidc_", "t3-04-oidc")
	cog := e2eRuntimeJWT("cognito_", "t3-04-cog")

	h.mockServer.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		msg := fmt.Sprintf("access denied with %s NSC_TOKEN_SPEC=leak_val_t3 %s", nsrt, cog)
		st, err := status.New(codes.PermissionDenied, msg).WithDetails(&iamv1beta.PermissionDeniedError{
			MissingAccessPermissions: []*iamv1beta.Access{
				{ResourceType: "github/runner-profile", ResourceId: oidc, Action: "create"},
			},
		})
		if err != nil {
			return nil, err
		}
		return nil, st.Err()
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "fail" {
  tag = "t3-04"
  os  = "ubuntu-24.04"
}
`)
	h.mustInit(t)
	stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
	if err == nil {
		t.Fatal("expected PermissionDenied error")
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "github/runner-profile") || !strings.Contains(combined, "create") {
		t.Errorf("expected PermissionDeniedError missing_access_permissions in CLI output:\n%s", combined)
	}
	for _, secret := range []string{nsrt, oidc, cog, "leak_val_t3", "sig_e2e_test_payload_9876543210"} {
		if strings.Contains(combined, secret) {
			t.Errorf("CLI stderr leaked secret %q:\n%s", secret, combined)
		}
	}
}

func TestE2E_T3_05_Auth_STExchange_WithNamespaceAPIComEndpoints(t *testing.T) {
	e2eInitBufconnTransport(t)
	t.Setenv("NSC_GLOBAL_ENDPOINT", "")
	t.Setenv("NSC_IAM_ENDPOINT", "")
	t.Setenv("NSC_ENDPOINT", "")

	// 1. Verify provider.Configure trusts namespaceapi.com with 0 warnings
	p := provider.New("e2e")()
	ctx := context.Background()
	resp := &tfprovider.ConfigureResponse{}
	p.Configure(ctx, tfprovider.ConfigureRequest{
		Config: e2eMakeProviderConfig(ctx, p, map[string]any{
			"token":            e2eRuntimeProviderToken(),
			"global_endpoint":  "https://global.namespaceapi.com:443",
			"iam_endpoint":     "https://iam.namespaceapi.com:443",
			"compute_endpoint": "https://compute.namespaceapi.com:443",
		}),
	}, resp)
	if resp.Diagnostics.HasError() || len(resp.Diagnostics.Warnings()) != 0 {
		t.Fatalf("expected 0 errors/warnings for namespaceapi.com, got err=%v warn=%v", resp.Diagnostics.Errors(), resp.Diagnostics.Warnings())
	}
	cfgClient, ok := resp.ResourceData.(*client.Client)
	if !ok || cfgClient == nil {
		t.Fatalf("expected *client.Client in ResourceData, got %T", resp.ResourceData)
	}
	defer func() { _ = cfgClient.Close() }()
	if cfgClient.GlobalEndpoint() != "https://global.namespaceapi.com:443" ||
		cfgClient.IAMEndpoint() != "https://iam.namespaceapi.com:443" ||
		cfgClient.ComputeEndpoint() != "https://compute.namespaceapi.com:443" {
		t.Errorf("unexpected configured namespaceapi.com endpoints: global=%q iam=%q compute=%q",
			cfgClient.GlobalEndpoint(), cfgClient.IAMEndpoint(), cfgClient.ComputeEndpoint())
	}

	// 2. Verify raw st_ TokenFile exchanges over UserSessionsService using offline-resolvable endpoint
	stTok := e2eRuntimeST("t3_05")
	exchanged := e2eRuntimeJWT("nsct_", "t3-05-ex")
	stFile := filepath.Join(t.TempDir(), "st_tok")
	if err := os.WriteFile(stFile, []byte(stTok), 0600); err != nil {
		t.Fatalf("write st_tok: %v", err)
	}

	sessSrv := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchanged}, nil
		},
	}
	var downstreamAuth string
	dialer := e2eStartSessionAndProfileServer(t, sessSrv, func(ctx context.Context, authHeader string) (*githubv1beta.ListProfilesResponse, error) {
		downstreamAuth = authHeader
		return &githubv1beta.ListProfilesResponse{}, nil
	})
	cl, err := client.New(ctx, client.Config{
		TokenFile:       stFile,
		GlobalEndpoint:  "https://localhost:443",
		IAMEndpoint:     "https://localhost:443",
		ComputeEndpoint: "https://localhost:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(dialer)},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer func() { _ = cl.Close() }()
	pc, err := cl.ProfileClient(ctx)
	if err != nil {
		t.Fatalf("ProfileClient: %v", err)
	}
	if _, err := pc.ListProfiles(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if downstreamAuth != "Bearer "+exchanged {
		t.Errorf("expected Bearer %s, got %q", exchanged, downstreamAuth)
	}
}

func TestE2E_T3_06_Auth_NSRT_Redact_TenantTokenUsagePolicy_AlreadyExistsDetails(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	nsrt := e2eRuntimeNSRT("t3_06")
	ephTok := e2eRuntimeJWT("nsct_", "t3-06-eph")
	var collideMode atomic.Bool

	h.mockServer.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		return &iamv1beta.IssueTenantTokenResponse{BearerToken: ephTok}, nil
	}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		if collideMode.Load() {
			st, err := status.New(codes.AlreadyExists, "group exists "+nsrt).WithDetails(&errdetails.ErrorInfo{
				Reason: "GROUP_ALREADY_EXISTS",
				Domain: "iam.namespaceapis.com",
			})
			if err != nil {
				return nil, err
			}
			return nil, st.Err()
		}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(nsrt)+`
resource "namespace_tenant_group" "grp" {
  name = "t3-06-grp"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = jsonencode({ concurrency_limits = { max_cpu = 16 }, devbox_limits = { max_count = 2 } })
  }]
}

ephemeral "namespace_tenant_token" "tok" {
  tenant_id = "tenant-t3-06"
  actor_id  = "actor-t3-06"
  policies = [{
    policy_type = "namespace.cloud.compute.v1beta.UsagePolicy"
    value       = jsonencode({ usage_limits = { builds = 25 } })
  }]
}
`)
	// First check plan + apply with nsrt_ + ephemeral token: neither may appear in plan/state/CLI.
	h.mustInit(t)
	if out, serr, err := h.runTerraform(t, nil, "plan", "-out=tfplan"); err != nil {
		t.Fatalf("terraform plan: %v\n%s\n%s", err, out, serr)
	} else if strings.Contains(out+serr, nsrt) || strings.Contains(out+serr, ephTok) {
		t.Errorf("plan output leaked nsrt_ or ephemeral token")
	}
	planBytes, err := os.ReadFile(filepath.Join(h.workDir, "tfplan"))
	if err != nil {
		t.Fatalf("read tfplan: %v", err)
	}
	if strings.Contains(string(planBytes), nsrt) || strings.Contains(string(planBytes), ephTok) {
		t.Errorf("binary tfplan leaked nsrt_ or ephemeral token")
	}

	// Simulate AlreadyExists with ErrorInfo on apply.
	collideMode.Store(true)
	out, serr, applyErr := h.runTerraform(t, nil, "apply", "-auto-approve")
	if applyErr == nil {
		t.Fatal("expected AlreadyExists failure")
	}
	combined := out + "\n" + serr
	if !strings.Contains(combined, "GROUP_ALREADY_EXISTS") {
		t.Errorf("expected AlreadyExists ErrorInfo detailsStr preserved, got:\n%s", combined)
	}
	if strings.Contains(combined, nsrt) {
		t.Errorf("AlreadyExists diagnostic leaked raw nsrt_ token:\n%s", combined)
	}
}

func TestE2E_T3_07_WindowsOS_UsagePolicyEnabledPlatforms_CustomAccess(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{Name: req.GetName(), Policies: req.GetPolicies()}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	h.mockServer.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{Label: "windows-2022", Spec: &imagesv1beta.BaseImage{Id: "win22", Os: "windows", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_github_base_images" "win" {
  labels = ["windows-2022"]
}

resource "namespace_tenant_group" "win_quota" {
  name = "t3-07-win-grp"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value = jsonencode({
      enabled_platforms = ["linux/amd64", "windows/amd64"]
      per_platform_limits = {
        "windows/amd64" = { concurrency_limits = { max_cpu = 16 } }
      }
    })
  }]
}

resource "namespace_github_runner_profile" "win" {
  tag          = "t3-07-win"
  os           = data.namespace_github_base_images.win.images[0].label
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [{
    resource_type = "vault/object"
    resource_id   = "win-cert"
    actions       = ["read"]
  }]
  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 16384
    machine_arch     = "amd64"
    os               = data.namespace_github_base_images.win.images[0].spec.os
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)
}

func TestE2E_T3_08_FailedPreconditionRevisionConflict_PositionalCacheVolumes(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)
	var conflictMode atomic.Bool
	origUpdate := h.mockServer.Profile.UpdateProfileFunc
	h.mockServer.Profile.UpdateProfileFunc = func(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
		if conflictMode.Load() {
			st, err := status.New(codes.FailedPrecondition, "revision mismatch: expected 2").WithDetails(&errdetails.ErrorInfo{
				Reason:   "REVISION_MISMATCH",
				Domain:   "github.namespaceapis.com",
				Metadata: map[string]string{"expected": "2", "actual": "1"},
			})
			if err != nil {
				return nil, err
			}
			return nil, st.Err()
		}
		return origUpdate(ctx, req)
	}

	base := e2eProviderHCLHeader(e2eRuntimeProviderToken())
	h.writeMainTF(t, base+`
resource "namespace_github_runner_profile" "conflict" {
  tag = "t3-08-conflict"
  os  = "ubuntu-24.04"
  cache_volume_settings = [
    { volume_size_gb = 50, allow_commit_from_branch = [] },
    { volume_size_gb = 100, allow_commit_from_branch = ["main"] }
  ]
}
`)
	h.mustInit(t)
	h.mustApply(t)

	// Simulate out-of-band concurrent revision bump returning FailedPrecondition + ErrorInfo
	conflictMode.Store(true)

	h.writeMainTF(t, base+`
resource "namespace_github_runner_profile" "conflict" {
  tag         = "t3-08-conflict"
  os          = "ubuntu-24.04"
  description = "trigger-update"
  cache_volume_settings = [
    { volume_size_gb = 50, allow_commit_from_branch = [] },
    { volume_size_gb = 100, allow_commit_from_branch = ["main"] }
  ]
}
`)
	stdout, stderr, err := h.runTerraform(t, nil, "apply", "-auto-approve")
	if err == nil {
		t.Fatal("expected Concurrency Conflict failure on UpdateProfile")
	}
	combined := stdout + "\n" + stderr
	for _, want := range []string{"Concurrency Conflict", "REVISION_MISMATCH", "terraform refresh"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in CLI error output:\n%s", want, combined)
		}
	}
}

func TestE2E_T3_09_GoModTidyDiffClean(t *testing.T) {
	root := e2eRepoRoot(t)
	cmd := exec.Command("go", "mod", "tidy", "-diff")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go mod tidy -diff failed: %v\n%s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected clean go mod tidy -diff, got:\n%s", string(out))
	}
}

func TestE2E_T3_10_GenerateAcceptanceReport_FailingTestJUnitScrubbed(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "python3")

	nsrt := e2eRuntimeNSRT("junit_fail")
	oidc := e2eRuntimeJWT("oidc_", "junit-oidc")
	cog := e2eRuntimeJWT("cognito_", "junit-cog")
	specVal := "junit_spec_secret_456"

	logContent := e2eBuildValidMatrixLog(map[string][]string{
		"TestAccMatrix_DriftDetection": {
			"FAIL detail: " + nsrt + " NSC_TOKEN_SPEC=" + specVal + " " + oidc + " " + cog,
		},
	}, "TestAccMatrix_DriftDetection")

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "fail_matrix.json")
	outDir := filepath.Join(tmp, "out")
	if err := os.WriteFile(logPath, []byte(logContent), 0600); err != nil {
		t.Fatalf("write fail_matrix.json: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "generate-acceptance-report.sh"),
		"--input", logPath,
		"--output-dir", outDir,
		"--commit-sha", "0123456789abcdef0123456789abcdef01234567",
		"--run-id", "99999",
		"--run-link", "https://github.com/bsuv/terraform-provider-namespace/actions/runs/99999",
		"--suite", "matrix",
	)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected Gate 2 failure exit 1 for failing test, got exit 0:\n%s", string(out))
	}

	junitBytes, err := os.ReadFile(filepath.Join(outDir, "junit.xml"))
	if err != nil {
		t.Fatalf("read junit.xml: %v", err)
	}
	var suites struct {
		TestSuites []struct {
			Cases []struct {
				Name    string `xml:"name,attr"`
				Failure *struct {
					Text string `xml:",chardata"`
				} `xml:"failure"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(junitBytes, &suites); err != nil {
		t.Fatalf("parse junit.xml: %v", err)
	}
	foundFailure := false
	for _, ts := range suites.TestSuites {
		for _, tc := range ts.Cases {
			if tc.Name == "TestAccMatrix_DriftDetection" && tc.Failure != nil {
				foundFailure = true
				for _, secret := range []string{nsrt, oidc, cog, specVal, "sig_e2e_test_payload_9876543210"} {
					if strings.Contains(tc.Failure.Text, secret) {
						t.Errorf("junit.xml <failure> leaked secret %q:\n%s", secret, tc.Failure.Text)
					}
				}
			}
		}
	}
	if !foundFailure {
		t.Errorf("expected <failure> element for TestAccMatrix_DriftDetection in junit.xml")
	}
}

func TestE2E_T3_11_Tfplugindocs_GenerateZeroDiffAndValidate(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "terraform")
	tfrcPath, tmpDir := e2eBuildLocalMirror(t, root)

	// 1. Generate providers schema JSON from isolated temp workspace
	schemaWS := filepath.Join(tmpDir, "schema_ws")
	if err := os.MkdirAll(schemaWS, 0755); err != nil {
		t.Fatalf("mkdir schemaWS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(schemaWS, "main.tf"), []byte(`
terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}
provider "namespace" {}
`), 0600); err != nil {
		t.Fatalf("write schemaWS main.tf: %v", err)
	}

	tfEnv := e2eCleanEnv("TF_CLI_CONFIG_FILE="+tfrcPath, "TF_IN_AUTOMATION=1")
	initCmd := exec.Command("terraform", "init", "-backend=false")
	initCmd.Dir = schemaWS
	initCmd.Env = tfEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("terraform init for schema generation failed: %v\n%s", err, string(out))
	}

	schemaCmd := exec.Command("terraform", "providers", "schema", "-json")
	schemaCmd.Dir = schemaWS
	schemaCmd.Env = tfEnv
	schemaJSON, err := schemaCmd.Output()
	if err != nil {
		t.Fatalf("terraform providers schema -json failed: %v", err)
	}
	// tfplugindocs looks up schemas under "namespace" or "registry.terraform.io/hashicorp/namespace"
	var parsedSchema map[string]any
	if err := json.Unmarshal(schemaJSON, &parsedSchema); err != nil {
		t.Fatalf("unmarshal providers schema JSON: %v", err)
	}
	if psMap, ok := parsedSchema["provider_schemas"].(map[string]any); ok {
		if bsuvSchema, exists := psMap["registry.terraform.io/bsuv/namespace"]; exists {
			psMap["registry.terraform.io/hashicorp/namespace"] = bsuvSchema
		}
	}
	rewrittenSchemaJSON, err := json.Marshal(parsedSchema)
	if err != nil {
		t.Fatalf("marshal rewritten schema JSON: %v", err)
	}
	schemaPath := filepath.Join(tmpDir, "schema.json")
	if err := os.WriteFile(schemaPath, rewrittenSchemaJSON, 0600); err != nil {
		t.Fatalf("write schema.json: %v", err)
	}

	// 2. Stage examples/ and templates/ into temp provider-dir so repo docs/ is never mutated
	docgenDir := filepath.Join(tmpDir, "docgen")
	e2eCopyDir(t, filepath.Join(root, "examples"), filepath.Join(docgenDir, "examples"))
	e2eCopyDir(t, filepath.Join(root, "templates"), filepath.Join(docgenDir, "templates"))

	genCmd := exec.Command("go", "run", "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs", "generate",
		"--provider-dir", docgenDir,
		"--providers-schema", schemaPath,
		"--provider-name", "namespace",
		"--rendered-provider-name", "Namespace Cloud",
	)
	genCmd.Dir = root
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("tfplugindocs generate failed: %v\n%s", err, string(out))
	}

	// 3. Compare generated docs vs <repo>/docs byte-for-byte (ignoring release_checklist.md)
	collectDocs := func(base string) map[string][]byte {
		files := map[string][]byte{}
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			rel, _ := filepath.Rel(base, path)
			if rel == "release_checklist.md" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			files[rel] = data
			return nil
		})
		return files
	}

	gotDocs := collectDocs(filepath.Join(docgenDir, "docs"))
	repoDocs := collectDocs(filepath.Join(root, "docs"))

	for rel, wantBytes := range repoDocs {
		gotBytes, ok := gotDocs[rel]
		if !ok {
			t.Errorf("generated docs missing file %s", rel)
			continue
		}
		if !bytes.Equal(gotBytes, wantBytes) {
			t.Errorf("docs/%s is out of date with tfplugindocs generate output", rel)
		}
	}
	for rel := range gotDocs {
		if _, ok := repoDocs[rel]; !ok {
			t.Errorf("tfplugindocs generated untracked doc file %s missing from docs/", rel)
		}
	}

	// 4. Run tfplugindocs validate
	valCmd := exec.Command("go", "run", "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs", "validate",
		"--provider-name", "namespace",
		"--providers-schema", schemaPath,
	)
	valCmd.Dir = root
	if out, err := valCmd.CombinedOutput(); err != nil {
		t.Fatalf("tfplugindocs validate failed: %v\n%s", err, string(out))
	}
}

// =============================================================================
// TIER 4: REAL-WORLD MULTI-RESOURCE SCENARIOS & REPOSITORY HYGIENE
// =============================================================================

func TestE2E_T4_01_MultiPlatformRunnerFleetLifecycle(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, _ = e2eInstallStatefulProfileMocks(t, h.mockServer)
	h.mockServer.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{Label: "ubuntu-24.04", Spec: &imagesv1beta.BaseImage{Id: "u24", Os: "linux", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
				{Label: "windows-2022", Spec: &imagesv1beta.BaseImage{Id: "w22", Os: "windows", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
			},
		}, nil
	}

	fleetHCL := e2eProviderHCLHeader(e2eRuntimeProviderToken()) + `
data "namespace_github_base_images" "fleet" {}

resource "namespace_github_runner_profile" "linux_arm" {
  tag          = "fleet-linux-arm"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_LIMITED"
  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 16384
    machine_arch     = "arm64"
    os               = "linux"
  }
  cache_volume_settings = [
    { volume_size_gb = 80, allow_commit_from_branch = [] },
    { volume_size_gb = 160, allow_commit_from_branch = ["main"] }
  ]
}

resource "namespace_github_runner_profile" "win_custom" {
  tag          = "fleet-win-custom"
  os           = "windows-2022"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [
    { resource_type = "vault/object", resource_id = "win-sign", actions = ["read"] },
    { resource_type = "cache/bazel", resource_id = "win-ci", actions = ["read", "write"] }
  ]
  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 32768
    machine_arch     = "amd64"
    os               = "windows"
  }
}

data "namespace_github_runner_profiles" "all" {
  depends_on = [
    namespace_github_runner_profile.linux_arm,
    namespace_github_runner_profile.win_custom,
  ]
}
`
	h.writeMainTF(t, fleetHCL)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("fleet state must not contain volume_tag")
	}
	var allProfiles []any
	for _, r := range st.Resources {
		if r.Type == "namespace_github_runner_profiles" {
			allProfiles = e2eMustSlice(t, r.Instances[0].Attributes["profiles"], "fleet profiles")
		}
	}
	if len(allProfiles) != 2 {
		t.Fatalf("expected 2 profiles in fleet data source, got %d", len(allProfiles))
	}

	if out, serr, err := h.runTerraform(t, nil, "destroy", "-auto-approve"); err != nil {
		t.Fatalf("terraform destroy failed: %v\n%s\n%s", err, out, serr)
	}
}

func TestE2E_T4_02_RegistryGarbageCollectionAndAuditPipeline(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	var mu sync.Mutex
	repoPolicies := map[string]*registryv1beta.GetRepositoryPolicyResponse{}
	shares := map[string]*registryv1beta.SharedImage{}

	h.mockServer.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		existing, exists := repoPolicies[req.GetRepository()]
		var expectedRevision int64
		if exists {
			expectedRevision = existing.GetRevision()
		}
		if req.GetRevision() != expectedRevision {
			return nil, status.Errorf(codes.FailedPrecondition, "revision mismatch: got %d, want %d", req.GetRevision(), expectedRevision)
		}
		if req.GetExpiration() == nil && req.GetExpirationEnforcement() == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN {
			delete(repoPolicies, req.GetRepository())
			return &emptypb.Empty{}, nil
		}
		repoPolicies[req.GetRepository()] = proto.Clone(&registryv1beta.GetRepositoryPolicyResponse{
			Revision:              expectedRevision + 1,
			Expiration:            req.GetExpiration(),
			ExpirationEnforcement: req.GetExpirationEnforcement(),
		}).(*registryv1beta.GetRepositoryPolicyResponse)
		return &emptypb.Empty{}, nil
	}
	h.mockServer.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		p, ok := repoPolicies[req.GetRepository()]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "repository policy for %q not found", req.GetRepository())
		}
		return proto.Clone(p).(*registryv1beta.GetRepositoryPolicyResponse), nil
	}
	h.mockServer.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
		mu.Lock()
		defer mu.Unlock()
		si := &registryv1beta.SharedImage{
			Id:               "share-t4-02",
			ImageRef:         "public.nscr.io/share-t4-02/" + req.GetSuffix(),
			SharedRepository: "shared/app",
			SharedDigest:     req.GetDigest(),
			Visibility:       req.GetVisibility(),
			ExpiresAt:        req.GetExpiresAt(),
		}
		shares[si.Id] = si
		return si, nil
	}
	h.mockServer.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*registryv1beta.SharedImage
		for _, s := range shares {
			list = append(list, s)
		}
		return &registryv1beta.ListSharedImagesResponse{SharedImages: list}, nil
	}
	h.mockServer.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(shares, req.GetId())
		return &emptypb.Empty{}, nil
	}

	delAt := timestamppb.New(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	imgDeleted := &registryv1beta.Image{
		Repository: "app/gc",
		Digest:     "sha256:gc111",
		CreatedAt:  timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
		ExpiresAt:  timestamppb.New(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)),
		DeletedAt:  delAt,
		Labels:     []*stdlib.Label{{Name: "gc", Value: "soft"}},
		Tags:       []string{"v1-gc"},
	}
	imgActive := &registryv1beta.Image{
		Repository: "app/gc",
		Digest:     "sha256:gc222",
		CreatedAt:  timestamppb.New(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
		ExpiresAt:  timestamppb.New(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)),
		Labels:     []*stdlib.Label{{Name: "gc", Value: "active"}},
		Tags:       []string{"v2-live"},
	}
	type listImagesCall struct {
		deletionFilterName string
		expiresAt          *stdlib.TimestampRange
	}
	var listCalls []listImagesCall
	h.mockServer.Registry.ListImagesFunc = func(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
		mu.Lock()
		listCalls = append(listCalls, listImagesCall{
			deletionFilterName: e2eProtoGetListImagesDeletionFilterName(t, req),
			expiresAt:          e2eProtoGetListImagesExpiresAt(t, req),
		})
		mu.Unlock()
		if string(req.GetPaginationCursor()) == "" {
			return &registryv1beta.ListImagesResponse{Images: []*registryv1beta.Image{imgDeleted}, PaginationCursor: []byte("next")}, nil
		}
		return &registryv1beta.ListImagesResponse{Images: []*registryv1beta.Image{imgActive}}, nil
	}
	h.mockServer.Registry.GetImageFunc = func(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
		return &registryv1beta.GetImageResponse{Image: imgDeleted}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_registry_repository_policy" "gc_policy" {
  repository             = "app/gc"
  expiration             = "720h"
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}

resource "namespace_registry_shared_image" "pub" {
  repository = "app/gc"
  digest     = "sha256:gc222"
  visibility = "PUBLIC"
  suffix     = "audit-live"
}

data "namespace_registry_images" "audit" {
  repository      = "app/gc"
  deletion_filter = "INCLUDE_DELETED"
  expires_at = {
    after  = "2026-11-15T00:00:00Z"
    before = "2026-12-01T00:00:00Z"
  }
}

data "namespace_registry_image" "deleted_item" {
  repository = "app/gc"
  reference  = "v1-gc"
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	mu.Lock()
	capturedCalls := append([]listImagesCall(nil), listCalls...)
	mu.Unlock()
	if len(capturedCalls) < 2 {
		t.Fatalf("expected at least 2 paginated ListImages calls, got %d", len(capturedCalls))
	}
	wantAfter := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	wantBefore := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range capturedCalls {
		if c.deletionFilterName != "INCLUDE_DELETED" {
			t.Errorf("ListImages call %d deletion_filter=%q, want INCLUDE_DELETED", i, c.deletionFilterName)
		}
		if c.expiresAt == nil || c.expiresAt.GetAfter() == nil || c.expiresAt.GetBefore() == nil {
			t.Errorf("ListImages call %d missing expires_at bounds: %+v", i, c.expiresAt)
		} else if !c.expiresAt.GetAfter().AsTime().Equal(wantAfter) || !c.expiresAt.GetBefore().AsTime().Equal(wantBefore) {
			t.Errorf("ListImages call %d expires_at bounds=(%s,%s), want (%s,%s)", i, c.expiresAt.GetAfter().AsTime(), c.expiresAt.GetBefore().AsTime(), wantAfter, wantBefore)
		}
	}

	st, _ := h.readState(t)
	var seenImagesDS, seenSingleDS bool
	for _, r := range st.Resources {
		switch r.Type {
		case "namespace_registry_images":
			seenImagesDS = true
			imgs := e2eMustSlice(t, r.Instances[0].Attributes["images"], "audit images")
			if len(imgs) != 2 {
				t.Fatalf("expected 2 images in audit list, got %d", len(imgs))
			}
			byDigest := map[string]map[string]any{}
			for _, rawImg := range imgs {
				m := e2eMustMap(t, rawImg, "audit image")
				byDigest[m["digest"].(string)] = m
			}
			delImg := byDigest["sha256:gc111"]
			if delImg == nil || delImg["deleted_at"] == nil || delImg["deleted_at"] == "" {
				t.Errorf("expected non-empty deleted_at on sha256:gc111, got %+v", delImg)
			}
			if lbls := e2eMustMap(t, delImg["labels"], "gc111 labels"); lbls["gc"] != "soft" {
				t.Errorf("expected gc=soft label on sha256:gc111, got %+v", lbls)
			}
			liveImg := byDigest["sha256:gc222"]
			if liveImg == nil || liveImg["deleted_at"] != nil {
				t.Errorf("expected nil deleted_at on sha256:gc222, got %+v", liveImg)
			}
			if lbls := e2eMustMap(t, liveImg["labels"], "gc222 labels"); lbls["gc"] != "active" {
				t.Errorf("expected gc=active label on sha256:gc222, got %+v", lbls)
			}
		case "namespace_registry_image":
			seenSingleDS = true
			attrs := r.Instances[0].Attributes
			if attrs["deleted_at"] == nil || attrs["deleted_at"] == "" {
				t.Errorf("expected non-empty deleted_at on deleted_item, got %v", attrs["deleted_at"])
			}
			if lbls := e2eMustMap(t, attrs["labels"], "deleted_item labels"); lbls["gc"] != "soft" {
				t.Errorf("expected gc=soft label on deleted_item, got %+v", lbls)
			}
		}
	}
	if !seenImagesDS || !seenSingleDS {
		t.Errorf("missing expected registry data sources in state: images=%v single=%v", seenImagesDS, seenSingleDS)
	}

	if out, serr, err := h.runTerraform(t, nil, "destroy", "-auto-approve"); err != nil {
		t.Fatalf("destroy failed: %v\n%s\n%s", err, out, serr)
	}
}

func TestE2E_T4_03_EnterpriseTenantOnboardingQuotaAndEphemeralToken(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{
			Name:              req.GetName(),
			Policies:          req.GetPolicies(),
			EnrolledTenantIds: []string{"tenant-ent-01"},
		}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}
	h.mockServer.Tenant.DeleteTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}

	ephTok := e2eRuntimeJWT("nsct_", "ent-eph-secret")
	h.mockServer.Token.IssueTenantTokenFunc = func(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
		return &iamv1beta.IssueTenantTokenResponse{BearerToken: ephTok}, nil
	}

	modDir := filepath.Join(h.workDir, "modules", "onboard")
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatalf("mkdir modDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "main.tf"), []byte(`
variable "scoped_token" {
  type      = string
  sensitive = true
  ephemeral = true
}
`), 0600); err != nil {
		t.Fatalf("write module: %v", err)
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_tenant_group" "ent" {
  name = "enterprise-onboarding"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value = jsonencode({
      concurrency_limits = { max_cpu = 128, max_memory_mb = 524288, max_instance_count = 32 }
      usage_limits       = { unit_minutes = 50000, builds = 2000, wall_seconds = 900000 }
      enabled_platforms  = ["linux/amd64", "linux/arm64", "windows/amd64"]
      per_platform_limits = {
        "windows/amd64" = { concurrency_limits = { max_cpu = 32 } }
      }
      devbox_limits = { max_count = 10 }
    })
  }]
}

data "namespace_tenant_group" "ent_ro" {
  name = namespace_tenant_group.ent.name
}

ephemeral "namespace_tenant_token" "scoped" {
  tenant_id     = data.namespace_tenant_group.ent_ro.enrolled_tenant_ids[0]
  actor_id      = "ci-deployer"
  duration_secs = 900
  policies = [{
    policy_type = "namespace.cloud.compute.v1beta.UsagePolicy"
    value       = jsonencode({ concurrency_limits = { max_cpu = 16 } })
  }]
  access = {
    grants = [{
      resource_type = "github/runner-profile"
      resource_id   = "*"
      actions       = ["read", "create"]
    }]
  }
}

module "consumer" {
  source       = "./modules/onboard"
  scoped_token = ephemeral.namespace_tenant_token.scoped.bearer_token
}
`)
	h.mustInit(t)
	if out, serr, err := h.runTerraform(t, nil, "plan", "-out=tfplan"); err != nil {
		t.Fatalf("terraform plan: %v\n%s\n%s", err, out, serr)
	}
	if out, serr, err := h.runTerraform(t, nil, "apply", "-auto-approve", "tfplan"); err != nil {
		t.Fatalf("terraform apply: %v\n%s\n%s", err, out, serr)
	}

	planBytes, _ := os.ReadFile(filepath.Join(h.workDir, "tfplan"))
	if strings.Contains(string(planBytes), ephTok) {
		t.Errorf("binary tfplan leaked ephemeral tenant token")
	}
	_, rawState := h.readState(t)
	if strings.Contains(string(rawState), ephTok) {
		t.Errorf("terraform.tfstate leaked ephemeral tenant token")
	}
}

func TestE2E_T4_04_LegacyV0StateMigrationZeroDowntimeUpgrade(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, profiles := e2eInstallStatefulProfileMocks(t, h.mockServer)
	profiles["prof-legacy-prod"] = &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof-legacy-prod",
		Version:   1,
		CreatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		UpdatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag:         "prod-runner",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_LIMITED,
			CacheVolumeSettings: []*githubv1beta.CacheVolumeSettings{
				{VolumeSizeGb: 120, AllowCommitFromBranch: []string{"main"}},
			},
		},
	}
	groups := map[string]*iamv1beta.DescribeTenantGroupResponse{
		"prod-grp": {Name: "prod-grp"},
	}
	h.mockServer.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		g, ok := groups[req.GetName()]
		if !ok {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return g, nil
	}
	h.mockServer.Tenant.EnsureTenantGroupFunc = func(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
		groups[req.GetName()] = &iamv1beta.DescribeTenantGroupResponse{Name: req.GetName(), Policies: req.GetPolicies()}
		return &iamv1beta.TenantGroupResponse{Name: req.GetName()}, nil
	}

	v0State := `{
  "version": 4,
  "terraform_version": "1.15.6",
  "serial": 4,
  "lineage": "t4-04-prod-migration",
  "outputs": {},
  "resources": [
    {
      "mode": "managed",
      "type": "namespace_github_runner_profile",
      "name": "prod",
      "provider": "provider[\"registry.terraform.io/bsuv/namespace\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "prof-legacy-prod",
            "profile_id": "prof-legacy-prod",
            "version": 1,
            "created_at": "2026-01-01T00:00:00Z",
            "updated_at": "2026-01-01T00:00:00Z",
            "tag": "prod-runner",
            "os": "ubuntu-24.04",
            "description": "",
            "emoji": "",
            "swap_size_mb": 0,
            "builder_mode": "BUILDER_MODE_UNSPECIFIED",
            "access_level": "ACCESS_LEVEL_LIMITED",
            "experimental_features": null,
            "instance_shape": null,
            "cache_volume_settings": [
              {
                "volume_tag": "prod-cache-v0",
                "volume_size_gb": 120,
                "volume_reserved_size_gb": 0,
                "bundled_container_cache": false,
                "bundled_buildkit_cache": false,
                "bundled_git_mirror": false,
                "bundled_runner_tools_cache": false,
                "bundled_runner_actions_cache": false,
                "allow_commit_from_branch": ["main"]
              }
            ],
            "custom_runner_image_spec": null,
            "network_policy": null,
            "custom_runner_images": null
          }
        }
      ]
    },
    {
      "mode": "managed",
      "type": "namespace_tenant_group",
      "name": "prod_grp",
      "provider": "provider[\"registry.terraform.io/bsuv/namespace\"]",
      "instances": [
        {
          "schema_version": 0,
          "attributes": {
            "id": "prod-grp",
            "name": "prod-grp",
            "policies": [],
            "enrolled_tenant_ids": []
          }
        }
      ]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(h.workDir, "terraform.tfstate"), []byte(v0State), 0600); err != nil {
		t.Fatalf("write v0 tfstate: %v", err)
	}

	h.writeMainTF(t, fmt.Sprintf(`terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

provider "namespace" {
  token            = %q
  global_endpoint  = "https://global.namespaceapi.com:443"
  iam_endpoint     = "https://iam.namespaceapi.com:443"
  compute_endpoint = "https://compute.namespaceapi.com:443"
}

resource "namespace_github_runner_profile" "prod" {
  tag          = "prod-runner"
  os           = "ubuntu-24.04"
  access_level = "ACCESS_LEVEL_CUSTOM"
  custom_permissions = [{
    resource_type = "vault/object"
    resource_id   = "prod-secret"
    actions       = ["read"]
  }]
  cache_volume_settings = [{
    volume_size_gb           = 120
    allow_commit_from_branch = ["main"]
  }]
}

resource "namespace_tenant_group" "prod_grp" {
  name = "prod-grp"
  policies = [{
    policy = "namespace.cloud.compute.v1beta.UsagePolicy"
    value  = jsonencode({ concurrency_limits = { max_cpu = 64 } })
  }]
}
`, e2eRuntimeProviderToken()))
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	st, raw := h.readState(t)
	if strings.Contains(string(raw), `"volume_tag"`) {
		t.Errorf("upgraded state still contains volume_tag")
	}
	for _, r := range st.Resources {
		if r.Type == "namespace_github_runner_profile" {
			if r.Instances[0].Attributes["id"] != "prof-legacy-prod" {
				t.Errorf("resource recreated instead of in-place upgrade; id=%v", r.Instances[0].Attributes["id"])
			}
		}
	}
}

func TestE2E_T4_05_SessionExchangeAndPermissionDeniedIncidentDiagnosis(t *testing.T) {
	e2eInitBufconnTransport(t)
	e2eRequireTool(t, "terraform")

	stTok := e2eRuntimeST("t4_05")
	exchangedTok := e2eRuntimeJWT("nsct_", "t4-05-exchanged")
	leakNSRT := e2eRuntimeNSRT("t4_05_leak")
	leakOIDC := e2eRuntimeJWT("oidc_", "t4-05-leak")

	stFile := filepath.Join(t.TempDir(), "raw_st_file")
	if err := os.WriteFile(stFile, []byte(stTok), 0600); err != nil {
		t.Fatalf("write raw_st_file: %v", err)
	}

	sessSrv := &e2eTestUserSessionsServer{
		handler: func(ctx context.Context, req *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
			return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: exchangedTok}, nil
		},
	}

	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	var authMu sync.Mutex
	var seenProfileAuth string
	mockProf := &testutil.MockProfileServiceServer{
		CreateProfileFunc: func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				if vals := md.Get("authorization"); len(vals) > 0 {
					authMu.Lock()
					seenProfileAuth = vals[0]
					authMu.Unlock()
				}
			}
			st, err := status.New(codes.PermissionDenied, "denied with "+leakNSRT).WithDetails(&iamv1beta.PermissionDeniedError{
				MissingAccessPermissions: []*iamv1beta.Access{
					{ResourceType: "github/runner-profile", ResourceId: leakOIDC, Action: "create"},
				},
			})
			if err != nil {
				return nil, err
			}
			return nil, st.Err()
		},
	}
	sessionsv1betagrpc.RegisterUserSessionsServiceServer(grpcSrv, sessSrv)
	githubv1betagrpc.RegisterProfileServiceServer(grpcSrv, mockProf)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = lis.Close()
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	cl, err := client.New(context.Background(), client.Config{
		TokenFile:       stFile,
		GlobalEndpoint:  "https://localhost:443",
		IAMEndpoint:     "https://localhost:443",
		ComputeEndpoint: "https://localhost:443",
		Creds:           insecure.NewCredentials(),
		DialOptions:     []grpc.DialOption{grpc.WithContextDialer(dialer)},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	h := e2eServeHarnessWithFactory(t, nil, providerserver.NewProtocol6(provider.NewWithClient("e2e", cl)()))
	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
resource "namespace_github_runner_profile" "incident" {
  tag = "t4-05-incident"
  os  = "ubuntu-24.04"
}
`)
	h.mustInit(t)
	stdout, stderr, applyErr := h.runTerraform(t, nil, "apply", "-auto-approve")
	if applyErr == nil {
		t.Fatal("expected PermissionDenied apply error")
	}
	authMu.Lock()
	gotAuth := seenProfileAuth
	authMu.Unlock()
	if gotAuth != "Bearer "+exchangedTok {
		t.Errorf("expected exchanged Bearer token on CreateProfile RPC, got %q", gotAuth)
	}
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "github/runner-profile") || !strings.Contains(combined, "create") {
		t.Errorf("expected missing_access_permissions in CLI diagnostic:\n%s", combined)
	}
	for _, secret := range []string{stTok, exchangedTok, leakNSRT, leakOIDC, "sig_e2e_test_payload_9876543210"} {
		if strings.Contains(combined, secret) {
			t.Errorf("CLI output leaked secret %q:\n%s", secret, combined)
		}
	}
}

func TestE2E_T4_06_MultiRegionBuilderAndWindowsRunnerDiscovery(t *testing.T) {
	h := e2eStartBlackBoxHarness(t)
	_, profiles := e2eInstallStatefulProfileMocks(t, h.mockServer)
	profiles["prof-seed-base"] = &githubv1beta.RunnerProfileWithStatus{
		ProfileId: "prof-seed-base",
		Version:   1,
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag:         "seed-linux",
			Os:          "ubuntu-24.04",
			AccessLevel: githubv1beta.AccessLevel_ACCESS_LEVEL_LIMITED,
		},
	}
	h.mockServer.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		return &builderv1beta.GetBuilderConfigurationResponse{
			BuildkitEndpoint: "bk-win.namespaceapi.com:443",
			Shape: &computev1beta.InstanceShape{
				VirtualCpu:      8,
				MemoryMegabytes: 16384,
				MachineArch:     "amd64",
				Os:              "windows",
			},
		}, nil
	}
	h.mockServer.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{Label: "windows-2022", Spec: &imagesv1beta.BaseImage{Id: "win22", Os: "windows", ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION}},
			},
		}, nil
	}

	h.writeMainTF(t, e2eProviderHCLHeader(e2eRuntimeProviderToken())+`
data "namespace_builder_configuration" "win" {
  platform = "windows/amd64"
}

data "namespace_github_base_images" "win" {
  labels = ["windows-2022"]
}

data "namespace_github_runner_profile" "seed" {
  tag = "seed-linux"
}

resource "namespace_github_runner_profile" "derived" {
  tag = "derived-win-runner"
  os  = data.namespace_github_base_images.win.images[0].label
  instance_shape = {
    virtual_cpu      = data.namespace_builder_configuration.win.shape.virtual_cpu
    memory_megabytes = data.namespace_builder_configuration.win.shape.memory_megabytes
    machine_arch     = data.namespace_builder_configuration.win.shape.machine_arch
    os               = data.namespace_builder_configuration.win.shape.os
  }
  cache_volume_settings = [{
    volume_size_gb           = 100
    allow_commit_from_branch = ["main"]
  }]
  network_policy = {
    egress                   = "EGRESS_POLICY_DOMAIN_ALLOW_LIST"
    egress_domain_allow_list = ["github.com"]
    advisory                 = false
  }
}
`)
	h.mustInit(t)
	h.mustApply(t)
	h.mustPlanNoDiff(t)

	if out, serr, err := h.runTerraform(t, nil, "refresh"); err != nil {
		t.Fatalf("terraform refresh failed: %v\n%s\n%s", err, out, serr)
	}
	if out, serr, err := h.runTerraform(t, nil, "destroy", "-auto-approve"); err != nil {
		t.Fatalf("terraform destroy failed: %v\n%s\n%s", err, out, serr)
	}
}

func TestE2E_T4_07_RepoHygiene_GofmtVetShellcheck(t *testing.T) {
	root := e2eRepoRoot(t)
	e2eRequireTool(t, "shellcheck")

	// 1. gofmt -l .
	fmtCmd := exec.Command("gofmt", "-l", ".")
	fmtCmd.Dir = root
	fmtOut, err := fmtCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gofmt -l . failed: %v\n%s", err, string(fmtOut))
	}
	if strings.TrimSpace(string(fmtOut)) != "" {
		t.Errorf("unformatted Go files detected:\n%s", string(fmtOut))
	}

	// 2. go vet ./... and go vet -tags=e2e ./...
	for _, args := range [][]string{
		{"vet", "./..."},
		{"vet", "-tags=e2e", "./..."},
	} {
		vetCmd := exec.Command("go", args...)
		vetCmd.Dir = root
		if out, err := vetCmd.CombinedOutput(); err != nil {
			t.Errorf("go %v failed: %v\n%s", args, err, string(out))
		}
	}

	// 3. shellcheck scripts/*.sh
	shFiles, err := filepath.Glob(filepath.Join(root, "scripts", "*.sh"))
	if err != nil || len(shFiles) == 0 {
		t.Fatalf("glob scripts/*.sh: err=%v len=%d", err, len(shFiles))
	}
	scCmd := exec.Command("shellcheck", shFiles...)
	scCmd.Dir = root
	if out, err := scCmd.CombinedOutput(); err != nil {
		t.Errorf("shellcheck scripts/*.sh failed: %v\n%s", err, string(out))
	}
}
