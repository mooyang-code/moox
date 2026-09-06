#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-deploy.XXXXXX")"
FIXTURE_ROOT="${TMP_ROOT}/repo"
ARCHIVE="${TMP_ROOT}/strategy.tar.gz"
trap 'rm -rf "${TMP_ROOT}"' EXIT

mkdir -p "${FIXTURE_ROOT}/scripts/lib" "${FIXTURE_ROOT}/scripts/deps" "${FIXTURE_ROOT}/scripts/deploy" "${FIXTURE_ROOT}/scripts/runtime" \
  "${FIXTURE_ROOT}/deploy" "${FIXTURE_ROOT}/modules" "${FIXTURE_ROOT}/packages" "${FIXTURE_ROOT}/bin"
cp "${ROOT}/scripts/deploy/deploy-moox.sh" "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh"
cp "${ROOT}/scripts/runtime/moox-storage-auth-check.sh" "${FIXTURE_ROOT}/scripts/runtime/moox-storage-auth-check.sh"
cp "${ROOT}/scripts/runtime/moox-storage-auth-rotate.sh" "${FIXTURE_ROOT}/scripts/runtime/moox-storage-auth-rotate.sh"
cp "${ROOT}/scripts/runtime/moox-log-rotate.sh" "${FIXTURE_ROOT}/scripts/runtime/moox-log-rotate.sh"
ln -s "${ROOT}/scripts/deploy/install-caddy-ca.sh" "${FIXTURE_ROOT}/scripts/deploy/install-caddy-ca.sh"
ln -s "${ROOT}/scripts/lib/caddy-managed.sh" "${FIXTURE_ROOT}/scripts/lib/caddy-managed.sh"
ln -s "${ROOT}/scripts/lib/loopback-listeners.sh" "${FIXTURE_ROOT}/scripts/lib/loopback-listeners.sh"
ln -s "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${FIXTURE_ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt"
ln -s "${ROOT}/deploy/caddy" "${FIXTURE_ROOT}/deploy/caddy"
ln -s "${ROOT}/modules/gateway" "${FIXTURE_ROOT}/modules/gateway"
ln -s "${ROOT}/modules/cli" "${FIXTURE_ROOT}/modules/cli"
ln -s "${ROOT}/modules/strategy" "${FIXTURE_ROOT}/modules/strategy"
ln -s "${ROOT}/modules/admin" "${FIXTURE_ROOT}/modules/admin"
ln -s "${ROOT}/packages/doctor" "${FIXTURE_ROOT}/packages/doctor"
mkdir -p "${FIXTURE_ROOT}/packages/pyruntime"
ln -s "${ROOT}/packages/pyruntime/python" "${FIXTURE_ROOT}/packages/pyruntime/python"
ln -s "${ROOT}/examples" "${FIXTURE_ROOT}/examples"
ln -s "${ROOT}/config" "${FIXTURE_ROOT}/config"

for binary in moox-gateway moox-gateway-cli moox-strategy moox-strategy-cli moox-admin moox-admin-cli moox-cli; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${FIXTURE_ROOT}/bin/${binary}"
  chmod +x "${FIXTURE_ROOT}/bin/${binary}"
done
printf '#!/usr/bin/env bash\nexit 1\n' >"${FIXTURE_ROOT}/bin/moox-admin-cli"

mkdir "${TMP_ROOT}/fake-path"
for command in ssh scp rsync; do
  printf '#!/usr/bin/env bash\necho "%s unexpectedly invoked" >&2\nexit 97\n' "${command}" >"${TMP_ROOT}/fake-path/${command}"
  chmod +x "${TMP_ROOT}/fake-path/${command}"
done

MOOX_TRADE_GATEWAY_URL="https://trade.example.test:11001" \
MOOX_TRADE_GATEWAY_NODE_ID="trade-node" \
MOOX_STORAGE_PRIMARY_AUTH_SECRET="strategy-contract-primary" \
MOOX_STORAGE_VIEW_AUTH_SECRET="strategy-contract-view" \
PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh" \
  --package-only --archive "${ARCHIVE}" --target localhost \
  --dir "${TMP_ROOT}/deploy" --stage "${TMP_ROOT}/stage" \
  --goos linux --goarch amd64 --skip-build --node-id strategy \
  --gateway-control-url http://127.0.0.1:11000 \
  --no-admin --no-storage --no-archive --no-eventbus --no-cloudnode \
  --no-collector --no-factor --no-trade --no-monitor --no-hostagent >/dev/null

mkdir "${TMP_ROOT}/unpacked"
tar -C "${TMP_ROOT}/unpacked" -xzf "${ARCHIVE}"

for path in \
  bin/moox-strategy \
  bin/moox-strategy-cli \
  strategy/config/app.yaml \
  strategy/config/trpc_go.yaml \
  python-runtime/moox_pyruntime/protocol.py
do
  [[ -e "${TMP_ROOT}/unpacked/${path}" ]] || { echo "missing Strategy deployment artifact: ${path}" >&2; exit 1; }
done

grep -q '^database: ../data/strategy/strategy.sqlite$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
# The browser/admin route targets StrategyMgr's HTTP listener, not the native
# tRPC listener used by machine clients.
grep -A6 'name: trpc.moox.strategy.StrategyMgr$' "${TMP_ROOT}/unpacked/strategy/config/trpc_go.yaml" | grep -q 'port: 11433'
grep -q '^  gateway_url: "https://trade.example.test:11001"$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml" || { echo 'missing rendered remote Trade Gateway URL' >&2; exit 1; }
grep -q '^  target_node: "trade-node"$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
grep -q '^  ca_file: ""$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
grep -q 'MOOX_TRADE_GATEWAY_CA_FILE=' "${TMP_ROOT}/unpacked/start.sh"
[[ -f "${TMP_ROOT}/unpacked/config/trade-gateway.json" ]] || { echo 'missing persisted Trade Gateway route placement' >&2; exit 1; }
# Exercise the generated restart helper without starting services or requiring
# a DNS resolver. The CLI import itself is tested against SQLite in Admin.
mkdir -p "${TMP_ROOT}/unpacked/logs/admin"
sed -n '/^import_trade_owner_route() {$/,/^}$/p' "${TMP_ROOT}/unpacked/start.sh" >"${TMP_ROOT}/import-owner.sh"
[[ -s "${TMP_ROOT}/import-owner.sh" ]]
cat >"${TMP_ROOT}/unpacked/bin/moox-admin-cli" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" >"${ROOT}/owner-import.args"
SH
chmod +x "${TMP_ROOT}/unpacked/bin/moox-admin-cli"
ROOT="${TMP_ROOT}/unpacked" MOOX_EVENTBUS_NATS_URL="tls://127.0.0.1:4222" \
  bash -c 'export ROOT; source "$1"; import_trade_owner_route' _ "${TMP_ROOT}/import-owner.sh"
python3 - "${TMP_ROOT}/unpacked/owner-import.args" <<'PY'
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    args = handle.read().splitlines()
assert args[:2] == ["service-deployments", "import"]
flags = dict(zip(args[2::2], args[3::2]))
assert flags["--node-id"] == "trade-node", flags
assert flags["--public-host"] == "trade.example.test", flags
assert flags["--only-services"] == "trade_owner,trade_console", flags
PY
cp "${TMP_ROOT}/unpacked/config/trade-gateway.json" "${TMP_ROOT}/trade-gateway-original.json"
printf '%s\n' '{"gateway_url":"http://127.0.0.2:11002","target_node":"trade-node"}' >"${TMP_ROOT}/unpacked/config/trade-gateway.json"
ROOT="${TMP_ROOT}/unpacked" MOOX_EVENTBUS_NATS_URL="tls://127.0.0.1:4222" \
  bash -c 'export ROOT; source "$1"; import_trade_owner_route' _ "${TMP_ROOT}/import-owner.sh"
grep -Fxq '127.0.0.2' "${TMP_ROOT}/unpacked/owner-import.args"
cp "${TMP_ROOT}/trade-gateway-original.json" "${TMP_ROOT}/unpacked/config/trade-gateway.json"
printf '#!/usr/bin/env bash\nexit 71\n' >"${TMP_ROOT}/unpacked/bin/moox-admin-cli"
if ROOT="${TMP_ROOT}/unpacked" MOOX_EVENTBUS_NATS_URL="tls://127.0.0.1:4222" \
  bash -c 'source "$1"; import_trade_owner_route' _ "${TMP_ROOT}/import-owner.sh"; then
  echo 'Trade owner import failure was swallowed' >&2; exit 1
fi
grep -Fq 'import_trade_owner_route ||' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'gateway_service_env_for strategy' "${TMP_ROOT}/unpacked/start.sh"
grep -Fq 'config/trade-gateway.json' "${TMP_ROOT}/unpacked/start.sh"
grep -Fq 'MOOX_TRADE_GATEWAY_URL=' "${TMP_ROOT}/unpacked/start.sh"
grep -q '^  credential_file: ""$' "${TMP_ROOT}/unpacked/strategy/config/app.yaml"
grep -Fq 'WITH_STRATEGY="${MOOX_WITH_STRATEGY:-${MOOX_INSTALLED_WITH_STRATEGY:-1}}"' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'start_strategy' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'MOOX_PYTHON_RUNTIME_PATH=${ROOT}/python-runtime' "${TMP_ROOT}/unpacked/start.sh"
grep -q 'strategy) url=http://127.0.0.1:11431/healthz' "${TMP_ROOT}/unpacked/healthcheck.sh"
grep -q 'stop_service "strategy"' "${TMP_ROOT}/unpacked/stop.sh"
! test -e "${TMP_ROOT}/unpacked/strategy/pyworker"
! test -e "${TMP_ROOT}/unpacked/strategy/pysdk"
! find "${TMP_ROOT}/unpacked/strategy" -type f \( -name '*.pyc' -o -name '*.sqlite' -o -name '*.db' \) -print -quit | grep -q .

# The synchronous Strategy owner reconciliation requires Trade to be serving
# first. Keep the ordering and readiness barrier executable rather than merely
# documenting it, including the component-overlay startup path.
trade_start_line=$(grep -n '^      start_trade$' "${ROOT}/scripts/deploy/deploy-moox.sh" | head -1 | cut -d: -f1)
strategy_start_line=$(grep -n '^      start_strategy$' "${ROOT}/scripts/deploy/deploy-moox.sh" | head -1 | cut -d: -f1)
[[ -n "${trade_start_line}" && -n "${strategy_start_line}" && ${trade_start_line} -lt ${strategy_start_line} ]]
grep -q 'wait_http "http://127.0.0.1:11210/readyz"' "${ROOT}/scripts/deploy/deploy-moox.sh"

# A control-only package must persist the same independent placement even
# though it does not contain Strategy or a local Trade/DNS resolver process.
MOOX_TRADE_GATEWAY_URL="https://trade.example.test:11001" \
MOOX_TRADE_GATEWAY_NODE_ID="trade-node" \
MOOX_STORAGE_PRIMARY_AUTH_SECRET="strategy-contract-primary" \
MOOX_STORAGE_VIEW_AUTH_SECRET="strategy-contract-view" \
PATH="${TMP_ROOT}/fake-path:${PATH}" "${FIXTURE_ROOT}/scripts/deploy/deploy-moox.sh" \
  --package-only --archive "${TMP_ROOT}/control.tar.gz" --target localhost \
  --dir "${TMP_ROOT}/control-deploy" --stage "${TMP_ROOT}/control-stage" \
  --goos linux --goarch amd64 --skip-build --node-id control \
  --gateway-control-url http://127.0.0.1:11000 \
  --no-strategy --no-web-host --no-storage --no-archive --no-eventbus --no-cloudnode \
  --no-collector --no-factor --no-trade --no-monitor --no-hostagent >/dev/null
cmp "${TMP_ROOT}/unpacked/config/trade-gateway.json" "${TMP_ROOT}/control-stage/config/trade-gateway.json"
[[ ! -e "${TMP_ROOT}/control-stage/strategy/config/app.yaml" ]]
grep -Fq 'import_trade_owner_route ||' "${TMP_ROOT}/control-stage/start.sh"
grep -Fq 'disabled_services+=(moox_trade trade_owner)' "${TMP_ROOT}/control-stage/start.sh"

echo 'Strategy deployment contract passed'
