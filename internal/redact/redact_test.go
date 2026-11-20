package redact_test

import (
	"encoding/json"
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
			name:     "Authorization Bearer header inside JSON string preserves closing quote",
			input:    `{"log":"authorization: Bearer secret_token_12345"}`,
			expected: `{"log":"authorization: Bearer [REDACTED TOKEN]"}`,
		},
		{
			name:     "Authorization Basic header inside JSON string preserves closing quote",
			input:    `{"log":"authorization: Basic dXNlcjpwYXNz"}`,
			expected: `{"log":"authorization: Basic [REDACTED SECRET]"}`,
		},
		{
			name:     "Authorization Basic JSON",
			input:    `{"Authorization":"Basic bXl1c2VyOm15cGFzcw=="}`,
			expected: `{"Authorization":"Basic [REDACTED SECRET]"}`,
		},
		{
			name:     "Unquoted authorization key with quoted short Bearer",
			input:    `authorization="Bearer short_tok"`,
			expected: `authorization="Bearer [REDACTED TOKEN]"`,
		},
		{
			name:     "Unquoted authorization key with quoted Basic",
			input:    `authorization="Basic dXNlcjpwYXNzd29yZF9sb25nX3NlY3JldF8xMjM0NTY3ODkwYWJjZGVm"`,
			expected: `authorization="Basic [REDACTED SECRET]"`,
		},
		{
			name:     "Unquoted authorization key with JSON-escaped quoted short Bearer",
			input:    `{"log":"authorization: \"Bearer short_tok\"","ok":1}`,
			expected: `{"log":"authorization: \"Bearer [REDACTED TOKEN]\"","ok":1}`,
		},
		{
			name:     "Unquoted authorization key with JSON-escaped quoted Basic",
			input:    `{"log":"authorization: \"Basic dXNlcjpwYXNz\"","ok":1}`,
			expected: `{"log":"authorization: \"Basic [REDACTED SECRET]\"","ok":1}`,
		},
		{
			name:     "Unquoted authorization key with non-scheme quoted prose stays untouched",
			input:    `authorization: "granted" {"log":"authorization: \"granted\"","ok":1}`,
			expected: `authorization: "granted" {"log":"authorization: \"granted\"","ok":1}`,
		},
		{
			name:     "Quoted authorization key with non-scheme value still redacted",
			input:    `{"authorization":"granted"}`,
			expected: `{"authorization":"[REDACTED TOKEN]"}`,
		},
		{
			name:     "Unquoted token before JSON escaped newline preserves next field",
			input:    `{"log":"token: my_secret_tok\nnext_field: ok","ok":1}`,
			expected: `{"log":"token: [REDACTED TOKEN]\nnext_field: ok","ok":1}`,
		},
		{
			name:     "Unquoted password before JSON escaped quote preserves valid JSON",
			input:    `{"log":"got password=my_pass\"}"}`,
			expected: `{"log":"got password=[REDACTED PASSWORD]\"}"}`,
		},
		{
			name:     "Unquoted env NSC_TOKEN before JSON escaped quote preserves valid JSON",
			input:    `{"log":"env NSC_TOKEN=my_secret_val\"}"}`,
			expected: `{"log":"env NSC_TOKEN=[REDACTED]\"}"}`,
		},
		{
			name:     "Unquoted api_key, cert, and secret before JSON escaped quotes preserve valid JSON",
			input:    `{"log":"api_key=my_key\" cert=my_cert\" secret=my_sec\"}"}`,
			expected: `{"log":"api_key=[REDACTED PRIVATE KEY]\" cert=[REDACTED CERTIFICATE]\" secret=[REDACTED SECRET]\"}"}`,
		},
		{
			name:     "Namespace tokens",
			input:    "tokens nsct_abc123 nscw_def456 nscu_ghi789 " + "nsrt_" + "revokable_1234567890" + " tok_jkl012 st_mno345 failed",
			expected: "tokens [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] failed",
		},
		{
			name:     "JWT-bearing nsrt_ token preserves REDACTED TOKEN priority",
			input:    "revokable jwt: " + "nsrt_" + sampleJWT + " done",
			expected: "revokable jwt: [REDACTED TOKEN] done",
		},
		{
			name:     "JWT-bearing Namespace prefixes all yield REDACTED TOKEN",
			input:    "nsct_" + sampleJWT + " " + "nscw_" + sampleJWT + " " + "nscu_" + sampleJWT + " " + "nsrt_" + sampleJWT + " " + "tok_" + sampleJWT + " " + "st_" + sampleJWT,
			expected: "[REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN] [REDACTED TOKEN]",
		},
		{
			name:     "Standalone 3-part JWT",
			input:    "Raw JWT rejected: " + sampleJWT + " expired",
			expected: "Raw JWT rejected: [REDACTED JWT] expired",
		},
		{
			name:     "Prefixed OIDC and Cognito JWTs",
			input:    "oidc=" + "oidc_" + sampleJWT + " cognito=" + "cognito_" + sampleJWT + " aws=" + "aws-cognito_" + sampleJWT,
			expected: "oidc=[REDACTED JWT] cognito=[REDACTED JWT] aws=[REDACTED JWT]",
		},
		{
			name:     "Prefixed JWT in structured JSON and URL query",
			input:    `{"oidc_token":"` + "oidc_" + sampleJWT + `"} url=https://example.invalid/cb?id_token=` + "cognito_" + sampleJWT + `&safe=1`,
			expected: `{"oidc_token":"[REDACTED JWT]"} url=https://example.invalid/cb?id_token=[REDACTED JWT]&safe=1`,
		},
		{
			name:     "Prefixed JWT in Authorization Bearer header",
			input:    "request failed: Authorization: Bearer " + "oidc_" + sampleJWT,
			expected: "request failed: Authorization: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "JWT label with Bearer",
			input:    "JWT: Bearer " + sampleJWT,
			expected: "JWT: Bearer [REDACTED TOKEN]",
		},
		{
			name:     "Environment variables including NSC_TOKEN_SPEC and NSC_TOKEN_SPEC_FILE",
			input:    "NSC_TOKEN=nsct_secret NSC_TOKEN_SPEC=" + "nsrt_" + "spec_secret_123456" + " NSC_TOKEN_SPEC_FILE=/run/secrets/spec.json CACHE_PASSWORD=pw123 NSC_ENDPOINT=https://iam.namespaceapis.com",
			expected: "NSC_TOKEN=[REDACTED] NSC_TOKEN_SPEC=[REDACTED] NSC_TOKEN_SPEC_FILE=[REDACTED] CACHE_PASSWORD=[REDACTED] NSC_ENDPOINT=https://iam.namespaceapis.com",
		},
		{
			name:     "Quoted JSON NSC_TOKEN_SPEC env var",
			input:    `NSC_TOKEN_SPEC='{"inline":"` + "nsrt_" + "spec_json_123456" + `"}' ok=1`,
			expected: `NSC_TOKEN_SPEC=[REDACTED] ok=1`,
		},
		{
			name:     "Unquoted single-line JSON NSC_TOKEN_SPEC env var",
			input:    `NSC_TOKEN_SPEC={"file":"/run/secrets/private.json"} ok=1`,
			expected: `NSC_TOKEN_SPEC=[REDACTED] ok=1`,
		},
		{
			name:     "Unquoted nested JSON NSC_TOKEN_SPEC env var",
			input:    `NSC_TOKEN_SPEC={"auth":{"file":"/run/secrets/private.json"}} ok=1`,
			expected: `NSC_TOKEN_SPEC=[REDACTED] ok=1`,
		},
		{
			name:     "Colon-delimited JSON NSC_TOKEN_SPEC and NSC_TOKEN_SPEC_FILE",
			input:    `{"NSC_TOKEN_SPEC_FILE":"/run/secrets/spec.json","NSC_TOKEN_SPEC":"` + "nsrt_" + `secret_123456"}`,
			expected: `{"NSC_TOKEN_SPEC_FILE":"[REDACTED TOKEN]","NSC_TOKEN_SPEC":"[REDACTED TOKEN]"}`,
		},
		{
			name:     "Colon-delimited JSON object NSC_TOKEN_SPEC",
			input:    `{"NSC_TOKEN_SPEC":{"file":"/run/secrets/colon_obj_secret.json"}}`,
			expected: `{"NSC_TOKEN_SPEC":"[REDACTED TOKEN]"}`,
		},
		{
			name:     "Colon-delimited JSON object token_spec with escaped quotes and backslashes",
			input:    `{"NSC_TOKEN_SPEC":{"payload":"a\"b}c\\","nested":{"k":"v\\\\\"}"}},"ok":1}`,
			expected: `{"NSC_TOKEN_SPEC":"[REDACTED TOKEN]","ok":1}`,
		},
		{
			name:     "Escaped JSON-in-JSON object NSC_TOKEN_SPEC",
			input:    `{"outer":"{\"NSC_TOKEN_SPEC\":{\"file\":\"/run/secrets/in_json.json\"}}"}`,
			expected: `{"outer":"{\"NSC_TOKEN_SPEC\":\"[REDACTED TOKEN]\"}"}`,
		},
		{
			name:     "Equals-delimited JSON-escaped NSC_TOKEN_SPEC inside JSON log string",
			input:    `{"log":"env NSC_TOKEN_SPEC={\"a\":\"x}y\",\"file\":\"/run/secrets/in_log.json\"} failed"}`,
			expected: `{"log":"env NSC_TOKEN_SPEC=[REDACTED] failed"}`,
		},
		{
			name:     "Two-level JSON-escaped equals-delimited NSC_TOKEN_SPEC inside nested JSON log",
			input:    `{"outer":"{\"log\":\"env NSC_TOKEN_SPEC={\\\"a\\\":\\\"x}y\\\",\\\"file\\\":\\\"/run/secrets/in_log2.json\\\"} failed\"}"}`,
			expected: `{"outer":"{\"log\":\"env NSC_TOKEN_SPEC=[REDACTED] failed\"}"}`,
		},
		{
			name:     "JSON-escaped equals-delimited NSC_TOKEN_SPEC with escaped newline after brace",
			input:    `{"log":"env NSC_TOKEN_SPEC={\n  \"a\":\"x}y\",\n  \"file\":\"/run/secrets/multiline_in_log.json\"\n} failed"}`,
			expected: `{"log":"env NSC_TOKEN_SPEC=[REDACTED] failed"}`,
		},
		{
			name:     "JSON-escaped equals-delimited NSC_TOKEN_SPEC with escaped tab after brace",
			input:    `{"log":"env NSC_TOKEN_SPEC={\t\"a\":\"x}y\",\"file\":\"/run/secrets/tab_in_log.json\"} failed"}`,
			expected: `{"log":"env NSC_TOKEN_SPEC=[REDACTED] failed"}`,
		},
		{
			name:     "JSON-escaped equals-delimited NSC_TOKEN_SPEC with escaped carriage return after brace",
			input:    `{"log":"env NSC_TOKEN_SPEC={\r  \"a\":\"x}y\",\r  \"file\":\"/run/secrets/cr_in_log.json\"\r} failed"}`,
			expected: `{"log":"env NSC_TOKEN_SPEC=[REDACTED] failed"}`,
		},
		{
			name:     "Two-level JSON-escaped equals-delimited NSC_TOKEN_SPEC with escaped newline after brace",
			input:    `{"outer":"{\"log\":\"env NSC_TOKEN_SPEC={\\n  \\\"a\\\":\\\"x}y\\\",\\n  \\\"file\\\":\\\"/run/secrets/multiline_in_log2.json\\\"\\n} failed\"}"}`,
			expected: `{"outer":"{\"log\":\"env NSC_TOKEN_SPEC=[REDACTED] failed\"}"}`,
		},
		{
			name:     "Unclosed equals-delimited JSON-escaped NSC_TOKEN_SPEC inside JSON log string falls back safely",
			input:    `{"log":"env NSC_TOKEN_SPEC={\"a\":\"1\",\"tok\":\"` + "nsrt_" + `unclosed_in_log_123\"","ok":1}`,
			expected: `{"log":"env NSC_TOKEN_SPEC={\"a\":\"1\",\"tok\":\"[REDACTED TOKEN]\"","ok":1}`,
		},
		{
			name:     "Two-level nested unquoted JSON NSC_TOKEN_SPEC env var with sibling fields",
			input:    `NSC_TOKEN_SPEC={"auth":{"nested":{"file":"/run/secrets/deep_1.json"}},"path":"/run/secrets/deep_2.json"} ok=1`,
			expected: `NSC_TOKEN_SPEC=[REDACTED] ok=1`,
		},
		{
			name:     "Unbalanced unclosed JSON object NSC_TOKEN_SPEC falls back safely",
			input:    "NSC_TOKEN_SPEC={\"file\":\"/run/secrets/unclosed\",\"token\":\"" + "nsrt_" + "fallback_tok_123\"\nsafe=1",
			expected: "NSC_TOKEN_SPEC={\"file\":\"/run/secrets/unclosed\",\"token\":\"[REDACTED TOKEN]\"\nsafe=1",
		},
		{
			name:     "PASSPHRASE and other env keys 2-pass idempotency",
			input:    "PASSPHRASE=my_secret_123 TOKEN=tok_val_1 SECRET=sec_val_2 PASSWORD=pw_val_3 API_KEY=ak_val_4 NSC_TOKEN=nsct_val_5 CACHE_PASSWORD=cpw_val_6 GPG_PRIVATE_KEY=gpg_val_7 TF_VAR_nsc_secret=tf_val_8",
			expected: "PASSPHRASE=[REDACTED PASSWORD] TOKEN=[REDACTED TOKEN] SECRET=[REDACTED SECRET] PASSWORD=[REDACTED PASSWORD] API_KEY=[REDACTED PRIVATE KEY] NSC_TOKEN=[REDACTED] CACHE_PASSWORD=[REDACTED] GPG_PRIVATE_KEY=[REDACTED] TF_VAR_nsc_secret=[REDACTED]",
		},
		{
			name:     "Already redacted markers and token-prefixed passwords are not split",
			input:    "PASSPHRASE=[REDACTED PASSWORD] NSC_TOKEN=[REDACTED TOKEN] (token: st_1234567890abcdef) failed password=" + "nsct_" + "pw_123 api_key=" + "nsct_" + "ak_456",
			expected: "PASSPHRASE=[REDACTED PASSWORD] NSC_TOKEN=[REDACTED TOKEN] (token: [REDACTED TOKEN]) failed password=[REDACTED PASSWORD] api_key=[REDACTED PRIVATE KEY]",
		},
		{
			name:     "Padded base64url JWTs (bare, prefixed, and nsrt_)",
			input:    "bare=" + "ey" + "JhbGciOiJIUzI1NiJ9" + "." + "ey" + "JzdWIiOiIxMjMifQ==" + "." + "c2ln==" + " oidc=" + "oidc_" + "ey" + "JhbGciOiJIUzI1NiJ9" + "." + "ey" + "JzdWIiOiIxMjMifQ==" + "." + "c2ln==" + " nsrt=" + "nsrt_" + "ey" + "JhbGciOiJIUzI1NiJ9" + "." + "ey" + "JzdWIiOiIxMjMifQ==" + "." + "c2ln==",
			expected: "bare=[REDACTED JWT] oidc=[REDACTED JWT] nsrt=[REDACTED TOKEN]",
		},
		{
			name:     "Opaque structured fields",
			input:    `{"bearerToken":"opaque1","client_secret":"opaque2","password":"opaque3","privateKeyPem":"opaque4","cert":"opaque5"}`,
			expected: `{"bearerToken":"[REDACTED TOKEN]","client_secret":"[REDACTED SECRET]","password":"[REDACTED PASSWORD]","privateKeyPem":"[REDACTED PRIVATE KEY]","cert":"[REDACTED CERTIFICATE]"}`,
		},
		{
			name:     "Query token and password preserve unrelated query params",
			input:    "url=https://example.invalid/auth?token=opaque_query_tok&password=pw123&safe=1",
			expected: "url=https://example.invalid/auth?token=[REDACTED TOKEN]&password=[REDACTED PASSWORD]&safe=1",
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
		"revokable " + "nsrt_" + "revokable_secret_123456",
		"prefixed " + "oidc_" + sampleJWT,
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
				if strings.Contains(out, "nsct_") || strings.Contains(out, "nsrt_") || strings.Contains(out, "oidc_") || strings.Contains(out, "secret-password") || strings.Contains(out, "BEGIN") || strings.Contains(out, "eyJ") {
					t.Errorf("unredacted secret in worker %d iter %d: %q", id, j, out)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestSecrets_TruncatedPEM_MetadataHeaders_AuthArrays_And_EscapedQuotes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Truncated private key with literal newlines preserves following key=val",
			input:    "err=" + "-----" + "BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nMIIEowIBAAKCAQEA\nNSC_ENDPOINT=https://global.namespaceapis.com",
			expected: "err=[REDACTED PRIVATE KEY]\nNSC_ENDPOINT=https://global.namespaceapis.com",
		},
		{
			name:     "Truncated certificate with escaped newlines in JSON preserves closing JSON",
			input:    `{"cert_err":"` + "-----" + `BEGIN CERTIFICATE-----\nMIIDXTCCAkWgAwIBAgIJ\nVersion: 1","ok":true}`,
			expected: `{"cert_err":"[REDACTED CERTIFICATE]","ok":true}`,
		},
		{
			name:     "gRPC metadata authorization headers Bearer and Basic",
			input:    "rpc error: metadata=map[authorization:[Bearer " + "nsct_" + "md_secret_12345] x-other:[1]] basic=map[authorization:[Basic dXNlcjpwYXNz]] already=map[authorization:[[REDACTED TOKEN]]]",
			expected: "rpc error: metadata=map[authorization:[Bearer [REDACTED TOKEN]] x-other:[1]] basic=map[authorization:[Basic [REDACTED SECRET]]] already=map[authorization:[[REDACTED TOKEN]]]",
		},
		{
			name:     "JSON authorization arrays raw and escaped",
			input:    `{"authorization":["Bearer ` + "nsct_" + `arr_secret_1","Basic dXNlcjpwYXNz"]} {"outer":"{\"authorization\":[\"Bearer ` + "nsrt_" + `nested_arr_2\"]}"}`,
			expected: `{"authorization":["Bearer [REDACTED TOKEN]","Basic [REDACTED SECRET]"]} {"outer":"{\"authorization\":[\"Bearer [REDACTED TOKEN]\"]}"}`,
		},
		{
			name:     "Nested escaped quotes inside quoted token and outer quote boundary",
			input:    `{"token":"abc\"def\\\"ghi","safe":"ok"} {"wrap":"{\"token\":\"inner_secret` + `","outer":"keep"}`,
			expected: `{"token":"[REDACTED TOKEN]","safe":"ok"} {"wrap":"{\"token\":\"[REDACTED TOKEN]","outer":"keep"}`,
		},
		{
			name:     "Unterminated quoted token terminated by newline falls back safely",
			input:    "token=\"unterminated_line\nsafe=1",
			expected: "token=\"unterminated_line\nsafe=1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redact.Secrets(tc.input)
			if got != tc.expected {
				t.Errorf("Secrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, got)
			}
		})
	}
}

func TestCredentialUsernames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "JSON quoted username",
			input:    `{"username":"cache_user_123","url":"https://cache.namespaceapis.com"}`,
			expected: `{"username":"[REDACTED SECRET]","url":"https://cache.namespaceapis.com"}`,
		},
		{
			name:     "Escaped JSON-in-JSON username",
			input:    `{"body":"{\"username\":\"nested_cache_user\"}"}`,
			expected: `{"body":"{\"username\":\"[REDACTED SECRET]\"}"}`,
		},
		{
			name:     "Unquoted username assignment",
			input:    "cache response username=plain_user_456 status=ok",
			expected: "cache response username=[REDACTED SECRET] status=ok",
		},
		{
			name:     "Unquoted username before JSON escaped quote preserves valid JSON",
			input:    `{"log":"username=u\"}"}`,
			expected: `{"log":"username=[REDACTED SECRET]\"}"}`,
		},
		{
			name:     "Normal text without username untouched",
			input:    "runner_profile_linux_x86_64",
			expected: "runner_profile_linux_x86_64",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redact.CredentialUsernames(tc.input)
			if got != tc.expected {
				t.Errorf("CredentialUsernames(%q) = %q, want %q", tc.input, got, tc.expected)
			}
			if strings.HasPrefix(tc.input, "{") && !json.Valid([]byte(got)) {
				t.Errorf("CredentialUsernames(%q) produced invalid JSON: %q", tc.input, got)
			}
			if second := redact.CredentialUsernames(got); second != got {
				t.Errorf("CredentialUsernames is not idempotent\nFirst:  %q\nSecond: %q", got, second)
			}
		})
	}
}

func TestSecrets_JSONValidity_OnEscapeBoundaries(t *testing.T) {
	t.Parallel()

	jsonInputs := []string{
		`{"log":"authorization: \"Bearer short_tok\"","ok":1}`,
		`{"log":"authorization: \"Basic dXNlcjpwYXNz\"","ok":1}`,
		`{"log":"authorization: \"granted\"","ok":1}`,
		`{"log":"token: my_secret_tok\nnext_field: ok","ok":1}`,
		`{"log":"got password=my_pass\"}"}`,
		`{"log":"env NSC_TOKEN=my_secret_val\"}"}`,
		`{"log":"api_key=my_key\" cert=my_cert\" secret=my_sec\"}"}`,
		`{"log":"got password=\"my_pass\"}"}`,
	}

	for _, input := range jsonInputs {
		got := redact.Secrets(input)
		if !json.Valid([]byte(got)) {
			t.Fatalf("redact.Secrets(%q) produced invalid JSON: %q", input, got)
		}
		if second := redact.Secrets(got); second != got {
			t.Fatalf("redact.Secrets(%q) is not 2-pass idempotent:\nfirst:  %q\nsecond: %q", input, got, second)
		}
	}
}

func TestSecrets_JSONEscapePrefixedSecrets_And_Boundaries(t *testing.T) {
	t.Parallel()

	sampleJWT := "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"ey" + "JzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0." +
		"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

	cases := []struct {
		name     string
		input    string
		expected string
		isJSON   bool
	}{
		{
			name:     "nsrt_ after JSON newline escape",
			input:    `{"log":"line1\n` + "nsrt_" + `live_secret_1234567890"}`,
			expected: `{"log":"line1\n[REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "nsct_ after JSON newline escape",
			input:    `{"log":"line1\n` + "nsct_" + `live_secret_123456"}`,
			expected: `{"log":"line1\n[REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "bare JWT after JSON tab escape",
			input:    `{"log":"jwt:\t` + sampleJWT + `"}`,
			expected: `{"log":"jwt:\t[REDACTED JWT]"}`,
			isJSON:   true,
		},
		{
			name:     "prefixed OIDC JWT after JSON newline escape preserves backslash-n and valid JSON",
			input:    `{"log":"jwt:\n` + "oidc_" + sampleJWT + `"}`,
			expected: `{"log":"jwt:\n[REDACTED JWT]"}`,
			isJSON:   true,
		},
		{
			name:     "NSC_TOKEN_SPEC_FILE after JSON newline escape",
			input:    `{"log":"env:\nNSC_TOKEN_SPEC_FILE=/run/secrets/private_spec.json"}`,
			expected: `{"log":"env:\nNSC_TOKEN_SPEC_FILE=[REDACTED]"}`,
			isJSON:   true,
		},
		{
			name:     "NSC_TOKEN after JSON newline escape",
			input:    `{"log":"env:\nNSC_TOKEN=opaque_secret_value"}`,
			expected: `{"log":"env:\nNSC_TOKEN=[REDACTED]"}`,
			isJSON:   true,
		},
		{
			name:     "NSC_TOKEN_SPEC object after JSON newline escape",
			input:    `{"log":"env:\nNSC_TOKEN_SPEC={\"file\":\"/run/secrets/obj1.json\"}"}`,
			expected: `{"log":"env:\nNSC_TOKEN_SPEC=[REDACTED]"}`,
			isJSON:   true,
		},
		{
			name:     "NSC_TOKEN object after JSON newline escape",
			input:    `{"log":"env:\nNSC_TOKEN={\"file\":\"/run/secrets/obj2.json\"}"}`,
			expected: `{"log":"env:\nNSC_TOKEN=[REDACTED]"}`,
			isJSON:   true,
		},
		{
			name:     "authorization Bearer after JSON newline escape",
			input:    `{"log":"hdr:\nauthorization: Bearer short_tok"}`,
			expected: `{"log":"hdr:\nauthorization: Bearer [REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "authorization Basic after JSON newline escape",
			input:    `{"log":"hdr:\nauthorization: Basic dXNlcjpwYXNz"}`,
			expected: `{"log":"hdr:\nauthorization: Basic [REDACTED SECRET]"}`,
			isJSON:   true,
		},
		{
			name:     "quoted authorization Bearer after JSON newline escape",
			input:    `{"log":"hdr:\nauthorization=\"Bearer short_tok\""}`,
			expected: `{"log":"hdr:\nauthorization=\"Bearer [REDACTED TOKEN]\""}`,
			isJSON:   true,
		},
		{
			name:     "password after JSON newline escape",
			input:    `{"log":"err:\npassword=my_secret_pass"}`,
			expected: `{"log":"err:\npassword=[REDACTED PASSWORD]"}`,
			isJSON:   true,
		},
		{
			name:     "quoted password after JSON newline escape",
			input:    `{"log":"err:\npassword=\"quoted_secret\""}`,
			expected: `{"log":"err:\npassword=\"[REDACTED PASSWORD]\""}`,
			isJSON:   true,
		},
		{
			name:     "secret after JSON tab escape",
			input:    `{"log":"err:\tsecret=my_secret_val"}`,
			expected: `{"log":"err:\tsecret=[REDACTED SECRET]"}`,
			isJSON:   true,
		},
		{
			name:     "standalone bearer after JSON newline escape",
			input:    `{"log":"hdr:\nbearer 1234567890abcdefghij"}`,
			expected: `{"log":"hdr:\nbearer [REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "nsrt_ after carriage return escape",
			input:    `{"log":"a\r` + "nsrt_" + `cr_secret_123456"}`,
			expected: `{"log":"a\r[REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "prefixed JWT after backspace escape",
			input:    `{"log":"a\b` + "oidc_" + sampleJWT + `"}`,
			expected: `{"log":"a\b[REDACTED JWT]"}`,
			isJSON:   true,
		},
		{
			name:     "password after formfeed escape",
			input:    `{"log":"a\fpassword=ff_secret_val"}`,
			expected: `{"log":"a\fpassword=[REDACTED PASSWORD]"}`,
			isJSON:   true,
		},
		{
			name:     "prefixed JWT after unicode newline escape u000a",
			input:    `{"log":"a\u000a` + "oidc_" + sampleJWT + `"}`,
			expected: `{"log":"a\u000a[REDACTED JWT]"}`,
			isJSON:   true,
		},
		{
			name:     "query password after unicode ampersand escape u0026 preserves safe param",
			input:    `{"log":"https://example.invalid/?a=1\u0026password=u26_secret\u0026safe=1"}`,
			expected: `{"log":"https://example.invalid/?a=1\u0026password=[REDACTED PASSWORD]\u0026safe=1"}`,
			isJSON:   true,
		},
		{
			name:     "metadata map auth header and key/cert/token fields after newline escape",
			input:    `{"log":"rpc:\nmap[authorization:[Bearer short_tok]]\napi_key=my_api_key\ncert=my_cert\ntoken=my_tok"}`,
			expected: `{"log":"rpc:\nmap[authorization:[Bearer [REDACTED TOKEN]]]\napi_key=[REDACTED PRIVATE KEY]\ncert=[REDACTED CERTIFICATE]\ntoken=[REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "odd triple backslash newline escape acts as boundary",
			input:    `{"log":"esc:\\\npassword=secret_val"}`,
			expected: `{"log":"esc:\\\npassword=[REDACTED PASSWORD]"}`,
			isJSON:   true,
		},
		{
			name:     "even double backslash n does not split literal npassword or nNSC_TOKEN or nnsrt_ or nauthorization",
			input:    `{"log":"literal:\\npassword=keep1 \\nNSC_TOKEN=keep2 \\n` + "nsrt_" + `keep3_123456 \\nauthorization: Bearer short_tok","my_token":"safe"}`,
			expected: `{"log":"literal:\\npassword=keep1 \\nNSC_TOKEN=keep2 \\n` + "nsrt_" + `keep3_123456 \\nauthorization: Bearer short_tok","my_token":"safe"}`,
			isJSON:   true,
		},
		{
			name:     "even double backslash before noidc_eyJ redacts literal noidc_eyJ while preserving valid JSON",
			input:    `{"log":"literal:\\` + "noidc_" + sampleJWT + `"}`,
			expected: `{"log":"literal:\\[REDACTED JWT]"}`,
			isJSON:   true,
		},
		{
			name:     "URL query string token and password preserve trailing ampersand safe parameter",
			input:    "url=https://example.invalid/auth?token=tok123&password=pw123&safe=1",
			expected: "url=https://example.invalid/auth?token=[REDACTED TOKEN]&password=[REDACTED PASSWORD]&safe=1",
		},
		{
			name:     "lowercase u003b before nsrt_ token acts as boundary",
			input:    `{"msg":"a\u003b` + "nsrt_" + `semi_secret_123456"}`,
			expected: `{"msg":"a\u003b[REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "lowercase u002f before prefixed oidc JWT acts as boundary",
			input:    `{"msg":"a\u002f` + "oidc_" + sampleJWT + `"}`,
			expected: `{"msg":"a\u002f[REDACTED JWT]"}`,
			isJSON:   true,
		},
		{
			name:     "lowercase u003f before password= acts as boundary",
			input:    `{"msg":"a\u003fpassword=u3f_pw_123"}`,
			expected: `{"msg":"a\u003fpassword=[REDACTED PASSWORD]"}`,
			isJSON:   true,
		},
		{
			name:     "lowercase u003b before secret= acts as boundary",
			input:    `{"msg":"a\u003bsecret=u3b_sec_123"}`,
			expected: `{"msg":"a\u003bsecret=[REDACTED SECRET]"}`,
			isJSON:   true,
		},
		{
			name:     "lowercase u003d and u002b before NSC_TOKEN= and authorization Bearer act as boundaries",
			input:    `{"msg":"a\u003dNSC_TOKEN=u3d_tok_123\u002bauthorization: Bearer short_u2b_tok"}`,
			expected: `{"msg":"a\u003dNSC_TOKEN=[REDACTED]\u002bauthorization: Bearer [REDACTED TOKEN]"}`,
			isJSON:   true,
		},
		{
			name:     "even double backslash u003b preserves literal non-escape boundary",
			input:    `{"msg":"literal:\\u003bpassword=keep_literal"}`,
			expected: `{"msg":"literal:\\u003bpassword=keep_literal"}`,
			isJSON:   true,
		},
		{
			name:     "non-boundary word hex escapes u0062 and u0066 preserve literal word prefix",
			input:    `{"msg":"a\u0062password=keep_bpass \u0066password=keep_fpass"}`,
			expected: `{"msg":"a\u0062password=keep_bpass \u0066password=keep_fpass"}`,
			isJSON:   true,
		},
		{
			name:     "URL query string passphrase and password stop at ampersand and preserve bar=2",
			input:    "https://example.invalid/cb?foo=1&passphrase=pp_999&password=pw_888&bar=2",
			expected: "https://example.invalid/cb?foo=1&passphrase=[REDACTED PASSWORD]&password=[REDACTED PASSWORD]&bar=2",
		},
		{
			name:     "JSON-wrapped URL query passphrase and password preserve trailing bar=2 and valid JSON",
			input:    `{"cb":"https://example.invalid/cb?foo=1&passphrase=pp_999&password=pw_888&bar=2"}`,
			expected: `{"cb":"https://example.invalid/cb?foo=1&passphrase=[REDACTED PASSWORD]&password=[REDACTED PASSWORD]&bar=2"}`,
			isJSON:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redact.Secrets(tc.input)
			if got != tc.expected {
				t.Fatalf("Secrets mismatch\nInput:    %q\nExpected: %q\nActual:   %q", tc.input, tc.expected, got)
			}
			if tc.isJSON && !json.Valid([]byte(got)) {
				t.Fatalf("Secrets produced invalid JSON: %q", got)
			}
			if second := redact.Secrets(got); second != got {
				t.Fatalf("Secrets is not 2-pass idempotent:\nfirst:  %q\nsecond: %q", got, second)
			}
		})
	}

	t.Run("CredentialUsernames after JSON escape and boundary preservation", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			input    string
			expected string
		}{
			{`{"log":"line1\nusername=plain_user_456"}`, `{"log":"line1\nusername=[REDACTED SECRET]"}`},
			{`{"log":"line1\nusername=\"quoted_user_456\""}`, `{"log":"line1\nusername=\"[REDACTED SECRET]\""}`},
			{`{"log":"line1\\nusername=keep_literal"}`, `{"log":"line1\\nusername=keep_literal"}`},
		} {
			got := redact.CredentialUsernames(tc.input)
			if got != tc.expected {
				t.Fatalf("CredentialUsernames(%q) = %q, want %q", tc.input, got, tc.expected)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("CredentialUsernames produced invalid JSON: %q", got)
			}
			if second := redact.CredentialUsernames(got); second != got {
				t.Fatalf("CredentialUsernames not idempotent: %q vs %q", got, second)
			}
		}
	})
}
