#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WATCHDOG="${ROOT}/scripts/moox-storage-view-watchdog.sh"
SERVICE="${ROOT}/deploy/systemd/system/moox-storage-view-watchdog.service"
TIMER="${ROOT}/deploy/systemd/system/moox-storage-view-watchdog.timer"

test -x "${WATCHDOG}"
grep -q 'flock -n' "${WATCHDOG}"
! grep -q 'MOOX_STORAGE_VIEW_DELIVER_POLICY' "${WATCHDOG}"
! grep -q 'MOOX_STORAGE_VIEW_DELIVER_POLICY' "${ROOT}/deploy/systemd/system/moox-storage-view-watchdog.service"
grep -q 'start.sh.*storage-view' "${WATCHDOG}"
grep -q 'OnUnitActiveSec=10s' "${TIMER}"
grep -q 'ExecStart=/usr/local/libexec/moox-storage-view-watchdog' "${SERVICE}"
grep -q 'StartLimitIntervalSec=0' "${SERVICE}"
grep -q 'User=__MOOX_USER__' "${SERVICE}"

echo "PASS: Storage View watchdog script and systemd timer contract"
