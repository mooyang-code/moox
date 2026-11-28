#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PACKAGE_SCRIPT="${ROOT}/scripts/build/package-skill.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-package-skill-test.XXXXXX")"
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

file_mode() {
  local mode
  if mode=$(stat -c '%a' "$1" 2>/dev/null); then
    printf '%s\n' "${mode}"
    return
  fi
  stat -f '%Lp' "$1"
}

assert_no_transient_files() {
  local dir="$1"
  if find "${dir}" \( -name 'moox-skill-stage.*' -o -name '*.tmp.*' \) -print -quit | grep -q .; then
    fail "temporary staging/archive files were not cleaned"
  fi
}

OUT="${TEST_ROOT}/dist/moox-skill.tar.gz"
EXTRACTED="${TEST_ROOT}/extracted"

# 技能目录里本地保存的 .env（云凭据）、密钥文件不能进分发包。
SKILL_FIXTURE="${TEST_ROOT}/skill"
mkdir -p "${SKILL_FIXTURE}/references" "${SKILL_FIXTURE}/scripts" "${SKILL_FIXTURE}/.hidden"
cp -R "${ROOT}/skills/moox/." "${SKILL_FIXTURE}/"
SENTINEL_ENV='TEST_ONLY_CLS_ENV_SECRET_2kD8'
printf 'CLS_SECRET_KEY=%s\n' "${SENTINEL_ENV}" >"${SKILL_FIXTURE}/.env"
printf '%s\n' "${SENTINEL_ENV}" >"${SKILL_FIXTURE}/references/local.env"
printf '%s\n' "${SENTINEL_ENV}" >"${SKILL_FIXTURE}/scripts/private.key"
printf '%s\n' "${SENTINEL_ENV}" >"${SKILL_FIXTURE}/.hidden/token"

mkdir -p "$(dirname "${OUT}")" "${EXTRACTED}"
SKILL_DIR="${SKILL_FIXTURE}" OUT="${OUT}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"

[[ -f "${OUT}" && ! -L "${OUT}" ]] || fail "package was not created as a regular file"
[[ "$(file_mode "${OUT}")" == 600 ]] || fail "package mode is not 0600"

if tar -tzf "${OUT}" >"${TEST_ROOT}/archive.list"; then
  grep -qx 'moox/SKILL.md' "${TEST_ROOT}/archive.list" || fail "SKILL.md missing from archive"
  grep -qx 'moox/references/data-query.md' "${TEST_ROOT}/archive.list" || fail "data-query reference missing from archive"
  if grep -Eq '(^|/)data-access\.yaml$|(^|/)data-kline\.sh$' "${TEST_ROOT}/archive.list"; then
    fail "removed skill data-access files present in archive"
  fi
  if grep -Eiq '(^|/)(\.ssh/|id_(rsa|ed25519)$|[^/]*credentials?\.json$|[^/]*secrets?\.env$)' "${TEST_ROOT}/archive.list"; then
    fail "credential-shaped file leaked into archive"
  fi
else
  fail "archive cannot be listed"
fi
if grep -Eq '(^|/)(\.env|local\.env|private\.key|\.hidden)(/|$)' "${TEST_ROOT}/archive.list"; then
  fail "local env/key files leaked into archive"
fi
tar -xzOf "${OUT}" >"${TEST_ROOT}/archive.content" 2>/dev/null || true
if grep -aq "${SENTINEL_ENV}" "${TEST_ROOT}/archive.content"; then
  fail "local credential content leaked into archive"
fi

tar -xzf "${OUT}" -C "${EXTRACTED}"
[[ -f "${EXTRACTED}/moox/SKILL.md" ]] || fail "extracted SKILL.md missing"
assert_no_transient_files "${TEST_ROOT}"

BAD_OUT_DIR="${TEST_ROOT}/out-is-directory"
mkdir "${BAD_OUT_DIR}"
if SKILL_DIR="${SKILL_FIXTURE}" OUT="${BAD_OUT_DIR}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"; then
  fail "package accepted OUT pointing to a directory"
fi
[[ -d "${BAD_OUT_DIR}" ]] || fail "directory OUT was modified"
assert_no_transient_files "${TEST_ROOT}"

BAD_OUT_TARGET="${TEST_ROOT}/out-directory-target"
BAD_OUT_LINK="${TEST_ROOT}/out-directory-link"
mkdir "${BAD_OUT_TARGET}"
ln -s "${BAD_OUT_TARGET}" "${BAD_OUT_LINK}"
if SKILL_DIR="${SKILL_FIXTURE}" OUT="${BAD_OUT_LINK}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"; then
  fail "package accepted OUT pointing to a directory symlink"
fi
[[ -L "${BAD_OUT_LINK}" ]] || fail "directory symlink OUT was modified"
[[ -z "$(find "${BAD_OUT_TARGET}" -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail "package wrote through directory symlink OUT"
assert_no_transient_files "${TEST_ROOT}"

printf 'old-package\n' >"${OUT}"
cp "${OUT}" "${TEST_ROOT}/old-package"
if SKILL_DIR="${TEST_ROOT}/missing-skill" OUT="${OUT}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"; then
  fail "package unexpectedly succeeded when skill directory was missing"
fi
cmp -s "${OUT}" "${TEST_ROOT}/old-package" || fail "failed package replaced the previous archive"
assert_no_transient_files "${TEST_ROOT}"

echo "PASS: package-skill staging contract"
