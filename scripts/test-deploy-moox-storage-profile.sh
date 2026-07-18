#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-storage-profile.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/storage.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy-moox.sh"
ln -s "${ROOT}/scripts/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/install-caddy-ca.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/lib/loopback-listeners.sh" "${FIXTURE_ROOT}/scripts/lib/loopback-listeners.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/storage" "${FIXTURE_ROOT}/modules/storage"
ln -s "${ROOT}/modules/gateway" "${FIXTURE_ROOT}/modules/gateway"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"
ln -s "${ROOT}/scripts/reset-storage-view-indexes.sh" "${FIXTURE_ROOT}/scripts/reset-storage-view-indexes.sh"

for binary in moox-storage moox-storage-cli moox-gateway moox-gateway-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done

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
for binary in moox-storage moox-storage-cli moox-storage-primary moox-storage-view; do
  [[ -x "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "missing storage binary: ${binary}" >&2; exit 1; }
done
for binary in moox-storage-view-index moox-storage-view-builder moox-storage-view-query; do
  [[ ! -e "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "unexpected split View binary: ${binary}" >&2; exit 1; }
done
for binary in moox-admin moox-web-host moox-cloudnode moox-collector; do
  [[ ! -e "${TMP_ROOT}/unpacked/bin/${binary}" ]] || { echo "unexpected storage binary: ${binary}" >&2; exit 1; }
done
[[ -d "${TMP_ROOT}/unpacked/storage/config" ]]
[[ -f "${TMP_ROOT}/unpacked/storage-view/config/trpc_go.yaml" ]]
[[ $(find "${TMP_ROOT}/unpacked/storage-view/config" -type f -name '*.yaml' | wc -l | tr -d ' ') == 1 ]]
[[ ! -e "${TMP_ROOT}/unpacked/admin" ]]
grep -A 20 '^server:' "${TMP_ROOT}/unpacked/storage/config/trpc_go.primary.yaml" | grep -q 'ip: 0.0.0.0'
grep -A 5 '^  admin:' "${TMP_ROOT}/unpacked/storage/config/trpc_go.primary.yaml" | grep -q 'ip: 127.0.0.1'
grep -q 'target: ip://127.0.0.1:20201' "${TMP_ROOT}/unpacked/storage-view/config/trpc_go.yaml"
grep -q '^    - view$' "${TMP_ROOT}/unpacked/storage-view/config/trpc_go.yaml"

echo 'storage deployment profile contract passed'
