#!/usr/bin/env bash
set -euo pipefail

[[ "$(uname -s)" == Linux ]] || { echo 'native core gate must execute on Linux' >&2; exit 1; }
for variable in MOOX_CORE_TEST_BINARY MOOX_RUNTIME_BINARY; do
  [[ "${!variable:-}" == /* && -f "${!variable}" && -x "${!variable}" ]] || { echo "${variable} must select a prebuilt absolute executable" >&2; exit 1; }
done
for variable in MOOX_BOOTSTRAP_HOST_ARCHIVE MOOX_BOOTSTRAP_CONTROL_ARCHIVE; do
  [[ "${!variable:-}" == /* && -f "${!variable}" ]] || { echo "${variable} must select an existing prebuilt archive" >&2; exit 1; }
done
export MOOX_RUNTIME_BINARY MOOX_BOOTSTRAP_HOST_ARCHIVE MOOX_BOOTSTRAP_CONTROL_ARCHIVE
[[ "${MOOX_CORE_SOURCE_ROOT:-}" == /* && -d "${MOOX_CORE_SOURCE_ROOT}/.git" ]] || { echo 'MOOX_CORE_SOURCE_ROOT must select public configuration templates in a synthetic Git repository' >&2; exit 1; }
export MOOX_CORE_SOURCE_ROOT
task_root="$(mktemp -d "${TMPDIR:-/tmp}/moox-native-core-gate.XXXXXXXX")"
trap 'rm -rf "${task_root}"' EXIT
"${MOOX_CORE_TEST_BINARY}" -test.v -test.count=1 -test.timeout=12m | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestNativeCoreBootstrapOverVerifiedSSHWithFullFleetTopology',
    'TestRuntimeIdentityIsPrivatePersistentAndBoundToTarget',
    'TestCoreBuildUsesOnlyLocalPureGoAndFrontendTools',
    'TestHostBuildUsesOnlyLocalPureGoTools',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required native core scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('native core Linux gate must not skip or fail a scenario')
print('native core/host Linux gate passed: verified SSH/SFTP, actual core and host deployment, export recovery, pause and repair, fresh candidate after rollback, local build policy; no compilation')
PY
