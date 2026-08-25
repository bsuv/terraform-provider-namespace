package client_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// =============================================================================
// ADVERSARIAL TEST SUITE: SECRET REDACTION & ERROR HANDLING
// =============================================================================

// TestAdversarial_PrivateKeyRedaction covers all PEM private key formats,
// CRLF endings, embedded keys, multiple keys, and edge cases.
func TestAdversarial_PrivateKeyRedaction(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "PKCS#8 Unencrypted Private Key (LF)",
			input: "Failed to configure TLS:\n" +
				testutil.SynthesizePEMBlock("PRIVATE KEY", "test-pkcs8-key-body") + "\n" +
				"Connection terminated",
			expected: "Failed to configure TLS:\n" +
				"[REDACTED PRIVATE KEY]\n" +
				"Connection terminated",
		},
		{
			name: "PKCS#8 Unencrypted Private Key (CRLF)",
			input: "Failed to configure TLS:\r\n" +
				testutil.PEMBeginHeader("PRIVATE KEY") + "\r\n" +
				"test-pkcs8-key-body\r\n" +
				testutil.PEMEndHeader("PRIVATE KEY") + "\r\n" +
				"Connection terminated",
			expected: "Failed to configure TLS:\r\n" +
				"[REDACTED PRIVATE KEY]\r\n" +
				"Connection terminated",
		},
		{
			name: "PKCS#1 RSA Private Key",
			input: "Error loading client cert:\n" +
				testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-rsa-key-body") + "\n" +
				"Handshake failed",
			expected: "Error loading client cert:\n" +
				"[REDACTED PRIVATE KEY]\n" +
				"Handshake failed",
		},
		{
			name: "SEC1 EC Private Key",
			input: "EC Key error: " +
				testutil.SynthesizePEMBlock("EC PRIVATE KEY", "test-ec-key-body") +
				" (invalid curve)",
			expected: "EC Key error: [REDACTED PRIVATE KEY] (invalid curve)",
		},
		{
			name: "OpenSSH Private Key",
			input: "SSH Auth failure:\n" +
				testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "test-openssh-key-body") + "\n" +
				"Permission denied (publickey)",
			expected: "SSH Auth failure:\n" +
				"[REDACTED PRIVATE KEY]\n" +
				"Permission denied (publickey)",
		},
		{
			name:     "PKCS#8 Encrypted Private Key",
			input:    testutil.SynthesizePEMBlock("ENCRYPTED PRIVATE KEY", "test-encrypted-key-body"),
			expected: "[REDACTED PRIVATE KEY]",
		},
		{
			name:     "DSA Private Key",
			input:    "DSA key: " + testutil.SynthesizePEMBlock("DSA PRIVATE KEY", "test-dsa-key-body"),
			expected: "DSA key: [REDACTED PRIVATE KEY]",
		},
		{
			name:     "ECDSA Private Key",
			input:    "ECDSA key: " + testutil.SynthesizePEMBlock("ECDSA PRIVATE KEY", "test-ecdsa-key-body"),
			expected: "ECDSA key: [REDACTED PRIVATE KEY]",
		},
		{
			name: "Multiple Distinct Private Keys in Single Message",
			input: "Cert1: " + testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-rsa-body-1") + "\n" +
				"Cert2: " + testutil.SynthesizePEMBlock("EC PRIVATE KEY", "test-ec-body-2") + "\n" +
				"Cert3: " + testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "test-ssh-body-3"),
			expected: "Cert1: [REDACTED PRIVATE KEY]\n" +
				"Cert2: [REDACTED PRIVATE KEY]\n" +
				"Cert3: [REDACTED PRIVATE KEY]",
		},
		{
			name:     "Key inside JSON string payload",
			input:    `{"status":"error","detail":"Key rejected: ` + strings.ReplaceAll(testutil.SynthesizePEMBlock("PRIVATE KEY", "mock-json-payload"), "\n", `\n`) + `"}`,
			expected: `{"status":"error","detail":"Key rejected: [REDACTED PRIVATE KEY]"}`,
		},
		{
			name:     "Key with hyphenated type",
			input:    testutil.SynthesizePEMBlock("X942-DH PRIVATE KEY", "mock-x942-payload"),
			expected: "[REDACTED PRIVATE KEY]",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := client.RedactSecrets(tc.input)
			if actual != tc.expected {
				t.Errorf("RedactSecrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, actual)
			}
		})
	}
}

// TestAdversarial_AuthorizationHeaderAndJSONPayloads tests various auth headers,
// case sensitivity, extra spacing, and JSON encodings.
func TestAdversarial_AuthorizationHeaderAndJSONPayloads(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Standard Authorization: Bearer <alphanumeric-token>",
			input:    "request failed: Authorization: Bearer secrettoken1234567890abcdef",
			expected: "request failed: Authorization: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Lowercase authorization: bearer <token>",
			input:    "header authorization: bearer secrettoken1234567890abcdef",
			expected: "header authorization: bearer [REDACTED TOKEN]",
		},
		{
			name:     "Uppercase AUTHORIZATION: BEARER <token>",
			input:    "AUTHORIZATION: BEARER SECRETTOKEN1234567890ABCDEF",
			expected: "AUTHORIZATION: BEARER [REDACTED TOKEN]",
		},
		{
			name:     "Mixed case and variable whitespace",
			input:    "AuThOrIzAtIoN:   BeArEr   very-long-secret-jwt-token-9876543210",
			expected: "AuThOrIzAtIoN:   BeArEr   [REDACTED TOKEN]",
		},
		{
			name:     "Authorization header in string map format",
			input:    "Headers: {Authorization: Bearer mySecretTokenLongString12345}",
			expected: "Headers: {Authorization: Bearer [REDACTED TOKEN]}",
		},
		{
			name:     "Authorization header in JSON key-value format",
			input:    `{"Authorization": "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0"}`,
			expected: `{"Authorization": "Bearer [REDACTED TOKEN]"}`,
		},
		{
			name:     "Authorization header with nsct token in JSON",
			input:    `{"request": {"headers": {"authorization": "Bearer nsct_live_user_123456789"}}}`,
			expected: `{"request": {"headers": {"authorization": "Bearer [REDACTED TOKEN]"}}}`,
		},
		{
			name:     "Bearer with JWT token containing dots and dashes",
			input:    "Bearer " + "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0" + "." + "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			expected: "Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Already redacted token is idempotent",
			input:    "Authorization: Bearer [REDACTED TOKEN]",
			expected: "Authorization: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Bearer already redacted is idempotent",
			input:    "Bearer [REDACTED TOKEN]",
			expected: "Bearer [REDACTED TOKEN]",
		},
		{
			name:     "gRPC metadata string representation",
			input:    "rpc error: code = Unauthenticated desc = metadata map[authorization:[Bearer nsct_cluster_tok_123456789]]",
			expected: "rpc error: code = Unauthenticated desc = metadata map[authorization:[Bearer [REDACTED TOKEN]]]",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := client.RedactSecrets(tc.input)
			if actual != tc.expected {
				t.Errorf("RedactSecrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, actual)
			}
		})
	}
}

// TestAdversarial_StandaloneTokens tests raw tokens without 'Bearer' or 'Authorization'.
func TestAdversarial_StandaloneTokens(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Standalone nsct_ token in plain text",
			input:    "Failed to authenticate with cluster token nsct_live_user_998877665544",
			expected: "Failed to authenticate with cluster token [REDACTED TOKEN]",
		},
		{
			name:     "Standalone nscw_ token in plain text",
			input:    "Workspace authentication token nscw_workspace_tok_aabbccddeeff expired",
			expected: "Workspace authentication token [REDACTED TOKEN] expired",
		},
		{
			name:     "Standalone st_ token in plain text",
			input:    "Session st_session_tok_1122334455 is invalid",
			expected: "Session [REDACTED TOKEN] is invalid",
		},
		{
			name:     "Standalone nsct_ token in double quotes",
			input:    `Invalid token "nsct_1234567890abcdef" provided`,
			expected: `Invalid token "[REDACTED TOKEN]" provided`,
		},
		{
			name:     "Standalone nscw_ token in single quotes",
			input:    "Invalid token 'nscw_1234567890abcdef' provided",
			expected: "Invalid token '[REDACTED TOKEN]' provided",
		},
		{
			name:     "Standalone st_ token in parentheses",
			input:    "(token: st_1234567890abcdef) failed",
			expected: "(token: [REDACTED TOKEN]) failed",
		},
		{
			name:     "Standalone nsct_ token with dots and dashes",
			input:    "Token nsct_cluster.region-1_zone.alpha_999 is revoked",
			expected: "Token [REDACTED TOKEN] is revoked",
		},
		{
			name:     "Standalone token in URL query parameter",
			input:    "HTTP 401: GET https://api.namespace.so/v1/clusters?token=nsct_cluster_secret_123&env=prod",
			expected: "HTTP 401: GET https://api.namespace.so/v1/clusters?token=[REDACTED TOKEN]&env=prod",
		},
		{
			name:     "Standalone token in JSON field",
			input:    `{"error": "bad_token", "provided_token": "nscw_workspace_token_secret_123"}`,
			expected: `{"error": "bad_token", "provided_token": "[REDACTED TOKEN]"}`,
		},
		{
			name:     "Multiple standalone tokens of different types in one message",
			input:    "Cluster token nsct_tok1 and workspace token nscw_tok2 and session token st_tok3 all failed",
			expected: "Cluster token [REDACTED TOKEN] and workspace token [REDACTED TOKEN] and session token [REDACTED TOKEN] all failed",
		},
		{
			name:     "Standalone nscu_ user token in plain text",
			input:    "User session authentication token nscu_user_tok_998877665544 expired",
			expected: "User session authentication token [REDACTED TOKEN] expired",
		},
		{
			name:     "Standalone tok_ generic token in plain text",
			input:    "API key tok_generic_tok_abcdef123456 rejected by gateway",
			expected: "API key [REDACTED TOKEN] rejected by gateway",
		},
		{
			name:     "Standalone JWT token (3-part base64url)",
			input:    "Raw JWT rejected: " + "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0" + "." + "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c" + " expired",
			expected: "Raw JWT rejected: [REDACTED JWT] expired",
		},
		{
			name:     "Environment variable NSC_TOKEN in error output",
			input:    "Failed reading NSC_TOKEN=nsct_secret_value_123456789 from environment",
			expected: "Failed reading NSC_TOKEN=[REDACTED] from environment",
		},
		{
			name:     "Environment variable NSC_PARTNER_TOKEN in error output",
			input:    "Auth error: NSC_PARTNER_TOKEN=eyJhbGciOi... is invalid",
			expected: "Auth error: NSC_PARTNER_TOKEN=[REDACTED] is invalid",
		},
		{
			name:     "Environment variable NSC_TRUSTED_JWT in error output",
			input:    "Config dump: NSC_TRUSTED_JWT=eyJhbGciOiJIUzI1Ni... NSC_ENDPOINT=https://iam.namespaceapis.com",
			expected: "Config dump: NSC_TRUSTED_JWT=[REDACTED] NSC_ENDPOINT=https://iam.namespaceapis.com",
		},
		{
			name:     "Environment variable NSC_TOKEN_FILE in error output",
			input:    "File read failed: NSC_TOKEN_FILE=/secrets/token.jwt permission denied",
			expected: "File read failed: NSC_TOKEN_FILE=[REDACTED] permission denied",
		},
		{
			name:     "Environment variable CACHE_PASSWORD in error output",
			input:    "Connection string CACHE_PASSWORD=secret_redis_pass_9988 rejected",
			expected: "Connection string CACHE_PASSWORD=[REDACTED] rejected",
		},
		{
			name:     "Normal non-token strings with underscores not falsely redacted",
			input:    "Resource name runner_profile_linux_x86_64 or tenant_group_engineering is valid",
			expected: "Resource name runner_profile_linux_x86_64 or tenant_group_engineering is valid",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := client.RedactSecrets(tc.input)
			if actual != tc.expected {
				t.Errorf("RedactSecrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, actual)
			}
		})
	}
}

// TestAdversarial_HandleGRPCError_Comprehensive checks secret redaction across
// all gRPC status codes, error details, and edge cases.
func TestAdversarial_HandleGRPCError_Comprehensive(t *testing.T) {
	ctx := context.Background()

	t.Run("Redacts secrets across ErrorInfo, BadRequest, and status message", func(t *testing.T) {
		st, _ := status.New(codes.InvalidArgument, "Validation failed with key "+testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "MIIE...")).WithDetails(
			&errdetails.ErrorInfo{
				Reason: "TOKEN_EXPIRED",
				Domain: "namespaceapis.com",
				Metadata: map[string]string{
					"token": "nsct_" + "secret_cluster_token_123",
				},
			},
			&errdetails.BadRequest{
				FieldViolations: []*errdetails.BadRequest_FieldViolation{
					{
						Field:       "spec.auth_token",
						Description: "Bearer " + "nscw_" + "workspace_token_9988 is invalid",
					},
					{
						Field:       "spec.ssh_key",
						Description: "Key " + testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "b3B...") + " is unsupported",
					},
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

		detail := diags[0].Detail()

		// Verify no secret leak
		forbiddenSubstrings := []string{
			testutil.PEMBeginHeader("RSA PRIVATE KEY"),
			testutil.PEMBeginHeader("OPENSSH PRIVATE KEY"),
			"nsct_" + "secret_cluster_token_123",
			"nscw_" + "workspace_token_9988",
		}
		for _, sub := range forbiddenSubstrings {
			if strings.Contains(detail, sub) {
				t.Errorf("secret leaked in diagnostic detail: found %q in:\n%s", sub, detail)
			}
		}

		// Verify redacted placeholders present
		if !strings.Contains(detail, "[REDACTED PRIVATE KEY]") {
			t.Errorf("expected [REDACTED PRIVATE KEY] in detail: %s", detail)
		}
		if !strings.Contains(detail, "[REDACTED TOKEN]") {
			t.Errorf("expected [REDACTED TOKEN] in detail: %s", detail)
		}
	})

	t.Run("Non-gRPC standard error is also redacted", func(t *testing.T) {
		plainErr := fmt.Errorf("network error sending token Bearer nsct_cluster_token_abc123 to backend")

		var diags diag.Diagnostics
		notFound := client.HandleGRPCError(ctx, plainErr, &diags, "read", "runner profile")

		if notFound {
			t.Fatal("expected notFound=false")
		}
		if !diags.HasError() {
			t.Fatal("expected error diagnostic")
		}

		detail := diags[0].Detail()
		if strings.Contains(detail, "nsct_cluster_token_abc123") {
			t.Errorf("secret leaked in plain error diagnostic: %s", detail)
		}
		if !strings.Contains(detail, "Bearer [REDACTED TOKEN]") {
			t.Errorf("expected redacted bearer token in plain error diagnostic: %s", detail)
		}
	})

	t.Run("Nil error produces no diagnostics and returns false", func(t *testing.T) {
		var diags diag.Diagnostics
		notFound := client.HandleGRPCError(ctx, nil, &diags, "create", "runner profile")
		if notFound {
			t.Error("expected false for nil error")
		}
		if diags.HasError() {
			t.Error("expected no diagnostics for nil error")
		}
	})
}

// TestAdversarial_RedactSecrets_ConcurrencyStress ensures RedactSecrets is
// entirely thread-safe and free of data races when invoked concurrently.
func TestAdversarial_RedactSecrets_ConcurrencyStress(t *testing.T) {
	inputs := []string{
		"Error loading key: " + testutil.SynthesizePEMBlock("PRIVATE KEY", "fixture"),
		"Header Authorization: Bearer " + "nsct_" + "live_token_1234567890",
		"Raw token " + "nscw_" + "workspace_9876543210 in log",
		"Session " + "st_" + "session_abc123 failed",
		"No secret here: normal error message",
		testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-rsa-body"),
		"Bearer " + "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0",
	}

	const numWorkers = 50
	const iterations = 100

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				idx := (workerID + j) % len(inputs)
				res := client.RedactSecrets(inputs[idx])
				if strings.Contains(res, "nsct_") || strings.Contains(res, "nscw_") || strings.Contains(res, "st_") || strings.Contains(res, "BEGIN") {
					t.Errorf("worker %d iteration %d failed redaction: %s", workerID, j, res)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestAdversarial_RedactSecrets_OpaqueStructuredFields(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"opaque-bearer-value-without-known-prefix",
		"opaque-session-value",
		"opaque-client-secret",
		"opaque-api-key",
		"opaque-private-key-value",
		"opaque-basic-authorization",
		"opaque-query-token",
		"opaque-auth-token",
		"opaque-credentials-value",
		"opaque-signing-key-value",
		"opaque-certificate-value",
		"opaque-passphrase-value",
	}
	input := strings.Join([]string{
		`{"bearerToken":"` + secrets[0] + `"}`,
		`{\"bearerToken\":\"` + secrets[0] + `\"}`,
		`session_token='` + secrets[1] + `'`,
		`client-secret=` + secrets[2],
		`{\"client-secret\":\"` + secrets[2] + `\"}`,
		`api_key: ` + secrets[3],
		`privateKeyPem="` + secrets[4] + `"`,
		`{\"privateKeyPem\":\"` + secrets[4] + `\"}`,
		`Authorization: Basic ` + secrets[5],
		`{\"Authorization\":\"Basic ` + secrets[5] + `\"}`,
		`https://example.invalid/path?token=` + secrets[6] + `&safe=value`,
		`{\"authToken\":\"` + secrets[7] + `\"}`,
		`credentials='` + secrets[8] + `'`,
		`{\"signingKey\":\"` + secrets[9] + `\"}`,
		`certificate=` + secrets[10],
		`{\"passphrase\":\"` + secrets[11] + `\"}`,
	}, "\n")

	redacted := client.RedactSecrets(input)
	for _, secret := range secrets {
		if strings.Contains(redacted, secret) {
			t.Fatalf("opaque structured secret leaked after redaction: %q in %q", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "&safe=value") {
		t.Fatalf("query-field redaction consumed unrelated data: %q", redacted)
	}
}
