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
[[ "${MOOX_BUSINESS_BINARY_DIRECTORY:-}" == /* && -d "${MOOX_BUSINESS_BINARY_DIRECTORY}" ]] || { echo 'MOOX_BUSINESS_BINARY_DIRECTORY must select prebuilt Access, egress, Trade and CGO Storage binaries' >&2; exit 1; }
export MOOX_BUSINESS_BINARY_DIRECTORY
[[ "${MOOX_FACTOR_PYTHON:-}" == /* && -x "${MOOX_FACTOR_PYTHON}" ]] || { echo 'MOOX_FACTOR_PYTHON must select a prepared real pandas/numpy interpreter for full control' >&2; exit 1; }
export MOOX_FACTOR_PYTHON
task_root="$(mktemp -d "${TMPDIR:-/tmp}/moox-native-core-gate.XXXXXXXX")"
trap 'rm -rf "${task_root}"' EXIT
"${MOOX_CORE_TEST_BINARY}" -test.v -test.count=1 -test.timeout=20m | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestNativeCoreBootstrapOverVerifiedSSHWithFullFleetTopology',
    'TestRuntimeIdentityIsPrivatePersistentAndBoundToTarget',
    'TestCoreRequestUsesConfiguredControlRootAndPrivateAdministrator',
    'TestCoreBuildUsesOnlyLocalPureGoAndFrontendTools',
    'TestHostBuildUsesOnlyLocalPureGoTools',
    'TestBusinessUnitSelectionAndExportRolesFollowActualPlacements',
    'TestStorageEnvironmentSharesOnlyRequiredPersistentCredentials',
    'TestControlConfigurationsPreserveBusinessSettingsAndBindActualEndpoints',
    'TestControlEnvironmentDistributesOnlyRequiredStorageRoleSecrets',
    'TestControlPreflightRejectsUnreadyAndUnrelatedRuntimeOrPython',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required native core scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('native core Linux gate must not skip or fail a scenario')
print('native core/host/business/full-control Linux gate passed: verified SSH/SFTP, nested control root, trusted HTTPS page/login, all nine real control services, business units including CGO Storage initialization, immutable retries, export recovery, pause/repair, original authorization binding, local build policy; no compilation')
PY
