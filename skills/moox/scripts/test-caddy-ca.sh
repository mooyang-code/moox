#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
SCRIPT="${ROOT}/skills/moox/scripts/caddy-ca.sh"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-skill-ca.XXXXXX")
trap 'rm -rf "${TMP}"' EXIT
mkdir -p "${TMP}/bin" "${TMP}/CA path with 'quote'"
CA_FILE="${TMP}/CA path with 'quote'/root ca.crt"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=MooX Skill Root' \
  -addext 'basicConstraints=critical,CA:TRUE' -keyout "${TMP}/root.key" -out "${CA_FILE}" >/dev/null 2>&1
FINGERPRINT=$(openssl x509 -in "${CA_FILE}" -noout -fingerprint -sha256 | cut -d= -f2)
FINGERPRINT_HEX=${FINGERPRINT//:/}

cat >"${TMP}/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Darwin\n'
EOF
cat >"${TMP}/bin/security" <<'EOF'
#!/usr/bin/env bash
printf 'security' >>"${MOCK_LOG:-/dev/null}"; printf ' <%s>' "$@" >>"${MOCK_LOG:-/dev/null}"; printf '\n' >>"${MOCK_LOG:-/dev/null}"
[[ "${MOCK_TRUSTED:-0}" != 1 ]] || printf 'SHA-256 hash: %s\n' "${MOCK_FINGERPRINT_HEX}"
EOF
cat >"${TMP}/bin/sudo" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == -n ]]; then
  shift
  if [[ "${1:-}" == -l ]]; then
    shift
    [[ "${1:-}" != -- ]] || shift
    [[ "${MOCK_SUDO_DENY:-0}" != 1 ]] || exit 1
    exit 0
  fi
  [[ "${MOCK_SUDO_DENY:-0}" != 1 ]] || exit 1
  [[ "${1:-}" != -- ]] || shift
fi
"$@"
EOF
chmod +x "${TMP}/bin"/*

status() {
  PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=$1 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" \
    "${SCRIPT}" status --ca-file "${CA_FILE}"
}

# RED: status reflects the platform trust store, not certificate validity alone.
grep -Fq '"trusted":false' < <(status 0)
grep -Fq '"trusted":true' < <(status 1)

inspect_out=$("${SCRIPT}" inspect --ca-file "${CA_FILE}")
grep -Eiq 'sha256 fingerprint=' <<<"${inspect_out}"
help_out=$("${SCRIPT}" trust-help --ca-file "${CA_FILE}")
grep -Fq 'curl --cacert' <<<"${help_out}"
if "${SCRIPT}" fetch --target localhost --deploy-dir "${TMP}" --output "${TMP}/root.key" >/dev/null 2>&1; then
  echo 'FAIL: private-key output path accepted' >&2; exit 1
fi
# fetch 从控制台代理的数据目录取根证书，并按期望指纹校验。
mkdir -p "${TMP}/deploy/data/console-proxy/caddy/pki/authorities/local"
cp "${CA_FILE}" "${TMP}/deploy/data/console-proxy/caddy/pki/authorities/local/root.crt"
fetched=$("${SCRIPT}" fetch --target localhost --deploy-dir "${TMP}/deploy" --output "${TMP}/fetched/root.crt")
[[ "${fetched}" == "${FINGERPRINT}" ]] || { echo "FAIL: fetch 输出的指纹 ${fetched} 与证书的 ${FINGERPRINT} 不一致" >&2; exit 1; }
cmp -s "${CA_FILE}" "${TMP}/fetched/root.crt"
if "${SCRIPT}" fetch --target localhost --deploy-dir "${TMP}/deploy" --output "${TMP}/fetched/other.crt" \
  --expected-fingerprint 'AA:BB' >/dev/null 2>&1; then
  echo 'FAIL: 指纹不一致时 fetch 必须失败' >&2; exit 1
fi
[[ ! -e "${TMP}/fetched/other.crt" ]] || { echo 'FAIL: 指纹不一致时必须删除下载的文件' >&2; exit 1; }

# install 调用系统信任库；没有免密 sudo 且 --non-interactive 时以 77 失败，不等待输入。
: >"${TMP}/install.log"
PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=0 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" MOCK_LOG="${TMP}/install.log" \
  "${SCRIPT}" install --ca-file "${CA_FILE}" >/dev/null
grep -Fq 'add-trusted-cert' "${TMP}/install.log"
set +e
PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=0 MOCK_SUDO_DENY=1 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" MOCK_LOG="${TMP}/install.log" \
  "${SCRIPT}" install --ca-file "${CA_FILE}" --non-interactive >/dev/null 2>&1
status=$?
set -e
[[ "${status}" -eq 77 ]] || { echo "FAIL: 拒绝授权时退出码是 ${status}，期望 77" >&2; exit 1; }

# install-target 已删除：没有主机进程需要信任控制台代理的根证书。
if "${SCRIPT}" install-target --target localhost --deploy-dir "${TMP}/deploy" >/dev/null 2>&1; then
  echo 'FAIL: install-target 应该已删除' >&2; exit 1
fi
printf 'PASS: skill status、fetch、install 和 CA 安全契约\n'
