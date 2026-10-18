#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

[[ "$(uname -s)" == Linux ]] || { echo 'unit package gate must execute on Linux' >&2; exit 1; }
for variable in MOOX_UNIT_PACKAGE_TEST_BINARY MOOX_RUNTIME_BINARY; do
  [[ -n "${!variable:-}" && "${!variable}" == /* && -f "${!variable}" && -x "${!variable}" ]] || {
    echo "${variable} must select an existing prebuilt absolute executable" >&2
    exit 1
  }
done
[[ -n "${MOOX_HOST_SOFTWARE_PACKAGE:-}" && "${MOOX_HOST_SOFTWARE_PACKAGE}" == /* && -f "${MOOX_HOST_SOFTWARE_PACKAGE}" ]] || {
  echo 'MOOX_HOST_SOFTWARE_PACKAGE must select an actual prebuilt host archive' >&2
  exit 1
}
[[ "${MOOX_HOST_PACKAGE_SHA256:-}" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'host package producer sha256 required' >&2; exit 1; }
export MOOX_RUNTIME_BINARY MOOX_HOST_SOFTWARE_PACKAGE MOOX_HOST_PACKAGE_SHA256
task_root="$(mktemp -d /tmp/moox-unit-package-gate.XXXXXXXX)"
trap 'rm -rf "${task_root}"' EXIT
(
  cd "${ROOT}/modules/cli/internal/setup/unitpackage"
  "${MOOX_UNIT_PACKAGE_TEST_BINARY}" -test.v -test.count=1 -test.timeout=6m
) | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestUnitPackagesHaveExactComponentBoundariesAndRepeatableDigests',
    'TestUnitPackagingExcludesIgnoredCredentialsAndRejectsSourceLinks',
    'TestUnitPackagingRejectsWrongTargetAndPreservesSourcesAndOutputs',
    'TestUnitInspectionRejectsTamperingAndProfileEscapes',
    'TestUnitInspectionRejectsDataAfterTarTerminator',
    'TestUnitExtractionPublishesVerifiedSoftwareForAllProfiles',
    'TestUnitExtractionRejectsUnexpectedMetadataAndCancellation',
    'TestUnitExtractionPreservesExistingObjectsAndConcurrentWinner',
    'TestUnitLinuxRuntimeExtractsSoftwareAndRejectsOtherArchitecture',
    'TestUnitLinuxRuntimeExtractsActualHostPackage',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required unit package scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('unit package Linux gate must not skip or fail a scenario')
print('unit package Linux gate passed: 10 required software/extraction scenarios, no compilation')
PY
