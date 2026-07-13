#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT}/scripts/deploy-moox.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-deploy-cls-contract.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

invalid_account_args=(
  --target localhost
  --dir "${TMP_ROOT}/deploy"
  --stage "${TMP_ROOT}/stage"
  --skip-build
  --no-start
  --no-storage
  --no-archive
  --no-eventbus
  --no-web-host
  --no-cloudnode
  --no-collector
  --no-factor
  --no-monitor
)
if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id "" 2>&1); then
  echo 'empty cloud account id unexpectedly accepted' >&2
  exit 1
fi
grep -q 'cloud-account-id cannot be empty' <<<"${output}"
if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id "   " 2>&1); then
  echo 'whitespace-only cloud account id unexpectedly accepted' >&2
  exit 1
fi
grep -q 'cloud-account-id cannot be empty' <<<"${output}"
for option_like in --enable-cls --no-start; do
  if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id "${option_like}" 2>&1); then
    echo "option-like cloud account id unexpectedly accepted: ${option_like}" >&2
    exit 1
  fi
  grep -q -- '--cloud-account-id requires a value' <<<"${output}"
done
if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id 'acct;touch' 2>&1); then
  echo 'cloud account id with unsafe characters unexpectedly accepted' >&2
  exit 1
fi
grep -q 'cloud account ID must match' <<<"${output}"

FIXTURE_ROOT="${TMP_ROOT}/repo"
DEPLOY_DIR="${TMP_ROOT}/existing-deploy"
STAGE_DIR="${TMP_ROOT}/fixture-stage"
STOP_MARKER="${TMP_ROOT}/stop.marker"
mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" \
  "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/bin" \
  "${FIXTURE_ROOT}/skills/moox/scripts"
cp "${SCRIPT}" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/admin" "${FIXTURE_ROOT}/modules/admin"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"
cat >"${FIXTURE_ROOT}/skills/moox/scripts/cls-bootstrap.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo 'fake CLS preflight failure' >&2
exit 1
EOF
chmod +x "${FIXTURE_ROOT}/skills/moox/scripts/cls-bootstrap.sh"
for binary in moox-admin moox-admin-cli moox-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done
mkdir -p "${DEPLOY_DIR}/data" "${DEPLOY_DIR}/secrets" "${DEPLOY_DIR}/bin"
: >"${DEPLOY_DIR}/data/admin.db"
printf 'MOOX_SERVICE_AUTH_SECRET_KEY=existing\n' >"${DEPLOY_DIR}/secrets/service-auth.env"
cat >"${DEPLOY_DIR}/stop.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: >"${MOOX_TEST_STOP_MARKER}"
EOF
chmod +x "${DEPLOY_DIR}/stop.sh"
if MOOX_TEST_STOP_MARKER="${STOP_MARKER}" \
  "${FIXTURE_ROOT}/scripts/deploy-moox.sh" \
    --target localhost --dir "${DEPLOY_DIR}" --stage "${STAGE_DIR}" \
    --skip-build --enable-cls --no-storage --no-archive --no-eventbus \
    --no-web-host --no-cloudnode --no-collector --no-factor --no-monitor \
    >"${TMP_ROOT}/fixture.out" 2>&1; then
  echo 'deployment unexpectedly succeeded after CLS preflight failure' >&2
  exit 1
fi
[[ ! -e "${STOP_MARKER}" ]] || {
  echo 'existing service was stopped before CLS preflight completed' >&2
  exit 1
}

grep -q -- '--cloud-account-id' "${SCRIPT}"
grep -q 'skills/moox/scripts/cls-bootstrap.sh' "${SCRIPT}"
grep -q 'prepare_cls_preflight' "${SCRIPT}"
if grep -q '^bootstrap_cls()' "${SCRIPT}"; then
  echo 'runtime start script still provisions CLS' >&2
  exit 1
fi

prepare_line=$(grep -n '^prepare_cls_preflight$' "${SCRIPT}" | tail -1 | cut -d: -f1)
sync_line=$(grep -n '^if is_local_target; then$' "${SCRIPT}" | tail -1 | cut -d: -f1)
[[ -n "${prepare_line}" && -n "${sync_line}" && ${prepare_line} -lt ${sync_line} ]]

grep -q 'ops tencent cls prepare' "${ROOT}/skills/moox/scripts/cls-bootstrap.sh"
echo 'CLS deployment ordering contract passed'
