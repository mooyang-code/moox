#!/usr/bin/env bash
set -euo pipefail

[[ "$(uname -s)" == Linux ]] || { echo 'bootstrap gate must execute on Linux' >&2; exit 1; }
for variable in MOOX_BOOTSTRAP_TEST_BINARY MOOX_RUNTIME_BINARY; do
  [[ "${!variable:-}" == /* && -f "${!variable}" && -x "${!variable}" ]] || { echo "${variable} must select a prebuilt absolute executable" >&2; exit 1; }
done
for variable in MOOX_BOOTSTRAP_HOST_ARCHIVE MOOX_BOOTSTRAP_CONTROL_ARCHIVE; do
  [[ "${!variable:-}" == /* && -f "${!variable}" ]] || { echo "${variable} must select an existing absolute archive" >&2; exit 1; }
done
for variable in MOOX_BOOTSTRAP_HOST_SHA256 MOOX_BOOTSTRAP_CONTROL_SHA256; do
  [[ "${!variable:-}" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "${variable} must be a producer-provided digest" >&2; exit 1; }
done
export MOOX_RUNTIME_BINARY MOOX_BOOTSTRAP_HOST_ARCHIVE MOOX_BOOTSTRAP_CONTROL_ARCHIVE
export MOOX_BOOTSTRAP_HOST_SHA256 MOOX_BOOTSTRAP_CONTROL_SHA256
task_root="$(mktemp -d /tmp/moox-unit-bootstrap-gate.XXXXXXXX)"
trap 'rm -rf "${task_root}"' EXIT
"${MOOX_BOOTSTRAP_TEST_BINARY}" -test.v -test.count=1 -test.timeout=10m | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestBootstrapRequestRejectsUnsafeAndAmbiguousPrivateInput',
    'TestBootstrapPublicOutputBoundAndJournalIdentities',
    'TestBootstrapRuntimeIdentityCannotChangeAfterInitialization',
    'TestBootstrapLinuxActualAdminGatewayAndServicesRecoverTogether',
    'TestBootstrapLinuxSIGKILLAtAdminStartupRecoversWholeHost',
    'TestBootstrapLinuxOfflineChildHoldsLockAndDiesWithParent',
    'TestBootstrapLinuxProxyCAConsumesAuthorizationAndImportsTrustedLegacy',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required bootstrap scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('bootstrap Linux gate must not skip or fail a scenario')
print('bootstrap Linux gate passed: 7 required actual initialization/recovery/CA/runtime-identity/private-input scenarios, no compilation')
PY
