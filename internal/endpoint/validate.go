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
	ipv4LinkLocalPrefix       = netip.MustParsePrefix("169.254.0.0/16")
	ipv6LinkLocalPrefix       = netip.MustParsePrefix("fe80::/10")
	awsIPv6MetadataAddr       = netip.MustParseAddr("fd00:ec2::254")
	alibabaIPv4MetadataAddr   = netip.MustParseAddr("100.100.100.200")
	ipv4CompatibleIPv6Prefix  = netip.MustParsePrefix("::/96")
	nat64WellKnownPrefix      = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalUsePrefix       = netip.MustParsePrefix("64:ff9b:1::/48")
	reservedGRPCResolverNames = map[string]struct{}{
		"unix":          {},
		"unix-abstract": {},
		"passthrough":   {},
		"dns":           {},
		"xds":           {},
		"google-c2p":    {},
	}
	blockedMetadataHostnames = map[string]struct{}{
		"metadata":                   {},
		"metadata.google.internal":   {},
		"metadata.goog":              {},
		"instance-data":              {},
		"instance-data.ec2.internal": {},
		"metadata.azure.internal":    {},
		"metadata.nic.internal":      {},
	}
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
			// Do not wrap *url.Error directly because it embeds the raw input URL,
			// which could contain userinfo credentials in diagnostics.
			return fmt.Errorf("endpoint must be a valid HTTPS URL")
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
	if u.RawQuery != "" || u.ForceQuery {
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

// ValidateHost checks whether a host (IP literal or hostname) is permitted as an
// endpoint or proxy target, rejecting unspecified/link-local/cloud-metadata IPs,
// cloud metadata hostnames, non-canonical numeric IPv4 literals, and reserved gRPC
// resolver scheme names.
func ValidateHost(host string) error {
	return validateHost(host)
}

func validateHost(host string) error {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return fmt.Errorf("endpoint host must not be empty")
	}

	if addr, err := netip.ParseAddr(trimmed); err == nil {
		return validateIPWithLabel(addr, host)
	}

	normalized := strings.ToLower(strings.TrimSuffix(trimmed, "."))
	if _, reserved := reservedGRPCResolverNames[normalized]; reserved {
		return fmt.Errorf("endpoint host %q must not be a reserved gRPC resolver scheme name", host)
	}
	if isBlockedMetadataHostname(normalized) {
		return fmt.Errorf("endpoint host %q must not target a cloud metadata hostname", host)
	}
	if isNonCanonicalNumericIPv4(normalized) {
		return fmt.Errorf("endpoint host %q must not use a non-canonical numeric IP address", host)
	}
	return nil
}

// ValidateIP checks whether a resolved or literal IP address is permitted as a
// dial target, rejecting unspecified, link-local, cloud metadata, and IPv6-embedded
// (IPv4-mapped, IPv4-compatible, NAT64) forms of those blocked addresses.
func ValidateIP(addr netip.Addr) error {
	return validateIPWithLabel(addr, addr.String())
}

func validateIPWithLabel(addr netip.Addr, label string) error {
	if !addr.IsValid() {
		return fmt.Errorf("endpoint host %q is not a valid IP address", label)
	}

	addr = addr.WithZone("").Unmap()
	if addr.IsUnspecified() {
		return fmt.Errorf("endpoint host %q must not be an unspecified IP address", label)
	}
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		ipv4LinkLocalPrefix.Contains(addr) || ipv6LinkLocalPrefix.Contains(addr) {
		return fmt.Errorf("endpoint host %q must not be a link-local or metadata IP address", label)
	}
	if addr == awsIPv6MetadataAddr || addr == alibabaIPv4MetadataAddr {
		return fmt.Errorf("endpoint host %q must not be a cloud metadata IP address", label)
	}

	if addr.Is6() && !addr.IsLoopback() {
		if ipv4CompatibleIPv6Prefix.Contains(addr) || nat64WellKnownPrefix.Contains(addr) || nat64LocalUsePrefix.Contains(addr) {
			b := addr.As16()
			embeddedV4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
			if err := validateIPWithLabel(embeddedV4, label); err != nil {
				return err
			}
		}
	}
	return nil
}

func isBlockedMetadataHostname(normalized string) bool {
	if _, blocked := blockedMetadataHostnames[normalized]; blocked {
		return true
	}
	for blocked := range blockedMetadataHostnames {
		if strings.HasSuffix(normalized, "."+blocked) {
			return true
		}
	}
	return false
}

// isNonCanonicalNumericIPv4 detects integer, hex, octal, or shorthand dotted
// numeric hostnames (such as 2852039166, 0xa9fea9fe, 0251.0376.0251.0376, or
// 169.254.43518) that netip.ParseAddr rejects but libc getaddrinfo may accept.
func isNonCanonicalNumericIPv4(normalized string) bool {
	if normalized == "" {
		return false
	}
	parts := strings.Split(normalized, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 0, 64); err != nil {
			return false
		}
	}
	return true
}

// IsTrustedHost reports whether raw resolves to a trusted Namespace domain
// (namespaceapis.com, namespaceapi.com, namespace.so, namespacelabs.dev or any subdomain) or a
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

	for _, domain := range []string{"namespaceapis.com", "namespaceapi.com", "namespace.so", "namespacelabs.dev"} {
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
