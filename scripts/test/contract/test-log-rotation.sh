#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-log-rotation.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

assert_file() { [[ -f "$1" ]] || { echo "missing file: $1" >&2; exit 1; }; }
assert_empty() { [[ ! -s "$1" ]] || { echo "file is not empty: $1" >&2; exit 1; }; }

LOG_ROOT="${TMP_ROOT}/logs"
mkdir -p "${LOG_ROOT}/factor" "${LOG_ROOT}/storage/view"
mkdir -p "${TMP_ROOT}/run"

# The active file can already be much larger than the configured ceiling. The
# rotator must retain only a bounded tail and truncate the file in place so a
# running service keeps writing through its existing descriptor.
dd if=/dev/zero of="${LOG_ROOT}/factor/stdout.log" bs=1048576 count=2 status=none
printf 'latest-factor-line\n' >>"${LOG_ROOT}/factor/stdout.log"
"${ROOT}/scripts/runtime/moox-log-rotate.sh" --root "${TMP_ROOT}" --max-size-mb 1 --backup-count 2

assert_file "${LOG_ROOT}/factor/stdout.log.1"
assert_empty "${LOG_ROOT}/factor/stdout.log"
[[ "$(wc -c <"${LOG_ROOT}/factor/stdout.log.1" | tr -d ' ')" -le 1048576 ]]
grep -aFq 'latest-factor-line' "${LOG_ROOT}/factor/stdout.log.1"

for marker in second third; do
  dd if=/dev/zero of="${LOG_ROOT}/factor/stdout.log" bs=1048576 count=2 status=none
  printf '%s\n' "${marker}" >>"${LOG_ROOT}/factor/stdout.log"
  "${ROOT}/scripts/runtime/moox-log-rotate.sh" --root "${TMP_ROOT}" --max-size-mb 1 --backup-count 2
done

assert_file "${LOG_ROOT}/factor/stdout.log.1"
assert_file "${LOG_ROOT}/factor/stdout.log.2"
[[ ! -e "${LOG_ROOT}/factor/stdout.log.3" ]]
grep -aFq 'third' "${LOG_ROOT}/factor/stdout.log.1"
grep -aFq 'second' "${LOG_ROOT}/factor/stdout.log.2"

dd if=/dev/zero of="${TMP_ROOT}/run/caddy.log" bs=1048576 count=2 status=none
"${ROOT}/scripts/runtime/moox-log-rotate.sh" --root "${TMP_ROOT}" --max-size-mb 1 --backup-count 2
assert_file "${TMP_ROOT}/run/caddy.log.1"
assert_empty "${TMP_ROOT}/run/caddy.log"

# A symlink must never let log maintenance truncate a file outside logs/.
printf 'do-not-touch\n' >"${TMP_ROOT}/outside.log"
ln -s "${TMP_ROOT}/outside.log" "${LOG_ROOT}/storage/view/trpc.log"
"${ROOT}/scripts/runtime/moox-log-rotate.sh" --root "${TMP_ROOT}" --max-size-mb 1 --backup-count 2
grep -Fqx 'do-not-touch' "${TMP_ROOT}/outside.log"

# A crash or power loss must not disable rotation forever. Recover a lock
# whose recorded owner is no longer running.
printf '99999999\n' >"${TMP_ROOT}/run/log-rotation.lock"
dd if=/dev/zero of="${LOG_ROOT}/storage/view/after-stale-lock.log" bs=1048576 count=2 status=none
"${ROOT}/scripts/runtime/moox-log-rotate.sh" --root "${TMP_ROOT}" --max-size-mb 1 --backup-count 2
assert_file "${LOG_ROOT}/storage/view/after-stale-lock.log.1"

if "${ROOT}/scripts/runtime/moox-log-rotate.sh" --root "${TMP_ROOT}" --max-size-mb 0 --backup-count 2 2>/dev/null; then
  echo 'zero max size must be rejected' >&2
  exit 1
fi

echo 'local log rotation contract passed'
