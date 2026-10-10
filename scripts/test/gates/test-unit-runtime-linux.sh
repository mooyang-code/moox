#!/usr/bin/env bash
set -euo pipefail

[[ "$(uname -s)" == Linux ]] || { echo 'unit runtime gate must execute on Linux' >&2; exit 1; }
for variable in MOOX_UNIT_RUNTIME_TEST_BINARY MOOX_RUNTIME_BINARY MOOX_RUNTIME_PROXY_BINARY; do
  [[ -n "${!variable:-}" && "${!variable}" == /* && -f "${!variable}" && -x "${!variable}" ]] || {
    echo "${variable} must select an existing prebuilt absolute executable" >&2
    exit 1
  }
done
export MOOX_RUNTIME_BINARY MOOX_RUNTIME_PROXY_BINARY
task_root="$(mktemp -d /tmp/moox-unit-runtime-gate.XXXXXXXX)"
trap 'rm -rf "${task_root}"' EXIT
"${MOOX_RUNTIME_BINARY}" version >"${task_root}/runtime-version.json"
"${MOOX_UNIT_RUNTIME_TEST_BINARY}" -test.v -test.count=1 -test.timeout=6m | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestRuntimeLinuxInterruptedBootstrapBlocksAllUnitsAndScopedRecovery',
    'TestRuntimeLinuxInterruptedInstallationBlocksAutomaticStartsUntilRecovery',
    'TestRuntimeLinuxScopedMaintenanceSerializesAndBoundsLifecycle',
    'TestRuntimeLinuxProcessIdentityRemainsCoherentAcrossExec',
    'TestRuntimeLinuxPausePersistsAcrossReleaseAndAllAutomaticStarts',
    'TestRuntimeLinuxUsesRunningReleaseBudgetBeforeForceAndKeepsDrainOnCancellation',
    'TestRuntimeLinuxForceWaitsFullBudgetAndObservesExit',
    'TestRuntimeLinuxWatchdogUsesLivenessAndDoesNotRestartForReadinessOrBadCredentials',
    'TestRuntimeLinuxMaintenanceLockSkipCancellationAndInheritedDescriptor',
    'TestRuntimeLinuxWatchdogRestartsFailedLivenessWithRunningGraceBudget',
    'TestRuntimeLinuxCancelledPauseMarksAllTargetsAndHostIdentityIsPersistent',
    'TestRuntimeLinuxRefusesUnrelatedOrReusedPIDAndDoesNotExposeSecrets',
    'TestRuntimeLinuxRealProxyDrainsHTTPSAndPreservesCAWhilePaused',
    'TestRuntimeLinuxStartBarrierRejectsUnrecordedChildAndRecoversDurableLaunch',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required runtime scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('runtime Linux gate must not skip or fail a scenario')
print('unit runtime Linux gate passed: 14 required kernel/real-proxy scenarios, no compilation')
PY
