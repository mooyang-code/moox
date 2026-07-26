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
PATH="${FAKE_BIN}:${PATH}" \
  MOOX_CLI="${FAKE_BIN}/moox-cli" \
  VERSION=contract-test \
  OUT_DIR="${TMP_ROOT}" \
  OUT_PATH="${package_path}" \
  bash "${ROOT}/scripts/build-collector-scf-package.sh"

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

echo "collector SCF package contract passed"
