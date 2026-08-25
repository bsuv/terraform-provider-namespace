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
def sanitize(text):
    def redact_quoted_field(value, key_pattern, marker, value_transform=None):
        # Match ordinary and JSON-escaped quoted keys/values. Sanitization runs
        # before go test -json is parsed, so escaped quotes are expected here.
        pattern = re.compile(
            rf'(?i)(?P<prefix>(?:\\?["\'])?(?:{key_pattern})(?:\\?["\'])?[ \t]*[:=][ \t]*'
            rf'(?P<quote>\\?["\']))(?P<value>.*?)(?P=quote)'
        )

        def replace(match):
            replacement = marker
            if value_transform is not None:
                replacement = value_transform(match.group('value'))
            return f"{match.group('prefix')}{replacement}{match.group('quote')}"

        return pattern.sub(replace, value)

    def redact_unquoted_field(value, key_pattern, marker):
        pattern = re.compile(
            rf'(?i)(?P<prefix>\b(?:{key_pattern})\b[ \t]*[:=][ \t]*)'
            rf'(?!\\?["\'])(?P<value>\[REDACTED_[A-Z_]+\]|[^\s,;{{}}\'"\\&]+)'
        )

        def replace(match):
            # Preserve authentication scheme labels. The scheme-aware pass
            # redacts the credential that follows them.
            if match.group('value').lower() in ('bearer', 'basic'):
                return match.group(0)
            return f"{match.group('prefix')}{marker}"

        return pattern.sub(replace, value)

    def authorization_marker(value):
        stripped = value.lstrip()
        if re.match(r'(?i)bearer\b', stripped):
            return 'Bearer [REDACTED_TOKEN]'
        if re.match(r'(?i)basic\b', stripped):
            return 'Basic [REDACTED_SECRET]'
        return '[REDACTED_TOKEN]'

    # Redact client certs and private keys (EC, OpenSSH, RSA, DSA, ECDSA, Encrypted, etc.)
    text = re.sub(r'-----BEGIN (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY|CERTIFICATE)-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*(?:PRIVATE KEY|CERTIFICATE)-----', '[REDACTED_KEY_OR_CERT]', text)
    # Redact secret environment assignments
    text = re.sub(r'(?i)(NSC_TOKEN|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE)=([^\s\n]+)', r'\1=[REDACTED]', text)
    # Redact authorization values in ordinary headers, maps, and JSON-escaped
    # structured logs. Preserve the scheme so evidence remains useful without
    # retaining the credential.
    text = redact_quoted_field(text, r'authorization', '[REDACTED_TOKEN]', authorization_marker)
    text = re.sub(
        r'(?i)(\bauthorization\b\s*[:=]\s*)(bearer|basic)\s+(?:\[REDACTED_[A-Z_]+\]|[^\s,;{}\'"\\&]+)',
        lambda match: f"{match.group(1)}{match.group(2)} " +
        ('[REDACTED_TOKEN]' if match.group(2).lower() == 'bearer' else '[REDACTED_SECRET]'),
        text,
    )
    text = redact_unquoted_field(text, r'authorization', '[REDACTED_TOKEN]')

    token_keys = r'(?:token|(?:bearer|id|session|access|refresh|partner|client|auth)[_-]?token|trusted[_-]?jwt|jwt)'
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
    # Redact Authorization Bearer headers (case-insensitive)
    text = re.sub(r'(?i)(authorization:\s*bearer\s+)(?:\[REDACTED_TOKEN\]|[^\s,;{}]+)', r'\1[REDACTED_TOKEN]', text)
    text = re.sub(r'(?i)(bearer\s+)(?:\[REDACTED_TOKEN\]|[a-zA-Z0-9_\-\.]{20,})', r'\1[REDACTED_TOKEN]', text)
    # Redact Namespace token formats: nsct_, nscw_, nscu_, tok_, st_
    text = re.sub(r'(?:nsct_|nscw_|nscu_|tok_|st_)[A-Za-z0-9_\-\.]+', '[REDACTED_TOKEN]', text)
    # Redact standalone JWT tokens (header.payload.sig)
    text = re.sub(r'eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+', '[REDACTED_JWT]', text)
    # Redact quoted and unquoted passwords (including JSON-escaped quotes)
    text = re.sub(r'(?i)([\\]*[\"\']?password[\\]*[\"\']?\s*[:=]\s*[\\]*[\"\'])(?:\[REDACTED_PASSWORD\]|[^\r\n]*?)([\\]*[\"\'])', r'\1[REDACTED_PASSWORD]\2', text)
    text = re.sub(r'(?i)(password\s*[:=]\s*)(?:\[REDACTED_PASSWORD\]|[^\s,;{}\'\"\\\\]+)', r'\1[REDACTED_PASSWORD]', text)
    return text

sanitized_log = sanitize(raw_content)
sanitized_log_path = os.path.join(output_dir, 'sanitized-acceptance.log')
with open(sanitized_log_path, 'w', encoding='utf-8') as f:
    f.write(sanitized_log)

# 3. Parse Test Results (supports both go test -json and verbose text)
tests = [] # list of dicts: {name, package, status, duration, output, failure_msg}
is_json = False
package_failures = set()

lines = sanitized_log.strip().split('\n')
json_events = []
for line in lines:
    line_clean = line.strip()
    if line_clean.startswith('{') and line_clean.endswith('}'):
        try:
            ev = json.loads(line_clean)
            json_events.append(ev)
            is_json = True
        except Exception:
            pass

if is_json and len(json_events) > 0:
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
                    'output': ''.join(test_outputs[key]),
                }

    for key, res in sorted(test_results.items()):
        tests.append(res)
else:
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
    attestations.append("- **Secret Scrubbing:** All bearer tokens (`nsct_...`, `nscw_...`, `nscu_...`, `tok_...`, `st_...`), JWT federation assertions, mTLS private keys (EC, RSA, DSA, OpenSSH), passwords, and sensitive environment variables have been sanitized and redacted from log outputs (verified via passing test assertion).")
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
