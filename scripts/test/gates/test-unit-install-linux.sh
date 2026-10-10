#!/usr/bin/env bash
set -euo pipefail

[[ "$(uname -s)" == Linux ]] || { echo 'unit installation gate must execute on Linux' >&2; exit 1; }
for variable in MOOX_UNIT_INSTALL_TEST_BINARY MOOX_RUNTIME_BINARY; do
  [[ "${!variable:-}" == /* && -f "${!variable}" && -x "${!variable}" ]] || { echo "${variable} must select a prebuilt absolute executable" >&2; exit 1; }
done
for variable in MOOX_HOST_MATERIAL_FIXTURE MOOX_UNIT_INSTALL_HOST_ARCHIVE MOOX_UNIT_INSTALL_ACCESS_ARCHIVE; do
  [[ "${!variable:-}" == /* && -f "${!variable}" ]] || { echo "${variable} must select an existing absolute input file" >&2; exit 1; }
done
for variable in MOOX_UNIT_INSTALL_HOST_SHA256 MOOX_UNIT_INSTALL_ACCESS_SHA256; do
  [[ "${!variable:-}" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "${variable} must be a producer-provided SHA256" >&2; exit 1; }
done
export MOOX_RUNTIME_BINARY MOOX_HOST_MATERIAL_FIXTURE
export MOOX_UNIT_INSTALL_HOST_ARCHIVE MOOX_UNIT_INSTALL_HOST_SHA256 MOOX_UNIT_INSTALL_ACCESS_ARCHIVE MOOX_UNIT_INSTALL_ACCESS_SHA256
task_root="$(mktemp -d /tmp/moox-unit-install-gate.XXXXXXXX)"
trap 'rm -rf "${task_root}"' EXIT
"${MOOX_UNIT_INSTALL_TEST_BINARY}" -test.v -test.count=1 -test.timeout=6m | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestConfigurationRenderingRejectsAmbiguousPrivateYAML',
    'TestPreparationRequestIsPrivateCanonicalAndDoesNotEchoInputs',
    'TestGeneratedLifecycleScriptTreatsHostPathsLiterally',
    'TestUnitPreparationLinuxUsesActualHostSoftwareAndPreservesPause',
    'TestUnitPreparationLinuxBusinessUsesHostViewAndOnlyAccessIdentities',
    'TestUnitPreparationLinuxFailureDoesNotPublishOrReplaceCurrent',
    'TestUnitPreparationLinuxReceiptRejectsChangedFilesAndEscapingHostView',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required installation scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('unit installation Linux gate must not skip or fail a scenario')
print('unit installation Linux gate passed: 7 required preparation/script scenarios, no compilation')
PY
