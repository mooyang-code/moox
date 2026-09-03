#!/usr/bin/env bash
set -euo pipefail

# Build every deployable MooX binary into a flat, upload-friendly release
# directory. The existing release.sh remains the full config/schema archive;
# this script is intentionally smaller for binary-only upgrades.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:-$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || echo dev)}"
TARGET_GOOS="${TARGET_GOOS:-${GOOS:-$(go env GOOS)}}"
TARGET_GOARCH="${TARGET_GOARCH:-${GOARCH:-$(go env GOARCH)}}"
OUTPUT_ROOT="${OUTPUT_ROOT:-${ROOT}/release}"
SKIP_BUILD=0
PUBLISH_LATEST=1
CLEAN=0

usage() {
  cat <<'EOF'
Usage: scripts/build/build-release-binaries.sh [options]

Build all MooX module binaries into release/moox-binaries-<version>-<goos>-<goarch>/bin.
The latest target is also copied to release/bin for simple deployment.

Options:
  --goos VALUE          Target GOOS (default: current Go host)
  --goarch VALUE        Target GOARCH (default: current Go host)
  --version VALUE       Version embedded in binaries and artifact name
  --output-root PATH    Release output directory (default: <repo>/release)
  --skip-build          Reuse binaries already present in <repo>/bin
  --no-latest            Do not refresh <output-root>/bin
  --clean               Remove this target artifact before building
  -h, --help            Show this help

Environment:
  STORAGE_CGO_ENABLED   Passed through to scripts/build/build.sh
  BUILD_* / GOFLAGS     Passed through to the module build
EOF
}

while (($# > 0)); do
  case "$1" in
    --goos)
      (($# >= 2)) || { echo "--goos requires a value" >&2; exit 2; }
      TARGET_GOOS="$2"
      shift 2
      ;;
    --goarch)
      (($# >= 2)) || { echo "--goarch requires a value" >&2; exit 2; }
      TARGET_GOARCH="$2"
      shift 2
      ;;
    --version)
      (($# >= 2)) || { echo "--version requires a value" >&2; exit 2; }
      VERSION="$2"
      shift 2
      ;;
    --output-root)
      (($# >= 2)) || { echo "--output-root requires a value" >&2; exit 2; }
      OUTPUT_ROOT="$2"
      shift 2
      ;;
    --skip-build)
      SKIP_BUILD=1
      shift
      ;;
    --no-latest)
      PUBLISH_LATEST=0
      shift
      ;;
    --clean)
      CLEAN=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "${TARGET_GOOS}" in
  linux|darwin|windows) ;;
  *) echo "unsupported GOOS: ${TARGET_GOOS}" >&2; exit 2 ;;
esac
case "${TARGET_GOARCH}" in
  amd64|arm64) ;;
  *) echo "unsupported GOARCH: ${TARGET_GOARCH}" >&2; exit 2 ;;
esac

artifact_name="moox-binaries-${VERSION}-${TARGET_GOOS}-${TARGET_GOARCH}"
artifact_dir="${OUTPUT_ROOT}/${artifact_name}"
bin_dir="${ROOT}/bin"

if ((CLEAN)); then
  rm -rf "${artifact_dir}"
fi

if ((SKIP_BUILD == 0)); then
  VERSION="${VERSION}" TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
    "${ROOT}/scripts/build/build.sh" all
  # The generic market_data SCF entrypoint is a separately packaged runtime
  # and is not part of the general release.sh archive.
  if [[ "${TARGET_GOOS}/${TARGET_GOARCH}" == "linux/amd64" ]]; then
    VERSION="${VERSION}" TARGET_GOOS="${TARGET_GOOS}" TARGET_GOARCH="${TARGET_GOARCH}" \
      "${ROOT}/scripts/build/build.sh" collector-scf
  fi
fi

binary_names=(
  moox-cli
  moox-admin moox-admin-cli
  moox-gateway moox-gateway-cli
  moox-eventbus
  moox-web-host
  moox-cloudnode moox-cloudnode-cli
  moox-collector moox-collector-cli
  moox-factor moox-factor-cli
  moox-strategy moox-strategy-cli
  moox-trade moox-trade-cli
  moox-monitor moox-monitor-cli
  moox-storage-primary moox-storage-node moox-storage-view moox-storage-cli
  moox-archive moox-archive-cli
)
if [[ "${TARGET_GOOS}" == "linux" ]]; then
  binary_names+=(moox-host-agent moox-host-agent-cli)
fi
if [[ "${TARGET_GOOS}/${TARGET_GOARCH}" == "linux/amd64" ]]; then
  binary_names+=(moox-collector-scf)
fi

binary_file() {
  local name="$1"
  [[ "${TARGET_GOOS}" == "windows" ]] && printf '%s.exe' "${name}" || printf '%s' "${name}"
}

for name in "${binary_names[@]}"; do
  source="${bin_dir}/$(binary_file "${name}")"
  [[ -s "${source}" ]] || {
    echo "missing compiled binary: ${source}; build without --skip-build or select a supported target" >&2
    exit 1
  }
done

rm -rf "${artifact_dir}"
mkdir -p "${artifact_dir}/bin" "${artifact_dir}/deploy"

for name in "${binary_names[@]}"; do
  source="${bin_dir}/$(binary_file "${name}")"
  cp "${source}" "${artifact_dir}/bin/"
done

cp "${ROOT}/scripts/release/publish-release-binaries.sh" "${artifact_dir}/deploy/"
chmod +x "${artifact_dir}/deploy/publish-release-binaries.sh" "${artifact_dir}/bin/"*

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

{
  printf 'schema_version=1\nversion=%s\ngoos=%s\ngoarch=%s\n' \
    "${VERSION}" "${TARGET_GOOS}" "${TARGET_GOARCH}"
  printf 'built_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  for name in "${binary_names[@]}"; do
    file="${artifact_dir}/bin/$(binary_file "${name}")"
    printf 'binary=%s\tsha256=%s\tsize=%s\n' \
      "$(basename "${file}")" "$(sha256 "${file}")" "$(wc -c <"${file}" | tr -d ' ')"
  done
} >"${artifact_dir}/manifest.txt"

cat >"${artifact_dir}/README.md" <<EOF
# MooX binary release

- version: ${VERSION}
- target: ${TARGET_GOOS}/${TARGET_GOARCH}
- binaries: \`bin/\`
- manifest: \`manifest.txt\`

From the repository root, publish these binaries without replacing target
configuration or data:

\`\`\`bash
./scripts/release/publish-release-binaries.sh \\
  --artifact ${artifact_dir} \\
  --target user@host \\
  --dir /home/ubuntu/moox/prod
\`\`\`

Add \`--restart\` only after checking the target's current service state.
EOF

if ((PUBLISH_LATEST)); then
  rm -rf "${OUTPUT_ROOT}/bin" "${OUTPUT_ROOT}/deploy"
  mkdir -p "${OUTPUT_ROOT}/bin" "${OUTPUT_ROOT}/deploy"
  cp -a "${artifact_dir}/bin/." "${OUTPUT_ROOT}/bin/"
  cp "${artifact_dir}/manifest.txt" "${OUTPUT_ROOT}/bin/manifest.txt"
  cp "${ROOT}/scripts/release/publish-release-binaries.sh" "${OUTPUT_ROOT}/deploy/"
  chmod +x "${OUTPUT_ROOT}/deploy/publish-release-binaries.sh" "${OUTPUT_ROOT}/bin/"*
fi

echo "==> binary release: ${artifact_dir}"
if ((PUBLISH_LATEST)); then
  echo "==> latest binaries: ${OUTPUT_ROOT}/bin"
fi
