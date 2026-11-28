#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${OUT:-${ROOT}/dist/moox-skill.tar.gz}"
SKILL_DIR="${SKILL_DIR:-${ROOT}/skills/moox}"

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
# 隐藏文件与密钥类文件不进分发包：本地调试用的 .env（例如 references/cls-query.md 让用户保存的 CLS 云凭据）、.ssh、*.key/*.pem。
find "${STAGE}/moox" -mindepth 1 \( -name '.*' -o -name '*.env' -o -name '*.key' -o -name '*.pem' \) -prune -exec rm -rf {} +

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
