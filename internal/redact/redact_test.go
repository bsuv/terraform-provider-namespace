package redact_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/redact"
)

func synthPEM(headerType, body string) string {
	return fmt.Sprintf("%s\n%s\n%s",
		"-----"+"BEGIN "+headerType+"-----",
		body,
		"-----"+"END "+headerType+"-----",
	)
}

func TestSecrets_Comprehensive(t *testing.T) {
	t.Parallel()

	sampleJWT := "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"ey" + "JzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0." +
		"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "PKCS8 private key",
			input:    "TLS failure:\n" + synthPEM("PRIVATE KEY", "mock-pkcs8-key-data") + "\ndone",
			expected: "TLS failure:\n[REDACTED PRIVATE KEY]\ndone",
		},
		{
			name:     "RSA private key with CRLF",
			input:    "Key:\r\n" + "-----" + "BEGIN RSA PRIVATE KEY-----\r\nmock-rsa\r\n" + "-----" + "END RSA PRIVATE KEY-----\r\n",
			expected: "Key:\r\n[REDACTED PRIVATE KEY]\r\n",
		},
		{
			name:     "X509 certificate",
			input:    "Cert:\n" + synthPEM("CERTIFICATE", "mock-cert-data"),
			expected: "Cert:\n[REDACTED CERTIFICATE]",
		},
		{
			name:     "Authorization Bearer header",
			input:    "request failed: Authorization: Bearer secrettoken1234567890abcdef",
			expected: "request failed: Authorization: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Authorization Basic JSON",
			input:    `{"Authorization":"Basic bXl1c2VyOm15cGFzcw=="}`,
			expected: `{"Authorization":"Basic [REDACTED SECRET]"}`,
		},
		{
			name:     "Namespace tokens",
			input:    "tokens nsct_abc123 nscw_def456 nscu_ghi789 tok_jkl012 st_mno345 failed",
			expected: "tokens [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] failed",
		},
		{
			name:     "Standalone 3-part JWT",
			input:    "Raw JWT rejected: " + sampleJWT + " expired",
			expected: "Raw JWT rejected: [REDACTED JWT] expired",
		},
		{
			name:     "JWT label with Bearer",
			input:    "JWT: Bearer " + sampleJWT,
			expected: "JWT: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Environment variables",
			input:    "NSC_TOKEN=nsct_secret CACHE_PASSWORD=pw123 NSC_ENDPOINT=https://iam.namespaceapis.com",
			expected: "NSC_TOKEN=[REDACTED] CACHE_PASSWORD=[REDACTED] NSC_ENDPOINT=https://iam.namespaceapis.com",
		},
		{
			name:     "Opaque structured fields",
			input:    `{"bearerToken":"opaque1","client_secret":"opaque2","password":"opaque3","privateKeyPem":"opaque4","cert":"opaque5"}`,
			expected: `{"bearerToken":"[REDACTED TOKEN]","client_secret":"[REDACTED SECRET]","password":"[REDACTED PASSWORD]","privateKeyPem":"[REDACTED PRIVATE KEY]","cert":"[REDACTED CERTIFICATE]"}`,
		},
		{
			name:     "Query token preserves unrelated query params",
			input:    "https://example.invalid/path?token=opaque_query_tok&safe=value",
			expected: "https://example.invalid/path?token=[REDACTED TOKEN]&safe=value",
		},
		{
			name:     "Normal resource names untouched",
			input:    "runner_profile_linux_x86_64 and tenant_group_engineering",
			expected: "runner_profile_linux_x86_64 and tenant_group_engineering",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual := redact.Secrets(tc.input)
			if actual != tc.expected {
				t.Errorf("Secrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, actual)
			}
			// Verify idempotency on second pass
			if second := redact.Secrets(actual); second != actual {
				t.Errorf("Secrets is not idempotent\nFirst:  %q\nSecond: %q", actual, second)
			}
		})
	}
}

func TestSecrets_Concurrency(t *testing.T) {
	t.Parallel()

	sampleJWT := "ey" + "JhbGciOiJIUzI1NiJ9." + "ey" + "JzdWIiOiIxMjMifQ." + "c2lnbmF0dXJl"
	inputs := []string{
		"key: " + synthPEM("EC PRIVATE KEY", "ec-body"),
		"bearer " + "nsct_" + "live_secret_123456",
		"jwt " + sampleJWT,
		`{"password":"secret-password"}`,
		"benign status message",
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				in := inputs[(id+j)%len(inputs)]
				out := redact.Secrets(in)
				if strings.Contains(out, "nsct_") || strings.Contains(out, "secret-password") || strings.Contains(out, "BEGIN") || strings.Contains(out, "eyJ") {
					t.Errorf("unredacted secret in worker %d iter %d: %q", id, j, out)
				}
			}
		}(i)
	}
	wg.Wait()
}
