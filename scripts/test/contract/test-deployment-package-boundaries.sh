#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TASK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-package-boundary.XXXXXX")"
trap 'rm -rf "${TASK_ROOT}"' EXIT
mkdir -p "${TASK_ROOT}/repo/scripts/build" "${TASK_ROOT}/repo/bin"
python3 - "${ROOT}/scripts/deploy/deploy-moox.sh" "${TASK_ROOT}/build-function.sh" <<'PY'
from pathlib import Path
import sys
source=Path(sys.argv[1]).read_text()
start=source.index('build_core_binaries() {')
end=source.index('\nbuild_web_host_binary()',start)
Path(sys.argv[2]).write_text(source[start:end])
PY
cat >"${TASK_ROOT}/repo/scripts/build/build.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s/%s:%s\n' "${TARGET_GOOS}" "${TARGET_GOARCH}" "$1" >>"${MOOX_BOUNDARY_LOG}"
if [[ "$1" == cli ]]; then printf '#!/bin/sh\nexit 0\n' >"${MOOX_BOUNDARY_ROOT}/bin/moox-cli"; chmod +x "${MOOX_BOUNDARY_ROOT}/bin/moox-cli"; fi
EOF
cat >"${TASK_ROOT}/repo/scripts/build/build-storage-linux.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' remote-cgo-storage >>"${MOOX_BOUNDARY_LOG}"
EOF
chmod +x "${TASK_ROOT}/repo/scripts/build/"*.sh
for component in access factor-mgr storage; do
  export MOOX_BOUNDARY_ROOT="${TASK_ROOT}/repo"
  export MOOX_BOUNDARY_LOG="${TASK_ROOT}/${component}.log"
  : >"${MOOX_BOUNDARY_LOG}"
  (
    ROOT="${MOOX_BOUNDARY_ROOT}"
    SKIP_BUILD=0
    HOST_GOOS=darwin
    HOST_GOARCH=arm64
    TARGET_GOOS=linux
    TARGET_GOARCH=amd64
    for flag in STORAGE ACCESS FACTOR_MGR ADMIN MONITOR HOSTAGENT GATEWAY CLOUDNODE EVENTBUS COLLECTOR STRATEGY TRADE ARCHIVE; do
      declare "WITH_${flag}=0"
    done
    case "${component}" in access) WITH_ACCESS=1 ;; factor-mgr) WITH_FACTOR_MGR=1 ;; storage) WITH_STORAGE=1 ;; esac
    unset MOOX_CLI
    log() { :; }
    fail() { printf '%s\n' "$1" >&2; exit 1; }
    source "${TASK_ROOT}/build-function.sh"
    build_core_binaries
  )
done
[[ "$(cat "${TASK_ROOT}/access.log")" == 'linux/amd64:access' ]]
[[ "$(cat "${TASK_ROOT}/factor-mgr.log")" == 'linux/amd64:factor-mgr' ]]
grep -Fxq 'darwin/arm64:cli' "${TASK_ROOT}/storage.log"
[[ "$(grep -Fxc 'remote-cgo-storage' "${TASK_ROOT}/storage.log")" == 1 ]]
grep -Fxq 'linux/amd64:cli' "${TASK_ROOT}/storage.log"
cat >"${TASK_ROOT}/wrapper-cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$PWD" == "${MOOX_BOUNDARY_WRAPPER_ROOT}" ]]
[[ "$1" == setup && "$2" == package && "$3" == --profile && "$4" == access && "$5" == --output && "$6" == 'path with spaces.tar.gz' ]]
EOF
chmod +x "${TASK_ROOT}/wrapper-cli"
MOOX_BOUNDARY_WRAPPER_ROOT="${ROOT}" MOOX_CLI="${TASK_ROOT}/wrapper-cli" \
  bash "${ROOT}/scripts/build/package-deployment.sh" --profile access --output 'path with spaces.tar.gz'
printf '%s\n' 'deployment packaging and CGO boundary contract passed'
