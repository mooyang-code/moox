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
write_dates_ca() {
  local name="$1" start="$2" end="$3" usage="$4" dir="${TMP_ROOT}/ca-${1}"
  mkdir -p "${dir}/certs"
  : >"${dir}/index.txt"
  printf '1000\n' >"${dir}/serial"
  openssl req -new -newkey rsa:2048 -nodes -subj "/CN=${name}" -keyout "${dir}/key.pem" -out "${dir}/request.pem" >/dev/null 2>&1
  printf '[ca]\ndefault_ca=CA_default\n[CA_default]\ndatabase=%s/index.txt\nserial=%s/serial\nnew_certs_dir=%s/certs\nprivate_key=%s/key.pem\ndefault_md=sha256\npolicy=policy_any\n[policy_any]\ncommonName=supplied\n[v3_ca]\nbasicConstraints=critical,CA:true\nkeyUsage=%s\n' \
    "${dir}" "${dir}" "${dir}" "${dir}" "${usage}" >"${dir}/openssl.cnf"
  openssl ca -config "${dir}/openssl.cnf" -selfsign -keyfile "${dir}/key.pem" -in "${dir}/request.pem" -out "${dir}/ca.pem" \
    -startdate "${start}" -enddate "${end}" -batch -notext -extensions v3_ca >/dev/null 2>&1
}

write_dates_ca expired 20250101000000Z 20260101000000Z keyCertSign,cRLSign
write_dates_ca future 20270101000000Z 20280101000000Z keyCertSign,cRLSign
write_dates_ca no-sign-usage 20250101000000Z 20280101000000Z digitalSignature
if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" SCF_ENTRYPOINT="market_data" VERSION="contract-test" OUT_PATH="${TMP_ROOT}/missing-secret.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh"; then
  echo "expected packaging to fail without a public EventBus CA" >&2
  exit 1
fi

for secret_case in nats-jwt nkey-seed; do
  secret_config="${TMP_ROOT}/config-${secret_case}"
  cp -R "${ROOT}/modules/collector/configs/scf/market_data" "${secret_config}"
  if [[ "${secret_case}" == "nats-jwt" ]]; then
    printf 'credentials: |\n  -----BEGIN NATS USER JWT-----\n  test-user-jwt\n  ------END NATS USER JWT------\n' >"${secret_config}/sources/market/credentials.yaml"
  else
    printf 'nats:\n  user_nkey_seed: SUABCDEF1234567890\n' >"${secret_config}/sources/market/credentials.yaml"
  fi
  if PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${secret_config}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/${secret_case}.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
    echo "expected packaging to reject ${secret_case}" >&2
    exit 1
  fi
  [[ ! -e "${TMP_ROOT}/${secret_case}.zip" ]]
done

for credential_case in scalar list structured structured-cased null-key comment quoted-comment quoted-punct-comment empty-quote-tail null-tail tilde-tail null-plus-comment-credential empty-plus-comment-credential; do
  credential_config="${TMP_ROOT}/config-${credential_case}"
  cp -R "${ROOT}/modules/collector/configs/scf/market_data" "${credential_config}"
  if [[ "${credential_case}" == "scalar" ]]; then
    printf 'note: MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "list" ]]; then
    printf 'notes:\n  - MOOX_EVENTBUS_NATS_PASSWORD=secret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "structured" ]]; then
    printf 'env:\n  - name: MOOX_STORAGE_PRIMARY_AUTH_SECRET\n    value: topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "structured-cased" ]]; then
    printf 'env:\n  - Name: MOOX_EVENTBUS_NATS_PASSWORD\n    Value: topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "null-key" ]]; then
    printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret:\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "quoted-comment" ]]; then
    printf '# "MOOX_STORAGE_PRIMARY_AUTH_SECRET": "topsecret"\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "quoted-punct-comment" ]]; then
    printf '# "MOOX_STORAGE_PRIMARY_AUTH_SECRET": ",topsecret"\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "empty-quote-tail" ]]; then
    printf '# MOOX_STORAGE_PRIMARY_AUTH_SECRET: ""topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "null-tail" ]]; then
    printf '# MOOX_STORAGE_PRIMARY_AUTH_SECRET: null topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "tilde-tail" ]]; then
    printf '# MOOX_STORAGE_PRIMARY_AUTH_SECRET: ~ topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "null-plus-comment-credential" ]]; then
    printf '# MOOX_STORAGE_PRIMARY_AUTH_SECRET: null # MOOX_EVENTBUS_NATS_PASSWORD=topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  elif [[ "${credential_case}" == "empty-plus-comment-credential" ]]; then
    printf '# MOOX_STORAGE_PRIMARY_AUTH_SECRET: "" # MOOX_EVENTBUS_NATS_PASSWORD=topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  else
    printf '# MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n' >"${credential_config}/sources/market/credential.yaml"
  fi
  if PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${credential_config}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/${credential_case}-credential.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
    echo "expected packaging to reject credential assignment in ${credential_case}" >&2
    exit 1
  fi
  [[ ! -e "${TMP_ROOT}/${credential_case}-credential.zip" ]]
done

for placeholder in null tilde empty quoted-whitespace implicit-null-eof; do
  placeholder_config="${TMP_ROOT}/config-placeholder-${placeholder}"
  cp -R "${ROOT}/modules/collector/configs/scf/market_data" "${placeholder_config}"
  case "${placeholder}" in
    null) printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET: null\n' >"${placeholder_config}/sources/market/placeholder.yaml" ;;
    tilde) printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET: ~\n' >"${placeholder_config}/sources/market/placeholder.yaml" ;;
    empty) printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET: ""\n' >"${placeholder_config}/sources/market/placeholder.yaml" ;;
    quoted-whitespace) printf "MOOX_STORAGE_PRIMARY_AUTH_SECRET: '  '\\n" >"${placeholder_config}/sources/market/placeholder.yaml" ;;
    implicit-null-eof) printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET:' >"${placeholder_config}/sources/market/placeholder.yaml" ;;
  esac
  placeholder_zip="${TMP_ROOT}/${placeholder}-placeholder.zip"
  PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${placeholder_config}" SCF_SPACE_ID="market_data" OUT_PATH="${placeholder_zip}" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"
  [[ -e "${placeholder_zip}" ]]
done

symlink_config="${TMP_ROOT}/config-symlink"
cp -R "${ROOT}/modules/collector/configs/scf/market_data" "${symlink_config}"
cp "${symlink_config}/sources/market/binance.yaml" "${TMP_ROOT}/outside-binance.yaml"
rm "${symlink_config}/sources/market/binance.yaml"
ln -s "${TMP_ROOT}/outside-binance.yaml" "${symlink_config}/sources/market/binance.yaml"
if PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${symlink_config}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/symlink-config.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
  echo "expected packaging to reject a symlinked source config" >&2
  exit 1
fi
[[ ! -e "${TMP_ROOT}/symlink-config.zip" ]]

config_dir_target="${TMP_ROOT}/config-dir-target"
cp -R "${ROOT}/modules/collector/configs/scf/market_data" "${config_dir_target}"
config_dir_symlink="${TMP_ROOT}/config-dir-symlink"
ln -s "${config_dir_target}" "${config_dir_symlink}"
if PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${config_dir_symlink}/./" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/symlink-config-dir.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
  echo "expected packaging to reject a symlinked SCF config directory" >&2
  exit 1
fi
[[ ! -e "${TMP_ROOT}/symlink-config-dir.zip" ]]

if PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${TMP_ROOT}/missing-config" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/missing-config.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
  echo "expected packaging to reject an explicitly missing SCF config directory" >&2
  exit 1
fi
[[ ! -e "${TMP_ROOT}/missing-config.zip" ]]

symlink_parent_base="${TMP_ROOT}/symlink-parent-base"
symlink_target="${TMP_ROOT}/symlink-target"
mkdir -p "${symlink_parent_base}" "${symlink_target}/child" "${symlink_target}/config"
cp -R "${ROOT}/modules/collector/configs/scf/market_data/sources" "${symlink_target}/config/sources"
ln -s "${symlink_target}/child" "${symlink_parent_base}/link"
if PATH="${FAKE_BIN}:${PATH}" SCF_CONFIG_DIR="${symlink_parent_base}/link/../config" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/symlink-parent-escape.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
  echo "expected packaging to use the normalized SCF config path" >&2
  exit 1
fi
[[ ! -e "${TMP_ROOT}/symlink-parent-escape.zip" ]]

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
if PATH="${FAKE_BIN}:${TMP_ROOT}/fail-bin:${PATH}" SCF_SPACE_ID="market_data" OUT_PATH="${atomic_package_path}" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca.pem"; then
  echo "expected zip failure to abort packaging" >&2
  exit 1
fi
cmp "${package_path}" "${atomic_package_path}"
if find "${TMP_ROOT}" -maxdepth 1 -type d -name '.collector-scf-package.*' -print -quit | grep -q .; then
  echo "temporary SCF archive directory was not cleaned up" >&2
  exit 1
fi

if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/private.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/private-key.pem"; then
  echo "expected private key CA rejection" >&2
  exit 1
fi
for invalid_ca in expired future no-sign-usage; do
  if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/${invalid_ca}.zip" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/ca-${invalid_ca}/ca.pem"; then
    echo "expected ${invalid_ca} EventBus CA rejection" >&2
    exit 1
  fi
  [[ ! -e "${TMP_ROOT}/${invalid_ca}.zip" ]]
done
printf 'CA:TRUE\n' >"${TMP_ROOT}/bad.pem"
if PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" OUT_PATH="${TMP_ROOT}/bad.zip" \
  bash "${ROOT}/scripts/build/build-collector-scf-package.sh" --eventbus-ca-file "${TMP_ROOT}/bad.pem"; then
  echo "expected malformed CA rejection" >&2
  exit 1
fi
