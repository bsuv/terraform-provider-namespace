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
// CHALLENGER 2: EMPIRICAL TEST SUITE
// =============================================================================

// TestEmpirical_Challenger2_R2_SecretRedaction_Comprehensive tests the complete
// matrix of private keys, certs, tokens, passwords, and env vars in RedactSecrets.
func TestEmpirical_Challenger2_R2_SecretRedaction_Comprehensive(t *testing.T) {
	testCases := []struct {
		name        string
		input       string
		mustContain []string
		mustNotFind []string
	}{
		{
			name:        "EC Private Key (SEC1)",
			input:       "Error loading mTLS key: " + testutil.SynthesizePEMBlock("EC PRIVATE KEY", "test-ec-sec1-data") + "\nHandshake failed",
			mustContain: []string{"Error loading mTLS key: [REDACTED PRIVATE KEY]\nHandshake failed"},
			mustNotFind: []string{testutil.PEMBeginHeader("EC PRIVATE KEY"), "test-ec-sec1-data", testutil.PEMEndHeader("EC PRIVATE KEY")},
		},
		{
			name: "OpenSSH Private Key",
			input: "Key verification failure:\n" +
				testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "test-openssh-data") + "\nRejected by bastion",
			mustContain: []string{"Key verification failure:\n[REDACTED PRIVATE KEY]\nRejected by bastion"},
			mustNotFind: []string{testutil.PEMBeginHeader("OPENSSH PRIVATE KEY"), "test-openssh-data", testutil.PEMEndHeader("OPENSSH PRIVATE KEY")},
		},
		{
			name: "RSA Private Key (PKCS#1)",
			input: "RSA error:\n" +
				testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "test-rsa-pkcs1-data") + "\nAbort",
			mustContain: []string{"RSA error:\n[REDACTED PRIVATE KEY]\nAbort"},
			mustNotFind: []string{testutil.PEMBeginHeader("RSA PRIVATE KEY"), "test-rsa-pkcs1-data", testutil.PEMEndHeader("RSA PRIVATE KEY")},
		},
		{
			name: "DSA Private Key",
			input: "DSA key rejected:\n" +
				testutil.SynthesizePEMBlock("DSA PRIVATE KEY", "test-dsa-data") + "\nFailed",
			mustContain: []string{"DSA key rejected:\n[REDACTED PRIVATE KEY]\nFailed"},
			mustNotFind: []string{testutil.PEMBeginHeader("DSA PRIVATE KEY"), "test-dsa-data", testutil.PEMEndHeader("DSA PRIVATE KEY")},
		},
		{
			name: "ECDSA Private Key",
			input: "ECDSA key:\n" +
				testutil.SynthesizePEMBlock("ECDSA PRIVATE KEY", "test-ecdsa-data") + "\nInvalid",
			mustContain: []string{"ECDSA key:\n[REDACTED PRIVATE KEY]\nInvalid"},
			mustNotFind: []string{testutil.PEMBeginHeader("ECDSA PRIVATE KEY"), "test-ecdsa-data", testutil.PEMEndHeader("ECDSA PRIVATE KEY")},
		},
		{
			name:        "PKCS#8 Unencrypted Private Key",
			input:       testutil.SynthesizePEMBlock("PRIVATE KEY", "test-pkcs8-unencrypted-data"),
			mustContain: []string{"[REDACTED PRIVATE KEY]"},
			mustNotFind: []string{testutil.PEMBeginHeader("PRIVATE KEY"), "test-pkcs8-unencrypted-data", testutil.PEMEndHeader("PRIVATE KEY")},
		},
		{
			name:        "PKCS#8 Encrypted Private Key",
			input:       testutil.SynthesizePEMBlock("ENCRYPTED PRIVATE KEY", "test-pkcs8-encrypted-data"),
			mustContain: []string{"[REDACTED PRIVATE KEY]"},
			mustNotFind: []string{testutil.PEMBeginHeader("ENCRYPTED PRIVATE KEY"), "test-pkcs8-encrypted-data", testutil.PEMEndHeader("ENCRYPTED PRIVATE KEY")},
		},
		{
			name: "X.509 Certificate",
			input: "Server cert:\n" +
				testutil.SynthesizePEMBlock("CERTIFICATE", "test-certificate-data") + "\nExpired",
			mustContain: []string{"Server cert:\n[REDACTED CERTIFICATE]\nExpired"},
			mustNotFind: []string{testutil.PEMBeginHeader("CERTIFICATE"), "test-certificate-data", testutil.PEMEndHeader("CERTIFICATE")},
		},
		{
			name:  "Passwords in various formats",
			input: `Configs: password = "super_secret_pwd_1", 'password': 'super_secret_pwd_2', "password": "super_secret_pwd_3", password: super_secret_pwd_4, CACHE_PASSWORD=redis_pass_123`,
			mustContain: []string{
				`password = "[REDACTED PASSWORD]"`,
				`'password': '[REDACTED PASSWORD]'`,
				`"password": "[REDACTED PASSWORD]"`,
				`password: [REDACTED PASSWORD]`,
				`CACHE_PASSWORD=[REDACTED]`,
			},
			mustNotFind: []string{"super_secret_pwd_1", "super_secret_pwd_2", "super_secret_pwd_3", "super_secret_pwd_4", "redis_pass_123"},
		},
		{
			name:  "Namespace Tokens (nsct_, nscw_, nscu_, tok_, st_)",
			input: "Tokens: nsct_cl_1234567890, nscw_ws_0987654321, nscu_usr_1122334455, tok_gen_5566778899, st_sess_9988776655",
			mustContain: []string{
				"Tokens: [REDACTED TOKEN], [REDACTED TOKEN], [REDACTED TOKEN], [REDACTED TOKEN], [REDACTED TOKEN]",
			},
			mustNotFind: []string{"nsct_cl_1234567890", "nscw_ws_0987654321", "nscu_usr_1122334455", "tok_gen_5566778899", "st_sess_9988776655"},
		},
		{
			name:  "JWT and Bearer Headers",
			input: "Authorization: Bearer " + "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0" + "." + "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			mustContain: []string{
				"Authorization: Bearer [REDACTED TOKEN]",
			},
			mustNotFind: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		},
		{
			name:  "Environment Variables (NSC_TOKEN, NSC_PARTNER_TOKEN, NSC_TRUSTED_JWT, NSC_TOKEN_FILE)",
			input: "Env dump: NSC_TOKEN=nsct_secret NSC_PARTNER_TOKEN=nscw_partner NSC_TRUSTED_JWT=eyJ... NSC_TOKEN_FILE=/etc/token.json",
			mustContain: []string{
				"NSC_TOKEN=[REDACTED]",
				"NSC_PARTNER_TOKEN=[REDACTED]",
				"NSC_TRUSTED_JWT=[REDACTED]",
				"NSC_TOKEN_FILE=[REDACTED]",
			},
			mustNotFind: []string{"nsct_secret", "nscw_partner", "/etc/token.json"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res := client.RedactSecrets(tc.input)
			for _, must := range tc.mustContain {
				if !strings.Contains(res, must) {
					t.Errorf("expected redacted text to contain %q, got:\n%s", must, res)
				}
			}
			for _, forbidden := range tc.mustNotFind {
				if strings.Contains(res, forbidden) {
					t.Errorf("redacted text leaked forbidden substring %q:\n%s", forbidden, res)
				}
			}
		})
	}
}

// TestEmpirical_Challenger2_R2_HandleGRPCError_Redaction tests that HandleGRPCError
// strips all secrets from summary, detail, and error details across gRPC status codes.
func TestEmpirical_Challenger2_R2_HandleGRPCError_Redaction(t *testing.T) {
	ctx := context.Background()

	codesToTest := []codes.Code{
		codes.InvalidArgument,
		codes.Unauthenticated,
		codes.PermissionDenied,
		codes.NotFound,
		codes.AlreadyExists,
		codes.FailedPrecondition,
		codes.Aborted,
		codes.Internal,
		codes.Unavailable,
	}

	for _, code := range codesToTest {
		t.Run(fmt.Sprintf("Status_%s", code), func(t *testing.T) {
			secretMsg := "RPC failed with key " + testutil.SynthesizePEMBlock("EC PRIVATE KEY", "test-ec-sec1-data") + "\nand token " + "nsct_" + "cluster_sec_123 and CACHE_PASSWORD=secretpwd"
			st, err := status.New(code, secretMsg).WithDetails(
				&errdetails.ErrorInfo{
					Reason: "nsct_" + "reason_tok",
					Domain: "namespaceapis.com",
					Metadata: map[string]string{
						"token":    "nsct_" + "meta_token_val",
						"ssh_key":  testutil.SynthesizePEMBlock("OPENSSH PRIVATE KEY", "test-openssh-data"),
						"password": "my_db_password_123",
					},
				},
				&errdetails.PreconditionFailure{
					Violations: []*errdetails.PreconditionFailure_Violation{
						{
							Type:        "REVISION_MISMATCH",
							Subject:     "res_1",
							Description: "Mismatch with token " + "nsct_" + "precon_tok",
						},
					},
				},
			)
			if err != nil {
				t.Fatalf("failed to construct status with details: %v", err)
			}

			var diags diag.Diagnostics
			// For NotFound on read/delete, it returns true without adding error diags
			action := "create"
			if code == codes.NotFound {
				action = "update"
			}
			isNF := client.HandleGRPCError(ctx, st.Err(), &diags, action, "test_resource")
			if isNF && code != codes.NotFound {
				t.Errorf("expected notFound=false for code %s", code)
			}

			if !diags.HasError() {
				t.Fatalf("expected error diagnostics for code %s", code)
			}

			for _, d := range diags.Errors() {
				summary := d.Summary()
				detail := d.Detail()
				fullDiag := summary + "\n" + detail

				forbidden := []string{
					testutil.PEMBeginHeader("EC PRIVATE KEY"),
					testutil.PEMBeginHeader("OPENSSH PRIVATE KEY"),
					"nsct_" + "cluster_sec_123",
					"secretpwd",
					"nsct_" + "reason_tok",
					"nsct_" + "meta_token_val",
					"my_db_password_123",
					"nsct_" + "precon_tok",
				}

				for _, f := range forbidden {
					if strings.Contains(fullDiag, f) {
						t.Errorf("code %s leaked secret %q in diagnostic:\n%s", code, f, fullDiag)
					}
				}
			}
		})
	}
}

// TestEmpirical_Challenger2_R2_Redaction_HighConcurrencyStress runs 100 parallel goroutines
// doing 100 iterations of redaction on complex payloads to verify race freedom.
func TestEmpirical_Challenger2_R2_Redaction_HighConcurrencyStress(t *testing.T) {
	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				input := fmt.Sprintf("goroutine %d iter %d: key=%s token=nsct_tok_%d_%d password=\"secret_%d\"",
					gid, i, testutil.SynthesizePEMBlock("RSA PRIVATE KEY", "MIIE..."), gid, i, gid)
				redacted := client.RedactSecrets(input)
				if strings.Contains(redacted, testutil.PEMBeginHeader("RSA PRIVATE KEY")) || strings.Contains(redacted, fmt.Sprintf("nsct_tok_%d_%d", gid, i)) || strings.Contains(redacted, fmt.Sprintf("secret_%d", gid)) {
					t.Errorf("concurrency leak on goroutine %d iteration %d: %s", gid, i, redacted)
				}
			}
		}(g)
	}

	wg.Wait()
}
