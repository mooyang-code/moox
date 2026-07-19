#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-e2e.XXXXXX")"
DEPLOY_DIR="${TMP_ROOT}/deploy"
STAGE_DIR="${TMP_ROOT}/stage"
export HOME="${TMP_ROOT}/home"
CONTROL_PID=""

cleanup() {
  local status=$?
  if [[ "${status}" -ne 0 && -f "${DEPLOY_DIR}/logs/strategy/stdout.log" ]]; then
    echo '--- Strategy deployment log ---' >&2
    tail -120 "${DEPLOY_DIR}/logs/strategy/stdout.log" >&2 || true
  fi
  if [[ -x "${DEPLOY_DIR}/stop.sh" ]]; then
    "${DEPLOY_DIR}/stop.sh" >/dev/null 2>&1 || true
  fi
  [[ -z "${CONTROL_PID}" ]] || kill "${CONTROL_PID}" >/dev/null 2>&1 || true
  chmod -R u+w "${TMP_ROOT}" 2>/dev/null || true
  rm -rf "${TMP_ROOT}"
  return "${status}"
}
trap cleanup EXIT

printf 'strategy-e2e-control-secret' >"${TMP_ROOT}/control.key"
printf 'strategy-e2e-service-secret' >"${TMP_ROOT}/service.key"
chmod 0600 "${TMP_ROOT}/control.key" "${TMP_ROOT}/service.key"
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=strategy-e2e-one \
  -keyout "${TMP_ROOT}/ca-one.key" -out "${TMP_ROOT}/ca-one.crt" -days 1 >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=strategy-e2e-two \
  -keyout "${TMP_ROOT}/ca-two.key" -out "${TMP_ROOT}/ca-two.crt" -days 1 >/dev/null 2>&1
cat "${TMP_ROOT}/ca-one.crt" "${TMP_ROOT}/ca-two.crt" >"${TMP_ROOT}/peers.pem"

python3 - <<'PY' &
import datetime
import hashlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

node_id = "strategy-e2e"
canonical = {"node_id": node_id, "disabled": False, "routes": None}
route_hash = hashlib.sha256(json.dumps(canonical, separators=(",", ":")).encode()).hexdigest()
snapshot = {
    "node_id": node_id,
    "route_hash": route_hash,
    "generated_at": datetime.datetime.now(datetime.UTC).isoformat().replace("+00:00", "Z"),
    "disabled": False,
    "routes": None,
}

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith("/api/gateway-control/routes?"):
            body = json.dumps(snapshot, separators=(",", ":")).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_error(404)

    def do_POST(self):
        if self.path == "/api/gateway-control/status":
            self.send_response(204)
            self.end_headers()
            return
        self.send_error(404)

    def log_message(self, *_):
        pass

ThreadingHTTPServer(("127.0.0.1", 11000), Handler).serve_forever()
PY
CONTROL_PID=$!
for _ in $(seq 1 30); do
  curl --silent --max-time 1 http://127.0.0.1:11000/ >/dev/null 2>&1 && break
  sleep 0.1
done

"${ROOT}/scripts/deploy-moox.sh" \
  --target localhost --dir "${DEPLOY_DIR}" --stage "${STAGE_DIR}" \
  --node-id strategy-e2e --gateway-control-url http://127.0.0.1:11000 \
  --gateway-ca-bundle "${TMP_ROOT}/peers.pem" \
  --gateway-control-key-file "${TMP_ROOT}/control.key" \
  --gateway-service-key-file "${TMP_ROOT}/service.key" \
  --no-admin --no-storage --no-archive --no-web-host --no-cloudnode \
  --no-collector --no-factor --no-monitor --reuse-web-assets

sign_health_request() {
  local timestamp nonce body_hash canonical signature
  set -a
  source "${DEPLOY_DIR}/secrets/health-auth.env"
  set +a
  timestamp=$(date +%s)
  nonce=$(openssl rand -hex 32)
  body_hash=$(printf '' | openssl dgst -sha256 | awk '{print $NF}')
  canonical=$(printf 'moox-request-v1\nGET\n/readyz\n%s\n%s\n%s' "${body_hash}" "${timestamp}" "${nonce}")
  signature=$(printf '%s' "${canonical}" | openssl dgst -sha256 -hmac "${MOOX_HEALTH_AUTH_SECRET_KEY}" | awk '{print $NF}')
  printf '%s/%s/%s/%s/%s' "${MOOX_HEALTH_AUTH_VERSION}" "${MOOX_HEALTH_AUTH_ACCESS_KEY}" "${timestamp}" "${nonce}" "${signature}"
}

fetch_ready() {
  local url="$1" output="$2"
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error --max-time 2 \
      -H "X-Moox-Health-Auth: $(sign_health_request)" "${url}" >"${output}" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "health endpoint did not become ready: ${url}" >&2
  return 1
}

strategy_health="${TMP_ROOT}/strategy-health.json"
eventbus_health="${TMP_ROOT}/eventbus-health.json"
fetch_ready http://127.0.0.1:11431/readyz "${strategy_health}"
fetch_ready http://127.0.0.1:11419/readyz "${eventbus_health}"

python3 - "${strategy_health}" "${eventbus_health}" <<'PY'
import json
import pathlib
import sys

strategy = json.loads(pathlib.Path(sys.argv[1]).read_text())
eventbus = json.loads(pathlib.Path(sys.argv[2]).read_text())
if strategy.get("ready") is not True:
    raise SystemExit(f"strategy is not ready: {strategy}")
details = strategy.get("details") or {}
if details.get("database_ready") is not True or details.get("python_worker_ready") is not True:
    raise SystemExit(f"strategy dependencies are not ready: {strategy}")
if details.get("eventbus_connected") is not True or details.get("outbox_pending_count") != 0:
    raise SystemExit(f"strategy EventBus/outbox is not ready: {strategy}")
if eventbus.get("ready") is not True:
    raise SystemExit(f"eventbus is not ready: {eventbus}")
PY

rpc_response="${TMP_ROOT}/engine-status.json"
curl --fail --silent --show-error --max-time 5 \
  -X POST -H 'Content-Type: application/json' --data '{}' \
  http://127.0.0.1:11430/trpc.moox.strategy.StrategyMgr/GetEngineStatus >"${rpc_response}"
python3 - "${rpc_response}" <<'PY'
import json
import pathlib
import sys

response = json.loads(pathlib.Path(sys.argv[1]).read_text())
ret = response.get("ret_info") or response.get("retInfo") or {}
if ret.get("code") != 0:
    raise SystemExit(f"GetEngineStatus failed: {response}")
if response.get("workers", 0) < 1:
    raise SystemExit(f"Strategy worker pool is unavailable: {response}")
PY

[[ -s "${DEPLOY_DIR}/data/strategy/strategy.sqlite" ]]
for service in gateway eventbus strategy; do
  pid=$(cat "${DEPLOY_DIR}/run/${service}.pid")
  ps -p "${pid}" >/dev/null 2>&1 || { echo "${service} process is not running" >&2; exit 1; }
done

echo 'Strategy compiled deployment E2E passed'
