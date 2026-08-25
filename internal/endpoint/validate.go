package endpoint

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Validate accepts an HTTPS URL or bare host[:port] and rejects endpoint
// components that could alter routing or carry credentials.
func Validate(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fmt.Errorf("endpoint must not be empty")
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
