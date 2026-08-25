package client_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// =============================================================================
// CHALLENGE 1: CLIENT TOKEN PRECEDENCE & CONFLICTING INPUTS
// =============================================================================

func TestChallenge_TokenPrecedence_Adversarial(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	validFileToken := filepath.Join(tmpDir, "valid_token.json")
	if err := os.WriteFile(validFileToken, []byte(`{"bearer_token":"token_from_valid_file"}`), 0600); err != nil {
		t.Fatalf("failed to create valid token file: %v", err)
	}

	invalidFileToken := filepath.Join(tmpDir, "invalid_token.json")
	if err := os.WriteFile(invalidFileToken, []byte(`not-json-content`), 0600); err != nil {
		t.Fatalf("failed to create invalid token file: %v", err)
	}

	nonExistentFile := filepath.Join(tmpDir, "non_existent_token.json")

	t.Run("Whitespace token falls through to TokenFile", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		cfg := client.AuthConfig{
			Token:     "   \t\n  ",
			TokenFile: validFileToken,
		}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected whitespace token to fall through to token_file, got: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil || tok != "token_from_valid_file" {
			t.Fatalf("expected token_from_valid_file, got tok=%q, err=%v", tok, err)
		}
	})

	t.Run("Whitespace token and whitespace token_file falls through to NSC_TOKEN", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "token_from_env_var")
		t.Setenv("NSC_TOKEN_FILE", "")

		cfg := client.AuthConfig{
			Token:     " \t ",
			TokenFile: "   ",
		}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected fall through to NSC_TOKEN, got: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil || tok != "token_from_env_var" {
			t.Fatalf("expected token_from_env_var, got tok=%q, err=%v", tok, err)
		}
	})

	t.Run("Whitespace NSC_TOKEN falls through to NSC_TOKEN_FILE", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "   \t  ")
		t.Setenv("NSC_TOKEN_FILE", validFileToken)

		cfg := client.AuthConfig{}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected fall through to NSC_TOKEN_FILE, got: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil || tok != "token_from_valid_file" {
			t.Fatalf("expected token_from_valid_file, got tok=%q, err=%v", tok, err)
		}
	})

	t.Run("Non-existent TokenFile returns error immediately without falling through to NSC_TOKEN", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "token_from_env_should_be_ignored")

		cfg := client.AuthConfig{
			TokenFile: nonExistentFile,
		}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil {
			t.Fatal("expected error for non-existent token_file, but got nil (it may have fallen through incorrectly)")
		}
		if !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("expected 'unreadable' in error message, got: %v", err)
		}
	})

	t.Run("Non-existent NSC_TOKEN_FILE returns error immediately without falling through to auth.LoadDefaults", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", nonExistentFile)

		cfg := client.AuthConfig{}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil {
			t.Fatal("expected error for non-existent NSC_TOKEN_FILE, but got nil")
		}
		if !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("expected 'unreadable' in error message, got: %v", err)
		}
	})

	t.Run("Malformed token file returns error eagerly during ResolveTokenSource", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		cfg := client.AuthConfig{
			TokenFile: invalidFileToken,
		}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil {
			t.Fatal("expected eager error for malformed token file during ResolveTokenSource, got nil")
		}
		if !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatalf("expected 'malformed JSON' in error message, got: %v", err)
		}
	})

	t.Run("Empty token file returns error eagerly during ResolveTokenSource", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		emptyFile := filepath.Join(tmpDir, "empty_token.json")
		if err := os.WriteFile(emptyFile, []byte("   \n\t  "), 0600); err != nil {
			t.Fatalf("failed to create empty token file: %v", err)
		}

		cfg := client.AuthConfig{
			TokenFile: emptyFile,
		}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil {
			t.Fatal("expected eager error for empty token file during ResolveTokenSource, got nil")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Fatalf("expected 'empty' in error message, got: %v", err)
		}
	})

	t.Run("Valid token file with session_token resolves during ResolveTokenSource", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		sessionTokenFile := filepath.Join(tmpDir, "session_token.json")
		tokenJSON := fmt.Sprintf(`{"session_token":"%s%s"}`, "st_", "sess_valid_12345")
		if err := os.WriteFile(sessionTokenFile, []byte(tokenJSON), 0600); err != nil {
			t.Fatalf("failed to create session token file: %v", err)
		}

		cfg := client.AuthConfig{
			TokenFile: sessionTokenFile,
		}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected success resolving session_token file, got: %v", err)
		}
		if ts == nil {
			t.Fatal("expected non-nil TokenSource for session_token file")
		}
	})

	t.Run("Valid token file with both bearer_token and session_token resolves during ResolveTokenSource", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		dualTokenFile := filepath.Join(tmpDir, "dual_token.json")
		if err := os.WriteFile(dualTokenFile, []byte(`{"bearer_token":"bt_primary_token","session_token":"st_secondary_token"}`), 0600); err != nil {
			t.Fatalf("failed to create dual token file: %v", err)
		}

		cfg := client.AuthConfig{
			TokenFile: dualTokenFile,
		}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected success resolving dual token file, got: %v", err)
		}
		if ts == nil {
			t.Fatal("expected non-nil TokenSource for dual token file")
		}
	})

	t.Run("NSC_TOKEN_FILE with session_token resolves during ResolveTokenSource", func(t *testing.T) {
		sessionTokenFile := filepath.Join(tmpDir, "env_session_token.json")
		if err := os.WriteFile(sessionTokenFile, []byte(`{"session_token":"st_from_env_file_67890"}`), 0600); err != nil {
			t.Fatalf("failed to create env session token file: %v", err)
		}

		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", sessionTokenFile)

		cfg := client.AuthConfig{}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected success resolving NSC_TOKEN_FILE with session_token, got: %v", err)
		}
		if ts == nil {
			t.Fatal("expected non-nil TokenSource for NSC_TOKEN_FILE with session_token")
		}
	})

	t.Run("Token file missing both bearer_token and session_token returns error eagerly during ResolveTokenSource", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		noTokenFile := filepath.Join(tmpDir, "no_token.json")
		if err := os.WriteFile(noTokenFile, []byte(`{"other_field":"value"}`), 0600); err != nil {
			t.Fatalf("failed to create no token file: %v", err)
		}

		cfg := client.AuthConfig{
			TokenFile: noTokenFile,
		}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil {
			t.Fatal("expected eager error for missing bearer_token and session_token during ResolveTokenSource, got nil")
		}
		if !strings.Contains(err.Error(), "bearer_token") || !strings.Contains(err.Error(), "session_token") {
			t.Fatalf("expected 'bearer_token' and 'session_token' in error message, got: %v", err)
		}
	})

	t.Run("Token file with empty strings for both bearer_token and session_token returns error eagerly", func(t *testing.T) {
		t.Setenv("NSC_TOKEN", "")
		t.Setenv("NSC_TOKEN_FILE", "")

		emptyTokensFile := filepath.Join(tmpDir, "empty_tokens.json")
		if err := os.WriteFile(emptyTokensFile, []byte(`{"bearer_token":"   ","session_token":"\t\n"}`), 0600); err != nil {
			t.Fatalf("failed to create empty tokens file: %v", err)
		}

		cfg := client.AuthConfig{
			TokenFile: emptyTokensFile,
		}
		_, err := client.ResolveTokenSource(ctx, cfg)
		if err == nil {
			t.Fatal("expected eager error for empty bearer_token and session_token, got nil")
		}
		if !strings.Contains(err.Error(), "bearer_token") || !strings.Contains(err.Error(), "session_token") {
			t.Fatalf("expected 'bearer_token' and 'session_token' in error message, got: %v", err)
		}
	})

	t.Run("Token trimming in StaticTokenSource", func(t *testing.T) {
		cfg := client.AuthConfig{
			Token: "  \t nsct_with_surrounding_spaces \n ",
		}
		ts, err := client.ResolveTokenSource(ctx, cfg)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		tok, err := ts.IssueToken(ctx, 0, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tok != "nsct_with_surrounding_spaces" {
			t.Fatalf("expected trimmed token, got %q", tok)
		}
	})

	t.Run("StaticTokenSource with whitespace only fails on IssueToken", func(t *testing.T) {
		ts := client.StaticTokenSource{Token: "   \t\n "}
		_, err := ts.IssueToken(ctx, 0, false)
		if err == nil {
			t.Fatal("expected error on IssueToken for whitespace token, got nil")
		}
	})
}

// =============================================================================
// CHALLENGE 2: RETRY ENGINE UNDER ADVERSARIAL CONDITIONS
// =============================================================================

func TestChallenge_RetryEngine_Adversarial(t *testing.T) {
	ctx := context.Background()

	t.Run("Context cancellation during backoff sleep aborts immediately", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)

		sleepCalled := make(chan struct{})
		mockSleep := func(sCtx context.Context, d time.Duration) error {
			close(sleepCalled)
			<-sCtx.Done()
			return sCtx.Err()
		}

		policy := client.DefaultReadPolicy()
		policy.Sleep = mockSleep

		var attempts int32
		errCh := make(chan error, 1)

		go func() {
			_, err := client.ExecuteWithRetry(cancelCtx, policy, func(opCtx context.Context) (string, error) {
				atomic.AddInt32(&attempts, 1)
				return "", status.Error(codes.Unavailable, "transient error")
			})
			errCh <- err
		}()

		<-sleepCalled
		cancel()

		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for retry engine to abort on context cancellation")
		}

		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt before cancellation during sleep, got %d", attempts)
		}
	})

	t.Run("Non-retryable gRPC status codes execute exactly once", func(t *testing.T) {
		nonRetryableCodes := []codes.Code{
			codes.InvalidArgument,
			codes.NotFound,
			codes.AlreadyExists,
			codes.PermissionDenied,
			codes.Unauthenticated,
			codes.FailedPrecondition,
			codes.Aborted,
			codes.OutOfRange,
			codes.Unimplemented,
			codes.Internal,
			codes.DataLoss,
			codes.Unknown,
			codes.Canceled,
		}

		for _, code := range nonRetryableCodes {
			t.Run(code.String(), func(t *testing.T) {
				var attempts int32
				policy := client.DefaultReadPolicy()
				policy.Sleep = func(ctx context.Context, d time.Duration) error {
					t.Fatalf("sleep should not be called for non-retryable code %s", code)
					return nil
				}

				_, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
					atomic.AddInt32(&attempts, 1)
					return "", status.Error(code, "test failure")
				})

				if err == nil {
					t.Fatalf("expected error for code %s, got nil", code)
				}
				if attempts != 1 {
					t.Fatalf("expected exactly 1 attempt for code %s, got %d", code, attempts)
				}
			})
		}
	})

	t.Run("ResourceExhausted without RetryInfo fails fast (1 attempt)", func(t *testing.T) {
		var attempts int32
		policy := client.DefaultReadPolicy()
		policy.Sleep = func(ctx context.Context, d time.Duration) error {
			t.Fatal("sleep should not be called for ResourceExhausted without RetryInfo")
			return nil
		}

		// Plain ResourceExhausted
		_, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", status.Error(codes.ResourceExhausted, "quota exceeded")
		})

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt for plain ResourceExhausted, got %d", attempts)
		}

		// ResourceExhausted with other details (ErrorInfo, BadRequest) but NO RetryInfo
		atomic.StoreInt32(&attempts, 0)
		stWithOtherDetails, _ := status.New(codes.ResourceExhausted, "quota exceeded").WithDetails(
			&errdetails.ErrorInfo{Reason: "QUOTA_LIMIT", Domain: "namespaceapis.com"},
			&errdetails.QuotaFailure{Violations: []*errdetails.QuotaFailure_Violation{{Subject: "project/123"}}},
		)

		_, err = client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", stWithOtherDetails.Err()
		})

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt for ResourceExhausted without RetryInfo details, got %d", attempts)
		}
	})

	t.Run("ResourceExhausted with RetryInfo retries with exact delay", func(t *testing.T) {
		var attempts int32
		var recordedDelays []time.Duration

		mockSleep := func(ctx context.Context, d time.Duration) error {
			recordedDelays = append(recordedDelays, d)
			return nil
		}

		policy := client.DefaultReadPolicy()
		policy.Sleep = mockSleep

		expectedDelay := 750 * time.Millisecond
		stWithRetry, _ := status.New(codes.ResourceExhausted, "rate limit").WithDetails(&errdetails.RetryInfo{
			RetryDelay: durationpb.New(expectedDelay),
		})

		res, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
			att := atomic.AddInt32(&attempts, 1)
			if att < 3 {
				return "", stWithRetry.Err()
			}
			return "recovered_after_retry_info", nil
		})

		if err != nil {
			t.Fatalf("expected success after retry info, got: %v", err)
		}
		if res != "recovered_after_retry_info" {
			t.Fatalf("expected 'recovered_after_retry_info', got %q", res)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 attempts, got %d", attempts)
		}
		if len(recordedDelays) != 2 {
			t.Fatalf("expected 2 sleeps, got %d", len(recordedDelays))
		}
		for i, d := range recordedDelays {
			if d != expectedDelay {
				t.Errorf("sleep %d: expected delay %v, got %v", i, expectedDelay, d)
			}
		}
	})

	t.Run("Remote DeadlineExceeded is not retried", func(t *testing.T) {
		var attempts int32
		policy := client.DefaultReadPolicy()
		policy.Sleep = func(ctx context.Context, d time.Duration) error { return nil }

		res, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", status.Error(codes.DeadlineExceeded, "context deadline exceeded in remote service")
		})

		if err == nil {
			t.Fatalf("expected error, got res=%q", res)
		}
		if st, ok := status.FromError(err); !ok || st.Code() != codes.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded status code, got: %v", err)
		}
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt for remote DeadlineExceeded, got %d", attempts)
		}
	})

	t.Run("Local attempt timeout retries when overall context has budget", func(t *testing.T) {
		var attempts int32
		policy := client.RetryPolicy{
			MaxAttempts:      3,
			AttemptTimeout:   20 * time.Millisecond,
			OperationTimeout: 500 * time.Millisecond,
			IsIdempotent:     true,
			Sleep:            func(ctx context.Context, d time.Duration) error { return nil },
		}

		res, err := client.ExecuteWithRetry(ctx, policy, func(attemptCtx context.Context) (string, error) {
			att := atomic.AddInt32(&attempts, 1)
			if att < 3 {
				<-attemptCtx.Done()
				return "", attemptCtx.Err()
			}
			return "success_after_local_timeout", nil
		})

		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if res != "success_after_local_timeout" {
			t.Fatalf("unexpected res: %q", res)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("Boundary: MaxAttempts <= 1 or non-idempotent never retries", func(t *testing.T) {
		boundaryCases := []struct {
			name         string
			maxAttempts  int
			isIdempotent bool
		}{
			{"MaxAttempts=0", 0, true},
			{"MaxAttempts=1", 1, true},
			{"MaxAttempts=-1", -1, true},
			{"MaxAttempts=5 but IsIdempotent=false", 5, false},
		}

		for _, tc := range boundaryCases {
			t.Run(tc.name, func(t *testing.T) {
				var attempts int32
				policy := client.RetryPolicy{
					MaxAttempts:      tc.maxAttempts,
					AttemptTimeout:   100 * time.Millisecond,
					OperationTimeout: 200 * time.Millisecond,
					IsIdempotent:     tc.isIdempotent,
					Sleep: func(ctx context.Context, d time.Duration) error {
						t.Fatal("sleep should never be called when MaxAttempts<=1 or IsIdempotent=false")
						return nil
					},
				}

				_, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
					atomic.AddInt32(&attempts, 1)
					return "", status.Error(codes.Unavailable, "unavailable")
				})

				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if attempts != 1 {
					t.Fatalf("expected exactly 1 attempt for %s, got %d", tc.name, attempts)
				}
			})
		}
	})

	t.Run("Max attempts reached returns last error without extra sleep", func(t *testing.T) {
		var attempts int32
		var sleepCount int32
		policy := client.DefaultReadPolicy()
		policy.MaxAttempts = 3
		policy.Sleep = func(ctx context.Context, d time.Duration) error {
			atomic.AddInt32(&sleepCount, 1)
			return nil
		}

		expectedErr := status.Error(codes.Unavailable, "persistent outage")
		_, err := client.ExecuteWithRetry(ctx, policy, func(ctx context.Context) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", expectedErr
		})

		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected %v, got %v", expectedErr, err)
		}
		if attempts != 3 {
			t.Fatalf("expected exactly 3 attempts, got %d", attempts)
		}
		if sleepCount != 2 {
			t.Fatalf("expected exactly 2 sleeps for 3 attempts, got %d", sleepCount)
		}
	})
}

// =============================================================================
// CHALLENGE 4: SECRET REDACTION WITH TRICKY VARIANTS & PEM KEYS
// =============================================================================

func TestChallenge_SecretRedaction_Adversarial(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Bearer token nsct_ prefix with trailing period and symbols",
			input:    "Error: failed request with Bearer nsct_live_user_123456789.abc-def_xyz!",
			expected: "Error: failed request with Bearer [REDACTED TOKEN]!",
		},
		{
			name:     "Bearer token nscw_ prefix",
			input:    "Worker auth: bearer nscw_workspace_tok_998877665544332211",
			expected: "Worker auth: bearer [REDACTED TOKEN]",
		},
		{
			name:     "Bearer token st_ session prefix",
			input:    "Session error: BEARER st_sess_0123456789abcdef",
			expected: "Session error: BEARER [REDACTED TOKEN]",
		},
		{
			name:     "Arbitrary 20+ char alphanumeric Bearer token (JWT)",
			input:    "JWT: Bearer " + "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0",
			expected: "JWT: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Authorization header regex",
			input:    "Headers: {Authorization: Bearer mySecretTokenLongString12345}",
			expected: "Headers: {Authorization: Bearer [REDACTED TOKEN]}",
		},
		{
			name:     "Multiline RSA Private Key",
			input:    "Key load failed:\n" + testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-rsa-key-body") + "\nEnd of log",
			expected: "Key load failed:\n[REDACTED PRIVATE KEY]\nEnd of log",
		},
		{
			name:     "Multiline EC Private Key",
			input:    testutil.SynthesizePEMBlock("EC PRIVATE KEY", "test-ec-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "Multiline OPENSSH Private Key",
			input:    "SSH failure: " + testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "test-openssh-key-body") + " connected",
			expected: "SSH failure: [REDACTED PRIVATE KEY] connected",
		},
		{
			name:     "Multiline Generic PRIVATE KEY",
			input:    testutil.SynthesizePEMBlock("PRIVATE KEY", "test-pkcs8-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "Multiline ENCRYPTED PRIVATE KEY",
			input:    testutil.SynthesizePEMBlock("ENCRYPTED PRIVATE KEY", "test-encrypted-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "Combined multiple tokens and keys in single message",
			input:    "Error with Bearer " + "nsct_" + "token1 and Authorization: Bearer token2 and key: " + testutil.SynthesizePEMBlock("PRIVATE KEY", "test-combined-key-body"),
			expected: "Error with Bearer [REDACTED TOKEN] and Authorization: Bearer [REDACTED TOKEN] and key: [REDACTED PRIVATE KEY]",
		},
		{
			name:     "Clean error message with no secrets untouched",
			input:    "Invalid resource configuration: runner_tag 'linux-x86_64' is already registered in tenant.",
			expected: "Invalid resource configuration: runner_tag 'linux-x86_64' is already registered in tenant.",
		},
		{
			name:     "Short bearer string (< 20 chars without known prefix) not falsely redacted",
			input:    "Bearer short",
			expected: "Bearer short",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := client.RedactSecrets(tc.input)
			if actual != tc.expected {
				t.Fatalf("RedactSecrets mismatch:\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, actual)
			}
		})
	}
}

func TestChallenge_HandleGRPCError_DetailsAndFormatting(t *testing.T) {
	ctx := context.Background()

	t.Run("HandleGRPCError formats ErrorInfo and BadRequest field violations with secret redaction", func(t *testing.T) {
		st, _ := status.New(codes.InvalidArgument, "invalid request with Bearer nsct_12345678901234567890").WithDetails(
			&errdetails.ErrorInfo{Reason: "INVALID_SPEC", Domain: "namespaceapis.com"},
			&errdetails.BadRequest{
				FieldViolations: []*errdetails.BadRequest_FieldViolation{
					{Field: "spec.token", Description: "token Bearer nsct_99999999999999999999 is expired"},
					{Field: "spec.tag", Description: "tag cannot be empty"},
				},
			},
		)

		var diags diag.Diagnostics
		notFound := client.HandleGRPCError(ctx, st.Err(), &diags, "create", "runner profile")

		if notFound {
			t.Fatal("expected notFound=false")
		}
		if !diags.HasError() {
			t.Fatal("expected error diagnostic")
		}

		errDiag := diags[0]
		summary := errDiag.Summary()
		detail := errDiag.Detail()

		if !strings.Contains(summary, "gRPC Error (InvalidArgument) during create runner profile") {
			t.Errorf("unexpected summary: %s", summary)
		}
		if strings.Contains(detail, "nsct_12345678901234567890") || strings.Contains(detail, "nsct_99999999999999999999") {
			t.Errorf("bearer tokens were not redacted in details: %s", detail)
		}
		if !strings.Contains(detail, "[REDACTED TOKEN]") {
			t.Errorf("expected [REDACTED TOKEN] in detail: %s", detail)
		}
		if !strings.Contains(detail, "Reason: INVALID_SPEC, Domain: namespaceapis.com") {
			t.Errorf("missing ErrorInfo detail: %s", detail)
		}
		if !strings.Contains(detail, "Field \"spec.token\"") || !strings.Contains(detail, "Field \"spec.tag\"") {
			t.Errorf("missing field violation detail: %s", detail)
		}
	})

	t.Run("HandleGRPCError special code summaries", func(t *testing.T) {
		cases := []struct {
			code      codes.Code
			action    string
			msg       string
			expSumm   string
			expDetSub string
		}{
			{
				code:      codes.AlreadyExists,
				action:    "create",
				msg:       "tenant group engineering already exists",
				expSumm:   "tenant group Already Exists during create",
				expDetSub: "Use `terraform import`",
			},
			{
				code:      codes.FailedPrecondition,
				action:    "update",
				msg:       "revision 4 does not match current version 5",
				expSumm:   "Concurrency Conflict on runner profile during update",
				expDetSub: "Run `terraform refresh` and re-plan",
			},
			{
				code:      codes.Unauthenticated,
				action:    "read",
				msg:       "token invalid",
				expSumm:   "Authorization Failure during read runner profile",
				expDetSub: "Verify provider credentials and IAM permissions",
			},
			{
				code:      codes.PermissionDenied,
				action:    "create",
				msg:       "missing permission namespace.runner.create",
				expSumm:   "Authorization Failure during create runner profile",
				expDetSub: "Verify provider credentials and IAM permissions",
			},
		}

		for _, tc := range cases {
			var diags diag.Diagnostics
			st := status.Error(tc.code, tc.msg)
			client.HandleGRPCError(ctx, st, &diags, tc.action, "runner profile")

			if !diags.HasError() {
				t.Fatalf("expected error for code %s", tc.code)
			}
			errDiag := diags[0]
			if !strings.Contains(errDiag.Summary(), tc.expSumm) && !strings.Contains(errDiag.Summary(), "Already Exists") {
				t.Errorf("code %s: expected summary containing %q, got %q", tc.code, tc.expSumm, errDiag.Summary())
			}
			if !strings.Contains(errDiag.Detail(), tc.expDetSub) {
				t.Errorf("code %s: expected detail containing %q, got %q", tc.code, tc.expDetSub, errDiag.Detail())
			}
		}
	})
}
