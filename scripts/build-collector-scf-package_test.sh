#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf-package-test.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

FAKE_BIN="${TMP_ROOT}/bin"
mkdir -p "${FAKE_BIN}"

cat >"${FAKE_BIN}/moox-cli" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' '{"resources":{"topic_id":"topic-contract-test"}}'
EOF

cat >"${FAKE_BIN}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
output=""
while (($#)); do
  if [[ "$1" == "-o" ]]; then
    output="$2"
    break
  fi
  shift
done
[[ -n "${output}" ]]
printf '#!/usr/bin/env bash\n' >"${output}"
chmod +x "${output}"
EOF
chmod +x "${FAKE_BIN}/moox-cli" "${FAKE_BIN}/go"

package_path="${TMP_ROOT}/collector-scf.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" \
    MOOX_CLI="${FAKE_BIN}/moox-cli" \
    MOOX_STORAGE_PRIMARY_AUTH_SECRET="storage-secret" \
    VERSION=contract-test \
    OUT_PATH="collector-scf.zip" \
    bash "${ROOT}/scripts/build-collector-scf-package.sh"
)

unzip -p "${package_path}" trpc_go.yaml >"${TMP_ROOT}/trpc_go.yaml"
python3 - "${TMP_ROOT}/trpc_go.yaml" <<'PY'
import sys
import yaml

with open(sys.argv[1], encoding="utf-8") as stream:
    document = yaml.safe_load(stream)
writers = document["plugins"]["log"]["default"]
cls = [writer for writer in writers if writer.get("writer") == "cls"]
console = [writer for writer in writers if writer.get("writer") == "console"]
assert len(cls) == 1, cls
assert cls[0]["level"] == "info", cls
assert cls[0]["remote_config"]["topic_id"] == "topic-contract-test", cls
assert len(console) == 1, console
assert console[0]["level"] == "info", console
services = document["server"]["service"]
sentinel = [service for service in services if service["name"] == "trpc.moox.collector.scf_observability.timer"]
assert len(sentinel) == 1, sentinel
assert sentinel[0]["timeout"] == 30000, sentinel
assert "*/30" in sentinel[0]["network"], sentinel
PY

unzip -p "${package_path}" sources/market/binance.yaml >"${TMP_ROOT}/binance.yaml"
python3 - "${TMP_ROOT}/binance.yaml" <<'PY'
import sys
import yaml

with open(sys.argv[1], encoding="utf-8") as stream:
    document = yaml.safe_load(stream)
bindings = document["storage"]["bindings"]
expected = "455dd0d9d5bf0130a27b70bc6805d5b0a2059d6ff68677f914576e0c5c092e32"
assert bindings["spot"]["auth_info"]["app_key"] == expected
assert bindings["swap"]["auth_info"]["app_key"] == expected
PY

mode=$(stat -f '%Lp' "${package_path}" 2>/dev/null || stat -c '%a' "${package_path}")
[[ "${mode}" == "600" ]]
python3 - "${package_path}" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as archive:
    text = b"\n".join(
        archive.read(name)
        for name in archive.namelist()
        if name.endswith((".yaml", ".yml", ".json"))
    )
assert b"storage-secret" not in text
PY

unzip -p "${package_path}" observability.env.example >"${TMP_ROOT}/observability.env.example"
for name in \
  MOOX_SCF_WATCHDOG_ENABLED MOOX_MONITOR_READY_URL MOOX_GATEWAY_READY_URL \
  MOOX_SCF_CANARY_ENABLED MOOX_SCF_CANARY_DATASET_ID MOOX_SCF_CANARY_SUBJECT_ID MOOX_SCF_CANARY_SERIES_TAG \
  MOOX_STORAGE_RPC_GATEWAY_TARGET MOOX_GATEWAY_SERVICE_KEY_ID MOOX_GATEWAY_SERVICE_SECRET_KEY \
  MOOX_EVENTBUS_URL MOOX_EVENTBUS_NATS_USERNAME MOOX_EVENTBUS_NATS_PASSWORD MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE \
  MOOX_HEALTH_HMAC_KEY_ID MOOX_HEALTH_HMAC_SECRET MOOX_MSGBOX_WECOM_WEBHOOK; do
  grep -q "^${name}=" "${TMP_ROOT}/observability.env.example"
done

for name in MOOX_SCF_CANARY_DATASET_ID MOOX_SCF_CANARY_FREQUENCY MOOX_SCF_CANARY_SERIES_TAG; do
  grep -q "\"${name}\"" "${ROOT}/modules/cloudnode/internal/rpc/node.go"
done

echo "collector SCF package contract passed"
