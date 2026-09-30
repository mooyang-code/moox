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
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=package-test-ca" -keyout "${TMP_ROOT}/private-key.pem" -out "${TMP_ROOT}/ca.pem" >/dev/null 2>&1
if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" SCF_ENTRYPOINT="market_data" VERSION="contract-test" OUT_PATH="${TMP_ROOT}/missing-secret.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh"; then
  echo "expected packaging to fail without a public EventBus CA" >&2
  exit 1
fi

package_path="${TMP_ROOT}/collector-scf.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" SCF_ENTRYPOINT="market_data" VERSION="contract-test" OUT_PATH="collector-scf.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"
)
listing_path="${TMP_ROOT}/listing.txt"
unzip -l "${package_path}" >"${listing_path}"
grep -q ' main$' "${listing_path}"
! grep -q 'trpc_go.yaml' "${listing_path}"
unzip -p "${package_path}" sources/market/binance.yaml >"${TMP_ROOT}/binance.yaml"
! grep -Eq 'binance-spot-collector|binance-swap-collector|test-storage-secret|test-eventbus-password' "${TMP_ROOT}/binance.yaml"
grep -q 'app_key: ""' "${TMP_ROOT}/binance.yaml"

stock_package_path="${TMP_ROOT}/collector-stockcn-scf.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="stockcn" SCF_ENTRYPOINT="market_data" MOOX_STORAGE_PRIMARY_AUTH_SECRET="test-storage-secret" MOOX_EVENTBUS_NATS_PASSWORD="test-eventbus-password" VERSION="contract-test" OUT_PATH="collector-stockcn-scf.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"
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
unzip -p "${stock_package_path}" sources/market/binance.yaml >"${TMP_ROOT}/stock-binance.yaml"
! grep -Eq 'binance-spot-collector|binance-swap-collector|test-storage-secret|test-eventbus-password' "${TMP_ROOT}/stock-binance.yaml"
grep -q 'app_key: ""' "${TMP_ROOT}/stock-binance.yaml"

unzip -p "${package_path}" certs/eventbus-ca.pem >"${TMP_ROOT}/packaged-ca.pem"
cmp "${TMP_ROOT}/ca.pem" "${TMP_ROOT}/packaged-ca.pem"
unzip -p "${stock_package_path}" certs/eventbus-ca.pem >"${TMP_ROOT}/stock-ca.pem"
cmp "${TMP_ROOT}/ca.pem" "${TMP_ROOT}/stock-ca.pem"
! unzip -p "${stock_package_path}" | grep -Eq 'test-storage-secret|test-eventbus-password'
if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/private.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/private-key.pem"; then
  echo "expected private key CA rejection" >&2
  exit 1
fi
printf 'not a certificate\n' >"${TMP_ROOT}/bad.pem"
if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/bad.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/bad.pem"; then
  echo "expected malformed CA rejection" >&2
  exit 1
fi
