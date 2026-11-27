#!/usr/bin/env bash
set -euo pipefail

SOURCE="${BASH_SOURCE[0]}"
while [[ -h "${SOURCE}" ]]; do
  SOURCE_DIR="$(cd -P "$(dirname "${SOURCE}")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "${SOURCE}")"
  [[ "${SOURCE}" != /* ]] && SOURCE="${SOURCE_DIR}/${SOURCE}"
done

ROOT="$(cd -P "$(dirname "${SOURCE}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-collector-scf-package-test.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT
FAKE_BIN="${TMP_ROOT}/bin"
mkdir -p "${FAKE_BIN}"
# 打包脚本用 go build -o 构建 SCF 单二进制（这里替换成占位文件）。
cat >"${FAKE_BIN}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
  if [[ "$1" == "-o" ]]; then printf '#!/usr/bin/env bash\n' >"$2"; chmod +x "$2"; exit 0; fi
  shift
done
exit 1
EOF
chmod +x "${FAKE_BIN}/go"

package_path="${TMP_ROOT}/collector-scf.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="crypto" SCF_ENTRYPOINT="market_data" VERSION="contract-test" OUT_PATH="collector-scf.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh"
)
listing_path="${TMP_ROOT}/listing.txt"
unzip -Z1 "${package_path}" >"${listing_path}"
[[ "$(cat "${listing_path}")" == "main" ]] || {
  echo "expected SCF package to contain only 'main', got:" >&2
  cat "${listing_path}" >&2
  exit 1
}

stock_package_path="${TMP_ROOT}/collector-stockcn-scf.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="stockcn" SCF_ENTRYPOINT="market_data" MOOX_CLS_SECRET_KEY="test-cls-secret" MOOX_EVENTBUS_NATS_PASSWORD="test-eventbus-password" VERSION="contract-test" OUT_PATH="collector-stockcn-scf.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh"
)
stock_listing_path="${TMP_ROOT}/stock-listing.txt"
unzip -Z1 "${stock_package_path}" >"${stock_listing_path}"
[[ "$(cat "${stock_listing_path}")" == "main" ]] || {
  echo "expected stockcn SCF package to contain only 'main', got:" >&2
  cat "${stock_listing_path}" >&2
  exit 1
}
! unzip -p "${stock_package_path}" | grep -Eq 'test-cls-secret|test-eventbus-password'

if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="crypto" SCF_ENTRYPOINT="invalid" OUT_PATH="${TMP_ROOT}/bad-entry.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh"; then
  echo "expected unsupported entrypoint to fail" >&2
  exit 1
fi
[[ ! -e "${TMP_ROOT}/bad-entry.zip" ]]

atomic_package_path="${TMP_ROOT}/collector-scf-atomic.zip"
cp "${package_path}" "${atomic_package_path}"
mkdir -p "${TMP_ROOT}/fail-bin"
cat >"${TMP_ROOT}/fail-bin/zip" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'partial archive\n' >"$2"
exit 17
EOF
chmod +x "${TMP_ROOT}/fail-bin/zip"
if PATH="${FAKE_BIN}:${TMP_ROOT}/fail-bin:${PATH}" SCF_SPACE_ID="crypto" OUT_PATH="${atomic_package_path}" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh"; then
  echo "expected zip failure to abort packaging" >&2
  exit 1
fi
cmp "${package_path}" "${atomic_package_path}"
if find "${TMP_ROOT}" -maxdepth 1 -type d -name '.collector-scf-package.*' -print -quit | grep -q .; then
  echo "temporary SCF archive directory was not cleaned up" >&2
  exit 1
fi
