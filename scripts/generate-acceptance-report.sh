#!/usr/bin/env bash
# ==============================================================================
# Namespace Terraform Provider — Acceptance Test Report Generator
# Parses acceptance test execution output (JSON or verbose log), sanitizes sensitive
# tokens and credentials, produces JUnit XML, markdown summary reports with
# per-RPC timings and commit SHA / workflow run link metadata, and writes step summary.
# Quality gates are evaluated before markdown rendering to guarantee synchronized status.
# ==============================================================================

set -euo pipefail

INPUT_FILE=""
OUTPUT_DIR="test-reports"
COMMIT_SHA="${GITHUB_SHA:-$(git rev-parse --verify HEAD 2>/dev/null || echo "unknown")}"
WORKFLOW_RUN_LINK="${GITHUB_RUN_LINK:-}"
RUN_ID="${GITHUB_RUN_ID:-unknown}"
REPO_NAME="${GITHUB_REPOSITORY:-bsuv/terraform-provider-namespace}"
TF_VERSION="${TERRAFORM_VERSION:-latest}"
SUITE="full"

if [ -z "$WORKFLOW_RUN_LINK" ] && [ "$RUN_ID" != "unknown" ]; then
  WORKFLOW_RUN_LINK="https://github.com/${REPO_NAME}/actions/runs/${RUN_ID}"
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --input|-i)
      INPUT_FILE="$2"
      shift 2
      ;;
    --output-dir|-o)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --commit-sha|-c)
      COMMIT_SHA="$2"
      shift 2
      ;;
    --run-link|-r)
      WORKFLOW_RUN_LINK="$2"
      shift 2
      ;;
    --run-id)
      RUN_ID="$2"
      shift 2
      ;;
    --tf-version)
      TF_VERSION="$2"
      shift 2
      ;;
    --suite)
      SUITE="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 --input <test-log-or-json> [--output-dir <dir>] [--commit-sha <sha>] [--run-link <url>] [--tf-version <ver>] [--suite <full|matrix|singleton>]"
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

if [ -z "$INPUT_FILE" ]; then
  echo "Error: --input <file> is required" >&2
  exit 1
fi

case "$SUITE" in
  full|matrix|singleton) ;;
  *)
    echo "Error: --suite must be one of full, matrix, or singleton" >&2
    exit 1
    ;;
esac

if [ "$INPUT_FILE" != "-" ] && [ "$INPUT_FILE" != "/dev/stdin" ] && [ ! -r "$INPUT_FILE" ]; then
  echo "Error: Input file '$INPUT_FILE' does not exist or is not readable" >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"

# The embedded Python program is supplied through a heredoc, so preserve piped test
# output in a temporary file before Python consumes stdin for its source code.
STDIN_TEMP=""
if [ "$INPUT_FILE" = "-" ] || [ "$INPUT_FILE" = "/dev/stdin" ]; then
  STDIN_TEMP="$(mktemp)"
  trap 'rm -f "$STDIN_TEMP"' EXIT
  cat > "$STDIN_TEMP"
  INPUT_FILE="$STDIN_TEMP"
fi

python3 - << 'PYEOF' "$INPUT_FILE" "$OUTPUT_DIR" "$COMMIT_SHA" "$WORKFLOW_RUN_LINK" "$TF_VERSION" "$REPO_NAME" "$RUN_ID" "$SUITE"
import sys
import os
import json
import re
import xml.etree.ElementTree as ET
from datetime import datetime, timezone

input_path = sys.argv[1]
output_dir = sys.argv[2]
commit_sha = sys.argv[3]
run_link = sys.argv[4]
tf_version = sys.argv[5]
repo_name = sys.argv[6]
run_id = sys.argv[7]
suite = sys.argv[8]

# 1. Read input file
with open(input_path, 'r', encoding='utf-8', errors='replace') as f:
    raw_content = f.read()

# 2. Secret Redaction / Sanitization
PEM_COMPLETE_RE = re.compile(
    r'-----BEGIN (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY(?: BLOCK)?|CERTIFICATE)-----[\s\S]*?'
    r'-----END (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY(?: BLOCK)?|CERTIFICATE)-----'
)
PEM_BEGIN_RE = re.compile(
    r'-----BEGIN (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY(?: BLOCK)?|CERTIFICATE)-----'
)
PEM_END_RE = re.compile(
    r'-----END (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY(?: BLOCK)?|CERTIFICATE)-----'
)
PEM_TRUNCATED_RE = re.compile(
    r'-----BEGIN (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY(?: BLOCK)?|CERTIFICATE)-----[^\r\n"\']*'
    r'(?:(?:\r?\n|\\r?\\n)[ \t]*(?:[A-Za-z0-9+/_\-]+={0,2}|(?:Version|Comment|Proc-Type|DEK-Info):[^\r\n"\']*)(?=\r?\n|\\r?\\n|["\']|$))*'
)


def _scan_quoted_value_end(text, start, bs_len, quote):
    step = 2 * (bs_len + 1)
    i = start
    n = len(text)
    while i < n:
        ch = text[i]
        if ch in ('\r', '\n'):
            return None
        if ch == '\\':
            b_start = i
            while i < n and text[i] == '\\':
                i += 1
            b_count = i - b_start
            if i < n and text[i] == quote:
                if b_count < bs_len:
                    return (b_start, b_start)
                if (b_count - bs_len) % step == 0:
                    return (i - bs_len, i + 1)
                i += 1
            continue
        if ch == quote:
            if bs_len == 0:
                return (i, i + 1)
            return (i, i)
        i += 1
    return None


def _scan_balanced_json_object_end(text, brace_idx, bs_len):
    n = len(text)
    if brace_idx >= n or text[brace_idx] != '{':
        return None
    if bs_len == 0:
        k = brace_idx + 1
        while k < n:
            if text[k] in (' ', '\t'):
                k += 1
                continue
            if text[k] == '\\':
                b_start = k
                while k < n and text[k] == '\\':
                    k += 1
                if k < n and text[k] in ('n', 'r', 't', 'b', 'f'):
                    k += 1
                    continue
                if k < n and text[k] in ('"', "'"):
                    bs_len = k - b_start
                break
            break
    step = 2 * (bs_len + 1)
    depth = 1
    str_quote = None
    i = brace_idx + 1
    while i < n:
        ch = text[i]
        if ch in ('\r', '\n'):
            return None
        if ch == '\\':
            b_start = i
            while i < n and text[i] == '\\':
                i += 1
            b_count = i - b_start
            if i < n and text[i] in ('"', "'"):
                q = text[i]
                if str_quote is not None and b_count < bs_len and q == str_quote:
                    return None
                if b_count >= bs_len and (b_count - bs_len) % step == 0:
                    if str_quote is None:
                        str_quote = q
                    elif str_quote == q:
                        str_quote = None
                i += 1
            continue
        if ch in ('"', "'"):
            if bs_len == 0:
                if str_quote is None:
                    str_quote = ch
                elif str_quote == ch:
                    str_quote = None
            elif str_quote == ch or (str_quote is None and ch == '"'):
                return None
            i += 1
            continue
        if str_quote is None:
            if ch == '{':
                depth += 1
            elif ch == '}':
                depth -= 1
                if depth == 0:
                    return i + 1
        i += 1
    return None


def _is_word_char(ch):
    return ('a' <= ch <= 'z') or ('A' <= ch <= 'Z') or ('0' <= ch <= '9') or ch == '_'


def _preceding_backslash_count(s, idx):
    count = 0
    i = idx - 1
    while i >= 0 and s[i] == '\\':
        count += 1
        i -= 1
    return count


def _parse_hex4(s):
    if len(s) != 4:
        return None
    for ch in s:
        if not (('0' <= ch <= '9') or ('a' <= ch <= 'f') or ('A' <= ch <= 'F')):
            return None
    return int(s, 16)


def _is_json_escape_start_at(s, idx):
    n = len(s)
    if idx < 1 or idx >= n:
        return False
    ch = s[idx]
    if ch in ('n', 'r', 't', 'b', 'f'):
        return _preceding_backslash_count(s, idx) % 2 == 1
    if ch == 'u':
        if idx + 4 >= n or _preceding_backslash_count(s, idx) % 2 != 1:
            return False
        return _parse_hex4(s[idx + 1:idx + 5]) is not None
    return False


def _is_json_escape_end_at(s, idx):
    n = len(s)
    if idx < 1 or idx >= n:
        return False
    if s[idx] in ('n', 'r', 't', 'b', 'f') and _preceding_backslash_count(s, idx) % 2 == 1:
        return True
    if idx >= 5 and s[idx - 4] == 'u' and _preceding_backslash_count(s, idx - 4) % 2 == 1:
        cp = _parse_hex4(s[idx - 3:idx + 1])
        if cp is not None:
            if cp < 128:
                return not _is_word_char(chr(cp))
            return True
    return False


def _is_json_escape_word_boundary(s, idx):
    n = len(s)
    if idx < 0 or idx > n:
        return False
    if idx < n and _is_json_escape_start_at(s, idx):
        return False
    if idx == 0:
        return True
    if not _is_word_char(s[idx - 1]):
        return True
    return _is_json_escape_end_at(s, idx - 1)


def _replace_with_boundary(text, pattern, repl_fn):
    out = []
    pos = 0
    n = len(text)
    while pos < n:
        m = pattern.search(text, pos)
        if not m:
            out.append(text[pos:])
            break
        match_start = m.start()
        match_end = m.end()
        if not _is_json_escape_word_boundary(text, match_start):
            next_pos = max(pos + 1, match_start + 1)
            out.append(text[pos:next_pos])
            pos = next_pos
            continue
        out.append(text[pos:match_start])
        out.append(repl_fn(m))
        pos = max(pos + 1, match_end)
    return ''.join(out)


TOKEN_SPEC_OBJECT_PREFIX_RE = re.compile(
    r'(?i)((?:(\\*)(["\']))?((?:TF_VAR_)?(?:nsc[_-]?)?token[_-]?spec(?:[_-]?file)?)(?:\\*["\'])?[ \t]*([:=])[ \t]*)\{'
)
REDACTED_PLACEHOLDER_RE = re.compile(r'^\[REDACTED(?:[ _][A-Z_]+)*\]$')


def sanitize(text):
    def authorization_marker(value):
        stripped = value.lstrip()
        if re.match(r'(?i)bearer\b', stripped):
            return 'Bearer [REDACTED_TOKEN]'
        if re.match(r'(?i)basic\b', stripped):
            return 'Basic [REDACTED_SECRET]'
        return '[REDACTED_TOKEN]'

    def authorization_scheme_marker(value):
        if re.match(r'(?i)^bearer\s+', value):
            return 'Bearer [REDACTED_TOKEN]'
        if re.match(r'(?i)^basic\s+', value):
            return 'Basic [REDACTED_SECRET]'
        return value

    def redact_quoted_field(value, key_pattern, marker, value_transform=None, require_quoted_key=False):
        if require_quoted_key:
            prefix_re = re.compile(
                rf'(?i)((?:\\*["\'])(?P<key>{key_pattern})(?:\\*["\'])[ \t]*[:=][ \t]*)(?P<bs>\\*)(?P<quote>["\'])'
            )
        else:
            prefix_re = re.compile(
                rf'(?i)((?:\\*["\'])?(?P<key>{key_pattern})(?:\\*["\'])?[ \t]*[:=][ \t]*)(?P<bs>\\*)(?P<quote>["\'])'
            )
        out = []
        pos = 0
        n = len(value)
        while pos < n:
            m = prefix_re.search(value, pos)
            if not m:
                out.append(value[pos:])
                break
            key_start = m.start('key')
            if not _is_json_escape_word_boundary(value, key_start):
                advance = max(pos + 1, key_start + 1)
                out.append(value[pos:advance])
                pos = advance
                continue
            val_start = m.end()
            bs_len = len(m.group('bs'))
            quote = m.group('quote')
            bounds = _scan_quoted_value_end(value, val_start, bs_len, quote)
            if bounds is None:
                out.append(value[pos:val_start])
                pos = val_start
                continue
            val_end, close_end = bounds
            raw_val = value[val_start:val_end]
            if value_transform is not None:
                replacement = value_transform(raw_val)
            elif REDACTED_PLACEHOLDER_RE.fullmatch(raw_val.strip()):
                replacement = raw_val
            else:
                replacement = marker
            out.append(value[pos:val_start])
            out.append(replacement)
            out.append(value[val_end:close_end])
            pos = close_end
        return ''.join(out)

    def redact_auth_arrays(value):
        prefix_re = re.compile(r'(?i)((?:\\*["\'])authorization(?:\\*["\'])[ \t]*:[ \t]*\[)')
        out = []
        pos = 0
        n = len(value)
        while pos < n:
            m = prefix_re.search(value, pos)
            if not m:
                out.append(value[pos:])
                break
            out.append(value[pos:m.end()])
            i = m.end()
            while i < n:
                ch = value[i]
                if ch in (']', '\r', '\n'):
                    out.append(ch)
                    i += 1
                    break
                if ch in ('\\', '"', "'"):
                    bs_start = i
                    while i < n and value[i] == '\\':
                        i += 1
                    if i < n and value[i] in ('"', "'"):
                        bs_len = i - bs_start
                        quote = value[i]
                        val_start = i + 1
                        bounds = _scan_quoted_value_end(value, val_start, bs_len, quote)
                        if bounds is not None:
                            val_end, close_end = bounds
                            out.append(value[bs_start:val_start])
                            out.append(authorization_marker(value[val_start:val_end]))
                            out.append(value[val_end:close_end])
                            i = close_end
                            continue
                        out.append(value[bs_start:i + 1])
                        i += 1
                        continue
                    out.append(value[bs_start:i])
                    continue
                out.append(ch)
                i += 1
            pos = i
        return ''.join(out)

    def redact_token_spec_objects(value):
        out = []
        pos = 0
        n = len(value)
        while pos < n:
            m = TOKEN_SPEC_OBJECT_PREFIX_RE.search(value, pos)
            if not m:
                out.append(value[pos:])
                break
            key_start = m.start(4)
            if not _is_json_escape_word_boundary(value, key_start):
                advance = max(pos + 1, key_start + 1)
                out.append(value[pos:advance])
                pos = advance
                continue
            prefix_start = m.start(1)
            prefix_end = m.end(1)
            brace_idx = m.end() - 1
            bs = m.group(2) or ''
            bs_len = len(bs)
            quote = m.group(3) or ''
            sep = m.group(5)

            obj_end = _scan_balanced_json_object_end(value, brace_idx, bs_len)
            if obj_end is None:
                out.append(value[pos:brace_idx + 1])
                pos = brace_idx + 1
                continue

            out.append(value[pos:prefix_start])
            out.append(value[prefix_start:prefix_end])
            if sep == '=':
                out.append('[REDACTED]')
            else:
                q = quote if quote else '"'
                out.append(f'{bs}{q}[REDACTED_TOKEN]{bs}{q}')
            pos = obj_end
        return ''.join(out)

    def redact_unquoted_field(value, key_pattern, marker):
        pattern = re.compile(
            rf'(?i)(?P<prefix>(?:{key_pattern})\b[ \t]*[:=][ \t]*)'
            rf'(?!\\*["\']|\[(?:bearer|basic)\b)(?P<value>\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{{}}\'"\\&]*)?|[^\s,;{{}}\'"\\&]+)'
        )

        def replace(match):
            # Preserve authentication scheme labels and already-redacted placeholders.
            val = match.group('value')
            if val.lower() in ('bearer', 'basic') or REDACTED_PLACEHOLDER_RE.fullmatch(val):
                return match.group(0)
            return f"{match.group('prefix')}{marker}"

        return _replace_with_boundary(value, pattern, replace)

    # Redact client certs and private keys (EC, OpenSSH, RSA, DSA, ECDSA, PGP, Encrypted, truncated)
    text = PEM_COMPLETE_RE.sub('[REDACTED_KEY_OR_CERT]', text)
    text = PEM_TRUNCATED_RE.sub('[REDACTED_KEY_OR_CERT]', text)
    # Redact URL userinfo credentials (e.g. https://user:pass@host)
    text = re.sub(r'(?i)((?:https?|grpcs?|ftp|wss?|socks5h?)://)([^\s/@?#"\'<>]+)@', r'\1[REDACTED_CREDENTIALS]@', text)
    # Redact balanced single-line JSON object values for token_spec / NSC_TOKEN_SPEC* (both ':' and '=' delimited)
    text = redact_token_spec_objects(text)
    # Redact secret environment assignments (word-bounded, supports quoted values with spaces and balanced JSON objects)
    env_var_obj_re = re.compile(
        r'(?i)(?P<key>NSC_TOKEN|NSC_TOKEN_SPEC_FILE|NSC_TOKEN_SPEC|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE|TF_VAR_nsc_[A-Za-z0-9_]+)=\{'
    )
    out_parts = []
    pos = 0
    while pos < len(text):
        m = env_var_obj_re.search(text, pos)
        if not m:
            out_parts.append(text[pos:])
            break
        key_start = m.start('key')
        if not _is_json_escape_word_boundary(text, key_start):
            advance = max(pos + 1, key_start + 1)
            out_parts.append(text[pos:advance])
            pos = advance
            continue
        brace_idx = m.end() - 1
        obj_end = _scan_balanced_json_object_end(text, brace_idx, 0)
        if obj_end is None:
            out_parts.append(text[pos:brace_idx + 1])
            pos = brace_idx + 1
            continue
        out_parts.append(text[pos:key_start])
        out_parts.append(f"{m.group('key')}=[REDACTED]")
        pos = obj_end
    text = ''.join(out_parts)
    env_var_re = re.compile(
        r'(?i)(?P<key>NSC_TOKEN|NSC_TOKEN_SPEC_FILE|NSC_TOKEN_SPEC|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE|TF_VAR_nsc_[A-Za-z0-9_]+)='
        r'(?P<val>"(?:\\.|[^"\\\r\n])*"|\'(?:\\.|[^\'\\\r\n])*\'|\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s\n,;{}\'"\\&]*)?|[^\s\n,;{}\'"\\&]+)'
    )
    text = _replace_with_boundary(
        text,
        env_var_re,
        lambda m: m.group(0) if REDACTED_PLACEHOLDER_RE.fullmatch(m.group('val')) else f"{m.group('key')}=[REDACTED]",
    )
    # Redact authorization values in JSON arrays, Go metadata maps, headers, and quoted fields.
    text = redact_auth_arrays(text)
    text = _replace_with_boundary(
        text,
        re.compile(r'(?i)(authorization:\s*\[)(bearer|basic)(\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\]|[^\]\r\n]+)(\])'),
        lambda match: f"{match.group(1)}{match.group(2)}{match.group(3)}"
        + ('[REDACTED_TOKEN]' if match.group(2).lower() == 'bearer' else '[REDACTED_SECRET]')
        + match.group(4),
    )
    text = redact_quoted_field(text, r'authorization', '[REDACTED_TOKEN]', authorization_marker, require_quoted_key=True)
    text = redact_quoted_field(text, r'authorization', '[REDACTED_TOKEN]', authorization_scheme_marker, require_quoted_key=False)
    text = _replace_with_boundary(
        text,
        re.compile(r'(?i)(authorization\b\s*[:=]\s*)(bearer|basic)\s+(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}\'"\\&\]]*)?|[^\s,;{}\'"\\&\]]+)'),
        lambda match: f"{match.group(1)}{match.group(2)} "
        + ('[REDACTED_TOKEN]' if match.group(2).lower() == 'bearer' else '[REDACTED_SECRET]'),
    )
    # Redact Authorization Bearer headers and standalone Bearer tokens
    text = _replace_with_boundary(
        text,
        re.compile(r'(?i)(authorization\s*[:=]\s*bearer\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}\'"\\&\]]*)?|[^\s,;{}\'"\\&\]]+)'),
        lambda match: f"{match.group(1)}[REDACTED_TOKEN]",
    )
    text = _replace_with_boundary(
        text,
        re.compile(r'(?i)(bearer\s+)(?:\[REDACTED(?:[ _][A-Z_]+)*\]|[a-zA-Z0-9_\-\.+/=]{20,})'),
        lambda match: f"{match.group(1)}[REDACTED_TOKEN]",
    )
    # Redact Namespace token formats and standalone/prefixed JWTs before unquoted field matching
    # so enclosing punctuation such as "(token: st_...) failed" is preserved intact.
    text = _replace_with_boundary(
        text,
        re.compile(r'(?:nsct_|nscw_|nscu_|nsrt_|tok_|st_)(?:eyJ[A-Za-z0-9_\-]+={0,2}\.[A-Za-z0-9_\-]+={0,2}\.[A-Za-z0-9_\-]+={0,2}|[A-Za-z0-9_\-\.]+={0,2})'),
        lambda _match: '[REDACTED_TOKEN]',
    )
    text = _replace_with_boundary(
        text,
        re.compile(r'(?:[A-Za-z0-9_-]+_)?eyJ[A-Za-z0-9_\-]+={0,2}\.[A-Za-z0-9_\-]+={0,2}\.[A-Za-z0-9_\-]+={0,2}'),
        lambda _match: '[REDACTED_JWT]',
    )
    text = redact_unquoted_field(text, r'authorization', '[REDACTED_TOKEN]')

    token_keys = r'(?:token|(?:bearer|id|session|access|refresh|partner|client|auth|tenant|workload|oidc)[_-]?token|(?:nsc[_-]?)?token[_-]?spec(?:[_-]?file)?|trusted[_-]?jwt|jwt)'
    private_key_keys = r'(?:(?:api|private|client|signing)[_-]?key(?:[_-]?pem)?)'
    certificate_keys = r'(?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)'
    secret_keys = r'(?:secret|client[_-]?secret|credentials?)'
    password_keys = r'(?:password|passphrase)'

    for key_pattern, marker in (
        (token_keys, '[REDACTED_TOKEN]'),
        (private_key_keys, '[REDACTED_PRIVATE_KEY]'),
        (certificate_keys, '[REDACTED_CERTIFICATE]'),
        (secret_keys, '[REDACTED_SECRET]'),
        (password_keys, '[REDACTED_PASSWORD]'),
    ):
        text = redact_quoted_field(text, key_pattern, marker)
        text = redact_unquoted_field(text, key_pattern, marker)
    # Redact unquoted passwords as final defense-in-depth
    text = _replace_with_boundary(
        text,
        re.compile(r'(?i)(password\s*[:=]\s*)(?:\[REDACTED(?:[ _][A-Z_]+)*\](?:[A-Za-z0-9_./+=-][^\s,;{}\'"\\&]*)?|[^\s,;{}\'"\\&]+)'),
        lambda match: f"{match.group(1)}[REDACTED_PASSWORD]",
    )
    return text


def sanitize_json_output_chunk(chunk, state_key, pem_states):
    """Sanitize a decoded go test -json Output string while tracking multiline PEMs
    per (Package, Test) so parallel test JSON events are never swallowed."""
    if pem_states.get(state_key):
        end_m = PEM_END_RE.search(chunk)
        if end_m:
            pem_states[state_key] = False
            remainder = chunk[end_m.end():]
            return sanitize(remainder) if remainder else ''
        return ''

    chunk = PEM_COMPLETE_RE.sub('[REDACTED_KEY_OR_CERT]', chunk)
    begin_m = PEM_BEGIN_RE.search(chunk)
    if begin_m:
        pem_states[state_key] = True
        prefix = sanitize(chunk[:begin_m.start()])
        trailing_nl = '\n' if chunk.endswith('\n') else ''
        return f"{prefix}[REDACTED_KEY_OR_CERT]{trailing_nl}"

    return sanitize(chunk)


# 3. Parse Test Results FIRST (supports go test -json and verbose text), then sanitize
# decoded outputs so multiline PEMs or secret env values cannot corrupt JSON event framing.
tests = [] # list of dicts: {name, package, status, duration, output, failure_msg}
is_json = False
package_failures = set()

raw_lines = raw_content.splitlines()
parsed_records = []
for raw_line in raw_lines:
    line_clean = raw_line.strip()
    if line_clean.startswith('{') and line_clean.endswith('}'):
        try:
            ev = json.loads(line_clean)
            if isinstance(ev, dict) and any(k in ev for k in ('Action', 'Package', 'Test', 'Output')):
                parsed_records.append((True, ev))
                is_json = True
                continue
        except Exception:
            pass
    parsed_records.append((False, raw_line))

sanitized_log_path = os.path.join(output_dir, 'sanitized-acceptance.log')

if is_json:
    pem_states = {}
    sanitized_lines = []
    json_events = []
    for is_ev, item in parsed_records:
        if is_ev:
            ev = dict(item)
            pkg = ev.get('Package', 'github.com/bsuv/terraform-provider-namespace/internal/provider')
            tname = ev.get('Test', '')
            if 'Output' in ev and isinstance(ev['Output'], str):
                ev['Output'] = sanitize_json_output_chunk(ev['Output'], (pkg, tname), pem_states)
            json_events.append(ev)
            sanitized_lines.append(json.dumps(ev, separators=(',', ':')))
        else:
            sanitized_lines.append(sanitize(item))

    sanitized_log = '\n'.join(sanitized_lines)
    if raw_content.endswith('\n'):
        sanitized_log += '\n'
    with open(sanitized_log_path, 'w', encoding='utf-8') as f:
        f.write(sanitized_log)

    test_outputs = {}
    test_results = {}
    for ev in json_events:
        tname = ev.get('Test')
        pkg = ev.get('Package', 'github.com/bsuv/terraform-provider-namespace/internal/provider')
        action = ev.get('Action')
        out = ev.get('Output', '')
        elapsed = ev.get('Elapsed', 0.0)

        if not tname and action == 'fail':
            package_failures.add(pkg)

        if tname:
            key = (pkg, tname)
            if key not in test_outputs:
                test_outputs[key] = []
            if out:
                test_outputs[key].append(out)

            if action in ('pass', 'fail', 'skip'):
                test_results[key] = {
                    'name': tname,
                    'package': pkg,
                    'status': action.upper(),
                    'duration': float(elapsed),
                    'output': sanitize(''.join(test_outputs[key])),
                }

    for key, res in sorted(test_results.items()):
        tests.append(res)
else:
    sanitized_log = sanitize(raw_content)
    with open(sanitized_log_path, 'w', encoding='utf-8') as f:
        f.write(sanitized_log)
    lines = sanitized_log.strip().split('\n')
    # Text parsing
    pass_re = re.compile(r'---\s+PASS:\s+(\S+)\s+\(([0-9\.]+)s\)')
    fail_re = re.compile(r'---\s+FAIL:\s+(\S+)\s+\(([0-9\.]+)s\)')
    skip_re = re.compile(r'---\s+SKIP:\s+(\S+)\s+\(([0-9\.]+)s\)')
    package_fail_re = re.compile(r'^FAIL\s+(\S+)(?:\s+[0-9\.]+s)?$', re.MULTILINE)

    package_failures.update(package_fail_re.findall(sanitized_log))

    current_test = None
    accumulated_output = []

    for line in lines:
        m_pass = pass_re.search(line)
        m_fail = fail_re.search(line)
        m_skip = skip_re.search(line)

        if m_pass:
            name, dur = m_pass.groups()
            tests.append({
                'name': name,
                'package': 'github.com/bsuv/terraform-provider-namespace/internal/provider',
                'status': 'PASS',
                'duration': float(dur),
                'output': '\n'.join(accumulated_output),
            })
            accumulated_output = []
        elif m_fail:
            name, dur = m_fail.groups()
            tests.append({
                'name': name,
                'package': 'github.com/bsuv/terraform-provider-namespace/internal/provider',
                'status': 'FAIL',
                'duration': float(dur),
                'output': '\n'.join(accumulated_output),
            })
            accumulated_output = []
        elif m_skip:
            name, dur = m_skip.groups()
            tests.append({
                'name': name,
                'package': 'github.com/bsuv/terraform-provider-namespace/internal/provider',
                'status': 'SKIP',
                'duration': float(dur),
                'output': '\n'.join(accumulated_output),
            })
            accumulated_output = []
        else:
            accumulated_output.append(line)

total_count = len(tests)
passed_count = sum(1 for t in tests if t['status'] == 'PASS')
failed_count = sum(1 for t in tests if t['status'] == 'FAIL')
skipped_count = sum(1 for t in tests if t['status'] == 'SKIP')
package_failure_count = len(package_failures)
total_duration = sum(t['duration'] for t in tests)

# 4. Generate JUnit XML
testsuites_elem = ET.Element('testsuites', {
    'name': 'Namespace Acceptance Test Suite',
    'tests': str(total_count + package_failure_count),
    'failures': str(failed_count),
    'errors': str(package_failure_count),
    'skipped': str(skipped_count),
    'time': f"{total_duration:.3f}",
})

testsuite_elem = ET.SubElement(testsuites_elem, 'testsuite', {
    'name': 'internal/provider',
    'tests': str(total_count + package_failure_count),
    'failures': str(failed_count),
    'errors': str(package_failure_count),
    'skipped': str(skipped_count),
    'time': f"{total_duration:.3f}",
    'timestamp': datetime.now(timezone.utc).isoformat(),
})

for t in tests:
    tc_elem = ET.SubElement(testsuite_elem, 'testcase', {
        'name': t['name'],
        'classname': t['package'],
        'time': f"{t['duration']:.3f}",
    })
    if t['status'] == 'FAIL':
        fail_elem = ET.SubElement(tc_elem, 'failure', {'message': f"Test {t['name']} failed"})
        fail_elem.text = sanitize(t.get('output', ''))
    elif t['status'] == 'SKIP':
        ET.SubElement(tc_elem, 'skipped', {'message': 'Test skipped'})

for failed_package in sorted(package_failures):
    tc_elem = ET.SubElement(testsuite_elem, 'testcase', {
        'name': 'package-level execution',
        'classname': failed_package,
        'time': '0.000',
    })
    ET.SubElement(tc_elem, 'error', {'message': f"Package {failed_package} failed outside an individual test"})

junit_path = os.path.join(output_dir, 'junit.xml')
tree = ET.ElementTree(testsuites_elem)
tree.write(junit_path, encoding='utf-8', xml_declaration=True)

# 5. Observation-Backed Scenario Classification
# Structured observation pattern emitted by acceptance and contract test cases:
obs_kv_pattern = re.compile(
    r'OBSERVATION:\s*RPC=([^|\n]+?)(?:\s*\|\s*|\s+)Endpoint=([^|\n]+?)(?:\s*\|\s*|\s+)Status=([^|\n]+?)(?:\s*\|\s*|\s+)Scope=([^\n\r]+)',
    re.IGNORECASE
)
obs_pipe_pattern = re.compile(
    r'OBSERVATION:\s*([^|\n]+?)\s*\|\s*([^|\n]+?)\s*\|\s*([^|\n]+?)\s*\|\s*([^\n\r]+)',
    re.IGNORECASE
)

def classify_scenario(test_item):
    test_output = test_item.get('output', '')

    # Derive claims strictly from structured test observations emitted in output
    if test_output:
        kv_matches = obs_kv_pattern.findall(test_output)
        if kv_matches:
            rpc_method, endpoint, status_code, scope = kv_matches[-1]
            rpc_clean = rpc_method.strip()
            endpoint_clean = endpoint.strip()
            status_clean = status_code.strip()
            scope_clean = scope.strip()
            is_valid = bool(rpc_clean and rpc_clean != 'Unspecified RPC' and
                            endpoint_clean and endpoint_clean != 'Unspecified Endpoint' and
                            status_clean and status_clean != 'Unobserved' and
                            scope_clean and scope_clean != 'No structured observation emitted')
            return (rpc_clean, endpoint_clean, status_clean, scope_clean, is_valid)

        pipe_matches = obs_pipe_pattern.findall(test_output)
        if pipe_matches:
            rpc_method, endpoint, status_code, scope = pipe_matches[-1]
            if not rpc_method.strip().upper().startswith('RPC='):
                rpc_clean = rpc_method.strip()
                endpoint_clean = endpoint.strip()
                status_clean = status_code.strip()
                scope_clean = scope.strip()
                is_valid = bool(rpc_clean and rpc_clean != 'Unspecified RPC' and
                                endpoint_clean and endpoint_clean != 'Unspecified Endpoint' and
                                status_clean and status_clean != 'Unobserved' and
                                scope_clean and scope_clean != 'No structured observation emitted')
                return (rpc_clean, endpoint_clean, status_clean, scope_clean, is_valid)

    # Strict observation-driven reporting: Never guess, synthesize, or infer claims from test names
    return ('Unspecified RPC', 'Unspecified Endpoint', 'Unobserved', 'No structured observation emitted', False)

assertion_pattern = re.compile(r'ASSERTION:\s*([A-Za-z0-9_]+)', re.IGNORECASE)

for t in tests:
    rpc_method, endpoint, status_code, notes, is_observed = classify_scenario(t)
    t['rpc_method'] = rpc_method
    t['endpoint'] = endpoint
    t['observation_status'] = status_code
    t['scope'] = notes
    t['is_observed'] = is_observed
    t_out = t.get('output', '')
    found_assertions = set(assertion_pattern.findall(t_out))
    t['assertions'] = found_assertions

# 6. Quality Gate Evaluation (Evaluated BEFORE generating and writing markdown reports)
gate_errors = []

# Gate 1: Fail if zero tests ran
if total_count == 0:
    gate_errors.append("Gate 1: Zero tests ran in acceptance test suite (total_count == 0).")

# Gate 2: Fail if any test failed
if failed_count > 0:
    failed_names = [t['name'] for t in tests if t['status'] == 'FAIL']
    gate_errors.append(f"Gate 2: {failed_count} test(s) failed in acceptance test suite ({', '.join(failed_names)}).")
if package_failures:
    gate_errors.append(f"Gate 2: package-level test failure(s) detected ({', '.join(sorted(package_failures))}).")

# Gate 3: Fail if critical matrix scenarios deviate from authoritative contract (fail-closed)
CRITICAL_TEST_CONTRACTS = {
    'TestAccMatrix_DriftDetection': {
        'expected_rpc': 'ProfileService.UpdateProfile',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.OK (Reconciled)',
        'expected_scope': 'Drift detection & HCL reconciliation',
        'required_assertion': 'DriftDetectionVerified',
    },
    'TestAccMatrix_ConcurrencyConflict': {
        'expected_rpc': 'ProfileService.UpdateProfile',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.FailedPrecondition',
        'expected_scope': 'Optimistic revision locking collision',
        'required_assertion': 'ConcurrencyConflictVerified',
    },
    'TestAccMatrix_ExternalDeletion': {
        'expected_rpc': 'ProfileService.DeleteProfile / Read',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.NotFound (Recreated)',
        'expected_scope': 'External purge & state recreation',
        'required_assertion': 'ExternalDeletionRecreatedVerified',
    },
    'TestAccMatrix_CollisionPrevention': {
        'expected_rpc': 'TenantService.EnsureTenantGroup',
        'expected_endpoint': 'IAM',
        'expected_status': 'codes.AlreadyExists (Import)',
        'expected_scope': 'Collision detection & import guidance',
        'required_assertion': 'CollisionPreventionVerified',
    },
    'TestAccMatrix_PaginationTraversal': {
        'expected_rpc': 'ContainerRegistryService.ListImages',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.OK (Cursor Traversal)',
        'expected_scope': '>100 image records pagination (live cursor traversal)',
        'required_assertion': 'PaginationMultipleCursorsVerified',
    },
    'TestAccMatrix_ExpirationDisappearance': {
        'expected_rpc': 'TokenService.IssueTenantToken / ProfileService.ListProfiles',
        'expected_endpoint': 'IAM / Global',
        'expected_status': 'codes.Unauthenticated (Rejected)',
        'expected_scope': 'Short-lived token expiry rejection',
        'required_assertion': 'ExpiredTokenRejectionVerified',
    },
    'TestAccProvider_AuthenticationPrecedence': {
        'expected_rpc': 'TenantMembershipService.ListTenantMembers',
        'expected_endpoint': 'IAM',
        'expected_status': 'codes.OK',
        'expected_scope': 'Authentication precedence resolution hierarchy verified',
        'required_assertion': 'AuthPrecedenceHierarchyVerified',
    },
    'TestAccEphemeral_CacheLifecycle_PlanAndDeferral': {
        'expected_rpc': 'BazelCacheService.EnsureBazelCache',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.OK',
        'expected_scope': 'Ephemeral cache plan deferral and apply execution',
        'required_assertion': 'CachePlanDeferralVerified',
    },
    'TestAccRunnerProfile_CustomImageCombinations': {
        'expected_rpc': 'ProfileService.CreateProfile',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.OK',
        'expected_scope': 'Custom runner image neither, Ubuntu-only, Docker-only, and combined specifications',
        'required_assertion': 'CustomRunnerImageCombinationsVerified',
    },
}

SINGLETON_TEST_CONTRACTS = {
    'TestAccRegistryPolicy_Default': {
        'expected_rpc': 'ContainerRegistryService.UpdateDefaultPolicy',
        'expected_endpoint': 'Global',
        'expected_status': 'codes.OK',
        'expected_scope': 'Default registry policy lifecycle and import verification',
    }
}

test_by_name = {t['name']: t for t in tests}

# Suite intent is explicit so a truncated full release run cannot be mistaken for
# the deliberately split matrix or singleton jobs.
is_singleton_suite = suite == 'singleton'
if is_singleton_suite:
    unexpected = sorted(t['name'] for t in tests if not (t['name'] == 'TestAccRegistryPolicy_Default' or t['name'].startswith('TestAccRegistryPolicy_Default/')))
    if unexpected:
        gate_errors.append(f"Gate 3: singleton suite contained unexpected test(s): {', '.join(unexpected)}.")
elif suite == 'matrix' and 'TestAccRegistryPolicy_Default' in test_by_name:
    gate_errors.append("Gate 3: matrix suite unexpectedly executed the serialized default registry policy test.")

if is_singleton_suite:
    if 'TestAccRegistryPolicy_Default' not in test_by_name:
        gate_errors.append("Gate 3: singleton suite is missing TestAccRegistryPolicy_Default.")
    for t in tests:
        if t['status'] != 'PASS':
            gate_errors.append(f"Gate 3: Singleton scenario '{t['name']}' did not pass (status={t['status']}).")
        elif not t['is_observed']:
            gate_errors.append(f"Gate 3: Singleton scenario '{t['name']}' did not emit a valid structured OBSERVATION log line.")
        elif t['name'] in SINGLETON_TEST_CONTRACTS:
            contract = SINGLETON_TEST_CONTRACTS[t['name']]
            if t['rpc_method'] != contract['expected_rpc']:
                gate_errors.append(f"Gate 3: Singleton scenario '{t['name']}' RPC mismatch: expected '{contract['expected_rpc']}', got '{t['rpc_method']}' (contract violation).")
            if t['endpoint'] != contract['expected_endpoint']:
                gate_errors.append(f"Gate 3: Singleton scenario '{t['name']}' Endpoint mismatch: expected '{contract['expected_endpoint']}', got '{t['endpoint']}' (contract violation).")
            if t['observation_status'] != contract['expected_status']:
                gate_errors.append(f"Gate 3: Singleton scenario '{t['name']}' Status mismatch: expected '{contract['expected_status']}', got '{t['observation_status']}' (contract violation).")
            if t['scope'] != contract['expected_scope']:
                gate_errors.append(f"Gate 3: Singleton scenario '{t['name']}' Scope mismatch: expected '{contract['expected_scope']}', got '{t['scope']}' (contract violation).")
else:
    # Fail closed if ANY critical matrix scenario is missing from test execution output
    missing_scenarios = [scen for scen in CRITICAL_TEST_CONTRACTS if scen not in test_by_name]
    if missing_scenarios:
        gate_errors.append(f"Gate 3: Critical matrix scenario(s) missing from test execution output: {', '.join(missing_scenarios)}. All {len(CRITICAL_TEST_CONTRACTS)} critical matrix scenarios must be executed (fail-closed).")

    # Strict contract validation for all critical scenarios
    for scen, contract in CRITICAL_TEST_CONTRACTS.items():
        if scen in test_by_name:
            t = test_by_name[scen]
            if t['status'] != 'PASS':
                gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' did not pass (status={t['status']}).")
            elif not t['is_observed']:
                gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' passed but did not emit a valid structured OBSERVATION log line (fail-closed).")
            else:
                if t['rpc_method'] != contract['expected_rpc']:
                    gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' RPC mismatch: expected '{contract['expected_rpc']}', got '{t['rpc_method']}' (contract violation).")
                if t['endpoint'] != contract['expected_endpoint']:
                    gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' Endpoint mismatch: expected '{contract['expected_endpoint']}', got '{t['endpoint']}' (contract violation).")
                if t['observation_status'] != contract['expected_status']:
                    gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' Status mismatch: expected '{contract['expected_status']}', got '{t['observation_status']}' (contract violation).")
                if t['scope'] != contract['expected_scope']:
                    gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' Scope mismatch: expected '{contract['expected_scope']}', got '{t['scope']}' (contract violation).")
                req_assertion = contract.get('required_assertion')
                if req_assertion and req_assertion not in t.get('assertions', set()):
                    gate_errors.append(f"Gate 3: Critical matrix scenario '{scen}' missing required passing assertion '{req_assertion}' (contract violation).")

    # Fail closed if any other TestAccMatrix_ scenario executed and did not pass or was not observed
    for t in tests:
        tname = t['name']
        if tname.startswith('TestAccMatrix_') and tname not in CRITICAL_TEST_CONTRACTS:
            if t['status'] != 'PASS':
                gate_errors.append(f"Gate 3: Matrix scenario '{tname}' did not pass (status={t['status']}).")
            elif not t['is_observed']:
                gate_errors.append(f"Gate 3: Matrix scenario '{tname}' did not emit a valid structured OBSERVATION log line.")

    # Every retained first-release public object must have a passing, structured
    # live observation. This prevents an unavailable fixture, endpoint, or
    # permission from becoming an innocuous SKIP in an otherwise green report.
    REQUIRED_PUBLIC_SURFACE_TESTS = [
        'TestAccProvider_Configuration',
        'TestAccProvider_MultiRegionAliases',
        'TestAccRunnerProfile_Lifecycle',
        'TestAccRunnerProfile_CustomImageCombinations',
        'TestAccTenantGroup_Lifecycle',
        'TestAccTenantGroup_InterveningWriter',
        'TestAccSharedImage_Lifecycle',
        'TestAccSharedImage_ExpirationDisappearance',
        'TestAccRegistryPolicy_Repository',
        'TestAccRegistryPolicy_Repository_Drift',
        'TestAccRegistryPolicy_Repository_ConcurrencyConflict',
        'TestAccDataSource_RunnerProfile',
        'TestAccDataSource_RunnerProfiles',
        'TestAccDataSource_BaseImages',
        'TestAccDataSource_TenantGroup',
        'TestAccDataSource_TenantMembers',
        'TestAccDataSource_CacheVolumeSummary',
        'TestAccDataSource_PersistentVolume',
        'TestAccDataSource_BuilderConfiguration',
        'TestAccDataSource_BuilderUsage',
        'TestAccDataSource_NetworkConfiguration',
        'TestAccDataSource_RegistryImage',
        'TestAccDataSource_RegistryImages',
        'TestAccDataSource_VaultSecretMetadata',
        'TestAccEphemeral_IDToken',
        'TestAccEphemeral_TenantToken',
        'TestAccEphemeral_ClientCertificate',
        'TestAccEphemeral_BazelCache',
        'TestAccEphemeral_GradleCache',
        'TestAccEphemeral_HTTPCache',
        'TestAccEphemeral_DownstreamConsumption',
        'TestAccEphemeral_CacheLifecycle_PlanAndDeferral',
    ]
    for required_test in REQUIRED_PUBLIC_SURFACE_TESTS:
        evidence_test = test_by_name.get(required_test)
        if evidence_test is None:
            gate_errors.append(f"Gate 3: first-release public-surface test '{required_test}' is missing.")
        elif evidence_test['status'] != 'PASS':
            gate_errors.append(f"Gate 3: first-release public-surface test '{required_test}' did not pass (status={evidence_test['status']}).")
        elif not evidence_test['is_observed']:
            gate_errors.append(f"Gate 3: first-release public-surface test '{required_test}' did not emit a valid structured OBSERVATION log line.")

    if suite == 'full':
        singleton_test = test_by_name.get('TestAccRegistryPolicy_Default')
        if singleton_test is None:
            gate_errors.append("Gate 3: full suite is missing TestAccRegistryPolicy_Default.")
        elif singleton_test['status'] != 'PASS':
            gate_errors.append(f"Gate 3: full-suite singleton TestAccRegistryPolicy_Default did not pass (status={singleton_test['status']}).")
        elif not singleton_test['is_observed']:
            gate_errors.append("Gate 3: full-suite singleton TestAccRegistryPolicy_Default did not emit a valid structured OBSERVATION log line.")
        else:
            contract = SINGLETON_TEST_CONTRACTS['TestAccRegistryPolicy_Default']
            if singleton_test['rpc_method'] != contract['expected_rpc']:
                gate_errors.append(f"Gate 3: full-suite singleton RPC mismatch: expected '{contract['expected_rpc']}', got '{singleton_test['rpc_method']}'.")
            if singleton_test['endpoint'] != contract['expected_endpoint']:
                gate_errors.append(f"Gate 3: full-suite singleton endpoint mismatch: expected '{contract['expected_endpoint']}', got '{singleton_test['endpoint']}'.")
            if singleton_test['observation_status'] != contract['expected_status']:
                gate_errors.append(f"Gate 3: full-suite singleton status mismatch: expected '{contract['expected_status']}', got '{singleton_test['observation_status']}'.")
            if singleton_test['scope'] != contract['expected_scope']:
                gate_errors.append(f"Gate 3: full-suite singleton scope mismatch: expected '{contract['expected_scope']}', got '{singleton_test['scope']}'.")

# Gate 4: Bind security attestations to the exact tests that prove them. A marker
# emitted by an unrelated passing test is not evidence.
ATTESTATION_TEST_CONTRACTS = {
    'SecretScrubbingVerified': [
        'TestAccSecurity_SecretScrubbing',
    ],
    'TenantIsolationVerified': [
        'TestAccSecurity_TenantIsolation',
    ],
    'NonPersistenceVerified': [
        'TestAccEphemeral_IDToken',
        'TestAccEphemeral_TenantToken',
        'TestAccEphemeral_ClientCertificate',
        'TestAccEphemeral_BazelCache',
        'TestAccEphemeral_GradleCache',
        'TestAccEphemeral_HTTPCache',
        'TestAccEphemeral_DownstreamConsumption',
        'TestAccEphemeral_CacheLifecycle_PlanAndDeferral',
    ],
}

verified_attestations = set()
attestation_contract_failures = {}
for assertion_id, required_tests in ATTESTATION_TEST_CONTRACTS.items():
    failures = []
    for required_test in required_tests:
        evidence_test = test_by_name.get(required_test)
        if evidence_test is None:
            failures.append(f"{required_test} missing")
        elif evidence_test['status'] != 'PASS':
            failures.append(f"{required_test} status={evidence_test['status']}")
        elif assertion_id not in evidence_test.get('assertions', set()):
            failures.append(f"{required_test} missing assertion")
    if failures:
        attestation_contract_failures[assertion_id] = failures
        if not is_singleton_suite:
            gate_errors.append(f"Gate 4: attestation '{assertion_id}' lacks authorized passing evidence ({'; '.join(failures)}).")
    else:
        verified_attestations.add(assertion_id)

# Gate 5: A passing report is release evidence only when it is tied to an immutable
# commit and the exact GitHub Actions run that produced it.
if not re.fullmatch(r'[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?', commit_sha):
    gate_errors.append("Gate 5: commit provenance is missing or is not a full 40/64-character Git object ID.")
if not re.fullmatch(r'[0-9]+', run_id):
    gate_errors.append("Gate 5: workflow run ID is missing or non-numeric.")
expected_run_link = f"https://github.com/{repo_name}/actions/runs/{run_id}"
if run_link != expected_run_link:
    gate_errors.append(f"Gate 5: workflow run link must exactly match '{expected_run_link}'.")

overall_passed = (len(gate_errors) == 0 and total_count > 0 and failed_count == 0 and not package_failures)
overall_status_display = '✅ PASS' if overall_passed else '❌ FAIL'

# 7. Conditional Security, Non-Persistence, and Tenant-Isolation Attestations
has_secret_scrubbing_assertion = ('SecretScrubbingVerified' in verified_attestations)
has_non_persistence_assertion = ('NonPersistenceVerified' in verified_attestations)
has_tenant_isolation_assertion = ('TenantIsolationVerified' in verified_attestations)

attestations = []

if has_secret_scrubbing_assertion:
    attestations.append("- **Secret Scrubbing:** All bearer tokens (`nsct_...`, `nscw_...`, `nscu_...`, `nsrt_...`, `tok_...`, `st_...`), JWT federation assertions, mTLS private keys (EC, RSA, DSA, OpenSSH), passwords, and sensitive environment variables have been sanitized and redacted from log outputs (verified via passing test assertion).")
else:
    attestations.append("- **Secret Scrubbing:** ⚠️ Unverified: the authorized SecretScrubbingVerified evidence contract was not satisfied in this run.")

if has_non_persistence_assertion:
    attestations.append("- **Non-Persistence:** Every first-release IAM and cache ephemeral passed its Terraform state-absence check, including downstream consumption and plan/apply deferral coverage (verified via the complete authorized test set).")
else:
    attestations.append("- **Non-Persistence:** ⚠️ Unverified: the complete authorized NonPersistenceVerified evidence contract was not satisfied in this run.")

if has_tenant_isolation_assertion:
    attestations.append("- **Tenant Isolation:** Standard, partner, and trusted-authority credentials were each fail-closed verified against the dedicated test tenant before protected mutations (verified via passing test assertion).")
else:
    attestations.append("- **Tenant Isolation:** ⚠️ Unverified: the authorized TenantIsolationVerified evidence contract was not satisfied in this run.")

# 8. Render Acceptance Report Markdown
now_utc = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")

report_lines = [
    "# Namespace Terraform Provider Acceptance Test Report",
    "",
    f"**Generated:** {now_utc}  ",
    f"**Target Repository:** `{repo_name}`  ",
    f"**Commit SHA:** `{commit_sha}`  ",
    f"**Workflow Run:** [{run_id}]({run_link})  ",
    f"**Terraform CLI Version:** `{tf_version}`  ",
    f"**Acceptance Suite:** `{suite}`  ",
    "",
    "---",
    "",
    "## 1. Summary Metrics",
    "",
    "| Metric | Value |",
    "|---|---|",
    f"| **Total Tests** | {total_count} |",
    f"| **Passed** | {passed_count} |",
    f"| **Failed** | {failed_count} |",
    f"| **Package-level Failures** | {package_failure_count} |",
    f"| **Skipped** | {skipped_count} |",
    f"| **Total Duration** | {total_duration:.2f}s |",
    f"| **Overall Status** | {overall_status_display} |",
    "",
    "---",
    "",
    "## 2. Per-RPC & Matrix Scenario Execution Records",
    "",
    "| Test Scenario | RPC Method | Endpoint | Status | Duration | Verifiable Contract Scope |",
    "|---|---|---|---|---|---|",
]

for t in tests:
    rpc_method = t['rpc_method']
    endpoint = t['endpoint']
    status_code = t['observation_status']
    notes = t['scope']
    status_icon = "✅ PASS" if t['status'] == 'PASS' else ("❌ FAIL" if t['status'] == 'FAIL' else "⚠️ SKIP")
    dur_str = f"{t['duration']:.2f}s"
    report_lines.append(f"| `{t['name']}` | `{rpc_method}` | {endpoint} | {status_icon} | {dur_str} | {notes} ({status_code}) |")

report_lines.extend([
    "",
    "---",
    "",
    "## 3. Security & Sanitization Attestation",
    "",
] + attestations)

if gate_errors:
    report_lines.extend([
        "",
        "---",
        "",
        "## 4. Quality Gate Failure Diagnostics",
        "",
        "⚠️ **Quality Gate Failures Detected:**",
    ])
    for err in gate_errors:
        report_lines.append(f"- {err}")

report_lines.extend([
    "",
    "---",
    "",
    "## 5. Traceable Artifacts" if gate_errors else "## 4. Traceable Artifacts",
    "",
    f"- **JUnit XML:** `{output_dir}/junit.xml`",
    f"- **Sanitized Log:** `{output_dir}/sanitized-acceptance.log`",
    f"- **Report Markdown:** `{output_dir}/acceptance-report.md`",
    "",
])

report_md = sanitize("\n".join(report_lines))
report_md_path = os.path.join(output_dir, 'acceptance-report.md')
with open(report_md_path, 'w', encoding='utf-8') as f:
    f.write(report_md)

# Write to GITHUB_STEP_SUMMARY if available
step_summary = os.getenv('GITHUB_STEP_SUMMARY')
if step_summary:
    with open(step_summary, 'a', encoding='utf-8') as f:
        f.write(report_md + '\n')

print(f"Report generated successfully in {output_dir}:")
print(f" - {report_md_path}")
print(f" - {junit_path}")
print(f" - {sanitized_log_path}")
print(f"Total: {total_count}, Passed: {passed_count}, Failed: {failed_count}, Skipped: {skipped_count}, Duration: {total_duration:.2f}s")
print(f"Overall Status: {overall_status_display}")

# 9. Strict Verification Exit
if not overall_passed:
    sys.stderr.write("=" * 70 + "\n")
    sys.stderr.write("QUALITY GATE CHECK FAILED:\n")
    for err in gate_errors:
        sys.stderr.write(f"  - {err}\n")
    sys.stderr.write("=" * 70 + "\n")
    sys.exit(1)

PYEOF
