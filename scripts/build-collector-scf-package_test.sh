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

echo "collector SCF package contract passed"
