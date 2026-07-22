#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-storage-profile.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/storage.tar.gz"
SHARD_ARCHIVE="${TMP_ROOT}/storage-with-shard.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/packages" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
ln -s "${ROOT}/scripts/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/install-caddy-ca.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/lib/loopback-listeners.sh" "${FIXTURE_ROOT}/scripts/lib/loopback-listeners.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/storage" "${FIXTURE_ROOT}/modules/storage"
ln -s "${ROOT}/modules/admin" "${FIXTURE_ROOT}/modules/admin"
ln -s "${ROOT}/modules/gateway" "${FIXTURE_ROOT}/modules/gateway"
ln -s "${ROOT}/modules/cli" "${FIXTURE_ROOT}/modules/cli"
ln -s "${ROOT}/packages/doctor" "${FIXTURE_ROOT}/packages/doctor"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"
ln -s "${ROOT}/scripts/reset-storage-view-indexes.sh" "${FIXTURE_ROOT}/scripts/reset-storage-view-indexes.sh"

for binary in moox-storage-primary moox-storage-view moox-storage-cli moox-storage-node moox-gateway moox-gateway-cli moox-admin moox-admin-cli moox-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done
printf '#!/usr/bin/env bash\nprintf "%%064d\\n" 0\n' >"${FIXTURE_ROOT}/bin/moox-admin-cli"
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
  --profile storage --package-only --archive "${ARCHIVE}" \
  --target localhost --dir "${TMP_ROOT}/deploy" --stage "${TMP_ROOT}/stage" \
  --goos linux --goarch amd64 --skip-build --node-id storage \
  --gateway-control-url http://127.0.0.1:11000 >/dev/null

[[ -f "${ARCHIVE}" ]]
mkdir "${TMP_ROOT}/unpacked"
tar -C "${TMP_ROOT}/unpacked" -xzf "${ARCHIVE}"
for binary in moox-storage-primary moox-storage-view moox-storage-cli moox-cli; do
  [[ -x "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "missing storage binary: ${binary}" >&2; exit 1; }
done
[[ -x "${TMP_ROOT}/unpacked/bin/moox-cli" ]]
for binary in moox-storage-view-index moox-storage-view-builder moox-storage-view-query; do
  [[ ! -e "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "unexpected split View binary: ${binary}" >&2; exit 1; }
done
for binary in moox-admin moox-web-host moox-cloudnode moox-collector; do
  [[ ! -e "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "unexpected storage binary: ${binary}" >&2; exit 1; }
done
[[ -x "${TMP_ROOT}/unpacked/bin/moox-storage-node" ]]
[[ -f "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env" ]]
[[ $(stat -f '%Lp' "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env" 2>/dev/null || stat -c '%a' "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env") == 600 ]]
grep -Eq '^MOOX_STORAGE_NODE_AUTH_SECRET=[0-9a-f]{64}$' "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env"
[[ -d "${TMP_ROOT}/unpacked/storage/config" ]]
[[ -f "${TMP_ROOT}/unpacked/storage-view/config/trpc_go.yaml" ]]
[[ -f "${TMP_ROOT}/unpacked/storage-node/config/trpc_go.yaml" ]]
[[ $(find "${TMP_ROOT}/unpacked/storage-view/config" -type f -name '*.yaml' | wc -l | tr -d ' ') == 1 ]]
[[ ! -e "${TMP_ROOT}/unpacked/admin" ]]
[[ -x "${TMP_ROOT}/unpacked/bin/moox-gateway" ]]
[[ -f "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env" ]]
[[ -f "${TMP_ROOT}/unpacked/build-provenance.json" ]]
grep -q '"schema_version":1' "${TMP_ROOT}/unpacked/build-provenance.json"
for binary in moox-storage-primary moox-storage-node moox-storage-view; do
  grep -Eq "\"${binary}\":\"[0-9a-f]{64}\"" "${TMP_ROOT}/unpacked/build-provenance.json"
done
[[ $(stat -f '%Lp' "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env" 2>/dev/null || stat -c '%a' "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env") == 600 ]]
grep -Eq '^MOOX_STORAGE_NODE_AUTH_SECRET=[0-9a-f]{64}$' "${TMP_ROOT}/unpacked/secrets/storage-node-auth.env"
grep -A 20 '^server:' "${TMP_ROOT}/unpacked/storage/config/trpc_go.yaml" | grep -q 'ip: 0.0.0.0'
grep -A 5 '^  admin:' "${TMP_ROOT}/unpacked/storage/config/trpc_go.yaml" | grep -q 'ip: 127.0.0.1'
grep -q 'target: ip://127.0.0.1:20201' "${TMP_ROOT}/unpacked/storage-view/config/trpc_go.yaml"
grep -q '^    - view$' "${TMP_ROOT}/unpacked/storage-view/config/trpc_go.yaml"
grep -q 'source "\${ROOT}/secrets/storage-node-auth.env"' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'register-node' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'import-seed' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'doctor bootstrap --format json' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'activate-datasets' "${TMP_ROOT}/unpacked/start.sh"
if grep -Eq 'MOOX_(METRICS|HOST)_STORAGE_ROUTE_SEED' "${FIXTURE_ROOT}/scripts/deploy-moox.sh"; then
  echo 'legacy storage route seed environment remains in deployment script' >&2
  exit 1
fi
assert_order() {
  local path="$1"
  shift
  local previous=0 pattern line
  for pattern in "$@"; do
    line="$(grep -n -m1 -E "${pattern}" "${path}" | cut -d: -f1)"
    [[ -n "${line}" && "${line}" -gt "${previous}" ]] || {
      echo "expected ${path} pattern in order after line ${previous}: ${pattern}" >&2
      exit 1
    }
    previous="${line}"
  done
}
assert_order "${TMP_ROOT}/unpacked/start.sh" \
  '^  start_storage_primary$' \
  '^  start_storage_node$' \
  'wait_tcp 127\.0\.0\.1 20107' \
  '^  register_storage_node$' \
  '^  import_storage_metadata$' \
  '^  start_storage_view$' \
  'wait_http http://127\.0\.0\.1:20211/healthz' \
  '^  if run_storage_doctor; then$' \
  '^    activate_storage_datasets$'
doctor_body="$(awk '/^run_storage_doctor\(\) \{/{capture=1} capture{print} capture && /^}/{exit}' "${TMP_ROOT}/unpacked/start.sh")"
grep -q 'doctor bootstrap --format json' <<<"${doctor_body}"
grep -q 'return 1' <<<"${doctor_body}"
grep -q '"conclusion".*"HEALTHY"' <<<"${doctor_body}"
if grep -q 'activate-datasets' <<<"${doctor_body}"; then
  echo 'ordinary Doctor path must not activate Datasets' >&2
  exit 1
fi
bootstrap_body="$(awk '/^complete_storage_bootstrap\(\) \{/{capture=1} capture{print} capture && /^}/{exit}' "${TMP_ROOT}/unpacked/start.sh")"
grep -q '^  if run_storage_doctor; then$' <<<"${bootstrap_body}"
grep -q '^    activate_storage_datasets$' <<<"${bootstrap_body}"
grep -q 'start_storage_view' <<<"${bootstrap_body}"
grep -q 'defer Dataset activation' <<<"${doctor_body}"

PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy-moox.sh" \
  --profile storage --with-storage-node --package-only --archive "${SHARD_ARCHIVE}" \
  --target localhost --dir "${TMP_ROOT}/deploy-shard" --stage "${TMP_ROOT}/stage-shard" \
  --goos linux --goarch amd64 --skip-build --node-id storage \
  --gateway-control-url http://127.0.0.1:11000 >/dev/null
mkdir "${TMP_ROOT}/unpacked-shard"
tar -C "${TMP_ROOT}/unpacked-shard" -xzf "${SHARD_ARCHIVE}"
[[ -x "${TMP_ROOT}/unpacked-shard/bin/moox-storage-node" ]]
[[ ! -e "${TMP_ROOT}/unpacked-shard/bin/moox-admin" ]]
[[ ! -e "${TMP_ROOT}/unpacked-shard/bin/moox-admin-cli" ]]
[[ -f "${TMP_ROOT}/unpacked-shard/storage-node/config/trpc_go.yaml" ]]
[[ -f "${TMP_ROOT}/unpacked-shard/storage-node/config/storage.yaml" ]]
grep -q 'port: 20107' "${TMP_ROOT}/unpacked-shard/storage-node/config/trpc_go.yaml"
grep -q 'node_id: storage-node-0' "${TMP_ROOT}/unpacked-shard/storage-node/config/storage.yaml"
grep -q 'credential_file: ""' "${TMP_ROOT}/unpacked-shard/storage-node/config/storage.yaml"
grep -q 'credential_file: ""' "${TMP_ROOT}/unpacked-shard/storage/config/storage.yaml"
grep -q 'default_services+=(storage-node)' "${TMP_ROOT}/unpacked-shard/healthcheck.sh"
grep -q 'service_name: trpc.moox.storage.DataNodeRuntime' "${TMP_ROOT}/unpacked-shard/storage/config/storage.yaml"
grep -q 'start_storage_node' "${TMP_ROOT}/unpacked-shard/start.sh"
grep -q 'register-node' "${TMP_ROOT}/unpacked-shard/start.sh"
grep -q 'import-seed' "${TMP_ROOT}/unpacked-shard/start.sh"
grep -q 'doctor bootstrap --format json' "${TMP_ROOT}/unpacked-shard/start.sh"
grep -q 'activate-datasets' "${TMP_ROOT}/unpacked-shard/start.sh"
grep -q 'source "\${ROOT}/secrets/storage-node-auth.env"' "${TMP_ROOT}/unpacked-shard/start.sh"
if grep -Eq 'MOOX_(METRICS|HOST)_STORAGE_ROUTE_SEED' "${FIXTURE_ROOT}/scripts/deploy-moox.sh"; then
  echo "storage deployment must not depend on route seed environment variables" >&2
  exit 1
fi
grep -q 'stop_service "storage-node"' "${TMP_ROOT}/unpacked-shard/stop.sh"

TLS_ARCHIVE="${TMP_ROOT}/storage-tls.tar.gz"
MOOX_EVENTBUS_ENABLE_TLS=1 PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy-moox.sh" \
  --profile storage --with-storage-node --package-only --archive "${TLS_ARCHIVE}" \
  --target localhost --dir "${TMP_ROOT}/deploy-tls" --stage "${TMP_ROOT}/stage-tls" \
  --goos linux --goarch amd64 --skip-build --node-id storage \
  --gateway-control-url http://127.0.0.1:11000 >/dev/null
mkdir "${TMP_ROOT}/unpacked-tls"
tar -C "${TMP_ROOT}/unpacked-tls" -xzf "${TLS_ARCHIVE}"
grep -q 'credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml' "${TMP_ROOT}/unpacked-tls/storage/config/storage.yaml"
grep -q 'credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml' "${TMP_ROOT}/unpacked-tls/storage-view/config/trpc_go.yaml"
grep -q 'credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml' "${TMP_ROOT}/unpacked-tls/storage-node/config/storage.yaml"

echo 'storage deployment profile contract passed'
