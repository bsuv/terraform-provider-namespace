#!/usr/bin/env python3
"""
Adversarial Unit Test Suite for scripts/generate-acceptance-report.sh

Verifies strict contract dictionary binding, fail-closed Gate 1/2/3 enforcement,
explicit assertion tracking for attestations, secret redaction, and error diagnostics.
"""

import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
GENERATE_REPORT_SCRIPT = os.path.join(SCRIPT_DIR, "generate-acceptance-report.sh")

CRITICAL_SCENARIOS = [
    {
        "name": "TestAccMatrix_DriftDetection",
        "rpc": "ProfileService.UpdateProfile",
        "endpoint": "Global",
        "status": "codes.OK (Reconciled)",
        "scope": "Drift detection & HCL reconciliation",
        "assertion": "DriftDetectionVerified",
    },
    {
        "name": "TestAccMatrix_ConcurrencyConflict",
        "rpc": "ProfileService.UpdateProfile",
        "endpoint": "Global",
        "status": "codes.FailedPrecondition",
        "scope": "Optimistic revision locking collision",
        "assertion": "ConcurrencyConflictVerified",
    },
    {
        "name": "TestAccMatrix_ExternalDeletion",
        "rpc": "ProfileService.DeleteProfile / Read",
        "endpoint": "Global",
        "status": "codes.NotFound (Recreated)",
        "scope": "External purge & state recreation",
        "assertion": "ExternalDeletionRecreatedVerified",
    },
    {
        "name": "TestAccMatrix_CollisionPrevention",
        "rpc": "TenantService.EnsureTenantGroup",
        "endpoint": "IAM",
        "status": "codes.AlreadyExists (Import)",
        "scope": "Collision detection & import guidance",
        "assertion": "CollisionPreventionVerified",
    },
    {
        "name": "TestAccMatrix_PaginationTraversal",
        "rpc": "ContainerRegistryService.ListImages",
        "endpoint": "Global",
        "status": "codes.OK (Cursor Traversal)",
        "scope": ">100 image records pagination (live cursor traversal)",
        "assertion": "PaginationMultipleCursorsVerified",
    },
    {
        "name": "TestAccMatrix_ExpirationDisappearance",
        "rpc": "TokenService.IssueTenantToken / ProfileService.ListProfiles",
        "endpoint": "IAM / Global",
        "status": "codes.Unauthenticated (Rejected)",
        "scope": "Short-lived token expiry rejection",
        "assertion": "ExpiredTokenRejectionVerified",
    },
    {
        "name": "TestAccProvider_AuthenticationPrecedence",
        "rpc": "TenantMembershipService.ListTenantMembers",
        "endpoint": "IAM",
        "status": "codes.OK",
        "scope": "Authentication precedence resolution hierarchy verified",
        "assertion": "AuthPrecedenceHierarchyVerified",
    },
    {
        "name": "TestAccEphemeral_CacheLifecycle_PlanAndDeferral",
        "rpc": "BazelCacheService.EnsureBazelCache",
        "endpoint": "Global",
        "status": "codes.OK",
        "scope": "Ephemeral cache plan deferral and apply execution",
        "assertion": "CachePlanDeferralVerified",
        "extra_assertions": ["NonPersistenceVerified"],
    },
    {
        "name": "TestAccRunnerProfile_CustomImageCombinations",
        "rpc": "ProfileService.CreateProfile",
        "endpoint": "Global",
        "status": "codes.OK",
        "scope": "Custom runner image neither, Ubuntu-only, Docker-only, and combined specifications",
        "assertion": "CustomRunnerImageCombinationsVerified",
    },
]

REQUIRED_PUBLIC_SURFACE_TESTS = [
    "TestAccProvider_Configuration",
    "TestAccProvider_MultiRegionAliases",
    "TestAccRunnerProfile_Lifecycle",
    "TestAccRunnerProfile_CustomImageCombinations",
    "TestAccTenantGroup_Lifecycle",
    "TestAccTenantGroup_InterveningWriter",
    "TestAccSharedImage_Lifecycle",
    "TestAccSharedImage_ExpirationDisappearance",
    "TestAccRegistryPolicy_Repository",
    "TestAccRegistryPolicy_Repository_Drift",
    "TestAccRegistryPolicy_Repository_ConcurrencyConflict",
    "TestAccDataSource_RunnerProfile",
    "TestAccDataSource_RunnerProfiles",
    "TestAccDataSource_BaseImages",
    "TestAccDataSource_TenantGroup",
    "TestAccDataSource_TenantMembers",
    "TestAccDataSource_CacheVolumeSummary",
    "TestAccDataSource_PersistentVolume",
    "TestAccDataSource_BuilderConfiguration",
    "TestAccDataSource_BuilderUsage",
    "TestAccDataSource_NetworkConfiguration",
    "TestAccDataSource_RegistryImage",
    "TestAccDataSource_RegistryImages",
    "TestAccDataSource_VaultSecretMetadata",
    "TestAccEphemeral_IDToken",
    "TestAccEphemeral_TenantToken",
    "TestAccEphemeral_ClientCertificate",
    "TestAccEphemeral_BazelCache",
    "TestAccEphemeral_GradleCache",
    "TestAccEphemeral_HTTPCache",
    "TestAccEphemeral_DownstreamConsumption",
    "TestAccEphemeral_CacheLifecycle_PlanAndDeferral",
]

ATTESTATION_TEST_CONTRACTS = {
    "SecretScrubbingVerified": ["TestAccSecurity_SecretScrubbing"],
    "TenantIsolationVerified": ["TestAccSecurity_TenantIsolation"],
    "NonPersistenceVerified": [
        "TestAccEphemeral_IDToken",
        "TestAccEphemeral_TenantToken",
        "TestAccEphemeral_ClientCertificate",
        "TestAccEphemeral_BazelCache",
        "TestAccEphemeral_GradleCache",
        "TestAccEphemeral_HTTPCache",
        "TestAccEphemeral_DownstreamConsumption",
        "TestAccEphemeral_CacheLifecycle_PlanAndDeferral",
    ],
}

TEST_COMMIT_SHA = "0123456789abcdef0123456789abcdef01234567"
TEST_RUN_ID = "99999"
TEST_RUN_LINK = "https://github.com/bsuv/terraform-provider-namespace/actions/runs/99999"


def make_json_test_event(test_name, action, output_lines=None, elapsed=0.15):
    """Formats a series of go test -json lines for a test case."""
    lines = []
    lines.append(f'{{"Action":"run","Test":"{test_name}","Package":"github.com/bsuv/terraform-provider-namespace/internal/provider"}}')
    if output_lines:
        for line in output_lines:
            safe_line = line.replace('\\', '\\\\').replace('"', '\\"').replace('\n', '\\n')
            lines.append(f'{{"Action":"output","Test":"{test_name}","Package":"github.com/bsuv/terraform-provider-namespace/internal/provider","Output":"{safe_line}\\n"}}')
    lines.append(f'{{"Action":"{action}","Test":"{test_name}","Package":"github.com/bsuv/terraform-provider-namespace/internal/provider","Elapsed":{elapsed}}}')
    return "\n".join(lines)


def build_valid_matrix_log(overrides=None, omit_scenarios=None, include_global_assertions=True, omit_attestations=None):
    """Builds a complete valid matrix log with all critical and public-surface tests."""
    overrides = overrides or {}
    omit_scenarios = set(omit_scenarios or [])
    omit_attestations = set(omit_attestations or [])
    events = []

    emitted_tests = set()
    for scen in CRITICAL_SCENARIOS:
        name = scen["name"]
        if name in omit_scenarios:
            continue

        scen_data = dict(scen)
        if name in overrides:
            scen_data.update(overrides[name])

        output_lines = []
        action = scen_data.get("action", "pass")

        # Add observation if not suppressed
        if scen_data.get("rpc") is not None:
            output_lines.append(
                f"OBSERVATION: RPC={scen_data['rpc']} Endpoint={scen_data['endpoint']} "
                f"Status={scen_data['status']} Scope={scen_data['scope']}"
            )

        # Add required scenario assertion if present
        if scen_data.get("assertion"):
            output_lines.append(f"ASSERTION: {scen_data['assertion']}")

        for assertion in scen_data.get("extra_assertions", []):
            output_lines.append(f"ASSERTION: {assertion}")

        events.append(make_json_test_event(name, action, output_lines))
        emitted_tests.add(name)

    if include_global_assertions:
        for assertion, test_names in ATTESTATION_TEST_CONTRACTS.items():
            if assertion in omit_attestations:
                continue
            for test_name in test_names:
                if test_name in emitted_tests:
                    continue
                events.append(make_json_test_event(test_name, "pass", [
                    "OBSERVATION: RPC=TestFixture.LiveCall Endpoint=Global Status=codes.OK "
                    "Scope=Authorized acceptance evidence fixture",
                    f"ASSERTION: {assertion}",
                ]))
                emitted_tests.add(test_name)

    for test_name in REQUIRED_PUBLIC_SURFACE_TESTS:
        if test_name in emitted_tests:
            continue
        events.append(make_json_test_event(test_name, "pass", [
            "OBSERVATION: RPC=TestFixture.LiveCall Endpoint=Global Status=codes.OK "
            "Scope=First-release public surface live verification",
        ]))
        emitted_tests.add(test_name)

    return "\n".join(events)


class TestAcceptanceReportGeneratorAdversarial(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.mkdtemp(prefix="nsc_report_test_")
        self.output_dir = os.path.join(self.temp_dir, "output")
        os.makedirs(self.output_dir, exist_ok=True)

    def tearDown(self):
        shutil.rmtree(self.temp_dir, ignore_errors=True)

    def run_generator(self, input_content, extra_args=None):
        input_path = os.path.join(self.temp_dir, "test_input.log")
        with open(input_path, "w", encoding="utf-8") as f:
            f.write(input_content)

        cmd = [
            "bash",
            GENERATE_REPORT_SCRIPT,
            "--input", input_path,
            "--output-dir", self.output_dir,
            "--commit-sha", TEST_COMMIT_SHA,
            "--run-id", TEST_RUN_ID,
            "--run-link", TEST_RUN_LINK,
            "--suite", "matrix",
        ]
        if extra_args:
            cmd.extend(extra_args)

        proc = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

        report_md_path = os.path.join(self.output_dir, "acceptance-report.md")
        report_md = ""
        if os.path.exists(report_md_path):
            with open(report_md_path, "r", encoding="utf-8") as f:
                report_md = f.read()

        sanitized_log_path = os.path.join(self.output_dir, "sanitized-acceptance.log")
        sanitized_log = ""
        if os.path.exists(sanitized_log_path):
            with open(sanitized_log_path, "r", encoding="utf-8") as f:
                sanitized_log = f.read()

        return proc, report_md, sanitized_log

    def run_generator_stdin(self, input_content):
        cmd = [
            "bash",
            GENERATE_REPORT_SCRIPT,
            "--input", "-",
            "--output-dir", self.output_dir,
            "--commit-sha", TEST_COMMIT_SHA,
            "--run-id", TEST_RUN_ID,
            "--run-link", TEST_RUN_LINK,
            "--suite", "matrix",
        ]
        proc = subprocess.run(cmd, input=input_content, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        report_md_path = os.path.join(self.output_dir, "acceptance-report.md")
        report_md = ""
        if os.path.exists(report_md_path):
            with open(report_md_path, "r", encoding="utf-8") as f:
                report_md = f.read()
        return proc, report_md

    # 1. Complete valid matrix run succeeds (exit 0, overall PASS, attestations emitted)
    def test_01_full_valid_test_run_succeeds(self):
        log_content = build_valid_matrix_log()
        proc, report_md, sanitized_log = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 0, f"Expected exit 0, got {proc.returncode}. Stderr: {proc.stderr}")
        self.assertIn("| **Overall Status** | ✅ PASS |", report_md)
        self.assertIn("Secret Scrubbing:", report_md)
        self.assertNotIn("⚠️ Unverified: No explicit passing SecretScrubbingVerified", report_md)
        self.assertNotIn("⚠️ Unverified: No explicit passing NonPersistenceVerified", report_md)
        self.assertNotIn("⚠️ Unverified: No explicit passing TenantIsolationVerified", report_md)
        self.assertTrue(os.path.exists(os.path.join(self.output_dir, "junit.xml")))
        self.assertTrue(os.path.exists(os.path.join(self.output_dir, "sanitized-acceptance.log")))

    # 2. Fabricated RPC fails Gate 3 (exit 1, overall FAIL)
    def test_02_fabricated_rpc_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={
                "TestAccMatrix_DriftDetection": {
                    "rpc": "FakeService.FakeMethod",
                }
            }
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for fabricated RPC")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("RPC mismatch", report_md)
        self.assertIn("contract violation", report_md)

    # 3. Wrong endpoint fails Gate 3 (exit 1, overall FAIL)
    def test_03_wrong_endpoint_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={
                "TestAccMatrix_CollisionPrevention": {
                    "endpoint": "Global",  # Contract requires "IAM"
                }
            }
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for wrong endpoint")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Endpoint mismatch", report_md)
        self.assertIn("contract violation", report_md)

    # 4. Invalid/mismatched status fails Gate 3 (exit 1, overall FAIL)
    def test_04_invalid_status_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={
                "TestAccMatrix_ConcurrencyConflict": {
                    "status": "codes.OK",  # Contract requires "codes.FailedPrecondition"
                }
            }
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for status mismatch")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Status mismatch", report_md)
        self.assertIn("contract violation", report_md)

    # 5. Missing critical test fails Gate 3 (exit 1, overall FAIL)
    def test_05_missing_critical_test_fails_gate_3(self):
        log_content = build_valid_matrix_log(omit_scenarios=["TestAccMatrix_PaginationTraversal"])
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for missing critical test")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Critical matrix scenario(s) missing from test execution output", report_md)
        self.assertIn("TestAccMatrix_PaginationTraversal", report_md)

    # 6. Failed critical test fails Gate 3 (exit 1, overall FAIL)
    def test_06_failed_critical_test_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={
                "TestAccMatrix_ExternalDeletion": {
                    "action": "fail",
                }
            }
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for failed critical test")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Critical matrix scenario 'TestAccMatrix_ExternalDeletion' did not pass", report_md)

    # 7. Skipped critical test fails Gate 3 (exit 1, overall FAIL)
    def test_07_skipped_critical_test_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={
                "TestAccMatrix_ExpirationDisappearance": {
                    "action": "skip",
                }
            }
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for skipped critical test")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Critical matrix scenario 'TestAccMatrix_ExpirationDisappearance' did not pass", report_md)

    # 8. Missing assertion fails Gate 3 (exit 1, overall FAIL)
    def test_08_missing_assertion_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={
                "TestAccMatrix_PaginationTraversal": {
                    "assertion": None,  # Suppress PaginationMultipleCursorsVerified
                }
            }
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for missing required assertion")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("missing required passing assertion 'PaginationMultipleCursorsVerified'", report_md)

    # 9. Partial test suite fails Gate 3 (exit 1, overall FAIL)
    def test_09_partial_test_suite_fails_gate_3(self):
        # Only run 2 tests out of 8
        log_content = build_valid_matrix_log(
            omit_scenarios=[
                "TestAccMatrix_ExternalDeletion",
                "TestAccMatrix_CollisionPrevention",
                "TestAccMatrix_PaginationTraversal",
                "TestAccMatrix_ExpirationDisappearance",
                "TestAccProvider_AuthenticationPrecedence",
                "TestAccEphemeral_CacheLifecycle_PlanAndDeferral",
            ]
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for partial test suite")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Critical matrix scenario(s) missing from test execution output", report_md)

    # 10. Zero tests executed fails Gate 1 (exit 1, overall FAIL)
    def test_10_zero_tests_executed_fails_gate_1(self):
        log_content = ""
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for zero tests")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Gate 1: Zero tests ran in acceptance test suite", report_md)

    # 11. Test execution failure fails Gate 2 (exit 1, overall FAIL)
    def test_11_test_execution_failure_fails_gate_2(self):
        matrix_log = build_valid_matrix_log()
        failing_non_critical = make_json_test_event(
            "TestAccSomeOtherResource_Failure",
            "fail",
            ["resource failed to create on remote backend"],
        )
        log_content = f"{matrix_log}\n{failing_non_critical}"
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for failing test")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Gate 2: 1 test(s) failed in acceptance test suite", report_md)

    # 12. Missing authorized security evidence fails Gate 4 and suppresses attestation
    def test_12_missing_security_assertion_suppresses_attestation(self):
        log_content = build_valid_matrix_log(omit_attestations=["SecretScrubbingVerified"])
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, f"Expected exit 1, got {proc.returncode}. Stderr: {proc.stderr}")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("authorized SecretScrubbingVerified evidence contract was not satisfied", report_md)
        self.assertIn("Gate 4: attestation 'SecretScrubbingVerified'", report_md)
        self.assertNotIn("- **Non-Persistence:** ⚠️ Unverified", report_md)
        self.assertNotIn("- **Tenant Isolation:** ⚠️ Unverified", report_md)

    # 13. Missing authorized isolation evidence fails Gate 4 and suppresses attestation
    def test_13_missing_isolation_assertion_suppresses_attestation(self):
        log_content = build_valid_matrix_log(omit_attestations=["TenantIsolationVerified"])
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, f"Expected exit 1, got {proc.returncode}. Stderr: {proc.stderr}")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("authorized TenantIsolationVerified evidence contract was not satisfied", report_md)
        self.assertIn("Gate 4: attestation 'TenantIsolationVerified'", report_md)
        self.assertNotIn("- **Secret Scrubbing:** ⚠️ Unverified", report_md)
        self.assertNotIn("- **Non-Persistence:** ⚠️ Unverified", report_md)

    # 14. Secret tokens/keys are thoroughly redacted in generated report and sanitized log
    def test_14_secret_tokens_keys_thoroughly_redacted(self):
        raw_token = "nsct_unredacted_sensitive_tenant_token_123456789"
        raw_workload_token = "nscw_workload_secret_token_987654321"
        # Assemble the synthetic JWT at runtime so the repository secret scanner
        # can reject every complete JWT-shaped literal without exempting tests.
        raw_jwt = ".".join([
            "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9",
            "eyJzdWIiOiIxMjM0NTY3ODkwIiwidGVuYW50X2lkIjoidGVzdCJ9",
            "signature_value_here",
        ])
        raw_rsa_key = "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Y1234567890abcdef==\n-----END RSA PRIVATE KEY-----"
        raw_password = 'password = "supersecretpassword123"'

        leaky_lines = [
            "OBSERVATION: RPC=ProfileService.UpdateProfile Endpoint=Global Status=codes.OK (Reconciled) Scope=Drift detection & HCL reconciliation",
            "ASSERTION: DriftDetectionVerified",
            f"Authorization: Bearer {raw_token}",
            f"Bearer {raw_jwt}",
            f"NSC_TOKEN={raw_workload_token}",
            raw_rsa_key,
            raw_password,
        ]

        matrix_log = build_valid_matrix_log(
            omit_scenarios=["TestAccMatrix_DriftDetection"],
        )
        # Insert leaky test event replacing TestAccMatrix_DriftDetection
        leaky_event = make_json_test_event("TestAccMatrix_DriftDetection", "pass", leaky_lines)
        log_content = f"{matrix_log}\n{leaky_event}"

        proc, report_md, sanitized_log = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 0, f"Expected exit 0, got {proc.returncode}. Stderr: {proc.stderr}")
        self.assertIn("| **Overall Status** | ✅ PASS |", report_md)

        # Verify raw secrets are strictly absent from sanitized log and report markdown
        self.assertNotIn(raw_token, sanitized_log)
        self.assertNotIn(raw_workload_token, sanitized_log)
        self.assertNotIn(raw_jwt, sanitized_log)
        self.assertNotIn("MIIEowIBAAKCAQEA0Y1234567890abcdef", sanitized_log)
        self.assertNotIn("supersecretpassword123", sanitized_log)

        self.assertNotIn(raw_token, report_md)
        self.assertNotIn(raw_workload_token, report_md)
        self.assertNotIn(raw_jwt, report_md)
        self.assertNotIn("supersecretpassword123", report_md)

        # Verify redaction placeholders are present
        self.assertIn("[REDACTED_TOKEN]", sanitized_log)
        self.assertIn("[REDACTED_KEY_OR_CERT]", sanitized_log)
        self.assertIn("[REDACTED_PASSWORD]", sanitized_log)

    # 15. A fabricated scope is a contract violation
    def test_15_fabricated_scope_fails_gate_3(self):
        log_content = build_valid_matrix_log(
            overrides={"TestAccMatrix_DriftDetection": {"scope": "Fabricated unrelated scope"}}
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for fabricated scope")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Scope mismatch", report_md)

    # 16. Package-level failures outside individual tests fail Gate 2 and JUnit
    def test_16_package_level_failure_fails_gate_2(self):
        package_failure = json.dumps({
            "Action": "fail",
            "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
            "Elapsed": 1.0,
        })
        log_content = f"{build_valid_matrix_log()}\n{package_failure}"
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for package-level failure")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("package-level test failure", report_md)
        with open(os.path.join(self.output_dir, "junit.xml"), "r", encoding="utf-8") as f:
            self.assertIn("package-level execution", f.read())

    # 17. Attestation markers emitted by unauthorized tests are rejected
    def test_17_misplaced_security_assertion_fails_gate_4(self):
        log_content = build_valid_matrix_log(
            overrides={"TestAccMatrix_DriftDetection": {"extra_assertions": ["SecretScrubbingVerified"]}},
            omit_attestations=["SecretScrubbingVerified"],
        )
        proc, report_md, _ = self.run_generator(log_content)

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for misplaced security assertion")
        self.assertIn("Gate 4: attestation 'SecretScrubbingVerified'", report_md)
        self.assertIn("authorized SecretScrubbingVerified evidence contract was not satisfied", report_md)

    # 18. Piped stdin input is supported despite the embedded Python heredoc
    def test_18_stdin_input_succeeds(self):
        proc, report_md = self.run_generator_stdin(build_valid_matrix_log())

        self.assertEqual(proc.returncode, 0, f"Expected exit 0, got {proc.returncode}. Stderr: {proc.stderr}")
        self.assertIn("| **Overall Status** | ✅ PASS |", report_md)

    # 19. Reports without immutable commit/run provenance cannot pass
    def test_19_invalid_provenance_fails_gate_5(self):
        proc, report_md, _ = self.run_generator(
            build_valid_matrix_log(),
            extra_args=["--commit-sha", "local-build", "--run-id", "unknown", "--run-link", ""],
        )

        self.assertEqual(proc.returncode, 1, "Expected exit 1 for invalid provenance")
        self.assertIn("| **Overall Status** | ❌ FAIL |", report_md)
        self.assertIn("Gate 5: commit provenance", report_md)
        self.assertIn("Gate 5: workflow run ID", report_md)

    # 20. The serialized singleton stage validates its own exact contract without full-suite attestations
    def test_20_singleton_suite_passes_exact_contract(self):
        singleton_log = make_json_test_event(
            "TestAccRegistryPolicy_Default",
            "pass",
            [
                "OBSERVATION: RPC=ContainerRegistryService.UpdateDefaultPolicy Endpoint=Global "
                "Status=codes.OK Scope=Default registry policy lifecycle and import verification"
            ],
        )
        proc, report_md, _ = self.run_generator(singleton_log, extra_args=["--suite", "singleton"])

        self.assertEqual(proc.returncode, 0, f"Expected singleton exit 0, got {proc.returncode}. Stderr: {proc.stderr}")
        self.assertIn("| **Overall Status** | ✅ PASS |", report_md)

    # 21. Full release mode cannot silently downgrade to a matrix run when the singleton is absent
    def test_21_full_suite_rejects_missing_singleton(self):
        proc, report_md, _ = self.run_generator(
            build_valid_matrix_log(),
            extra_args=["--suite", "full"],
        )

        self.assertEqual(proc.returncode, 1, "Expected full suite without singleton to fail")
        self.assertIn("full suite is missing TestAccRegistryPolicy_Default", report_md)

    # 22. A skipped retained public-object test is a release gate failure
    def test_22_skipped_public_surface_test_fails(self):
        skipped = make_json_test_event(
            "TestAccDataSource_VaultSecretMetadata",
            "skip",
            ["fixture unavailable"],
        )
        proc, report_md, _ = self.run_generator(f"{build_valid_matrix_log()}\n{skipped}")

        self.assertEqual(proc.returncode, 1, "Expected skipped public-surface test to fail")
        self.assertIn("TestAccDataSource_VaultSecretMetadata", report_md)
        self.assertIn("did not pass (status=SKIP)", report_md)

    # 23. Full release mode passes only with the exact serialized singleton contract included
    def test_23_full_suite_with_singleton_passes(self):
        singleton = make_json_test_event(
            "TestAccRegistryPolicy_Default",
            "pass",
            [
                "OBSERVATION: RPC=ContainerRegistryService.UpdateDefaultPolicy Endpoint=Global "
                "Status=codes.OK Scope=Default registry policy lifecycle and import verification"
            ],
        )
        proc, report_md, _ = self.run_generator(
            f"{build_valid_matrix_log()}\n{singleton}",
            extra_args=["--suite", "full"],
        )

        self.assertEqual(proc.returncode, 0, f"Expected complete full suite to pass: {proc.stderr}")
        self.assertIn("| **Overall Status** | ✅ PASS |", report_md)

    # 24. Opaque structured credentials are redacted without recognizable prefixes
    def test_24_opaque_structured_credentials_are_redacted(self):
        opaque_values = {
            "bearer": "opaque" + "BearerCredentialValue987654321",
            "session": "opaque" + "SessionCredentialValue987654321",
            "client_secret": "opaque" + "ClientCredentialValue987654321",
            "api_key": "opaque" + "ApiCredentialValue987654321",
            "private_key": "opaque" + "PrivateCredentialValue987654321",
            "basic": "opaque" + "BasicCredentialValue987654321",
            "query": "opaque" + "QueryCredentialValue987654321",
            "auth_token": "opaque" + "AuthTokenValue987654321",
            "credentials": "opaque" + "CredentialsValue987654321",
            "signing_key": "opaque" + "SigningKeyValue987654321",
            "certificate": "opaque" + "CertificateValue987654321",
            "passphrase": "opaque" + "PassphraseValue987654321",
        }
        structured_lines = [
            json.dumps({"bearerToken": opaque_values["bearer"]}),
            f"session_token={opaque_values['session']}",
            json.dumps({"client-secret": opaque_values["client_secret"]}),
            f"api_key: {opaque_values['api_key']}",
            f'privateKeyPem="{opaque_values["private_key"]}"',
            json.dumps({"Authorization": f"Basic {opaque_values['basic']}"}),
            f"https://example.invalid/path?token={opaque_values['query']}&safe=value",
            json.dumps({"authToken": opaque_values["auth_token"]}),
            f"credentials={opaque_values['credentials']}",
            json.dumps({"signingKey": opaque_values["signing_key"]}),
            f"certificate={opaque_values['certificate']}",
            json.dumps({"passphrase": opaque_values["passphrase"]}),
        ]
        leaky_event = make_json_test_event(
            "TestAccSecurity_OpaqueStructuredSecretScrubbing",
            "pass",
            structured_lines,
        )
        proc, report_md, sanitized_log = self.run_generator(
            f"{build_valid_matrix_log()}\n{leaky_event}"
        )

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for value in opaque_values.values():
            self.assertNotIn(value, sanitized_log)
            self.assertNotIn(value, report_md)
        self.assertIn("[REDACTED_TOKEN]", sanitized_log)
        self.assertIn("[REDACTED_SECRET]", sanitized_log)
        self.assertIn("[REDACTED_PRIVATE_KEY]", sanitized_log)
        self.assertIn("safe=value", sanitized_log)

    # 25. Natural-language labels do not consume the following line or auth scheme
    def test_25_multiline_labels_and_bearer_scheme_remain_readable(self):
        raw_token = "opaque" + "BearerCredentialValue246813579"
        raw_key = (
            "-----BEGIN " + "RSA PRIVATE KEY-----\n"
            "synthetic-key-material\n"
            "-----END RSA PRIVATE KEY-----"
        )
        event = make_json_test_event(
            "TestAccSecurity_ReadableRedaction",
            "pass",
            [f"JWT: Bearer {raw_token}", f"Error loading client cert:\n{raw_key}"],
        )
        proc, _, sanitized_log = self.run_generator(
            f"{build_valid_matrix_log()}\n{event}"
        )

        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertNotIn(raw_token, sanitized_log)
        self.assertNotIn("synthetic-key-material", sanitized_log)
        self.assertIn("JWT: Bearer [REDACTED_TOKEN]", sanitized_log)
        self.assertIn("client cert:\\n[REDACTED_KEY_OR_CERT]", sanitized_log)


if __name__ == "__main__":
    unittest.main()
