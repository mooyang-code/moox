#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CNB_CONFIG="${ROOT}/.cnb/security/code_scan_config.yml"
CNB_IGNORE="${ROOT}/.scanignore"
CNB_PIPELINE="${ROOT}/.cnb.yml"
GITHUB_CI="${ROOT}/.github/workflows/ci.yml"

test -s "$CNB_CONFIG"
grep -Fxq 'secrets:' "$CNB_CONFIG"
grep -Fxq '  scan:' "$CNB_CONFIG"
grep -Fxq '    ignoreFrom: .scanignore' "$CNB_CONFIG"
test -f "$CNB_IGNORE"
! grep -Fxq '**' "$CNB_IGNORE"

grep -Fq 'pull_request:' "$CNB_PIPELINE"
grep -Fq 'ghcr.io/gitleaks/gitleaks:v8.30.1' "$CNB_PIPELINE"
grep -Fq -- '--redact' "$CNB_PIPELINE"
grep -Fq -- '--exit-code' "$CNB_PIPELINE"
grep -Fq -- '--log-opts=$CNB_PULL_REQUEST_TARGET_SHA..$CNB_PULL_REQUEST_SHA' "$CNB_PIPELINE"
grep -Fq 'cnbcool/code-security-config' "$CNB_PIPELINE"
grep -Fq 'name: Secret scan' "$GITHUB_CI"
grep -Fq 'if: github.event_name == '\''pull_request'\''' "$GITHUB_CI"
grep -Fq 'fetch-depth: 0' "$GITHUB_CI"
grep -Fq 'uses: docker://ghcr.io/gitleaks/gitleaks:v8.30.1' "$GITHUB_CI"
grep -Fq 'github.event.pull_request.base.sha' "$GITHUB_CI"
grep -Fq 'github.event.pull_request.head.sha' "$GITHUB_CI"

echo 'secret scan contract passed'
