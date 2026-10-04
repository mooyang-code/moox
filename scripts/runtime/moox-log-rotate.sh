#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: moox-log-rotate.sh --root DIR [--max-size-mb N] [--backup-count N]

Bounds every regular *.log file below DIR, excluding runtime data and secrets,
using copy-truncate rotation.
EOF
}

root=""
max_size_mb="${MOOX_LOCAL_LOG_MAX_SIZE_MB:-50}"
backup_count="${MOOX_LOCAL_LOG_BACKUP_COUNT:-5}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root="${2:-}"; shift 2 ;;
    --max-size-mb) max_size_mb="${2:-}"; shift 2 ;;
    --backup-count) backup_count="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "${root}" && "${root}" == /* && "${root}" != / ]] || {
  echo "--root must be a non-root absolute path" >&2
  exit 2
}
[[ "${max_size_mb}" =~ ^[1-9][0-9]*$ && "${max_size_mb}" -le 10240 ]] || {
  echo "--max-size-mb must be between 1 and 10240" >&2
  exit 2
}
[[ "${backup_count}" =~ ^[1-9][0-9]*$ && "${backup_count}" -le 100 ]] || {
  echo "--backup-count must be between 1 and 100" >&2
  exit 2
}

mkdir -p "${root}/run"
lock_file="${root}/run/log-rotation.lock"
if command -v flock >/dev/null 2>&1; then
	# Kernel-owned locks disappear automatically after crashes and avoid stale
	# owner takeover races between concurrent manual runs.
	exec 9>"${lock_file}"
	flock -n 9 || exit 0
else
	# Portable fallback for development hosts without flock. It deliberately
	# avoids unsafe stale-lock deletion; production Linux uses the branch above.
	lock_dir="${lock_file}.d"
	mkdir "${lock_dir}" 2>/dev/null || exit 0
	trap 'rmdir "${lock_dir}" 2>/dev/null || true' EXIT
fi

max_bytes=$((max_size_mb * 1024 * 1024))
file_size() {
  local file="$1"
  if stat -c '%s' "${file}" >/dev/null 2>&1; then
    stat -c '%s' "${file}"
  else
    stat -f '%z' "${file}"
  fi
}

rotate_file() {
  local file="$1" size index tmp
  size="$(file_size "${file}")"
  [[ "${size}" =~ ^[0-9]+$ && "${size}" -gt "${max_bytes}" ]] || return 0

  rm -f -- "${file}.${backup_count}"
  for ((index = backup_count; index > 1; index--)); do
    [[ -f "${file}.$((index - 1))" && ! -L "${file}.$((index - 1))" ]] || continue
    mv -f -- "${file}.$((index - 1))" "${file}.${index}"
  done

  tmp="${file}.1.tmp.$$"
  # Keep only the most useful tail when an old unbounded log is discovered;
  # this also prevents rotation itself from temporarily doubling disk usage.
  tail -c "${max_bytes}" -- "${file}" >"${tmp}"
  chmod --reference="${file}" "${tmp}" 2>/dev/null || chmod 0600 "${tmp}"
  mv -f -- "${tmp}" "${file}.1"
  : >"${file}"
}

while IFS= read -r -d '' file; do
  rotate_file "${file}"
done < <(find -P "${root}" \
  \( -path "${root}/data" -o -path "${root}/secrets" -o -path "${root}/certs" \) -prune -o \
  -type f -name '*.log' -print0)
