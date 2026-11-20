package redact

import (
	"regexp"
	"strings"
)

const (
	tokenKeyPattern       = `(?:token|(?:bearer|id|session|access|refresh|partner|client|auth|tenant|workload|oidc)[_-]?token|trusted[_-]?jwt|jwt)`
	keyFieldKeyPattern    = `(?:(?:api|client|private|signing)[_-]?key(?:[_-]?pem)?)`
	certFieldKeyPattern   = `(?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)`
	secretFieldKeyPattern = `(?:client[_-]?secret|credentials?|secret)`
	passFieldKeyPattern   = `(?:password|passphrase)`
)

var (
	privateKeyRegex      = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	certRegex            = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*CERTIFICATE-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*CERTIFICATE-----`)
	beginPrivateKeyRegex = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	beginCertRegex       = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*CERTIFICATE-----`)
	pemBodyLineRegex     = regexp.MustCompile(`^(?:[A-Za-z0-9+/_\-]+={0,2}|(?:Version|Comment|Proc-Type|DEK-Info):.*)$`)
	urlUserinfoRegex     = regexp.MustCompile(`(?i)((?:https?|grpcs?|ftp|wss?|socks5h?)://)([^\s/@?#"'<>]+)@`)
	envVarRegex          = regexp.MustCompile(`(?i)\b(NSC_TOKEN|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE|TF_VAR_nsc_[A-Za-z0-9_]+)=(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\s\n,;{}'"]+)`)

	mdAuthHeaderRegex         = regexp.MustCompile(`(?i)(\bauthorization:\s*\[)(bearer|basic)(\s+)(?:\[REDACTED (?:TOKEN|SECRET)\]|[^\]\r\n]+)(\])`)
	authArrayPrefixRegex      = regexp.MustCompile(`(?i)((?:\\*["'])authorization(?:\\*["'])[ \t]*:[ \t]*\[)`)
	authHeaderRegex           = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*bearer\s+)(?:\[REDACTED TOKEN\]|[^\s,;{}\]]+)`)
	basicAuthorizationRegex   = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*basic\s+)(?:\[REDACTED SECRET\]|[^\r\n,;{}\]]+)`)
	quotedAuthPrefixRegex     = compileQuotedFieldPrefix(`authorization`, true)
	bearerValueRegex          = regexp.MustCompile(`(?i)^(bearer\s+)`)
	basicValueRegex           = regexp.MustCompile(`(?i)^(basic\s+)`)
	bearerTokenRegex          = regexp.MustCompile(`(?i)\b(bearer\s+)(?:\[REDACTED TOKEN\]|[a-zA-Z0-9_\-\.+/=]{20,})`)
	nsTokenRegex              = regexp.MustCompile(`\b(?:nsct_|nscw_|nscu_|tok_|st_)[a-zA-Z0-9_\-\.]+`)
	jwtRegex                  = regexp.MustCompile(`\beyJ[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+`)
	quotedPassPrefixRegex     = compileQuotedFieldPrefix(passFieldKeyPattern, false)
	unquotedPassRegex         = regexp.MustCompile(`(?i)\b(` + passFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED PASSWORD\]|[^\s,;{}'"]+)`)
	quotedTokenPrefixRegex    = compileQuotedFieldPrefix(tokenKeyPattern, false)
	unquotedTokenFieldRegex   = regexp.MustCompile(`(?i)\b(` + tokenKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED (?:TOKEN|JWT)\]|[^\s,;{}'"&]+)`)
	quotedKeyPrefixRegex      = compileQuotedFieldPrefix(keyFieldKeyPattern, false)
	unquotedKeyFieldRegex     = regexp.MustCompile(`(?i)\b(` + keyFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED PRIVATE KEY\]|[^\s,;{}'"&]+)`)
	quotedCertPrefixRegex     = compileQuotedFieldPrefix(certFieldKeyPattern, false)
	unquotedCertFieldRegex    = regexp.MustCompile(`(?i)\b(` + certFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED CERTIFICATE\]|[^\s,;{}'"&]+)`)
	quotedSecretPrefixRegex   = compileQuotedFieldPrefix(secretFieldKeyPattern, false)
	unquotedSecretFieldRegex  = regexp.MustCompile(`(?i)\b(` + secretFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED SECRET\]|[^\s,;{}'"&]+)`)
	quotedUsernamePrefixRegex = compileQuotedFieldPrefix(`username`, false)
	unquotedUsernameRegex     = regexp.MustCompile(`(?i)\b(username[ \t]*[:=][ \t]*)(?:\[REDACTED SECRET\]|[^\s,;{}'"&]+)`)
)

func compileQuotedFieldPrefix(keyPattern string, requireQuotedKey bool) *regexp.Regexp {
	if requireQuotedKey {
		return regexp.MustCompile(`(?i)((?:\\*["'])` + keyPattern + `(?:\\*["'])[ \t]*[:=][ \t]*)(\\*)(["'])`)
	}
	return regexp.MustCompile(`(?i)((?:\\*["'])?(?:` + keyPattern + `)(?:\\*["'])?[ \t]*[:=][ \t]*)(\\*)(["'])`)
}

// Secrets removes sensitive data (tokens, JWTs, private keys, certificates, passwords,
// URL credentials, and credential-like structured fields) from error strings, logs, and diagnostics.
func Secrets(msg string) string {
	msg = privateKeyRegex.ReplaceAllString(msg, "[REDACTED PRIVATE KEY]")
	msg = certRegex.ReplaceAllString(msg, "[REDACTED CERTIFICATE]")
	msg = redactTruncatedPEM(msg, beginPrivateKeyRegex, "[REDACTED PRIVATE KEY]")
	msg = redactTruncatedPEM(msg, beginCertRegex, "[REDACTED CERTIFICATE]")
	msg = urlUserinfoRegex.ReplaceAllString(msg, "${1}[REDACTED CREDENTIALS]@")
	msg = envVarRegex.ReplaceAllString(msg, "${1}=[REDACTED]")
	msg = redactAuthArrays(msg)
	msg = redactMDAuthHeaders(msg)
	msg = authHeaderRegex.ReplaceAllString(msg, "${1}[REDACTED TOKEN]")
	msg = basicAuthorizationRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	msg = redactQuotedFields(msg, quotedAuthPrefixRegex, redactAuthValue)
	msg = bearerTokenRegex.ReplaceAllString(msg, "${1}[REDACTED TOKEN]")
	msg = nsTokenRegex.ReplaceAllString(msg, "[REDACTED TOKEN]")
	msg = jwtRegex.ReplaceAllString(msg, "[REDACTED JWT]")
	msg = redactQuotedFields(msg, quotedPassPrefixRegex, constantRedaction("[REDACTED PASSWORD]"))
	msg = unquotedPassRegex.ReplaceAllString(msg, "${1}[REDACTED PASSWORD]")
	msg = redactQuotedFields(msg, quotedTokenPrefixRegex, redactQuotedTokenValue)
	msg = redactUnquotedTokenFields(msg)
	msg = redactQuotedFields(msg, quotedKeyPrefixRegex, constantRedaction("[REDACTED PRIVATE KEY]"))
	msg = unquotedKeyFieldRegex.ReplaceAllString(msg, "${1}[REDACTED PRIVATE KEY]")
	msg = redactQuotedFields(msg, quotedCertPrefixRegex, constantRedaction("[REDACTED CERTIFICATE]"))
	msg = unquotedCertFieldRegex.ReplaceAllString(msg, "${1}[REDACTED CERTIFICATE]")
	msg = redactQuotedFields(msg, quotedSecretPrefixRegex, constantRedaction("[REDACTED SECRET]"))
	msg = unquotedSecretFieldRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	return msg
}

// CredentialUsernames redacts structured username fields in credential-issuing RPC
// payloads (such as cache credential responses where username is schema-sensitive).
func CredentialUsernames(msg string) string {
	msg = redactQuotedFields(msg, quotedUsernamePrefixRegex, constantRedaction("[REDACTED SECRET]"))
	msg = unquotedUsernameRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	return msg
}

func redactTruncatedPEM(msg string, beginRe *regexp.Regexp, marker string) string {
	var out strings.Builder
	pos := 0
	for pos < len(msg) {
		loc := beginRe.FindStringIndex(msg[pos:])
		if loc == nil {
			out.WriteString(msg[pos:])
			break
		}
		matchStart := pos + loc[0]
		end := pos + loc[1]
		out.WriteString(msg[pos:matchStart])

		// Consume any trailing characters on the BEGIN line itself (stopping at quote or newline).
		for end < len(msg) && msg[end] != '\r' && msg[end] != '\n' && msg[end] != '"' && msg[end] != '\'' {
			if end+1 < len(msg) && msg[end] == '\\' && (msg[end+1] == 'n' || msg[end+1] == 'r') {
				break
			}
			end++
		}

		// Consume subsequent whole PEM body lines (base64/armor headers) without eating
		// unrelated lines such as KEY=value assignments or surrounding JSON framing.
		for end < len(msg) {
			sepLen := 0
			switch {
			case msg[end] == '\r' && end+1 < len(msg) && msg[end+1] == '\n':
				sepLen = 2
			case msg[end] == '\n':
				sepLen = 1
			case end+3 < len(msg) && msg[end:end+4] == `\r\n`:
				sepLen = 4
			case end+1 < len(msg) && msg[end:end+2] == `\n`:
				sepLen = 2
			}
			if sepLen == 0 {
				break
			}
			lineStart := end + sepLen
			lineEnd := lineStart
			for lineEnd < len(msg) && msg[lineEnd] != '\r' && msg[lineEnd] != '\n' && msg[lineEnd] != '"' && msg[lineEnd] != '\'' {
				if lineEnd+1 < len(msg) && msg[lineEnd] == '\\' && (msg[lineEnd+1] == 'n' || msg[lineEnd+1] == 'r') {
					break
				}
				lineEnd++
			}
			trimmed := strings.TrimSpace(msg[lineStart:lineEnd])
			if trimmed == "" || !pemBodyLineRegex.MatchString(trimmed) {
				break
			}
			end = lineEnd
		}

		out.WriteString(marker)
		pos = end
	}
	return out.String()
}

func constantRedaction(marker string) func(string) string {
	return func(string) string {
		return marker
	}
}

func redactAuthValue(val string) string {
	if scheme := bearerValueRegex.FindStringSubmatch(val); len(scheme) == 2 {
		return scheme[1] + "[REDACTED TOKEN]"
	}
	if scheme := basicValueRegex.FindStringSubmatch(val); len(scheme) == 2 {
		return scheme[1] + "[REDACTED SECRET]"
	}
	return "[REDACTED TOKEN]"
}

func redactQuotedTokenValue(val string) string {
	if strings.TrimSpace(val) == "[REDACTED JWT]" {
		return val
	}
	return "[REDACTED TOKEN]"
}

func redactMDAuthHeaders(msg string) string {
	return mdAuthHeaderRegex.ReplaceAllStringFunc(msg, func(match string) string {
		parts := mdAuthHeaderRegex.FindStringSubmatch(match)
		if len(parts) != 5 {
			return "[REDACTED TOKEN]"
		}
		if strings.EqualFold(parts[2], "basic") {
			return parts[1] + parts[2] + parts[3] + "[REDACTED SECRET]" + parts[4]
		}
		return parts[1] + parts[2] + parts[3] + "[REDACTED TOKEN]" + parts[4]
	})
}

func redactAuthArrays(msg string) string {
	var out strings.Builder
	pos := 0
	for pos < len(msg) {
		loc := authArrayPrefixRegex.FindStringIndex(msg[pos:])
		if loc == nil {
			out.WriteString(msg[pos:])
			break
		}
		start := pos + loc[0]
		arrContentStart := pos + loc[1]
		out.WriteString(msg[pos:arrContentStart])

		i := arrContentStart
		for i < len(msg) {
			if msg[i] == ']' || msg[i] == '\r' || msg[i] == '\n' {
				out.WriteByte(msg[i])
				i++
				break
			}
			// Look for an opening quote (possibly backslash-escaped for JSON-in-JSON).
			if msg[i] == '\\' || msg[i] == '"' || msg[i] == '\'' {
				bsStart := i
				for i < len(msg) && msg[i] == '\\' {
					i++
				}
				if i < len(msg) && (msg[i] == '"' || msg[i] == '\'') {
					bsLen := i - bsStart
					quote := msg[i]
					valStart := i + 1
					valEnd, closeEnd, ok := scanQuotedValueEnd(msg, valStart, bsLen, quote)
					if ok {
						out.WriteString(msg[bsStart:valStart])
						out.WriteString(redactAuthValue(msg[valStart:valEnd]))
						out.WriteString(msg[valEnd:closeEnd])
						i = closeEnd
						continue
					}
					out.WriteString(msg[bsStart : i+1])
					i++
					continue
				}
				out.WriteString(msg[bsStart:i])
				continue
			}
			out.WriteByte(msg[i])
			i++
		}
		pos = i
		if pos <= start {
			break
		}
	}
	return out.String()
}

// redactQuotedFields scans msg for structured key/value pairs whose value starts with
// a raw or JSON-escaped quote, finds the matching closing quote at the same escaping
// depth, and transforms the full value without truncating at embedded quotes.
func redactQuotedFields(msg string, prefixRegex *regexp.Regexp, transform func(string) string) string {
	var out strings.Builder
	pos := 0
	for pos < len(msg) {
		loc := prefixRegex.FindStringSubmatchIndex(msg[pos:])
		if loc == nil {
			out.WriteString(msg[pos:])
			break
		}
		matchStart := pos + loc[0]
		valStart := pos + loc[1]
		bsLen := loc[5] - loc[4]
		quote := msg[pos+loc[6]]

		valEnd, closeEnd, ok := scanQuotedValueEnd(msg, valStart, bsLen, quote)
		if !ok {
			out.WriteString(msg[pos:valStart])
			pos = valStart
			continue
		}

		out.WriteString(msg[pos:matchStart])
		out.WriteString(msg[matchStart:valStart])
		out.WriteString(transform(msg[valStart:valEnd]))
		out.WriteString(msg[valEnd:closeEnd])
		pos = closeEnd
	}
	return out.String()
}

// scanQuotedValueEnd scans forward from start to find the matching closing quote
// delimiter matching bsLen preceding backslashes and quote character.
func scanQuotedValueEnd(s string, start int, bsLen int, quote byte) (int, int, bool) {
	step := 2 * (bsLen + 1)
	for i := start; i < len(s); {
		ch := s[i]
		if ch == '\r' || ch == '\n' {
			return 0, 0, false
		}
		if ch == '\\' {
			bStart := i
			for i < len(s) && s[i] == '\\' {
				i++
			}
			bCount := i - bStart
			if i < len(s) && s[i] == quote {
				if bCount < bsLen {
					// Reached an outer closing quote before the inner escaped string closed.
					return bStart, bStart, true
				}
				if (bCount-bsLen)%step == 0 {
					return i - bsLen, i + 1, true
				}
				// Escaped literal quote inside the value; skip past it and keep scanning.
				i++
			}
			continue
		}
		if ch == quote {
			if bsLen == 0 {
				return i, i + 1, true
			}
			// Reached an outer unescaped quote before an escaped inner quote closed.
			return i, i, true
		}
		i++
	}
	return 0, 0, false
}

func redactUnquotedTokenFields(msg string) string {
	return unquotedTokenFieldRegex.ReplaceAllStringFunc(msg, func(match string) string {
		parts := unquotedTokenFieldRegex.FindStringSubmatch(match)
		if len(parts) < 2 {
			return "[REDACTED TOKEN]"
		}
		val := strings.TrimSpace(strings.TrimPrefix(match, parts[1]))
		if val == "[REDACTED JWT]" {
			return match
		}
		// A diagnostic such as "JWT: Bearer <token>" uses JWT as a label and
		// Bearer as an authentication scheme, not as the secret field value.
		// The bearer-token pass has already redacted the actual credential.
		if strings.EqualFold(val, "bearer") {
			return match
		}
		return parts[1] + "[REDACTED TOKEN]"
	})
}
