#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
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
    bash "${ROOT}/scripts/build-collector-scf-package.sh"
)
listing_path="${TMP_ROOT}/listing.txt"
unzip -l "${package_path}" >"${listing_path}"
grep -q ' main$' "${listing_path}"
! grep -q 'trpc_go.yaml' "${listing_path}"

# The implementation file is also a supported direct invocation path. Keep
# this check because the public symlink and the implementation have different
# BASH_SOURCE paths.
direct_package_path="${TMP_ROOT}/collector-scf-direct.zip"
(
  cd "${TMP_ROOT}"
  PATH="${FAKE_BIN}:${PATH}" SCF_SPACE_ID="market_data" SCF_ENTRYPOINT="market_data" VERSION="contract-test-direct" OUT_PATH="${direct_package_path}" \
    bash "${ROOT}/scripts/build/build-collector-scf-package.sh"
)
unzip -l "${direct_package_path}" >"${TMP_ROOT}/direct-listing.txt"
grep -q ' main$' "${TMP_ROOT}/direct-listing.txt"
! grep -q 'trpc_go.yaml' "${TMP_ROOT}/direct-listing.txt"
