package redact

import (
	"regexp"
	"strings"
)

var (
	privateKeyRegex          = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*PRIVATE KEY-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*PRIVATE KEY-----`)
	certRegex                = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*CERTIFICATE-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*CERTIFICATE-----`)
	envVarRegex              = regexp.MustCompile(`(?i)\b(NSC_TOKEN|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE)=([^\s\n]+)`)
	authHeaderRegex          = regexp.MustCompile(`(?i)\b(authorization:\s*bearer\s+)(?:\[REDACTED TOKEN\]|[^\s,;{}]+)`)
	basicAuthorizationRegex  = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*basic\s+)(?:\[REDACTED SECRET\]|[^\r\n,;{}]+)`)
	quotedAuthorizationRegex = regexp.MustCompile(`(?i)((?:\\?["'])authorization(?:\\?["'])\s*[:=]\s*(?:\\?["']))([^"'\r\n]*?)((?:\\?["']))`)
	bearerValueRegex         = regexp.MustCompile(`(?i)^(bearer\s+)`)
	basicValueRegex          = regexp.MustCompile(`(?i)^(basic\s+)`)
	bearerTokenRegex         = regexp.MustCompile(`(?i)\b(bearer\s+)(?:\[REDACTED TOKEN\]|[a-zA-Z0-9_\-\.]{20,})`)
	nsTokenRegex             = regexp.MustCompile(`\b(?:nsct_|nscw_|nscu_|tok_|st_)[a-zA-Z0-9_\-\.]+`)
	jwtRegex                 = regexp.MustCompile(`\beyJ[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+`)
	quotedPassRegex          = regexp.MustCompile(`(?i)((?:\\?["'])?(?:password|passphrase)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED PASSWORD\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedPassRegex        = regexp.MustCompile(`(?i)\b((?:password|passphrase)[ \t]*[:=][ \t]*)(?:\[REDACTED PASSWORD\]|[^\s,;{}'"]+)`)
	quotedTokenFieldRegex    = regexp.MustCompile(`(?i)((?:\\?["'])?(?:token|bearer[_-]?token|id[_-]?token|session[_-]?token|access[_-]?token|refresh[_-]?token|partner[_-]?token|client[_-]?token|auth[_-]?token|trusted[_-]?jwt|jwt)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED (?:TOKEN|JWT)\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedTokenFieldRegex  = regexp.MustCompile(`(?i)\b((?:token|bearer[_-]?token|id[_-]?token|session[_-]?token|access[_-]?token|refresh[_-]?token|partner[_-]?token|client[_-]?token|auth[_-]?token|trusted[_-]?jwt|jwt)[ \t]*[:=][ \t]*)(?:\[REDACTED (?:TOKEN|JWT)\]|[^\s,;{}'"&]+)`)
	quotedKeyFieldRegex      = regexp.MustCompile(`(?i)((?:\\?["'])?(?:(?:api|client|private|signing)[_-]?key(?:[_-]?pem)?)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED PRIVATE KEY\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedKeyFieldRegex    = regexp.MustCompile(`(?i)\b((?:(?:api|client|private|signing)[_-]?key(?:[_-]?pem)?)[ \t]*[:=][ \t]*)(?:\[REDACTED PRIVATE KEY\]|[^\s,;{}'"&]+)`)
	quotedCertFieldRegex     = regexp.MustCompile(`(?i)((?:\\?["'])?(?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED CERTIFICATE\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedCertFieldRegex   = regexp.MustCompile(`(?i)\b((?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)[ \t]*[:=][ \t]*)(?:\[REDACTED CERTIFICATE\]|[^\s,;{}'"&]+)`)
	quotedSecretFieldRegex   = regexp.MustCompile(`(?i)((?:\\?["'])?(?:client[_-]?secret|credentials?|secret)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED SECRET\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedSecretFieldRegex = regexp.MustCompile(`(?i)\b((?:client[_-]?secret|credentials?|secret)[ \t]*[:=][ \t]*)(?:\[REDACTED SECRET\]|[^\s,;{}'"&]+)`)
)

// Secrets removes sensitive data (tokens, JWTs, private keys, certificates, passwords,
// and credential-like structured fields) from error strings, logs, and diagnostics.
func Secrets(msg string) string {
	msg = privateKeyRegex.ReplaceAllString(msg, "[REDACTED PRIVATE KEY]")
	msg = certRegex.ReplaceAllString(msg, "[REDACTED CERTIFICATE]")
	msg = envVarRegex.ReplaceAllString(msg, "${1}=[REDACTED]")
	msg = authHeaderRegex.ReplaceAllString(msg, "${1}[REDACTED TOKEN]")
	msg = basicAuthorizationRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	msg = quotedAuthorizationRegex.ReplaceAllStringFunc(msg, redactQuotedAuthorization)
	msg = bearerTokenRegex.ReplaceAllString(msg, "${1}[REDACTED TOKEN]")
	msg = nsTokenRegex.ReplaceAllString(msg, "[REDACTED TOKEN]")
	msg = jwtRegex.ReplaceAllString(msg, "[REDACTED JWT]")
	msg = quotedPassRegex.ReplaceAllString(msg, "${1}[REDACTED PASSWORD]${2}")
	msg = unquotedPassRegex.ReplaceAllString(msg, "${1}[REDACTED PASSWORD]")
	msg = redactTokenFields(msg, quotedTokenFieldRegex, "${1}[REDACTED TOKEN]${2}")
	msg = redactTokenFields(msg, unquotedTokenFieldRegex, "${1}[REDACTED TOKEN]")
	msg = quotedKeyFieldRegex.ReplaceAllString(msg, "${1}[REDACTED PRIVATE KEY]${2}")
	msg = unquotedKeyFieldRegex.ReplaceAllString(msg, "${1}[REDACTED PRIVATE KEY]")
	msg = quotedCertFieldRegex.ReplaceAllString(msg, "${1}[REDACTED CERTIFICATE]${2}")
	msg = unquotedCertFieldRegex.ReplaceAllString(msg, "${1}[REDACTED CERTIFICATE]")
	msg = quotedSecretFieldRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]${2}")
	msg = unquotedSecretFieldRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	return msg
}

func redactQuotedAuthorization(match string) string {
	parts := quotedAuthorizationRegex.FindStringSubmatch(match)
	if len(parts) != 4 {
		return "[REDACTED TOKEN]"
	}

	if scheme := bearerValueRegex.FindStringSubmatch(parts[2]); len(scheme) == 2 {
		return parts[1] + scheme[1] + "[REDACTED TOKEN]" + parts[3]
	}
	if scheme := basicValueRegex.FindStringSubmatch(parts[2]); len(scheme) == 2 {
		return parts[1] + scheme[1] + "[REDACTED SECRET]" + parts[3]
	}

	return parts[1] + "[REDACTED TOKEN]" + parts[3]
}

func redactTokenFields(msg string, fieldRegex *regexp.Regexp, replacement string) string {
	return fieldRegex.ReplaceAllStringFunc(msg, func(match string) string {
		if strings.Contains(match, "[REDACTED JWT]") {
			return match
		}
		// A diagnostic such as "JWT: Bearer <token>" uses JWT as a label and
		// Bearer as an authentication scheme, not as the secret field value.
		// The bearer-token pass has already redacted the actual credential.
		if fieldRegex == unquotedTokenFieldRegex {
			parts := fieldRegex.FindStringSubmatch(match)
			if len(parts) > 1 && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(match, parts[1])), "bearer") {
				return match
			}
		}
		return fieldRegex.ReplaceAllString(match, replacement)
	})
}
