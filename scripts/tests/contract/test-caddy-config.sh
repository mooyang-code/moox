#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CONFIG=${CADDY_CONFIG:-"${ROOT_DIR}/deploy/caddy/Caddyfile"}
NO_ADMIN_CONFIG="${ROOT_DIR}/deploy/caddy/Caddyfile.no-admin"
PUBLIC_CONFIG="${ROOT_DIR}/deploy/caddy/Caddyfile.public"
PUBLIC_NO_ADMIN_CONFIG="${ROOT_DIR}/deploy/caddy/Caddyfile.public.no-admin"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf 'PASS: %s\n' "$*"
}

[[ -f "${CONFIG}" ]] || fail "Caddyfile is missing: ${CONFIG}"

# These checks remain useful on developer machines where Caddy is not installed.
grep -Fq 'https://{$MOOX_PUBLIC_HOST}:{$MOOX_BROWSER_HTTPS_PORT:9527}' "${CONFIG}" || fail 'browser HTTPS listener is missing'
grep -Fq 'https://{$MOOX_PUBLIC_HOST}:{$MOOX_SERVICE_HTTPS_PORT:11001}' "${CONFIG}" || fail 'service HTTPS listener is missing'
grep -Fq 'handle /api/admin/*' "${CONFIG}" || fail 'browser Admin allowlist is missing'
grep -Fq 'handle /api/gateway-control/*' "${CONFIG}" || fail 'browser Gateway control allowlist is missing'
grep -Fq 'handle /api/service/*' "${CONFIG}" || fail 'service API allowlist is missing'
grep -Fq 'reverse_proxy 127.0.0.1:11000' "${CONFIG}" || fail 'Admin control upstream is missing'
grep -Fq 'reverse_proxy 127.0.0.1:9528' "${CONFIG}" || fail 'web upstream is missing'
grep -Fq 'reverse_proxy 127.0.0.1:11002' "${CONFIG}" || fail 'Gateway service upstream is missing'
[[ -f "${NO_ADMIN_CONFIG}" ]] || fail 'no-admin Caddy configuration is missing'
grep -Fq 'handle /api/service/*' "${NO_ADMIN_CONFIG}" || fail 'no-admin service API allowlist is missing'
grep -Fq 'reverse_proxy 127.0.0.1:11002' "${NO_ADMIN_CONFIG}" || fail 'no-admin Gateway upstream is missing'
if grep -Fq '/api/gateway-control/' "${NO_ADMIN_CONFIG}" || grep -Fq 'MOOX_BROWSER_HTTPS_PORT' "${NO_ADMIN_CONFIG}"; then
  fail 'no-admin Caddy configuration exposed the browser control plane'
fi
grep -Fq 'Content-Security-Policy' "${CONFIG}" || fail 'browser CSP is missing'
if grep -Eq '^[[:space:]]*handle_path([[:space:]]|$)' "${CONFIG}"; then
  fail 'handle_path would strip upstream request paths'
fi
for public_config in "${PUBLIC_CONFIG}" "${PUBLIC_NO_ADMIN_CONFIG}"; do
  [[ -f "${public_config}" ]] || fail "public Caddy configuration is missing: ${public_config}"
  grep -Fq 'profile shortlived' "${public_config}" || fail "${public_config} does not request short-lived certificates"
  grep -Fq 'disable_tlsalpn_challenge' "${public_config}" || fail "${public_config} does not force the port-80 HTTP challenge"
  if grep -Fq 'tls internal' "${public_config}"; then
    fail "${public_config} still uses the private Caddy CA"
  fi
done
pass 'static route-isolation contract'

if [[ -n "${CADDY_BIN:-}" ]]; then
  [[ -x "${CADDY_BIN}" ]] || fail "CADDY_BIN is not executable: ${CADDY_BIN}"
elif [[ -x "${ROOT_DIR}/bin/caddy" ]]; then
  CADDY_BIN="${ROOT_DIR}/bin/caddy"
elif command -v caddy >/dev/null 2>&1; then
  CADDY_BIN=$(command -v caddy)
else
  printf 'SKIP: runtime Caddy validation (set CADDY_BIN or install caddy)\n'
  exit 0
fi

command -v python3 >/dev/null 2>&1 || fail 'python3 is required for runtime upstream fixtures'
command -v curl >/dev/null 2>&1 || fail 'curl is required for runtime HTTPS assertions'

TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/moox-caddy-test.XXXXXX")
CADDY_PID=
UPSTREAM_PID=
cleanup() {
  [[ -z "${CADDY_PID}" ]] || kill "${CADDY_PID}" 2>/dev/null || true
  [[ -z "${UPSTREAM_PID}" ]] || kill "${UPSTREAM_PID}" 2>/dev/null || true
  wait "${CADDY_PID}" "${UPSTREAM_PID}" 2>/dev/null || true
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT INT TERM

export MOOX_PUBLIC_HOST=127.0.0.1
export XDG_DATA_HOME="${TMP_DIR}/data"
export XDG_CONFIG_HOME="${TMP_DIR}/config"

"${CADDY_BIN}" validate --config "${CONFIG}" --adapter caddyfile
"${CADDY_BIN}" validate --config "${PUBLIC_CONFIG}" --adapter caddyfile
"${CADDY_BIN}" validate --config "${PUBLIC_NO_ADMIN_CONFIG}" --adapter caddyfile
pass 'Caddy configuration validation'

cat >"${TMP_DIR}/upstreams.py" <<'PY'
import socketserver
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    upstream = "unknown"

    def do_GET(self):
        if self.headers.get("Upgrade", "").lower() == "websocket":
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.end_headers()
            return
        if self.path == "/api/admin/stream":
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            self.wfile.write(b"6\r\nfirst\n\r\n")
            self.wfile.flush()
            time.sleep(1.5)
            self.wfile.write(b"7\r\nsecond\n\r\n0\r\n\r\n")
            self.wfile.flush()
            return
        body = f"{self.upstream} {self.path}\n".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass

def serve(port, name):
    cls = type(f"{name}Handler", (Handler,), {"upstream": name})
    ThreadingHTTPServer(("127.0.0.1", port), cls).serve_forever()

for port, name in ((11000, "admin"), (9528, "web"), (11002, "service")):
    threading.Thread(target=serve, args=(port, name), daemon=True).start()
threading.Event().wait()
PY

python3 "${TMP_DIR}/upstreams.py" &
UPSTREAM_PID=$!
"${CADDY_BIN}" run --config "${CONFIG}" --adapter caddyfile >"${TMP_DIR}/caddy.log" 2>&1 &
CADDY_PID=$!

ROOT_CA="${XDG_DATA_HOME}/caddy/pki/authorities/local/root.crt"
for _ in $(seq 1 100); do
  [[ -s "${ROOT_CA}" ]] && curl --silent --fail --cacert "${ROOT_CA}" https://127.0.0.1:9527/ >/dev/null 2>&1 && break
  kill -0 "${CADDY_PID}" 2>/dev/null || { cat "${TMP_DIR}/caddy.log" >&2; fail 'Caddy exited during startup'; }
  sleep 0.1
done
[[ -s "${ROOT_CA}" ]] || fail 'Caddy internal CA certificate was not created'
pass 'internal CA certificate creation'

request() { curl --silent --show-error --cacert "${ROOT_CA}" "$@"; }
status() { request --output /dev/null --write-out '%{http_code}' "$1"; }

[[ $(request https://127.0.0.1:9527/api/admin/ping) == 'admin /api/admin/ping' ]] || fail 'Admin path was not preserved'
[[ $(request https://127.0.0.1:9527/api/gateway-control/routes) == 'admin /api/gateway-control/routes' ]] || fail 'Gateway control path was not preserved'
[[ $(request https://127.0.0.1:9527/site/path) == 'web /site/path' ]] || fail 'site path did not reach web-host'
for path in /api/service/ping /api/other /healthz /readyz /metrics; do
  [[ $(status "https://127.0.0.1:9527${path}") == 404 ]] || fail "browser edge exposed ${path}"
done
[[ $(request https://127.0.0.1:11001/api/service/ping) == 'service /api/service/ping' ]] || fail 'service path was not preserved'
for path in / /api/admin/ping /api/other /healthz /readyz /metrics; do
  [[ $(status "https://127.0.0.1:11001${path}") == 404 ]] || fail "service edge exposed ${path}"
done
pass 'HTTPS route isolation and path preservation'

ws_headers=$(request --http1.1 --include --no-buffer --max-time 2 \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  https://127.0.0.1:9527/api/admin/ws 2>/dev/null || true)
grep -Eq '^HTTP/1\.[01] 101 ' <<<"${ws_headers}" || fail 'WebSocket upgrade did not survive the browser edge'
pass 'WebSocket upgrade proxying'

stream_file="${TMP_DIR}/stream.out"
request --no-buffer https://127.0.0.1:9527/api/admin/stream >"${stream_file}" &
STREAM_PID=$!
sleep 0.5
grep -Fq 'first' "${stream_file}" || fail 'streaming response was buffered at the edge'
wait "${STREAM_PID}"
grep -Fq 'second' "${stream_file}" || fail 'streaming response did not complete'
pass 'streaming Admin response proxying'
