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

    # 26. Interleaved multiline PEM across parallel JSON events cannot swallow a failing test
    def test_26_interleaved_multiline_pem_preserves_parallel_test_failure(self):
        pkg = "github.com/bsuv/terraform-provider-namespace/internal/provider"
        interleaved = "\n".join([
            json.dumps({"Action": "run", "Test": "TestAccParallelPEM_A", "Package": pkg}),
            json.dumps({"Action": "run", "Test": "TestAccParallelFail_B", "Package": pkg}),
            json.dumps({"Action": "output", "Test": "TestAccParallelPEM_A", "Package": pkg, "Output": "-----BEGIN " + "RSA PRIVATE KEY-----\n"}),
            json.dumps({"Action": "output", "Test": "TestAccParallelFail_B", "Package": pkg, "Output": "fatal failure in TestB\n"}),
            json.dumps({"Action": "fail", "Test": "TestAccParallelFail_B", "Package": pkg, "Elapsed": 0.12}),
            json.dumps({"Action": "output", "Test": "TestAccParallelPEM_A", "Package": pkg, "Output": "MIIEsecretParallelKeyBodyXYZ123\n"}),
            json.dumps({"Action": "output", "Test": "TestAccParallelPEM_A", "Package": pkg, "Output": "-----END RSA PRIVATE KEY-----\n"}),
            json.dumps({"Action": "pass", "Test": "TestAccParallelPEM_A", "Package": pkg, "Elapsed": 0.25}),
        ])
        proc, report_md, sanitized_log = self.run_generator(f"{build_valid_matrix_log()}\n{interleaved}")

        self.assertEqual(proc.returncode, 1, "Expected Gate 2 failure for interleaved failing test")
        self.assertIn("Gate 2: 1 test(s) failed", report_md)
        self.assertIn("TestAccParallelFail_B", report_md)
        self.assertNotIn("MIIEsecretParallelKeyBodyXYZ123", sanitized_log)
        self.assertNotIn("MIIEsecretParallelKeyBodyXYZ123", report_md)

    # 27. Word-bounded st_ redaction does not corrupt normal identifiers
    def test_27_st_prefix_does_not_corrupt_normal_identifiers(self):
        event = make_json_test_event(
            "TestAccSecurity_IdentifierIntegrity",
            "pass",
            [
                "manifest_file=ok host_name=example test_token=ok latest_version=1 list_images=2 request_id=abc",
                "leaked_session=st_real_session_secret_999",
            ],
        )
        proc, report_md, sanitized_log = self.run_generator(f"{build_valid_matrix_log()}\n{event}")

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for ident in ("manifest_file", "host_name", "latest_version", "list_images", "request_id"):
            self.assertIn(ident, sanitized_log)
        self.assertNotIn("st_real_session_secret_999", sanitized_log)
        self.assertNotIn("st_real_session_secret_999", report_md)

    # 28. Env vars, escaped/double-escaped quotes, URL userinfo, PGP, JSON arrays, and tenant_token redact cleanly
    def test_28_env_vars_escaped_quotes_and_url_userinfo_preserve_json_and_redact(self):
        pgp_block = (
            "-----BEGIN " + "PGP PRIVATE KEY BLOCK-----\n"
            "secret-pgp-armor-body-777\n"
            "-----END PGP PRIVATE KEY BLOCK-----"
        )
        secrets = [
            "partnerSecretEnv44",
            "EscapedPassTail987",
            "DoubleEscTok42",
            "URLPassSecret55",
            "ShortArrTok123",
            "BasicArrCred456",
            "secret-pgp-armor-body-777",
            "opaqueTenantVal88",
            "SingleQuotePassTail66",
        ]
        lines = [
            f"TF_VAR_nsc_partner_token={secrets[0]}",
            f'{{"password":"abc\\"{secrets[1]}"}}',
            f'{{\\"bearer_token\\":\\"{secrets[2]}\\"}}',
            f"https://admin:{secrets[3]}@global.namespaceapis.com:443",
            f'{{"authorization":["Bearer {secrets[4]}","Basic {secrets[5]}"]}}',
            pgp_block,
            f"tenant_token={secrets[7]}",
            f'{{"password":"my\'{secrets[8]}"}}',
        ]
        event = make_json_test_event("TestAccSecurity_ExtendedSanitization", "pass", lines)
        proc, report_md, sanitized_log = self.run_generator(f"{build_valid_matrix_log()}\n{event}")

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in secrets:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)
        # Ensure every emitted JSON line in sanitized_log remains valid JSON
        for line in sanitized_log.strip().splitlines():
            json.loads(line)

    # 29. Non-JSON stderr lines captured via 2>&1 alongside JSON events are sanitized cleanly
    def test_29_stderr_lines_interleaved_in_json_stream_are_sanitized(self):
        stderr_tok = "nsct_" + "stderr_val_123456"
        stderr_pass = "stderr-pass-99"
        stderr_line = f"warning: leaked NSC_TOKEN={stderr_tok} and https://u:{stderr_pass}@host:443"
        proc, report_md, sanitized_log = self.run_generator(f"{build_valid_matrix_log()}\n{stderr_line}")

        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertNotIn(stderr_tok, sanitized_log)
        self.assertNotIn(stderr_pass, sanitized_log)
        self.assertNotIn(stderr_tok, report_md)
        self.assertIn("NSC_TOKEN=[REDACTED]", sanitized_log)

    # 30. Opaque nsrt_ tokens, NSC_TOKEN_SPEC*, and prefixed JWTs (oidc_eyJ..., cognito_eyJ...) are redacted
    def test_30_nsrt_token_spec_and_prefixed_jwts_redacted(self):
        raw_jwt = ".".join(["eyJhbGciOiJIUzI1NiJ9", "eyJzdWIiOiJ1c2VyXzEyMyJ9", "sig_value_abcdef123456"])
        nsrt_opaque = "nsrt_" + "revokable_opaque_token_123456789"
        nsrt_jwt = "nsrt_" + raw_jwt
        oidc_jwt = "oidc_" + raw_jwt
        cognito_jwt = "cognito_" + raw_jwt
        spec_val = "nsrt_" + "spec_inline_secret_123456"
        spec_file = "/run/secrets/nsc-token-spec.json"

        events = [
            json.dumps({
                "Time": "2026-01-01T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_TokenRedaction",
                "Output": (
                    f"opaque={nsrt_opaque} jwt={nsrt_jwt} oidc={oidc_jwt} cognito={cognito_jwt} "
                    f"NSC_TOKEN_SPEC={spec_val} NSC_TOKEN_SPEC_FILE={spec_file}\n"
                ),
            }),
            json.dumps({
                "Time": "2026-01-01T00:00:01Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_TokenRedaction",
                "Output": f"quoted_spec NSC_TOKEN_SPEC='{{\"token\":\"{spec_val}\"}}' end\n",
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        secrets = [nsrt_opaque, nsrt_jwt, oidc_jwt, cognito_jwt, spec_val, spec_file, raw_jwt]
        for secret in secrets:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        self.assertIn("[REDACTED_TOKEN]", sanitized_log)
        self.assertIn("[REDACTED_JWT]", sanitized_log)
        self.assertIn("NSC_TOKEN_SPEC=[REDACTED]", sanitized_log)
        self.assertIn("NSC_TOKEN_SPEC_FILE=[REDACTED]", sanitized_log)
        self.assertIn("- **Secret Scrubbing:** All bearer tokens", report_md)
        for line in sanitized_log.strip().splitlines():
            json.loads(line)

    # 31. Padded base64url JWTs and colon-delimited / unquoted JSON NSC_TOKEN_SPEC* are redacted
    def test_31_padded_jwts_and_nsc_token_spec_json_redacted(self):
        padded_jwt = ".".join(["ey" + "JhbGciOiJIUzI1NiJ9", "ey" + "JzdWIiOiIxMjMifQ==", "c2ln=="])
        oidc_padded = "oidc_" + padded_jwt
        nsrt_padded = "nsrt_" + padded_jwt
        spec_val = "nsrt_" + "json_spec_secret_987654"
        spec_file = "/run/secrets/nsc-private-spec.json"
        unquoted_path = "/run/secrets/nsc-unquoted-spec.json"

        events = [
            json.dumps({
                "Time": "2026-01-01T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_PaddedJWTAndSpecJSON",
                "Output": f"bare={padded_jwt} oidc={oidc_padded} nsrt={nsrt_padded}\n",
            }),
            json.dumps({
                "Time": "2026-01-01T00:00:01Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_PaddedJWTAndSpecJSON",
                "Output": (
                    f'{{"NSC_TOKEN_SPEC_FILE":"{spec_file}","NSC_TOKEN_SPEC":"{spec_val}"}} '
                    f'NSC_TOKEN_SPEC={{"file":"{unquoted_path}"}} ok=1\n'
                ),
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in [padded_jwt, oidc_padded, nsrt_padded, spec_val, spec_file, unquoted_path]:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        self.assertIn("bare=[REDACTED_JWT] oidc=[REDACTED_JWT] nsrt=[REDACTED_TOKEN]", sanitized_log)
        self.assertIn("NSC_TOKEN_SPEC=[REDACTED] ok=1", sanitized_log)
        for line in sanitized_log.strip().splitlines():
            json.loads(line)

    # 32. Colon-delimited JSON object NSC_TOKEN_SPEC, 2-level nested NSC_TOKEN_SPEC=, escaped quotes, unclosed brace, and PASSPHRASE idempotency
    def test_32_colon_object_and_deep_nested_token_spec_redacted(self):
        colon_obj_secret = "/run/secrets/colon_obj_secret.json"
        deep_1 = "/run/secrets/deep_1.json"
        deep_2 = "/run/secrets/deep_2.json"
        escaped_secret = "/run/secrets/escaped_quote_secret.json"
        unclosed_secret = "nsrt_" + "unclosed_fallback_secret_12345"
        passphrase_val = "my_secret_passphrase_123"

        events = [
            json.dumps({
                "Time": "2026-01-01T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_NestedTokenSpecObject",
                "Output": (
                    f'{{"NSC_TOKEN_SPEC":{{"file":"{colon_obj_secret}"}}}} '
                    f'NSC_TOKEN_SPEC={{"auth":{{"nested":{{"file":"{deep_1}"}}}},"path":"{deep_2}"}} ok=1\n'
                ),
            }),
            json.dumps({
                "Time": "2026-01-01T00:00:01Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_NestedTokenSpecObject",
                "Output": (
                    f'{{"NSC_TOKEN_SPEC":{{"payload":"a\\"b}}c\\\\","file":"{escaped_secret}"}},"ok":1}} '
                    f'PASSPHRASE={passphrase_val} PRE_REDACTED=[REDACTED PASSWORD] PASSPHRASE=[REDACTED PASSWORD]\n'
                ),
            }),
            json.dumps({
                "Time": "2026-01-01T00:00:02Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_NestedTokenSpecObject",
                "Output": f'NSC_TOKEN_SPEC={{"unclosed":"{unclosed_secret}\nsafe=1\n',
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in [colon_obj_secret, deep_1, deep_2, escaped_secret, unclosed_secret, passphrase_val]:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        parsed_outputs = "\n".join(
            json.loads(line).get("Output", "")
            for line in sanitized_log.strip().splitlines()
        )
        self.assertIn('{"NSC_TOKEN_SPEC":"[REDACTED_TOKEN]"}', parsed_outputs)
        self.assertIn("NSC_TOKEN_SPEC=[REDACTED] ok=1", parsed_outputs)
        self.assertIn('{"NSC_TOKEN_SPEC":"[REDACTED_TOKEN]","ok":1}', parsed_outputs)
        self.assertIn("PASSPHRASE=[REDACTED]", parsed_outputs)
        self.assertIn("PASSPHRASE=[REDACTED PASSWORD]", parsed_outputs)
        self.assertNotIn("PASSWORD] PASSWORD]", parsed_outputs)
        self.assertIn("safe=1", parsed_outputs)

    # 33. JSON-escaped equals-delimited NSC_TOKEN_SPEC inside JSON logs, Go-redacted auth markers, parenthesized st_ tokens, and 2-pass idempotency
    def test_33_json_escaped_equals_token_spec_auth_markers_and_parenthesized_tokens(self):
        in_log_secret = "/run/secrets/in_log.json"
        in_log_nested_secret = "/run/secrets/in_log2.json"
        multiline_in_log_secret = "/run/secrets/multiline_in_log.json"
        tab_in_log_secret = "/run/secrets/tab_in_log.json"
        cr_in_log_secret = "/run/secrets/cr_in_log.json"
        multiline_in_log_nested_secret = "/run/secrets/multiline_in_log2.json"
        unclosed_tok = "nsrt_" + "unclosed_log_tok_12345"
        st_tok = "st_" + "1234567890abcdef"
        in_log_line = (
            '{"log":"env NSC_TOKEN_SPEC={\\"a\\":\\"x}y\\",\\"file\\":\\"'
            + in_log_secret
            + '\\"} failed"}'
        )
        multiline_in_log_line = (
            '{"log":"env NSC_TOKEN_SPEC={\\n  \\"a\\":\\"x}y\\",\\n  \\"file\\":\\"'
            + multiline_in_log_secret
            + '\\"\\n} failed"}'
        )
        tab_in_log_line = (
            '{"log":"env NSC_TOKEN_SPEC={\\t\\"a\\":\\"x}y\\",\\"file\\":\\"'
            + tab_in_log_secret
            + '\\"} failed"}'
        )
        cr_in_log_line = (
            '{"log":"env NSC_TOKEN_SPEC={\\r  \\"a\\":\\"x}y\\",\\r  \\"file\\":\\"'
            + cr_in_log_secret
            + '\\"\\r} failed"}'
        )
        nested_in_log_line = (
            '{"outer":"{\\"log\\":\\"env NSC_TOKEN_SPEC={\\\\\\"a\\\\\\":\\\\\\"x}y\\\\\\",\\\\\\"file\\\\\\":\\\\\\"'
            + in_log_nested_secret
            + '\\\\\\"} failed\\"}"}'
        )
        nested_multiline_in_log_line = (
            '{"outer":"{\\"log\\":\\"env NSC_TOKEN_SPEC={\\\\n  \\\\\\"a\\\\\\":\\\\\\"x}y\\\\\\",\\\\n  \\\\\\"file\\\\\\":\\\\\\"'
            + multiline_in_log_nested_secret
            + '\\\\\\"\\\\n} failed\\"}"}'
        )
        unclosed_in_log_line = (
            '{"log":"env NSC_TOKEN_SPEC={\\"a\\":\\"1\\",\\"tok\\":\\"'
            + unclosed_tok
            + '\\"","ok":1}'
        )
        auth_markers_line = (
            "authorization: Bearer [REDACTED TOKEN] "
            "authorization: Basic [REDACTED SECRET] "
            "authorization: [Bearer [REDACTED TOKEN]] "
            "authorization: [Basic [REDACTED SECRET]]"
        )
        json_auth_bearer_line = '{"log":"authorization: Bearer secret_token_12345"}'
        json_auth_basic_line = '{"log":"authorization: Basic dXNlcjpwYXNz"}'
        paren_token_line = f"(token: {st_tok}) failed"

        events = [
            json.dumps({
                "Time": "2026-11-20T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_JsonEscapedSpecAndMarkers",
                "Output": (
                    f"{in_log_line}\n{multiline_in_log_line}\n{tab_in_log_line}\n{cr_in_log_line}\n"
                    f"{nested_in_log_line}\n{nested_multiline_in_log_line}\n{unclosed_in_log_line}\n"
                    f"{auth_markers_line}\n{json_auth_bearer_line}\n{json_auth_basic_line}\n{paren_token_line}\n"
                ),
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in [
            in_log_secret,
            in_log_nested_secret,
            multiline_in_log_secret,
            tab_in_log_secret,
            cr_in_log_secret,
            multiline_in_log_nested_secret,
            unclosed_tok,
            st_tok,
            "secret_token_12345",
            "dXNlcjpwYXNz",
        ]:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        parsed_outputs = "\n".join(
            json.loads(line).get("Output", "")
            for line in sanitized_log.strip().splitlines()
        )
        self.assertEqual(parsed_outputs.count('{"log":"env NSC_TOKEN_SPEC=[REDACTED] failed"}'), 4)
        self.assertEqual(parsed_outputs.count('{"outer":"{\\"log\\":\\"env NSC_TOKEN_SPEC=[REDACTED] failed\\"}"}'), 2)
        self.assertIn('{"log":"env NSC_TOKEN_SPEC={\\"a\\":\\"1\\",\\"tok\\":\\"[REDACTED_TOKEN]\\"","ok":1}', parsed_outputs)
        self.assertIn("authorization: Bearer [REDACTED_TOKEN] authorization: Basic [REDACTED_SECRET]", parsed_outputs)
        self.assertIn("authorization: [Bearer [REDACTED_TOKEN]] authorization: [Basic [REDACTED_SECRET]]", parsed_outputs)
        self.assertIn('{"log":"authorization: Bearer [REDACTED_TOKEN]"}', parsed_outputs)
        self.assertIn('{"log":"authorization: Basic [REDACTED_SECRET]"}', parsed_outputs)
        self.assertNotIn("TOKEN] TOKEN]", parsed_outputs)
        self.assertNotIn("SECRET] SECRET]", parsed_outputs)
        self.assertIn("(token: [REDACTED_TOKEN]) failed", parsed_outputs)

        # Verify 2-pass idempotency of sanitized-acceptance.log
        proc2, _, sanitized_log2 = self.run_generator(sanitized_log)
        self.assertEqual(proc2.returncode, 0, proc2.stderr)
        self.assertEqual(sanitized_log2, sanitized_log)

    # 34. Quoted Bearer/Basic credentials under unquoted authorization keys and JSON escape boundaries
    def test_34_unquoted_auth_quoted_creds_and_json_escape_boundaries(self):
        short_bearer = "short_tok"
        long_basic = "dXNlcjpwYXNzd29yZF9sb25nX3NlY3JldF8xMjM0NTY3ODkwYWJjZGVm"
        short_basic = "dXNlcjpwYXNz"
        tok_nl_secret = "my_secret_tok"
        pass_quote_secret = "my_pass"
        env_quote_secret = "my_secret_val"

        raw_auth_bearer = f'authorization="Bearer {short_bearer}"'
        raw_auth_basic = f'authorization="Basic {long_basic}"'
        json_auth_bearer = '{"log":"authorization: \\"Bearer ' + short_bearer + '\\"","ok":1}'
        json_auth_basic = '{"log":"authorization: \\"Basic ' + short_basic + '\\"","ok":1}'
        prose_auth = 'authorization: "granted"'
        json_prose_auth = '{"log":"authorization: \\"granted\\"","ok":1}'
        json_tok_nl = '{"log":"token: ' + tok_nl_secret + '\\nnext_field: ok","ok":1}'
        json_pass_quote = '{"log":"got password=' + pass_quote_secret + '\\"}"}'
        json_env_quote = '{"log":"env NSC_TOKEN=' + env_quote_secret + '\\"}"}'

        events = [
            json.dumps({
                "Time": "2026-11-20T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_UnquotedAuthAndEscapeBoundaries",
                "Output": "\n".join([
                    raw_auth_bearer,
                    raw_auth_basic,
                    json_auth_bearer,
                    json_auth_basic,
                    prose_auth,
                    json_prose_auth,
                    json_tok_nl,
                    json_pass_quote,
                    json_env_quote,
                ]) + "\n",
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in [
            short_bearer,
            long_basic,
            short_basic,
            tok_nl_secret,
            pass_quote_secret,
            env_quote_secret,
        ]:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        parsed_outputs = []
        for line in sanitized_log.strip().splitlines():
            ev = json.loads(line)
            if ev.get("Test") == "TestAccAuth_UnquotedAuthAndEscapeBoundaries" and "Output" in ev:
                parsed_outputs.extend(ev["Output"].splitlines())

        self.assertIn('authorization="Bearer [REDACTED_TOKEN]"', parsed_outputs)
        self.assertIn('authorization="Basic [REDACTED_SECRET]"', parsed_outputs)
        self.assertIn('{"log":"authorization: \\"Bearer [REDACTED_TOKEN]\\"","ok":1}', parsed_outputs)
        self.assertIn('{"log":"authorization: \\"Basic [REDACTED_SECRET]\\"","ok":1}', parsed_outputs)
        self.assertIn('authorization: "granted"', parsed_outputs)
        self.assertIn('{"log":"authorization: \\"granted\\"","ok":1}', parsed_outputs)
        self.assertIn('{"log":"token: [REDACTED_TOKEN]\\nnext_field: ok","ok":1}', parsed_outputs)
        self.assertIn('{"log":"got password=[REDACTED_PASSWORD]\\"}"}', parsed_outputs)
        self.assertIn('{"log":"env NSC_TOKEN=[REDACTED]\\"}"}', parsed_outputs)

        for out_line in parsed_outputs:
            if out_line.startswith("{"):
                json.loads(out_line)

        proc2, _, sanitized_log2 = self.run_generator(sanitized_log)
        self.assertEqual(proc2.returncode, 0, proc2.stderr)
        self.assertEqual(sanitized_log2, sanitized_log)

    # 35. JSON-escape-prefixed secrets (\n, \r, \t, \b, \f, \u000a, \u0026), literal \\n boundary preservation, and query &safe=1 preservation
    def test_35_json_escape_prefixed_secrets_and_query_string_amp(self):
        raw_jwt = ".".join(["ey" + "JhbGciOiJIUzI1NiJ9", "ey" + "JzdWIiOiJlc2NfdGVzdCJ9", "esc_sig_123456"])
        nsrt_secret = "nsrt_" + "esc_revokable_secret_123456"
        nsct_secret = "nsct_" + "esc_cluster_secret_123456"
        oidc_jwt = "oidc_" + raw_jwt
        spec_file_secret = "/run/secrets/esc_private_spec.json"
        nsc_tok_secret = "esc_opaque_nsc_tok_val"
        spec_obj_secret = "/run/secrets/esc_obj1.json"
        tok_obj_secret = "/run/secrets/esc_obj2.json"
        bearer_short = "esc_short_tok"
        basic_secret = "dXNlcj" + "pwYXNzX2VzYw=="
        pass_secret = "esc_pass_secret_123"
        sec_secret = "esc_tab_secret_123"
        bearer_long = "1234567890abcdefghij"
        cr_nsrt = "nsrt_" + "cr_revokable_123456"
        bs_oidc = "oidc_" + ".".join(["ey" + "JhbGciOiJIUzI1NiJ9", "ey" + "JzdWIiOiJicyJ9", "bs_sig_123"])
        ff_pass = "ff_secret_pass_123"
        u0a_oidc = "oidc_" + ".".join(["ey" + "JhbGciOiJIUzI1NiJ9", "ey" + "JzdWIiOiJ1MGEifQ", "u0a_sig_123"])
        u26_pass = "u26_secret_pass_123"
        query_tok = "q_tok_123"
        query_pw = "q_pw_123"

        raw_lines = [
            f'{{"log":"line1\\n{nsrt_secret}"}}',
            f'{{"log":"line1\\n{nsct_secret}"}}',
            f'{{"log":"jwt:\\t{raw_jwt}"}}',
            f'{{"log":"jwt:\\n{oidc_jwt}"}}',
            f'{{"log":"env:\\nNSC_TOKEN_SPEC_FILE={spec_file_secret}"}}',
            f'{{"log":"env:\\nNSC_TOKEN={nsc_tok_secret}"}}',
            f'{{"log":"env:\\nNSC_TOKEN_SPEC={{\\"file\\":\\"{spec_obj_secret}\\"}}"}}',
            f'{{"log":"env:\\nNSC_TOKEN={{\\"file\\":\\"{tok_obj_secret}\\"}}"}}',
            f'{{"log":"hdr:\\nauthorization: Bearer {bearer_short}"}}',
            f'{{"log":"hdr:\\nauthorization: Basic {basic_secret}"}}',
            f'{{"log":"err:\\npassword={pass_secret}"}}',
            f'{{"log":"err:\\tsecret={sec_secret}"}}',
            f'{{"log":"hdr:\\nbearer {bearer_long}"}}',
            f'{{"log":"cr:\\r{cr_nsrt}"}}',
            f'{{"log":"bs:\\b{bs_oidc}"}}',
            f'{{"log":"ff:\\fpassword={ff_pass}"}}',
            f'{{"log":"u0a:\\u000a{u0a_oidc}"}}',
            f'{{"log":"https://example.invalid/?a=1\\u0026password={u26_pass}\\u0026safe=1"}}',
            '{"log":"literal:\\\\npassword=keep_literal_boundary"}',
            f'url=https://example.invalid/auth?token={query_tok}&password={query_pw}&safe=1',
        ]

        events = [
            json.dumps({
                "Time": "2026-11-20T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_JSONEscapePrefixesAndQueryAmp",
                "Output": "\n".join(raw_lines) + "\n",
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in [
            raw_jwt,
            nsrt_secret,
            nsct_secret,
            oidc_jwt,
            spec_file_secret,
            nsc_tok_secret,
            spec_obj_secret,
            tok_obj_secret,
            bearer_short,
            basic_secret,
            pass_secret,
            sec_secret,
            bearer_long,
            cr_nsrt,
            bs_oidc,
            ff_pass,
            u0a_oidc,
            u26_pass,
            query_tok,
            query_pw,
        ]:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        parsed_outputs = []
        for line in sanitized_log.strip().splitlines():
            ev = json.loads(line)
            if ev.get("Test") == "TestAccAuth_JSONEscapePrefixesAndQueryAmp" and "Output" in ev:
                parsed_outputs.extend(ev["Output"].splitlines())

        expected_outputs = [
            '{"log":"line1\\n[REDACTED_TOKEN]"}',
            '{"log":"line1\\n[REDACTED_TOKEN]"}',
            '{"log":"jwt:\\t[REDACTED_JWT]"}',
            '{"log":"jwt:\\n[REDACTED_JWT]"}',
            '{"log":"env:\\nNSC_TOKEN_SPEC_FILE=[REDACTED]"}',
            '{"log":"env:\\nNSC_TOKEN=[REDACTED]"}',
            '{"log":"env:\\nNSC_TOKEN_SPEC=[REDACTED]"}',
            '{"log":"env:\\nNSC_TOKEN=[REDACTED]"}',
            '{"log":"hdr:\\nauthorization: Bearer [REDACTED_TOKEN]"}',
            '{"log":"hdr:\\nauthorization: Basic [REDACTED_SECRET]"}',
            '{"log":"err:\\npassword=[REDACTED_PASSWORD]"}',
            '{"log":"err:\\tsecret=[REDACTED_SECRET]"}',
            '{"log":"hdr:\\nbearer [REDACTED_TOKEN]"}',
            '{"log":"cr:\\r[REDACTED_TOKEN]"}',
            '{"log":"bs:\\b[REDACTED_JWT]"}',
            '{"log":"ff:\\fpassword=[REDACTED_PASSWORD]"}',
            '{"log":"u0a:\\u000a[REDACTED_JWT]"}',
            '{"log":"https://example.invalid/?a=1\\u0026password=[REDACTED_PASSWORD]\\u0026safe=1"}',
            '{"log":"literal:\\\\npassword=keep_literal_boundary"}',
            'url=https://example.invalid/auth?token=[REDACTED_TOKEN]&password=[REDACTED_PASSWORD]&safe=1',
        ]
        for want in expected_outputs:
            self.assertIn(want, parsed_outputs)

        for out_line in parsed_outputs:
            if out_line.startswith("{"):
                json.loads(out_line)

        proc2, _, sanitized_log2 = self.run_generator(sanitized_log)
        self.assertEqual(proc2.returncode, 0, proc2.stderr)
        self.assertEqual(sanitized_log2, sanitized_log)

    # 36. Lowercase \uXXXb / \uXXXf unicode escape boundaries and URL query passphrase/password & boundary
    def test_36_lowercase_hex_bf_unicode_boundaries_and_query_passphrase(self):
        raw_jwt = ".".join(["ey" + "JhbGciOiJIUzI1NiJ9", "ey" + "JzdWIiOiJ1MzJmIn0", "u32f_sig_123456"])
        u3b_nsrt = "nsrt_" + "u3b_semi_secret_123456"
        u2f_oidc = "oidc_" + raw_jwt
        u3f_pw = "u3f_pw_secret_123"
        u3b_sec = "u3b_sec_secret_123"
        u3d_nsc_tok = "u3d_nsc_tok_123"
        u2b_bearer = "u2b_short_bearer_tok"
        q_pp = "pp_999"
        q_pw = "pw_888"

        raw_lines = [
            f'{{"msg":"a\\u003b{u3b_nsrt}"}}',
            f'{{"msg":"a\\u002f{u2f_oidc}"}}',
            f'{{"msg":"a\\u003fpassword={u3f_pw}"}}',
            f'{{"msg":"a\\u003bsecret={u3b_sec}"}}',
            f'{{"msg":"a\\u003dNSC_TOKEN={u3d_nsc_tok}\\u002bauthorization: Bearer {u2b_bearer}"}}',
            '{"msg":"literal:\\\\u003bpassword=keep_literal"}',
            '{"msg":"a\\u0062password=keep_bpass \\u0066password=keep_fpass"}',
            f'https://example.invalid/cb?foo=1&passphrase={q_pp}&password={q_pw}&bar=2',
            f'{{"cb":"https://example.invalid/cb?foo=1&passphrase={q_pp}&password={q_pw}&bar=2"}}',
        ]

        events = [
            json.dumps({
                "Time": "2026-11-20T00:00:00Z",
                "Action": "output",
                "Package": "github.com/bsuv/terraform-provider-namespace/internal/provider",
                "Test": "TestAccAuth_LowercaseHexBFAndQueryPassphrase",
                "Output": "\n".join(raw_lines) + "\n",
            }),
        ]
        log_input = f"{build_valid_matrix_log()}\n" + "\n".join(events)
        proc, report_md, sanitized_log = self.run_generator(log_input)

        self.assertEqual(proc.returncode, 0, proc.stderr)
        for secret in [
            raw_jwt,
            u3b_nsrt,
            u2f_oidc,
            u3f_pw,
            u3b_sec,
            u3d_nsc_tok,
            u2b_bearer,
            q_pp,
            q_pw,
        ]:
            self.assertNotIn(secret, sanitized_log)
            self.assertNotIn(secret, report_md)

        parsed_outputs = []
        for line in sanitized_log.strip().splitlines():
            ev = json.loads(line)
            if ev.get("Test") == "TestAccAuth_LowercaseHexBFAndQueryPassphrase" and "Output" in ev:
                parsed_outputs.extend(ev["Output"].splitlines())

        expected_outputs = [
            '{"msg":"a\\u003b[REDACTED_TOKEN]"}',
            '{"msg":"a\\u002f[REDACTED_JWT]"}',
            '{"msg":"a\\u003fpassword=[REDACTED_PASSWORD]"}',
            '{"msg":"a\\u003bsecret=[REDACTED_SECRET]"}',
            '{"msg":"a\\u003dNSC_TOKEN=[REDACTED]\\u002bauthorization: Bearer [REDACTED_TOKEN]"}',
            '{"msg":"literal:\\\\u003bpassword=keep_literal"}',
            '{"msg":"a\\u0062password=keep_bpass \\u0066password=keep_fpass"}',
            'https://example.invalid/cb?foo=1&passphrase=[REDACTED]&password=[REDACTED_PASSWORD]&bar=2',
            '{"cb":"https://example.invalid/cb?foo=1&passphrase=[REDACTED]&password=[REDACTED_PASSWORD]&bar=2"}',
        ]
        for want in expected_outputs:
            self.assertIn(want, parsed_outputs)

        for out_line in parsed_outputs:
            if out_line.startswith("{"):
                json.loads(out_line)

        proc2, _, sanitized_log2 = self.run_generator(sanitized_log)
        self.assertEqual(proc2.returncode, 0, proc2.stderr)
        self.assertEqual(sanitized_log2, sanitized_log)


if __name__ == "__main__":
    unittest.main()
