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
  stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"
}

assert_no_transient_files() {
  local dir="$1"
  if find "${dir}" \( -name 'moox-skill-stage.*' -o -name '*.tmp.*' \) -print -quit | grep -q .; then
    fail "temporary staging/archive files were not cleaned"
  fi
}

CONFIG="${TEST_ROOT}/custom.toml"
ARGS_LOG="${TEST_ROOT}/cli.args"
FAKE_CLI="${TEST_ROOT}/moox-cli"
OUT="${TEST_ROOT}/dist/moox-skill.tar.gz"
EXTRACTED="${TEST_ROOT}/extracted"
SENTINEL_ROOT='TEST_ONLY_ROOT_SECRET_7hQ2'
SENTINEL_SSH='TEST_ONLY_SSH_PRIVATE_KEY_4pL9'
SENTINEL_CLOUD='TEST_ONLY_TENCENT_SECRET_8rW3'

cat >"${CONFIG}" <<EOF
root_secret = "${SENTINEL_ROOT}"
ssh_private_key = "${SENTINEL_SSH}"
tencent_secret_key = "${SENTINEL_CLOUD}"
EOF

cat >"${FAKE_CLI}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >"${ARGS_LOG:?}"
[[ "$#" -eq 8 ]]
[[ "$1" == setup && "$2" == export-skill-config ]]
[[ "$3" == --file && "$4" == "${CONFIG:?}" ]]
[[ "$5" == --space && "$6" == crypto_market ]]
[[ "$7" == --output ]]
mkdir -p "$(dirname "$8")"
printf '%s\n' 'version: 1' 'gateway:' '  secret: TEST_ONLY_EXPORTED_GATEWAY_SECRET' >"$8"
chmod 0600 "$8"
EOF
chmod +x "${FAKE_CLI}"

mkdir -p "$(dirname "${OUT}")" "${EXTRACTED}"
CONFIG="${CONFIG}" MOOX_CLI="${FAKE_CLI}" OUT="${OUT}" \
  ARGS_LOG="${ARGS_LOG}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"

[[ -f "${OUT}" && ! -L "${OUT}" ]] || fail "package was not created as a regular file"
[[ "$(file_mode "${OUT}")" == 600 ]] || fail "package mode is not 0600"

if tar -tzf "${OUT}" >"${TEST_ROOT}/archive.list"; then
  grep -qx 'moox/config/data-access.yaml' "${TEST_ROOT}/archive.list" || fail "generated config missing from archive"
  grep -qx 'moox/scripts/data-kline.sh' "${TEST_ROOT}/archive.list" || fail "data-kline wrapper missing from archive"
  if grep -Eq '(^|/)custom\.toml$' "${TEST_ROOT}/archive.list"; then
    fail "custom.toml leaked into archive"
  fi
  if grep -Eiq '(^|/)(\.ssh/|id_(rsa|ed25519)$|[^/]*credentials?\.json$|[^/]*secrets?\.env$)' "${TEST_ROOT}/archive.list"; then
    fail "credential-shaped file leaked into archive"
  fi
else
  fail "archive cannot be listed"
fi

tar -xzf "${OUT}" -C "${EXTRACTED}"
PACKAGED_CONFIG="${EXTRACTED}/moox/config/data-access.yaml"
[[ -f "${PACKAGED_CONFIG}" && ! -L "${PACKAGED_CONFIG}" ]] || fail "packaged config is not a regular file"
[[ "$(file_mode "${PACKAGED_CONFIG}")" == 600 ]] || fail "packaged config mode is not 0600"
[[ -x "${EXTRACTED}/moox/scripts/data-kline.sh" ]] || fail "packaged data-kline wrapper is not executable"
grep -q 'TEST_ONLY_EXPORTED_GATEWAY_SECRET' "${PACKAGED_CONFIG}" || fail "fake CLI output was not packaged"
if rg --hidden -F "${SENTINEL_ROOT}" "${EXTRACTED}" >/dev/null || \
   rg --hidden -F "${SENTINEL_SSH}" "${EXTRACTED}" >/dev/null || \
   rg --hidden -F "${SENTINEL_CLOUD}" "${EXTRACTED}" >/dev/null; then
  fail "setup secret marker leaked into archive"
fi

expected_args=$(cat <<EOF
setup
export-skill-config
--file
${CONFIG}
--space
crypto_market
--output
EOF
)
actual_prefix="$(sed -n '1,7p' "${ARGS_LOG}")"
[[ "${actual_prefix}" == "${expected_args}" ]] || fail "export command arguments are incorrect"
[[ "$(sed -n '8p' "${ARGS_LOG}")" == */moox/config/data-access.yaml ]] || fail "export output did not target staging config"
assert_no_transient_files "${TEST_ROOT}"

BAD_OUT_DIR="${TEST_ROOT}/out-is-directory"
mkdir "${BAD_OUT_DIR}"
if CONFIG="${CONFIG}" MOOX_CLI="${FAKE_CLI}" OUT="${BAD_OUT_DIR}" \
  ARGS_LOG="${ARGS_LOG}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"; then
  fail "package accepted OUT pointing to a directory"
fi
[[ -d "${BAD_OUT_DIR}" ]] || fail "directory OUT was modified"
assert_no_transient_files "${TEST_ROOT}"

BAD_OUT_TARGET="${TEST_ROOT}/out-directory-target"
BAD_OUT_LINK="${TEST_ROOT}/out-directory-link"
mkdir "${BAD_OUT_TARGET}"
ln -s "${BAD_OUT_TARGET}" "${BAD_OUT_LINK}"
if CONFIG="${CONFIG}" MOOX_CLI="${FAKE_CLI}" OUT="${BAD_OUT_LINK}" \
  ARGS_LOG="${ARGS_LOG}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"; then
  fail "package accepted OUT pointing to a directory symlink"
fi
[[ -L "${BAD_OUT_LINK}" ]] || fail "directory symlink OUT was modified"
[[ -z "$(find "${BAD_OUT_TARGET}" -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail "package wrote through directory symlink OUT"
assert_no_transient_files "${TEST_ROOT}"

printf 'old-package\n' >"${OUT}"
cp "${OUT}" "${TEST_ROOT}/old-package"
cat >"${FAKE_CLI}" <<'EOF'
#!/usr/bin/env bash
exit 42
EOF
chmod +x "${FAKE_CLI}"
if CONFIG="${CONFIG}" MOOX_CLI="${FAKE_CLI}" OUT="${OUT}" TMPDIR="${TEST_ROOT}" "${PACKAGE_SCRIPT}"; then
  fail "package unexpectedly succeeded when config export failed"
fi
cmp -s "${OUT}" "${TEST_ROOT}/old-package" || fail "failed package replaced the previous archive"
assert_no_transient_files "${TEST_ROOT}"

echo "PASS: package-skill staging contract"
