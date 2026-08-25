#!/usr/bin/env bash
# ==============================================================================
# Namespace Terraform Provider — Adversarial Acceptance Report Verification Runner
# Executes the Python adversarial test suite for scripts/generate-acceptance-report.sh
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> Running adversarial acceptance report generator test suite..."
python3 "${SCRIPT_DIR}/test_acceptance_report_generator.py" -v

echo "==> All adversarial acceptance report generator tests passed successfully!"
