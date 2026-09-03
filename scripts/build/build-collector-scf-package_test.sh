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
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" SCF_ENTRYPOINT="market_data" VERSION="contract-test" OUT_PATH="collector-scf.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh"
)
listing_path="${TMP_ROOT}/listing.txt"
unzip -l "${package_path}" >"${listing_path}"
grep -q ' main$' "${listing_path}"
! grep -q 'trpc_go.yaml' "${listing_path}"

stock_package_path="${TMP_ROOT}/collector-stockcn-scf.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="stockcn" SCF_ENTRYPOINT="market_data" MOOX_STORAGE_PRIMARY_AUTH_SECRET="test-storage-secret" VERSION="contract-test" OUT_PATH="collector-stockcn-scf.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh"
)
stock_listing_path="${TMP_ROOT}/stock-listing.txt"
unzip -l "${stock_package_path}" >"${stock_listing_path}"
grep -q ' main$' "${stock_listing_path}"
grep -q ' sources/market/binance.yaml$' "${stock_listing_path}"
grep -q ' sources/market/sina.yaml$' "${stock_listing_path}"
grep -q ' sources/market/tencent.yaml$' "${stock_listing_path}"
grep -q ' sources/market/eastmoney.yaml$' "${stock_listing_path}"
grep -q ' sources/market/baidu.yaml$' "${stock_listing_path}"
grep -q ' markets/stockcn/calendar.yaml$' "${stock_listing_path}"
grep -q ' markets/stockcn/route.yaml$' "${stock_listing_path}"
! grep -q 'trpc_go.yaml' "${stock_listing_path}"
