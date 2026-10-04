#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${CONFIG:-${ROOT}/moox.toml}"
MOOX_CLI="${MOOX_CLI:-${ROOT}/bin/moox-cli}"
OUT="${OUT:-${ROOT}/dist/moox-skill.tar.gz}"
SKILL_DIR="${ROOT}/skills/moox"

STAGE=""
ARCHIVE_TMP=""
cleanup() {
  [[ -z "${STAGE}" ]] || rm -rf "${STAGE}"
  [[ -z "${ARCHIVE_TMP}" ]] || rm -f "${ARCHIVE_TMP}"
}
trap cleanup EXIT

file_mode() {
  local mode
  if mode=$(stat -c '%a' "$1" 2>/dev/null); then
    printf '%s\n' "${mode}"
    return
  fi
  stat -f '%Lp' "$1"
}

[[ -d "${SKILL_DIR}" ]] || {
  echo "missing skill directory: ${SKILL_DIR}" >&2
  exit 1
}
[[ -x "${MOOX_CLI}" ]] || {
  echo "moox-cli is not executable: ${MOOX_CLI}" >&2
  exit 1
}
if [[ -L "${OUT}" ]]; then
  echo "output archive must not be a symlink: ${OUT}" >&2
  exit 1
fi
if [[ -e "${OUT}" && ! -f "${OUT}" ]]; then
  echo "output archive must be a regular file: ${OUT}" >&2
  exit 1
fi

mkdir -p "$(dirname "${OUT}")"
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/moox-skill-stage.XXXXXX")"
mkdir -p "${STAGE}/moox"
cp -R "${SKILL_DIR}/." "${STAGE}/moox/"
rm -rf "${STAGE}/moox/config"
mkdir -p "${STAGE}/moox/config"

PACKAGED_CONFIG="${STAGE}/moox/config/data-access.yaml"
"${MOOX_CLI}" setup export-skill-config \
  --file "${CONFIG}" \
  --space crypto \
  --output "${PACKAGED_CONFIG}"

if [[ ! -f "${PACKAGED_CONFIG}" || -L "${PACKAGED_CONFIG}" ]]; then
  echo "exported config must be a regular non-symlink file" >&2
  exit 1
fi
if [[ "$(file_mode "${PACKAGED_CONFIG}")" != 600 ]]; then
  echo "exported config must have permission 0600" >&2
  exit 1
fi

ARCHIVE_TMP="$(mktemp "${OUT}.tmp.XXXXXX")"
tar -C "${STAGE}" -czf "${ARCHIVE_TMP}" moox
chmod 0600 "${ARCHIVE_TMP}"
mv -f "${ARCHIVE_TMP}" "${OUT}"
ARCHIVE_TMP=""
if [[ ! -f "${OUT}" || -L "${OUT}" || "$(file_mode "${OUT}")" != 600 ]]; then
  echo "output archive must be a regular non-symlink file with permission 0600" >&2
  exit 1
fi

echo "==> skill package: ${OUT}"
