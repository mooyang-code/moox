#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-engine-deploy.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT
DIR="${TMP_ROOT}/engine"
mkdir -p "${DIR}/bin" "${DIR}/venv/bin" "${DIR}/secrets"
DIR="$(cd "${DIR}" && pwd -P)"
printf '#!/usr/bin/env bash\nexit 0\n' >"${DIR}/bin/moox-factor-engine"
printf '#!/usr/bin/env bash\nexit 0\n' >"${DIR}/venv/bin/python"
chmod +x "${DIR}/bin/moox-factor-engine" "${DIR}/venv/bin/python"
for secret in access-factor-engine.key storage-primary-auth.secret factor-eventbus.yaml; do
  printf 'secret-%s\n' "${secret}" >"${DIR}/secrets/${secret}"
  chmod 0600 "${DIR}/secrets/${secret}"
done

install_args=(
  --dir "${DIR}" --skip-build --no-start --engine-id factor-engine@contract
  --access-address storage.example:11004 --access-id access@storage
  --access-key-id assigned-factor-key-17
  --eventbus-url tls://control.example:4222
)
export MOOX_LAUNCH_AGENTS_DIR="${TMP_ROOT}/LaunchAgents" MOOX_SYSTEMD_USER_DIR="${TMP_ROOT}/systemd"
HTTPS_PROXY=http://127.0.0.1:7897 "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null

config="${DIR}/config/engine.yaml"
grep -Fq 'id: factor-engine@contract' "${config}"
grep -Fq 'key_id: "assigned-factor-key-17"' "${config}"
grep -Fq 'access_address: "storage.example:11004"' "${config}"
grep -Fq 'access_id: "access@storage"' "${config}"
grep -Fq "key_file: ${DIR}/secrets/access-factor-engine.key" "${config}"
grep -Fq 'tls://control.example:4222' "${config}"
! grep -Eq '^database:' "${config}"
[[ -s "${DIR}/config/trpc_go.engine.yaml" && -s "${DIR}/pyworker/worker.py" && -s "${DIR}/pyworker/codec.py" ]]
grep -Eq '^MOOX_HEALTH_AUTH_SECRET_KEY=[0-9a-f]{64}$' "${DIR}/secrets/health-auth.env"

if [[ "$(uname -s)" == Darwin ]]; then
  service="${TMP_ROOT}/LaunchAgents/com.moox.factor-engine.plist"
  plutil -lint "${service}" >/dev/null
else
  service="${TMP_ROOT}/systemd/moox-factor-engine.service"
fi
[[ -s "${service}" ]]
grep -Fq "${DIR}/bin/moox-factor-engine" "${service}"
# The worker imports moox_pyruntime from the shipped runtime directory.
[[ -s "${DIR}/python-runtime/moox_pyruntime/protocol.py" ]]
grep -Fq "${DIR}/python-runtime" "${service}"
if grep -Eq '(HTTPS?_PROXY|https?_proxy)[^ ]*=|<key>HTTPS?_PROXY' "${service}"; then
  echo "the engine service must not carry proxy variables" >&2
  exit 1
fi

chmod 0644 "${DIR}/secrets/access-factor-engine.key"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null 2>&1; then
  echo "installer accepted a group-readable credential" >&2
  exit 1
fi
chmod 0600 "${DIR}/secrets/access-factor-engine.key"
rm "${DIR}/secrets/storage-primary-auth.secret"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null 2>&1; then
  echo "installer accepted a missing Storage auth secret" >&2
  exit 1
fi

echo 'factor engine deployment contract passed'
