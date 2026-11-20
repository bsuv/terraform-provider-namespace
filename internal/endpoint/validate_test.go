package endpoint_test

import (
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/endpoint"
)

func TestValidate_ControlCharsAndWhitespace(t *testing.T) {
	t.Parallel()

	invalidCases := []struct {
		name        string
		input       string
		errContains string
	}{
		{"Empty", "", "must not be empty"},
		{"Spaces only", "   ", "must not be empty"},
		{"NUL byte", "https://global.namespaceapis.com\x00", "control characters"},
		{"Trailing newline", "https://global.namespaceapis.com\n", "control characters"},
		{"Leading CR", "\rhttps://global.namespaceapis.com", "control characters"},
		{"Embedded tab", "https://global.namespaceapis.com\t:443", "control characters"},
		{"DEL char", "https://global.namespaceapis.com\x7f", "control characters"},
		{"Unit separator", "https://global.namespaceapis.com\x1f", "control characters"},
		{"Embedded space in URL", "https://global.namespaceapis.com evil.com", "embedded whitespace"},
		{"Embedded space in host", "global.namespaceapis.com :443", "embedded whitespace"},
		{"Embedded NBSP", "https://global.\u00a0namespaceapis.com", "embedded whitespace"},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := endpoint.Validate(tc.input)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.input)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}

	// Leading/trailing ASCII spaces remain accepted (trimmed)
	if err := endpoint.Validate(" HTTPS://UPPER.example.com/ "); err != nil {
		t.Errorf("expected trimmed valid endpoint to pass, got: %v", err)
	}
}

func TestValidate_MetadataSSRFAndUnspecifiedBlocked(t *testing.T) {
	t.Parallel()

	blocked := []struct {
		name        string
		input       string
		errContains string
	}{
		{"Unspecified IPv4 HTTPS", "https://0.0.0.0:443", "unspecified"},
		{"Unspecified IPv4 bare", "0.0.0.0", "unspecified"},
		{"Unspecified IPv6 HTTPS", "https://[::]:443", "unspecified"},
		{"Unspecified IPv6 bare", "[::]", "unspecified"},
		{"Unspecified IPv4-mapped IPv6", "https://[::ffff:0.0.0.0]:443", "unspecified"},
		{"AWS/GCP/Azure metadata IPv4", "https://169.254.169.254", "link-local or metadata"},
		{"AWS/GCP/Azure metadata IPv4 with port", "169.254.169.254:443", "link-local or metadata"},
		{"Link-local IPv4 start", "https://169.254.0.1:443", "link-local or metadata"},
		{"Link-local IPv4 end", "https://169.254.255.254:443", "link-local or metadata"},
		{"IPv4-mapped IPv6 metadata", "https://[::ffff:169.254.169.254]:443", "link-local or metadata"},
		{"IPv6 link-local", "https://[fe80::1]:443", "link-local or metadata"},
		{"IPv6 link-local zoned", "https://[fe80::1%25eth0]:443", "link-local or metadata"},
		{"IPv6 link-local upper bound", "https://[febf::1]:443", "link-local or metadata"},
		{"AWS IPv6 IMDS", "https://[fd00:ec2::254]:443", "cloud metadata"},
		{"AWS IPv6 IMDS bare", "[fd00:ec2::254]", "cloud metadata"},
		{"Alibaba IPv4 metadata", "https://100.100.100.200:443", "cloud metadata"},
		{"IPv4-compatible IPv6 metadata", "https://[::169.254.169.254]:443", "link-local or metadata"},
		{"NAT64 well-known metadata", "https://[64:ff9b::169.254.169.254]:443", "link-local or metadata"},
		{"NAT64 well-known Alibaba metadata", "https://[64:ff9b::100.100.100.200]:443", "cloud metadata"},
		{"NAT64 local-use metadata", "https://[64:ff9b:1::169.254.169.254]:443", "link-local or metadata"},
		{"GCP metadata hostname", "https://metadata.google.internal:443", "cloud metadata hostname"},
		{"GCP metadata goog", "metadata.goog", "cloud metadata hostname"},
		{"GCP metadata subdomain", "https://sub.metadata.google.internal", "cloud metadata hostname"},
		{"AWS instance-data hostname", "https://instance-data:443", "cloud metadata hostname"},
		{"AWS ec2 instance-data hostname", "https://instance-data.ec2.internal", "cloud metadata hostname"},
		{"Azure metadata hostname", "https://metadata.azure.internal", "cloud metadata hostname"},
		{"Non-canonical decimal IPv4", "https://2852039166:443", "non-canonical numeric IP"},
		{"Non-canonical hex IPv4", "https://0xa9fea9fe:443", "non-canonical numeric IP"},
		{"Non-canonical octal IPv4", "https://0251.0376.0251.0376:443", "non-canonical numeric IP"},
		{"Non-canonical shorthand IPv4", "https://169.254.43518:443", "non-canonical numeric IP"},
		{"Reserved gRPC unix scheme bare", "unix:443", "reserved gRPC resolver scheme"},
		{"Reserved gRPC unix scheme HTTPS", "https://unix", "reserved gRPC resolver scheme"},
		{"Reserved gRPC unix scheme HTTPS port", "https://unix:443", "reserved gRPC resolver scheme"},
		{"Reserved gRPC unix-abstract scheme", "unix-abstract:1234", "reserved gRPC resolver scheme"},
		{"Reserved gRPC passthrough scheme", "https://passthrough:443", "reserved gRPC resolver scheme"},
		{"Reserved gRPC dns scheme", "dns:443", "reserved gRPC resolver scheme"},
		{"Reserved gRPC xds scheme", "xds:443", "reserved gRPC resolver scheme"},
		{"Reserved gRPC google-c2p scheme", "google-c2p:443", "reserved gRPC resolver scheme"},
		{"Empty trailing query string", "https://global.namespaceapis.com?", "query parameters"},
	}

	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := endpoint.Validate(tc.input)
			if err == nil {
				t.Fatalf("expected SSRF/unspecified IP %q to be rejected, got nil", tc.input)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q for %q, got: %v", tc.errContains, tc.input, err)
			}
		})
	}

	allowed := []string{
		"https://global.namespaceapis.com:443",
		"https://custom-proxy.corp.example:8443",
		"localhost:8080",
		"https://localhost:8443",
		"https://127.0.0.1:8443",
		"127.0.0.1:9000",
		"https://[::1]:8443",
		"[::1]:8443",
		"https://10.20.30.40:443",
		"https://172.16.10.5:443",
		"https://192.168.1.50:443",
		"https://[fd00:1234::1]:443",
		"https://[2001:db8::1]:443",
	}

	for _, ep := range allowed {
		t.Run("Allowed_"+ep, func(t *testing.T) {
			t.Parallel()
			if err := endpoint.Validate(ep); err != nil {
				t.Errorf("expected endpoint %q to remain allowed, got: %v", ep, err)
			}
		})
	}
}

func TestValidate_MalformedURLDoesNotLeakCredentials(t *testing.T) {
	t.Parallel()

	err := endpoint.Validate("https://user:SuperSecretPass99@global.namespaceapis.com:badport")
	if err == nil {
		t.Fatal("expected malformed HTTPS URL with bad port to fail validation")
	}
	if strings.Contains(err.Error(), "SuperSecretPass99") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("endpoint.Validate leaked URL credentials in error: %v", err)
	}
}

func TestIsTrustedHost(t *testing.T) {
	t.Parallel()

	trusted := []string{
		"https://global.namespaceapis.com",
		"https://global.namespaceapis.com:443",
		"https://eu.compute.namespaceapis.com:443",
		"https://namespaceapis.com",
		"https://api.namespace.so",
		"https://namespace.so:443",
		"https://registry.namespacelabs.dev",
		"https://namespacelabs.dev",
		"GLOBAL.NAMESPACEAPIS.COM",
		"https://localhost:8443",
		"localhost:9000",
		"https://127.0.0.1:8443",
		"https://[::1]:8443",
		"[::1]",
		"https://[::ffff:127.0.0.1]:8443",
	}

	for _, ep := range trusted {
		t.Run("Trusted_"+ep, func(t *testing.T) {
			t.Parallel()
			if !endpoint.IsTrustedHost(ep) {
				t.Errorf("expected %q to be trusted", ep)
			}
		})
	}

	untrusted := []string{
		"",
		"   ",
		"https://proxy.corp.example:443",
		"https://evil-namespaceapis.com",
		"https://namespaceapis.com.evil.com",
		"https://evilnamespace.so",
		"https://namespacelabs.dev.attacker.invalid",
		"https://10.0.0.1:443",
		"https://192.168.1.1:8443",
		"https://[2001:db8::1]:443",
	}

	for _, ep := range untrusted {
		t.Run("Untrusted_"+ep, func(t *testing.T) {
			t.Parallel()
			if endpoint.IsTrustedHost(ep) {
				t.Errorf("expected %q NOT to be trusted", ep)
			}
		})
	}
}
