#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-deploy-admin.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
DEPLOY_DIR="${TMP_ROOT}/deploy"
PASSWORD_FILE="${TMP_ROOT}/admin.password"
CALLS_FILE="${TMP_ROOT}/calls"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
for module in admin archive eventbus; do ln -s "${ROOT}/modules/${module}" "${FIXTURE_ROOT}/modules/${module}"; done
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"

for binary in moox-admin moox-cli moox-eventbus moox-archive moox-archive-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done
cat >"${FIXTURE_ROOT}/bin/moox-admin-cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  random-secret) printf '{"status":"ok","bytes":32,"secret":"%064d"}\n' 0 ;;
  user)
    password=$(cat)
    printf '%s\n' "$*" >>"${MOOX_TEST_CALLS_FILE}"
    printf '%s' "${password}" >"${MOOX_TEST_PASSWORD_CAPTURE}"
    mkdir -p "$(dirname "${MOOX_TEST_ADMIN_DB}")"
    : >"${MOOX_TEST_ADMIN_DB}"
    ;;
  *) exit 0 ;;
esac
EOF
chmod +x "${FIXTURE_ROOT}/bin/moox-admin-cli"

printf '%s\n' 'correct horse battery staple' >"${PASSWORD_FILE}"
chmod 0600 "${PASSWORD_FILE}"
export MOOX_TEST_CALLS_FILE="${CALLS_FILE}"
export MOOX_TEST_PASSWORD_CAPTURE="${TMP_ROOT}/password.capture"
export MOOX_TEST_ADMIN_DB="${DEPLOY_DIR}/data/admin.db"

deploy() {
  "${FIXTURE_ROOT}/scripts/deploy-moox.sh" --target localhost --dir "${DEPLOY_DIR}" --stage "${TMP_ROOT}/stage" \
    --skip-build --no-start --no-storage --no-web-host --no-cloudnode --no-collector --no-factor --no-monitor "$@" >/dev/null
}

if deploy 2>"${TMP_ROOT}/missing.err"; then
  echo 'non-interactive first deployment unexpectedly accepted no password file' >&2
  exit 1
fi
grep -q -- '--admin-password-file' "${TMP_ROOT}/missing.err"

chmod 0644 "${PASSWORD_FILE}"
if deploy --admin-password-file "${PASSWORD_FILE}" 2>"${TMP_ROOT}/mode.err"; then
  echo 'deployment unexpectedly accepted a non-0600 password file' >&2
  exit 1
fi
grep -q '0600' "${TMP_ROOT}/mode.err"

chmod 0600 "${PASSWORD_FILE}"
deploy --admin-username operator --admin-password-file "${PASSWORD_FILE}"
[[ $(stat -f '%Lp' "${DEPLOY_DIR}/secrets/service-auth.env") == 600 ]]
[[ $(stat -f '%Lp' "${DEPLOY_DIR}/secrets/admin-jwt.env") == 600 ]]
grep -Eq '^MOOX_SERVICE_AUTH_SECRET_KEY=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/service-auth.env"
grep -Eq '^MOOX_ADMIN_JWT_SECRET_KEY=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/admin-jwt.env"
service_hash=$(shasum -a 256 "${DEPLOY_DIR}/secrets/service-auth.env")
jwt_hash=$(shasum -a 256 "${DEPLOY_DIR}/secrets/admin-jwt.env")
grep -q '^user ensure --db-path .*/data/admin.db --username operator --password-stdin$' "${CALLS_FILE}"
[[ $(cat "${TMP_ROOT}/password.capture") == 'correct horse battery staple' ]]
! grep -R -q 'correct horse battery staple' "${TMP_ROOT}/stage" "${DEPLOY_DIR}" "${ROOT}/release/deploy-stage" 2>/dev/null

: >"${CALLS_FILE}"
deploy
[[ $(shasum -a 256 "${DEPLOY_DIR}/secrets/service-auth.env") == "${service_hash}" ]]
[[ $(shasum -a 256 "${DEPLOY_DIR}/secrets/admin-jwt.env") == "${jwt_hash}" ]]
[[ ! -s "${CALLS_FILE}" ]] || { echo 'redeployment changed an existing Admin DB user' >&2; exit 1; }

deploy --reset-data --admin-password-file "${PASSWORD_FILE}"
tail -1 "${CALLS_FILE}" | grep -q '^user ensure --db-path .*/data/admin.db --username admin --password-stdin$'

echo 'admin bootstrap deployment contract passed'
