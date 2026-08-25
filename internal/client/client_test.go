package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestStaticTokenSource(t *testing.T) {
	ts := client.StaticTokenSource{Token: "nsct_static_12345"}
	tok, err := ts.IssueToken(context.Background(), 5*time.Minute, false)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tok != "nsct_static_12345" {
		t.Fatalf("expected token nsct_static_12345, got %q", tok)
	}

	emptyTS := client.StaticTokenSource{Token: ""}
	_, err = emptyTS.IssueToken(context.Background(), 5*time.Minute, false)
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestResolveTokenSource_Precedence(t *testing.T) {
	ctx := context.Background()

	// 1. Explicit token in config takes highest priority
	cfg1 := client.AuthConfig{Token: "token_from_config", TokenFile: "/ignored/path"}
	ts1, err := client.ResolveTokenSource(ctx, cfg1)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	tok1, _ := ts1.IssueToken(ctx, 0, false)
	if tok1 != "token_from_config" {
		t.Fatalf("expected token_from_config, got %q", tok1)
	}

	// 2. Explicit token_file in config takes second priority
	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "token.json")
	if err := os.WriteFile(tokenFile, []byte(`{"bearer_token":"token_from_file"}`), 0600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}

	cfg2 := client.AuthConfig{TokenFile: tokenFile}
	ts2, err := client.ResolveTokenSource(ctx, cfg2)
	if err != nil {
		t.Fatalf("expected success for token_file, got: %v", err)
	}
	tok2, _ := ts2.IssueToken(ctx, 0, false)
	if tok2 != "token_from_file" {
		t.Fatalf("expected token_from_file, got %q", tok2)
	}

	// 3. Unreadable token_file returns an immediate actionable error
	cfg3 := client.AuthConfig{TokenFile: filepath.Join(tmpDir, "nonexistent.json")}
	_, err = client.ResolveTokenSource(ctx, cfg3)
	if err == nil {
		t.Fatal("expected error for missing token file, got nil")
	}

	// 4. Configured token_file with session_token
	sessionFile := filepath.Join(tmpDir, "session_token.json")
	if err := os.WriteFile(sessionFile, []byte(`{"session_token":"token_from_session_file"}`), 0600); err != nil {
		t.Fatalf("failed to write session token file: %v", err)
	}

	cfg4 := client.AuthConfig{TokenFile: sessionFile}
	ts4, err := client.ResolveTokenSource(ctx, cfg4)
	if err != nil {
		t.Fatalf("expected success for session token_file, got: %v", err)
	}
	if ts4 == nil {
		t.Fatal("expected non-nil TokenSource for session token_file")
	}

	// 5. Env var NSC_TOKEN
	t.Setenv("NSC_TOKEN", "token_from_nsc_token_env")
	ts5, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
	if err != nil {
		t.Fatalf("expected success from NSC_TOKEN, got: %v", err)
	}
	tok5, _ := ts5.IssueToken(ctx, 0, false)
	if tok5 != "token_from_nsc_token_env" {
		t.Fatalf("expected token_from_nsc_token_env, got %q", tok5)
	}

	// 6. Env var NSC_TOKEN_FILE with session_token
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", sessionFile)
	ts6, err := client.ResolveTokenSource(ctx, client.AuthConfig{})
	if err != nil {
		t.Fatalf("expected success from NSC_TOKEN_FILE with session_token, got: %v", err)
	}
	if ts6 == nil {
		t.Fatal("expected non-nil TokenSource for NSC_TOKEN_FILE with session_token")
	}
}

func TestEndpointValidation(t *testing.T) {
	valid := []string{
		"https://global.namespaceapis.com:443",
		"https://iam.namespaceapis.com",
		"https://eu.compute.namespaceapis.com:443",
		"global.namespaceapis.com:443",
	}
	for _, ep := range valid {
		if err := client.ValidateEndpoint(ep); err != nil {
			t.Errorf("expected valid endpoint %q, got error: %v", ep, err)
		}
	}

	invalid := []struct {
		endpoint string
		desc     string
	}{
		{"http://global.namespaceapis.com:443", "plain HTTP"},
		{"https://global.namespaceapis.com/some/path", "URL path"},
		{"ftp://invalid.com", "unsupported scheme"},
		{"https://user:password@global.namespaceapis.com:443", "HTTPS with user:password userinfo"},
		{"https://admin@global.namespaceapis.com:443", "HTTPS with username userinfo"},
		{"user:secret@global.namespaceapis.com:443", "Bare host with user:secret userinfo"},
		{"https://global.namespaceapis.com:443?token=secret123", "HTTPS with query string"},
		{"https://global.namespaceapis.com:443#frag", "HTTPS with fragment"},
	}
	for _, tc := range invalid {
		err := client.ValidateEndpoint(tc.endpoint)
		if err == nil {
			t.Errorf("expected invalid endpoint %q (%s) to fail validation, got nil", tc.endpoint, tc.desc)
			continue
		}
		// Verify no credentials or raw userinfo leak in the error message
		errMsg := err.Error()
		if strings.Contains(errMsg, "password") || strings.Contains(errMsg, "secret") || strings.Contains(errMsg, "user:") || strings.Contains(errMsg, "admin@") {
			t.Errorf("ValidateEndpoint error leaked sensitive credentials: %q", errMsg)
		}
	}
}

func TestExecuteWithRetry_IdempotentRead(t *testing.T) {
	ctx := context.Background()

	var attempts int32
	mockSleep := func(ctx context.Context, d time.Duration) error {
		return nil
	}

	policy := client.DefaultReadPolicy()
	policy.Sleep = mockSleep

	// Scenario 1: Transient Unavailable recovers on attempt 3
	res, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
		att := atomic.AddInt32(&attempts, 1)
		if att < 3 {
			return "", status.Error(codes.Unavailable, "temporary outage")
		}
		return "success", nil
	})

	if err != nil {
		t.Fatalf("expected retry success, got error: %v", err)
	}
	if res != "success" {
		t.Fatalf("expected 'success', got %q", res)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}

	// Scenario 2: ResourceExhausted with RetryInfo recovers
	atomic.StoreInt32(&attempts, 0)
	stWithRetry, _ := status.New(codes.ResourceExhausted, "rate limited").WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(100 * time.Millisecond),
	})

	res, err = client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
		att := atomic.AddInt32(&attempts, 1)
		if att < 2 {
			return "", stWithRetry.Err()
		}
		return "recovered", nil
	})

	if err != nil {
		t.Fatalf("expected retry success on rate limit, got: %v", err)
	}
	if res != "recovered" {
		t.Fatalf("expected 'recovered', got %q", res)
	}

	// Scenario 3: Non-retryable error (InvalidArgument) fails immediately
	atomic.StoreInt32(&attempts, 0)
	_, err = client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", status.Error(codes.InvalidArgument, "invalid field")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt for InvalidArgument, got %d", attempts)
	}
}

func TestExecuteWithRetry_MutatingFailFast(t *testing.T) {
	ctx := context.Background()
	policy := client.DefaultMutatingPolicy()

	var attempts int32
	_, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", status.Error(codes.Unavailable, "transient error during create")
	})

	if err == nil {
		t.Fatal("expected mutating call to return error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("expected mutating call to fail-fast on attempt 1, got %d attempts", attempts)
	}
}

func TestExecuteWithRetry_DeadlineExceeded_NoRetryOnOpCtxDone(t *testing.T) {
	// When the caller context is already cancelled / timed out, do NOT retry DeadlineExceeded
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	policy := client.DefaultReadPolicy()
	var attempts int32

	_, err := client.ExecuteWithRetry(ctx, policy, func(callCtx context.Context) (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", status.Error(codes.DeadlineExceeded, "context deadline exceeded")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt when context is cancelled, got %d", attempts)
	}
}

func TestExecuteWithRetry_RetryDelayExceedsDeadline_Aborts(t *testing.T) {
	ctx := context.Background()
	policy := client.DefaultReadPolicy()
	policy.OperationTimeout = 100 * time.Millisecond

	stWithLargeDelay, _ := status.New(codes.ResourceExhausted, "rate limited").WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(10 * time.Second), // 10s > 100ms
	})

	var attempts int32
	_, err := client.ExecuteWithRetry(ctx, policy, func(callCtx context.Context) (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", stWithLargeDelay.Err()
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("expected abort on attempt 1 because retryDelay exceeds remaining deadline, got %d attempts", attempts)
	}
}

func TestExecuteWithRetry_SleepErrorPropagation(t *testing.T) {
	ctx := context.Background()
	policy := client.DefaultReadPolicy()
	customSleepErr := errors.New("sleeper interrupted")
	policy.Sleep = func(ctx context.Context, d time.Duration) error {
		return customSleepErr
	}

	var attempts int32
	_, err := client.ExecuteWithRetry(ctx, policy, func(callCtx context.Context) (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", status.Error(codes.Unavailable, "transient error")
	})

	if err == nil {
		t.Fatal("expected error when sleep fails, got nil")
	}
	if !errors.Is(err, customSleepErr) && err.Error() != customSleepErr.Error() {
		t.Fatalf("expected custom sleep error, got: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt before sleep failed, got %d", attempts)
	}
}

func TestPolicyTimeouts(t *testing.T) {
	readPolicy := client.DefaultReadPolicy()
	if readPolicy.OperationTimeout != 30*time.Second {
		t.Errorf("expected DefaultReadPolicy OperationTimeout 30s, got %v", readPolicy.OperationTimeout)
	}

	paginatedPolicy := client.DefaultPaginatedReadPolicy()
	if paginatedPolicy.OperationTimeout != 2*time.Minute {
		t.Errorf("expected DefaultPaginatedReadPolicy OperationTimeout 2m, got %v", paginatedPolicy.OperationTimeout)
	}

	mutatingPolicy := client.DefaultMutatingPolicy()
	if mutatingPolicy.OperationTimeout != 60*time.Second {
		t.Errorf("expected DefaultMutatingPolicy OperationTimeout 60s, got %v", mutatingPolicy.OperationTimeout)
	}

	tokenPolicy := client.TokenIssuancePolicy()
	if tokenPolicy.OperationTimeout != 30*time.Second {
		t.Errorf("expected TokenIssuancePolicy OperationTimeout 30s, got %v", tokenPolicy.OperationTimeout)
	}
}

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Generic PKCS#8 Private Key",
			input:    "Error loading key:\n" + testutil.SynthesizePEMBlock("PRIVATE KEY", "test-pkcs8-key-body") + "\nFailed",
			expected: "Error loading key:\n[REDACTED PRIVATE KEY]\nFailed",
		},
		{
			name:     "RSA Private Key",
			input:    testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-rsa-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "EC Private Key",
			input:    testutil.SynthesizePEMBlock("EC PRIVATE KEY", "test-ec-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "OpenSSH Private Key",
			input:    "SSH key: " + testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "test-openssh-key-body") + " loaded",
			expected: "SSH key: [REDACTED PRIVATE KEY] loaded",
		},
		{
			name:     "Encrypted Private Key",
			input:    testutil.SynthesizePEMBlock("ENCRYPTED PRIVATE KEY", "test-encrypted-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "Authorization Header No Double Redaction",
			input:    "Headers: {Authorization: Bearer mySecretTokenLongString12345}",
			expected: "Headers: {Authorization: Bearer [REDACTED TOKEN]}",
		},
		{
			name:     "Bearer with Namespace Cluster Token",
			input:    "Failed call: Bearer nsct_cluster_token_12345",
			expected: "Failed call: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Standalone Namespace Cluster Token Without Bearer",
			input:    "Invalid token \"nsct_isolated_cluster_token_9999\" in field spec.token",
			expected: "Invalid token \"[REDACTED TOKEN]\" in field spec.token",
		},
		{
			name:     "Standalone Namespace Workspace Token Without Bearer",
			input:    "Workspace token nscw_workspace_token_abc.def-123 is expired",
			expected: "Workspace token [REDACTED TOKEN] is expired",
		},
		{
			name:     "Standalone Namespace Session Token Without Bearer",
			input:    "Session st_session_token_xyz987 failed authentication",
			expected: "Session [REDACTED TOKEN] failed authentication",
		},
		{
			name:     "Bearer JWT Token (20+ chars)",
			input:    "JWT: Bearer " + "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0",
			expected: "JWT: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Multiple Secrets Combined",
			input:    "Error with Bearer " + "nsct_" + "token1 and Authorization: Bearer token2 and key: " + testutil.SynthesizePEMBlock("PRIVATE KEY", "test-combined-key-body"),
			expected: "Error with Bearer [REDACTED TOKEN] and Authorization: Bearer [REDACTED TOKEN] and key: [REDACTED PRIVATE KEY]",
		},
		{
			name:     "Clean Message Untouched",
			input:    "Resource runner_profile linux-x86_64 created successfully",
			expected: "Resource runner_profile linux-x86_64 created successfully",
		},
		{
			name:     "Short Bearer String Not Falsely Redacted",
			input:    "Bearer short",
			expected: "Bearer short",
		},
		{
			name:     "Idempotent on already redacted string",
			input:    "Headers: {Authorization: Bearer [REDACTED TOKEN]}",
			expected: "Headers: {Authorization: Bearer [REDACTED TOKEN]}",
		},
		{
			name:     "Quoted JSON Password",
			input:    `{"username": "admin", "password": "super-secret-password-123"}`,
			expected: `{"username": "admin", "password": "[REDACTED PASSWORD]"}`,
		},
		{
			name:     "Quoted JSON Password No Whitespace",
			input:    `{"password":"super-secret"}`,
			expected: `{"password":"[REDACTED PASSWORD]"}`,
		},
		{
			name:     "Single Quoted Password",
			input:    `{'password': 'my-single-quote-pass'}`,
			expected: `{'password': '[REDACTED PASSWORD]'}`,
		},
		{
			name:     "Unquoted Password Key-Value",
			input:    `Failed connecting with password: plainSecretPass`,
			expected: `Failed connecting with password: [REDACTED PASSWORD]`,
		},
		{
			name:     "Password Equal Sign",
			input:    `db_conn_str: password=mySecretDbPass user=admin`,
			expected: `db_conn_str: password=[REDACTED PASSWORD] user=admin`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := client.RedactSecrets(tc.input)
			if actual != tc.expected {
				t.Errorf("RedactSecrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, actual)
			}
		})
	}
}

func TestHandleGRPCError(t *testing.T) {
	ctx := context.Background()

	// 1. NotFound on Read -> returns true, suppresses diagnostic error
	var diagsRead diag.Diagnostics
	isNF := client.HandleGRPCError(ctx, status.Error(codes.NotFound, "profile not found"), &diagsRead, "read", "runner profile")
	if !isNF {
		t.Fatal("expected isNotFound=true on NotFound during read")
	}
	if diagsRead.HasError() {
		t.Fatalf("expected no error diagnostics for NotFound on Read, got: %v", diagsRead)
	}

	// 2. NotFound on Delete -> returns true, suppresses diagnostic error
	var diagsDel diag.Diagnostics
	isNF = client.HandleGRPCError(ctx, status.Error(codes.NotFound, "profile not found"), &diagsDel, "delete", "runner profile")
	if !isNF {
		t.Fatal("expected isNotFound=true on NotFound during delete")
	}
	if diagsDel.HasError() {
		t.Fatalf("expected no error diagnostics for NotFound on Delete, got: %v", diagsDel)
	}

	// 3. NotFound on Create -> returns false, adds diagnostic error
	var diagsCreate diag.Diagnostics
	isNF = client.HandleGRPCError(ctx, status.Error(codes.NotFound, "parent not found"), &diagsCreate, "create", "runner profile")
	if isNF {
		t.Fatal("expected isNotFound=false on NotFound during create")
	}
	if !diagsCreate.HasError() {
		t.Fatal("expected error diagnostic for NotFound on Create")
	}

	// 4. AlreadyExists on Create -> adds actionable import message
	var diagsAE diag.Diagnostics
	client.HandleGRPCError(ctx, status.Error(codes.AlreadyExists, "group already exists"), &diagsAE, "create", "tenant group")
	if !diagsAE.HasError() {
		t.Fatal("expected error diagnostic for AlreadyExists")
	}

	// 5. FailedPrecondition on Update -> adds refresh conflict message
	var diagsFP diag.Diagnostics
	client.HandleGRPCError(ctx, status.Error(codes.FailedPrecondition, "version mismatch"), &diagsFP, "update", "runner profile")
	if !diagsFP.HasError() {
		t.Fatal("expected error diagnostic for FailedPrecondition conflict")
	}

	// 6. ErrorInfo with Metadata is preserved in formatted diagnostics
	var diagsMeta diag.Diagnostics
	stWithMeta, err := status.New(codes.InvalidArgument, "invalid parameter").WithDetails(&errdetails.ErrorInfo{
		Reason: "INVALID_FIELD_VALUE",
		Domain: "namespace.so",
		Metadata: map[string]string{
			"field": "spec.os",
			"tier":  "enterprise",
		},
	})
	if err != nil {
		t.Fatalf("failed to create status with details: %v", err)
	}
	client.HandleGRPCError(ctx, stWithMeta.Err(), &diagsMeta, "create", "runner profile")
	if !diagsMeta.HasError() {
		t.Fatal("expected error diagnostic for status with ErrorInfo")
	}
	diagDetail := diagsMeta.Errors()[0].Detail()
	if !strings.Contains(diagDetail, "field=spec.os") || !strings.Contains(diagDetail, "tier=enterprise") {
		t.Errorf("expected ErrorInfo.Metadata preserved in diagnostic detail, got: %s", diagDetail)
	}
}

func TestMockServerConnectivity(t *testing.T) {
	srv := testutil.NewMockServer()
	srv.Profile.CreateProfileFunc = func(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
		return &githubv1beta.CreateProfileResponse{
			Profile: &githubv1beta.RunnerProfileWithStatus{
				ProfileId: "prof_mock_123",
				Spec:      req.Spec,
				Version:   1,
			},
		}, nil
	}

	srv.Tenant.DescribeTenantGroupFunc = func(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return &iamv1beta.DescribeTenantGroupResponse{
			Name: req.Name,
		}, nil
	}

	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              5,
			DefaultExpiration:     durationpb.New(24 * time.Hour),
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
		}, nil
	}

	cl := testutil.NewTestClient(t, srv)
	ctx := context.Background()

	// Test Profile Client
	profClient, err := cl.ProfileClient(ctx)
	if err != nil {
		t.Fatalf("failed to get profile client: %v", err)
	}
	resp, err := profClient.CreateProfile(ctx, &githubv1beta.CreateProfileRequest{
		Spec: &githubv1beta.RunnerProfileSpec{Tag: "test-tag"},
	})
	if err != nil {
		t.Fatalf("CreateProfile failed: %v", err)
	}
	if resp.Profile.ProfileId != "prof_mock_123" {
		t.Fatalf("expected profile_id prof_mock_123, got %q", resp.Profile.ProfileId)
	}

	// Test Tenant Client
	tenantClient, err := cl.TenantClient(ctx)
	if err != nil {
		t.Fatalf("failed to get tenant client: %v", err)
	}
	tgResp, err := tenantClient.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{Name: "engineering"})
	if err != nil {
		t.Fatalf("DescribeTenantGroup failed: %v", err)
	}
	if tgResp.Name != "engineering" {
		t.Fatalf("expected group name engineering, got %q", tgResp.Name)
	}

	// Test Registry Client
	regClient, err := cl.RegistryClient(ctx)
	if err != nil {
		t.Fatalf("failed to get registry client: %v", err)
	}
	polResp, err := regClient.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetDefaultPolicy failed: %v", err)
	}
	if polResp.Revision != 5 {
		t.Fatalf("expected revision 5, got %d", polResp.Revision)
	}
}

func TestClient_HelpersAndEndpoints(t *testing.T) {
	// 1. Test status code helper functions
	if !client.IsNotFound(status.Error(codes.NotFound, "missing")) {
		t.Error("expected IsNotFound to be true")
	}
	if client.IsNotFound(nil) || client.IsNotFound(errors.New("generic")) {
		t.Error("expected IsNotFound to be false")
	}

	if !client.IsAlreadyExists(status.Error(codes.AlreadyExists, "duplicate")) {
		t.Error("expected IsAlreadyExists to be true")
	}
	if client.IsAlreadyExists(nil) || client.IsAlreadyExists(errors.New("generic")) {
		t.Error("expected IsAlreadyExists to be false")
	}

	if !client.IsFailedPrecondition(status.Error(codes.FailedPrecondition, "conflict")) {
		t.Error("expected IsFailedPrecondition to be true")
	}
	if client.IsFailedPrecondition(nil) || client.IsFailedPrecondition(errors.New("generic")) {
		t.Error("expected IsFailedPrecondition to be false")
	}

	// 2. Test DefaultPaginatedReadPolicy
	pagPolicy := client.DefaultPaginatedReadPolicy()
	if pagPolicy.MaxAttempts != 4 || pagPolicy.OperationTimeout != 2*time.Minute || !pagPolicy.IsIdempotent {
		t.Errorf("unexpected paginated read policy: %+v", pagPolicy)
	}

	// 3. Test Missing Compute Endpoint Error
	pool, err := client.NewEndpointPool(client.EndpointConfig{
		TokenSource: client.StaticTokenSource{Token: "test"},
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	_, err = pool.ComputeConn(context.Background())
	if err == nil {
		t.Fatal("expected error for unconfigured compute endpoint")
	}
}
