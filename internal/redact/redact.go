package redact

import (
	"regexp"
	"strings"
)

const (
	tokenKeyPattern       = `(?:token|(?:bearer|id|session|access|refresh|partner|client|auth|tenant|workload|oidc)[_-]?token|(?:nsc[_-]?)?token[_-]?spec(?:[_-]?file)?|trusted[_-]?jwt|jwt)`
	keyFieldKeyPattern    = `(?:(?:api|client|private|signing)[_-]?key(?:[_-]?pem)?)`
	certFieldKeyPattern   = `(?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)`
	secretFieldKeyPattern = `(?:client[_-]?secret|credentials?|secret)`
	passFieldKeyPattern   = `(?:password|passphrase)`
)

var (
	privateKeyRegex            = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	certRegex                  = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*CERTIFICATE-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*CERTIFICATE-----`)
	beginPrivateKeyRegex       = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	beginCertRegex             = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*CERTIFICATE-----`)
	pemBodyLineRegex           = regexp.MustCompile(`^(?:[A-Za-z0-9+/_\-]+={0,2}|(?:Version|Comment|Proc-Type|DEK-Info):.*)$`)
	urlUserinfoRegex           = regexp.MustCompile(`(?i)((?:https?|grpcs?|ftp|wss?|socks5h?)://)([^\s/@?#"'<>]+)@`)
	tokenSpecObjectPrefixRegex = regexp.MustCompile(`(?i)((?:(\\*)(["']))?((?:TF_VAR_)?(?:nsc[_-]?)?token[_-]?spec(?:[_-]?file)?)(?:\\*["'])?[ \t]*([:=])[ \t]*)\{`)
	redactedPlaceholderRegex   = regexp.MustCompile(`^\[REDACTED(?:[ _][A-Z_]+)*\]$`)
	envVarObjectPrefixRegex    = regexp.MustCompile(`(?i)(NSC_TOKEN|NSC_TOKEN_SPEC_FILE|NSC_TOKEN_SPEC|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE|TF_VAR_nsc_[A-Za-z0-9_]+)=\{`)
	envVarRegex                = regexp.MustCompile(`(?i)(NSC_TOKEN|NSC_TOKEN_SPEC_FILE|NSC_TOKEN_SPEC|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE|TF_VAR_nsc_[A-Za-z0-9_]+)=(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s\n,;{}'"\\&]*)?|[^\s\n,;{}'"\\&]+)`)

	mdAuthHeaderRegex           = regexp.MustCompile(`(?i)(authorization:\s*\[)(bearer|basic)(\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\]|[^\]\r\n]+)(\])`)
	authArrayPrefixRegex        = regexp.MustCompile(`(?i)((?:\\*["'])authorization(?:\\*["'])[ \t]*:[ \t]*\[)`)
	authHeaderRegex             = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&\]]*)?|[^\s,;{}'"\\&\]]+)`)
	basicAuthorizationRegex     = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*basic\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\r\n,;{}'"\\&\]]*)?|[^\r\n,;{}'"\\&\]]+)`)
	quotedAuthPrefixRegex       = compileQuotedFieldPrefix(`authorization`, true)
	quotedAuthSchemePrefixRegex = compileQuotedFieldPrefix(`authorization`, false)
	bearerValueRegex            = regexp.MustCompile(`(?i)^(bearer\s+)`)
	basicValueRegex             = regexp.MustCompile(`(?i)^(basic\s+)`)
	bearerTokenRegex            = regexp.MustCompile(`(?i)(bearer\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\]|[a-zA-Z0-9_\-\.+/=]{20,})`)
	nsTokenRegex                = regexp.MustCompile(`(?:nsct_|nscw_|nscu_|nsrt_|tok_|st_)(?:eyJ[a-zA-Z0-9_\-]+={0,2}\.[a-zA-Z0-9_\-]+={0,2}\.[a-zA-Z0-9_\-]+={0,2}|[a-zA-Z0-9_\-\.]+={0,2})`)
	jwtRegex                    = regexp.MustCompile(`(?:[A-Za-z0-9_-]+_)?eyJ[a-zA-Z0-9_\-]+={0,2}\.[a-zA-Z0-9_\-]+={0,2}\.[a-zA-Z0-9_\-]+={0,2}`)
	quotedPassPrefixRegex       = compileQuotedFieldPrefix(passFieldKeyPattern, false)
	unquotedPassRegex           = regexp.MustCompile(`(?i)(` + passFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&]*)?|[^\s,;{}'"\\&]+)`)
	quotedTokenPrefixRegex      = compileQuotedFieldPrefix(tokenKeyPattern, false)
	unquotedTokenFieldRegex     = regexp.MustCompile(`(?i)(` + tokenKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&]*)?|[^\s,;{}'"\\&]+)`)
	quotedKeyPrefixRegex        = compileQuotedFieldPrefix(keyFieldKeyPattern, false)
	unquotedKeyFieldRegex       = regexp.MustCompile(`(?i)(` + keyFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&]*)?|[^\s,;{}'"\\&]+)`)
	quotedCertPrefixRegex       = compileQuotedFieldPrefix(certFieldKeyPattern, false)
	unquotedCertFieldRegex      = regexp.MustCompile(`(?i)(` + certFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&]*)?|[^\s,;{}'"\\&]+)`)
	quotedSecretPrefixRegex     = compileQuotedFieldPrefix(secretFieldKeyPattern, false)
	unquotedSecretFieldRegex    = regexp.MustCompile(`(?i)(` + secretFieldKeyPattern + `[ \t]*[:=][ \t]*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&]*)?|[^\s,;{}'"\\&]+)`)
	quotedUsernamePrefixRegex   = compileQuotedFieldPrefix(`username`, false)
	unquotedUsernameRegex       = regexp.MustCompile(`(?i)(username[ \t]*[:=][ \t]*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}'"\\&]*)?|[^\s,;{}'"\\&]+)`)
)

func compileQuotedFieldPrefix(keyPattern string, requireQuotedKey bool) *regexp.Regexp {
	if requireQuotedKey {
		return regexp.MustCompile(`(?i)((?:\\*["'])(` + keyPattern + `)(?:\\*["'])[ \t]*[:=][ \t]*)(\\*)(["'])`)
	}
	return regexp.MustCompile(`(?i)((?:\\*["'])?(` + keyPattern + `)(?:\\*["'])?[ \t]*[:=][ \t]*)(\\*)(["'])`)
}

// Secrets removes sensitive data (tokens, JWTs, private keys, certificates, passwords,
// URL credentials, and credential-like structured fields) from error strings, logs, and diagnostics.
func Secrets(msg string) string {
	msg = privateKeyRegex.ReplaceAllString(msg, "[REDACTED PRIVATE KEY]")
	msg = certRegex.ReplaceAllString(msg, "[REDACTED CERTIFICATE]")
	msg = redactTruncatedPEM(msg, beginPrivateKeyRegex, "[REDACTED PRIVATE KEY]")
	msg = redactTruncatedPEM(msg, beginCertRegex, "[REDACTED CERTIFICATE]")
	msg = urlUserinfoRegex.ReplaceAllString(msg, "${1}[REDACTED CREDENTIALS]@")
	msg = redactTokenSpecObjects(msg)
	msg = redactEnvVars(msg)
	msg = redactAuthArrays(msg)
	msg = redactMDAuthHeaders(msg)
	msg = replacePrefixWithBoundary(msg, authHeaderRegex, "[REDACTED TOKEN]")
	msg = replacePrefixWithBoundary(msg, basicAuthorizationRegex, "[REDACTED SECRET]")
	msg = redactQuotedFields(msg, quotedAuthPrefixRegex, redactAuthValue)
	msg = redactQuotedFields(msg, quotedAuthSchemePrefixRegex, redactAuthSchemeValue)
	msg = replacePrefixWithBoundary(msg, bearerTokenRegex, "[REDACTED TOKEN]")
	msg = replaceConstantWithBoundary(msg, nsTokenRegex, "[REDACTED TOKEN]")
	msg = replaceConstantWithBoundary(msg, jwtRegex, "[REDACTED JWT]")
	msg = redactQuotedFields(msg, quotedPassPrefixRegex, constantRedaction("[REDACTED PASSWORD]"))
	msg = replacePrefixWithBoundary(msg, unquotedPassRegex, "[REDACTED PASSWORD]")
	msg = redactQuotedFields(msg, quotedTokenPrefixRegex, redactQuotedTokenValue)
	msg = redactUnquotedTokenFields(msg)
	msg = redactQuotedFields(msg, quotedKeyPrefixRegex, constantRedaction("[REDACTED PRIVATE KEY]"))
	msg = replacePrefixWithBoundary(msg, unquotedKeyFieldRegex, "[REDACTED PRIVATE KEY]")
	msg = redactQuotedFields(msg, quotedCertPrefixRegex, constantRedaction("[REDACTED CERTIFICATE]"))
	msg = replacePrefixWithBoundary(msg, unquotedCertFieldRegex, "[REDACTED CERTIFICATE]")
	msg = redactQuotedFields(msg, quotedSecretPrefixRegex, constantRedaction("[REDACTED SECRET]"))
	msg = replacePrefixWithBoundary(msg, unquotedSecretFieldRegex, "[REDACTED SECRET]")
	return msg
}

// CredentialUsernames redacts structured username fields in credential-issuing RPC
// payloads (such as cache credential responses where username is schema-sensitive).
func CredentialUsernames(msg string) string {
	msg = redactQuotedFields(msg, quotedUsernamePrefixRegex, constantRedaction("[REDACTED SECRET]"))
	msg = replacePrefixWithBoundary(msg, unquotedUsernameRegex, "[REDACTED SECRET]")
	return msg
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func precedingBackslashCount(s string, idx int) int {
	count := 0
	for i := idx - 1; i >= 0 && s[i] == '\\'; i-- {
		count++
	}
	return count
}

func hexVal(b byte) (int, bool) {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0'), true
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10, true
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10, true
	default:
		return 0, false
	}
}

func parseHex4(s string) (uint16, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var val uint16
	for i := 0; i < 4; i++ {
		d, ok := hexVal(s[i])
		if !ok {
			return 0, false
		}
		val = (val << 4) | uint16(d)
	}
	return val, true
}

func isJSONEscapeStartAt(s string, idx int) bool {
	if idx < 1 || idx >= len(s) {
		return false
	}
	switch s[idx] {
	case 'n', 'r', 't', 'b', 'f':
		return precedingBackslashCount(s, idx)%2 == 1
	case 'u':
		if idx+4 >= len(s) || precedingBackslashCount(s, idx)%2 != 1 {
			return false
		}
		_, ok := parseHex4(s[idx+1 : idx+5])
		return ok
	default:
		return false
	}
}

func isJSONEscapeEndAt(s string, idx int) bool {
	if idx < 1 || idx >= len(s) {
		return false
	}
	switch s[idx] {
	case 'n', 'r', 't', 'b', 'f':
		if precedingBackslashCount(s, idx)%2 == 1 {
			return true
		}
	}
	if idx >= 5 && s[idx-4] == 'u' && precedingBackslashCount(s, idx-4)%2 == 1 {
		if cp, ok := parseHex4(s[idx-3 : idx+1]); ok {
			if cp < 128 {
				return !isWordByte(byte(cp))
			}
			return true
		}
	}
	return false
}

// isJSONEscapeWordBoundary reports whether idx in s is a valid leading word boundary,
// treating JSON escape sequences (\n, \r, \t, \b, \f, and non-word \uXXXX) as
// boundaries while never treating the escape letter itself as a leading word character.
func isJSONEscapeWordBoundary(s string, idx int) bool {
	if idx < 0 || idx > len(s) {
		return false
	}
	if idx < len(s) && isJSONEscapeStartAt(s, idx) {
		return false
	}
	if idx == 0 {
		return true
	}
	if !isWordByte(s[idx-1]) {
		return true
	}
	return isJSONEscapeEndAt(s, idx-1)
}

func replaceAllWithBoundary(msg string, re *regexp.Regexp, fn func(msg string, pos int, loc []int) string) string {
	var out strings.Builder
	pos := 0
	for pos < len(msg) {
		loc := re.FindStringSubmatchIndex(msg[pos:])
		if loc == nil {
			out.WriteString(msg[pos:])
			break
		}
		matchStart := pos + loc[0]
		matchEnd := pos + loc[1]
		if !isJSONEscapeWordBoundary(msg, matchStart) {
			nextPos := matchStart + 1
			if nextPos <= pos {
				nextPos = pos + 1
			}
			out.WriteString(msg[pos:nextPos])
			pos = nextPos
			continue
		}
		out.WriteString(msg[pos:matchStart])
		out.WriteString(fn(msg, pos, loc))
		if matchEnd <= pos {
			pos++
		} else {
			pos = matchEnd
		}
	}
	return out.String()
}

func replacePrefixWithBoundary(msg string, re *regexp.Regexp, marker string) string {
	return replaceAllWithBoundary(msg, re, func(s string, pos int, loc []int) string {
		if len(loc) < 4 || loc[2] == -1 || loc[3] == -1 {
			return marker
		}
		return s[pos+loc[2]:pos+loc[3]] + marker
	})
}

func replaceConstantWithBoundary(msg string, re *regexp.Regexp, marker string) string {
	return replaceAllWithBoundary(msg, re, func(string, int, []int) string {
		return marker
	})
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

func redactAuthSchemeValue(val string) string {
	if scheme := bearerValueRegex.FindStringSubmatch(val); len(scheme) == 2 {
		return scheme[1] + "[REDACTED TOKEN]"
	}
	if scheme := basicValueRegex.FindStringSubmatch(val); len(scheme) == 2 {
		return scheme[1] + "[REDACTED SECRET]"
	}
	return val
}

func redactQuotedTokenValue(val string) string {
	trimmed := strings.TrimSpace(val)
	if redactedPlaceholderRegex.MatchString(trimmed) {
		return val
	}
	return "[REDACTED TOKEN]"
}

func redactMDAuthHeaders(msg string) string {
	return replaceAllWithBoundary(msg, mdAuthHeaderRegex, func(s string, pos int, loc []int) string {
		if len(loc) < 10 {
			return "[REDACTED TOKEN]"
		}
		p1 := s[pos+loc[2] : pos+loc[3]]
		p2 := s[pos+loc[4] : pos+loc[5]]
		p3 := s[pos+loc[6] : pos+loc[7]]
		p4 := s[pos+loc[8] : pos+loc[9]]
		if strings.EqualFold(p2, "basic") {
			return p1 + p2 + p3 + "[REDACTED SECRET]" + p4
		}
		return p1 + p2 + p3 + "[REDACTED TOKEN]" + p4
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
		keyStart := pos + loc[4]
		if !isJSONEscapeWordBoundary(msg, keyStart) {
			nextPos := keyStart + 1
			if nextPos <= pos {
				nextPos = pos + 1
			}
			out.WriteString(msg[pos:nextPos])
			pos = nextPos
			continue
		}
		valStart := pos + loc[1]
		bsLen := loc[7] - loc[6]
		quote := msg[pos+loc[8]]

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

func redactEnvVars(msg string) string {
	msg = redactEnvVarObjects(msg)
	return replaceAllWithBoundary(msg, envVarRegex, func(s string, pos int, loc []int) string {
		if len(loc) < 4 || loc[2] == -1 || loc[3] == -1 {
			return "[REDACTED]"
		}
		match := s[pos+loc[0] : pos+loc[1]]
		key := s[pos+loc[2] : pos+loc[3]]
		val := strings.TrimPrefix(match, key+"=")
		if redactedPlaceholderRegex.MatchString(val) {
			return match
		}
		return key + "=[REDACTED]"
	})
}

func redactEnvVarObjects(msg string) string {
	var out strings.Builder
	pos := 0
	for pos < len(msg) {
		loc := envVarObjectPrefixRegex.FindStringSubmatchIndex(msg[pos:])
		if loc == nil {
			out.WriteString(msg[pos:])
			break
		}
		keyStart := pos + loc[2]
		if !isJSONEscapeWordBoundary(msg, keyStart) {
			nextPos := keyStart + 1
			if nextPos <= pos {
				nextPos = pos + 1
			}
			out.WriteString(msg[pos:nextPos])
			pos = nextPos
			continue
		}
		keyEnd := pos + loc[3]
		braceIdx := pos + loc[1] - 1

		objEnd, ok := scanBalancedJSONObjectEnd(msg, braceIdx, 0)
		if !ok {
			out.WriteString(msg[pos : braceIdx+1])
			pos = braceIdx + 1
			continue
		}

		out.WriteString(msg[pos:keyStart])
		out.WriteString(msg[keyStart:keyEnd])
		out.WriteString("=[REDACTED]")
		pos = objEnd
	}
	return out.String()
}

func redactTokenSpecObjects(msg string) string {
	var out strings.Builder
	pos := 0
	for pos < len(msg) {
		loc := tokenSpecObjectPrefixRegex.FindStringSubmatchIndex(msg[pos:])
		if loc == nil {
			out.WriteString(msg[pos:])
			break
		}
		keyStart := pos + loc[8]
		if !isJSONEscapeWordBoundary(msg, keyStart) {
			nextPos := keyStart + 1
			if nextPos <= pos {
				nextPos = pos + 1
			}
			out.WriteString(msg[pos:nextPos])
			pos = nextPos
			continue
		}
		prefixStart := pos + loc[2]
		prefixEnd := pos + loc[3]
		braceIdx := pos + loc[1] - 1

		bsLen := 0
		bs := ""
		if loc[4] != -1 && loc[5] != -1 {
			bsLen = loc[5] - loc[4]
			bs = msg[pos+loc[4] : pos+loc[5]]
		}
		quote := ""
		if loc[6] != -1 && loc[7] != -1 {
			quote = msg[pos+loc[6] : pos+loc[7]]
		}
		sep := msg[pos+loc[10] : pos+loc[11]]

		objEnd, ok := scanBalancedJSONObjectEnd(msg, braceIdx, bsLen)
		if !ok {
			out.WriteString(msg[pos : braceIdx+1])
			pos = braceIdx + 1
			continue
		}

		out.WriteString(msg[pos:prefixStart])
		out.WriteString(msg[prefixStart:prefixEnd])
		if sep == "=" {
			out.WriteString("[REDACTED]")
		} else {
			q := quote
			if q == "" {
				q = `"`
			}
			out.WriteString(bs + q + "[REDACTED TOKEN]" + bs + q)
		}
		pos = objEnd
	}
	return out.String()
}

// scanBalancedJSONObjectEnd scans forward from braceIdx ('{') on the same line to
// find the matching '}' while respecting JSON string literals and escapes.
func scanBalancedJSONObjectEnd(s string, braceIdx int, bsLen int) (int, bool) {
	if braceIdx >= len(s) || s[braceIdx] != '{' {
		return 0, false
	}
	if bsLen == 0 {
		k := braceIdx + 1
		for k < len(s) {
			if s[k] == ' ' || s[k] == '\t' {
				k++
				continue
			}
			if s[k] == '\\' {
				bStart := k
				for k < len(s) && s[k] == '\\' {
					k++
				}
				if k < len(s) && (s[k] == 'n' || s[k] == 'r' || s[k] == 't' || s[k] == 'b' || s[k] == 'f') {
					k++
					continue
				}
				if k < len(s) && (s[k] == '"' || s[k] == '\'') {
					bsLen = k - bStart
				}
				break
			}
			break
		}
	}
	step := 2 * (bsLen + 1)
	depth := 1
	var strQuote byte
	for i := braceIdx + 1; i < len(s); {
		ch := s[i]
		if ch == '\r' || ch == '\n' {
			return 0, false
		}
		if ch == '\\' {
			bStart := i
			for i < len(s) && s[i] == '\\' {
				i++
			}
			bCount := i - bStart
			if i < len(s) && (s[i] == '"' || s[i] == '\'') {
				q := s[i]
				if strQuote != 0 && bCount < bsLen && q == strQuote {
					return 0, false
				}
				if bCount >= bsLen && (bCount-bsLen)%step == 0 {
					switch strQuote {
					case 0:
						strQuote = q
					case q:
						strQuote = 0
					}
				}
				i++
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			if bsLen == 0 {
				switch strQuote {
				case 0:
					strQuote = ch
				case ch:
					strQuote = 0
				}
			} else if strQuote == ch || (strQuote == 0 && ch == '"') {
				return 0, false
			}
			i++
			continue
		}
		if strQuote == 0 {
			switch ch {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		i++
	}
	return 0, false
}

func redactUnquotedTokenFields(msg string) string {
	return replaceAllWithBoundary(msg, unquotedTokenFieldRegex, func(s string, pos int, loc []int) string {
		if len(loc) < 4 || loc[2] == -1 || loc[3] == -1 {
			return "[REDACTED TOKEN]"
		}
		match := s[pos+loc[0] : pos+loc[1]]
		prefix := s[pos+loc[2] : pos+loc[3]]
		val := strings.TrimSpace(strings.TrimPrefix(match, prefix))
		if redactedPlaceholderRegex.MatchString(val) {
			return match
		}
		// A diagnostic such as "JWT: Bearer <token>" uses JWT as a label and
		// Bearer as an authentication scheme, not as the secret field value.
		// The bearer-token pass has already redacted the actual credential.
		if strings.EqualFold(val, "bearer") {
			return match
		}
		return prefix + "[REDACTED TOKEN]"
	})
}
