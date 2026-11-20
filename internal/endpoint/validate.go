package endpoint

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

var (
	ipv4LinkLocalPrefix = netip.MustParsePrefix("169.254.0.0/16")
	ipv6LinkLocalPrefix = netip.MustParsePrefix("fe80::/10")
	awsIPv6MetadataAddr = netip.MustParseAddr("fd00:ec2::254")
)

// Validate accepts an HTTPS URL or bare host[:port] and rejects endpoint
// components that could alter routing, carry credentials, contain control/whitespace
// characters, or target unspecified or cloud-metadata/link-local addresses.
func Validate(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fmt.Errorf("endpoint must not be empty")
	}

	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("endpoint must not contain control characters")
		}
	}
	for _, r := range value {
		if unicode.IsSpace(r) {
			return fmt.Errorf("endpoint must not contain embedded whitespace")
		}
	}

	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") {
		return fmt.Errorf("endpoint must use HTTPS; http scheme is not supported")
	}

	if strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("endpoint must be a valid HTTPS URL: %w", err)
		}
		if u.Host == "" || u.Hostname() == "" {
			return fmt.Errorf("endpoint must be a valid HTTPS URL: missing host")
		}
		return validateURL(u)
	}

	if strings.Contains(value, "://") {
		return fmt.Errorf("endpoint must use HTTPS scheme")
	}
	if strings.Contains(value, "/") {
		return fmt.Errorf("endpoint must not contain path segments")
	}

	u, err := url.Parse("https://" + value)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("endpoint must be a valid host[:port] or HTTPS URL")
	}
	return validateURL(u)
}

func validateURL(u *url.URL) error {
	if u.User != nil {
		return fmt.Errorf("endpoint must not contain userinfo or credentials; userinfo/credentials are forbidden")
	}
	if u.RawQuery != "" {
		return fmt.Errorf("endpoint must not contain query parameters")
	}
	if u.Fragment != "" {
		return fmt.Errorf("endpoint must not contain URL fragments")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("endpoint must not contain path segments")
	}

	if err := validateHost(u.Hostname()); err != nil {
		return err
	}

	port := u.Port()
	if strings.HasSuffix(u.Host, ":") && port == "" {
		return fmt.Errorf("endpoint contains an empty port")
	}
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("endpoint port must be between 1 and 65535")
		}
	}

	return nil
}

func validateHost(host string) error {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}

	addr = addr.WithZone("").Unmap()
	if addr.IsUnspecified() {
		return fmt.Errorf("endpoint host %q must not be an unspecified IP address", host)
	}
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		ipv4LinkLocalPrefix.Contains(addr) || ipv6LinkLocalPrefix.Contains(addr) {
		return fmt.Errorf("endpoint host %q must not be a link-local or metadata IP address", host)
	}
	if addr == awsIPv6MetadataAddr {
		return fmt.Errorf("endpoint host %q must not be a cloud metadata IP address", host)
	}
	return nil
}

// IsTrustedHost reports whether raw resolves to a trusted Namespace domain
// (namespaceapis.com, namespace.so, namespacelabs.dev or any subdomain) or a
// loopback host (localhost, 127.0.0.0/8, ::1).
func IsTrustedHost(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}

	parseValue := value
	if !strings.HasPrefix(strings.ToLower(value), "https://") {
		parseValue = "https://" + value
	}
	u, err := url.Parse(parseValue)
	if err != nil {
		return false
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}

	for _, domain := range []string{"namespaceapis.com", "namespace.so", "namespacelabs.dev"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}

	if host == "localhost" {
		return true
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.WithZone("").Unmap().IsLoopback()
	}

	return false
}
