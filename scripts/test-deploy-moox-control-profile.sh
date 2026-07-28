#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-control-profile.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/control.tar.gz"
DEFAULT_ARCHIVE="${TMP_ROOT}/default-control.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

file_mode() {
  local mode
  if mode=$(stat -f '%Lp' "$1" 2>/dev/null); then
    printf '%s\n' "${mode}"
    return
  fi
  stat -c '%a' "$1"
}

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" \
  "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/packages" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
ln -s "${ROOT}/scripts/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/install-caddy-ca.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/lib/loopback-listeners.sh" "${FIXTURE_ROOT}/scripts/lib/loopback-listeners.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/admin" "${FIXTURE_ROOT}/modules/admin"
ln -s "${ROOT}/modules/gateway" "${FIXTURE_ROOT}/modules/gateway"
ln -s "${ROOT}/modules/cli" "${FIXTURE_ROOT}/modules/cli"
ln -s "${ROOT}/modules/eventbus" "${FIXTURE_ROOT}/modules/eventbus"
ln -s "${ROOT}/modules/cloudnode" "${FIXTURE_ROOT}/modules/cloudnode"
ln -s "${ROOT}/modules/collector" "${FIXTURE_ROOT}/modules/collector"
ln -s "${ROOT}/modules/monitor" "${FIXTURE_ROOT}/modules/monitor"
ln -s "${ROOT}/packages/doctor" "${FIXTURE_ROOT}/packages/doctor"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"

for binary in \
  moox-admin moox-cli moox-gateway moox-gateway-cli moox-web-host \
  moox-eventbus moox-cloudnode moox-cloudnode-cli \
  moox-collector moox-collector-cli moox-collector-scf \
  moox-monitor moox-monitor-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done
cat >"${FIXTURE_ROOT}/bin/moox-admin-cli" <<'EOF'
#!/usr/bin/env bash
exit 126
EOF
chmod +x "${FIXTURE_ROOT}/bin/moox-admin-cli"

mkdir "${TMP_ROOT}/fake-path"
for command in ssh scp rsync; do
  cat >"${TMP_ROOT}/fake-path/${command}" <<EOF
#!/usr/bin/env bash
echo '${command} unexpectedly invoked by --package-only' >&2
exit 97
EOF
  chmod +x "${TMP_ROOT}/fake-path/${command}"
done

PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy-moox.sh" \
  --profile control --package-only --archive "${ARCHIVE}" \
  --target localhost --dir "${TMP_ROOT}/deploy" --stage "${TMP_ROOT}/stage" \
  --goos linux --goarch amd64 --skip-build --reuse-web-assets \
  --node-id control --gateway-control-url http://127.0.0.1:11000 \
  --monitor-instance-id monitor-control \
  --public-host 106.53.107.122 --service-https-port 11001 >/dev/null

[[ -f "${ARCHIVE}" ]]
mode=$(file_mode "${ARCHIVE}")
[[ "${mode}" == 600 ]]

mkdir "${TMP_ROOT}/unpacked"
tar -C "${TMP_ROOT}/unpacked" -xzf "${ARCHIVE}"
for binary in \
  moox-admin moox-admin-cli moox-cli moox-gateway moox-gateway-cli moox-web-host \
  moox-eventbus moox-cloudnode moox-cloudnode-cli \
  moox-collector moox-collector-cli moox-collector-scf \
  moox-monitor moox-monitor-cli; do
  [[ -x "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "missing control binary: ${binary}" >&2; exit 1; }
done
for binary in moox-storage moox-archive moox-factor moox-strategy; do
  [[ ! -e "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "unexpected control binary: ${binary}" >&2; exit 1; }
done
[[ -s "${TMP_ROOT}/unpacked/certs/gateway/peers.pem" ]]
[[ ! -e "${TMP_ROOT}/unpacked/storage" ]]
[[ -d "${TMP_ROOT}/unpacked/cloudnode" ]]
[[ -d "${TMP_ROOT}/unpacked/collector" ]]
grep -q '^node_batch:' "${TMP_ROOT}/unpacked/cloudnode/config/app.yaml"
grep -q '  batch_size: 3' "${TMP_ROOT}/unpacked/cloudnode/config/app.yaml"
grep -q '  poll_interval: 500ms' "${TMP_ROOT}/unpacked/cloudnode/config/app.yaml"
grep -q 'native_addr: 0.0.0.0:11003' "${TMP_ROOT}/unpacked/gateway/config/app.yaml"
grep -q 'health_addr: 0.0.0.0:11012' "${TMP_ROOT}/unpacked/gateway/config/app.yaml"
sed -n '/name: trpc.moox.monitor.Health/,/name:/p' "${TMP_ROOT}/unpacked/monitor/config/trpc_go.yaml" |
  grep -q 'ip: 0.0.0.0'
grep -Fq -- '--disable-storage-shard' "${TMP_ROOT}/unpacked/start.sh"
! grep -Fq -- '--disable-storage-node' "${TMP_ROOT}/unpacked/start.sh"
grep -Fq 'SCF_SERVICE_GATEWAY_TARGET="${MOOX_SCF_SERVICE_GATEWAY_TARGET:-https://106.53.107.122:11001}"' "${TMP_ROOT}/unpacked/start.sh"
grep -Fq 'SCF_STORAGE_RPC_GATEWAY_TARGET="${MOOX_SCF_STORAGE_RPC_GATEWAY_TARGET:-ip://106.53.107.122:11003}"' "${TMP_ROOT}/unpacked/start.sh"
grep -Fq '"MOOX_SCF_SERVICE_GATEWAY_TARGET=${SCF_SERVICE_GATEWAY_TARGET}"' "${TMP_ROOT}/unpacked/start.sh"
grep -Fq '"MOOX_SCF_STORAGE_RPC_GATEWAY_TARGET=${SCF_STORAGE_RPC_GATEWAY_TARGET}"' "${TMP_ROOT}/unpacked/start.sh"

PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy-moox.sh" \
  --package-only --archive "${DEFAULT_ARCHIVE}" \
  --target localhost --dir "${TMP_ROOT}/deploy-default" --stage "${TMP_ROOT}/stage-default" \
  --goos linux --goarch amd64 --skip-build --reuse-web-assets \
  --no-storage --no-archive --no-factor --no-strategy --no-monitor \
  --node-id control --gateway-control-url http://127.0.0.1:11000 >/dev/null

mkdir "${TMP_ROOT}/unpacked-default"
tar -C "${TMP_ROOT}/unpacked-default" -xzf "${DEFAULT_ARCHIVE}"
grep -q 'native_addr: 0.0.0.0:11003' "${TMP_ROOT}/unpacked-default/gateway/config/app.yaml"

echo 'control deployment profile contract passed'
