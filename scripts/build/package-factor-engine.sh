#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build/package-factor-engine.sh --output FILE [--binary PATH] [--skip-build]

Assemble a deploy-service ZIP for moox-factor-engine. The package contains the
engine binary, engine configs, Python worker, runtime protocol package and
lifecycle scripts. Credentials are never included.
EOF
}

die() {
  printf 'package-factor-engine: %s\n' "$1" >&2
  exit 1
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
output=''
binary=''
skip_build=0
while (($# > 0)); do
  case "$1" in
    --output)
      (($# >= 2)) || die 'missing value for --output'
      output=$2
      shift 2
      ;;
    --binary)
      (($# >= 2)) || die 'missing value for --binary'
      binary=$2
      shift 2
      ;;
    --skip-build)
      skip_build=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "${output}" ]] || die '--output is required'
if [[ "${skip_build}" -eq 0 ]]; then
  host_goos="$(go env GOOS)"
  host_goarch="$(go env GOARCH)"
  target_goos="${TARGET_GOOS:-linux}"
  target_goarch="${TARGET_GOARCH:-amd64}"
  if [[ "${target_goos}" != "${host_goos}" || "${target_goarch}" != "${host_goarch}" ]]; then
    [[ "${target_goos}" == linux ]] || die "cross-platform engine build supports only Linux targets"
    config="${MOOX_CONFIG:-${ROOT}/moox.toml}"
    if [[ ! -f "${config}" && -f "${ROOT}/../../moox.toml" ]]; then
      config="${ROOT}/../../moox.toml"
    fi
    if [[ ! -x "${ROOT}/bin/moox-cli" ]]; then
      bash "${ROOT}/scripts/build/build.sh" cli
    fi
    CONFIG="${config}" MOOX_LINUX_CGO_TARGET=factor-engine \
      bash "${ROOT}/scripts/build/build-storage-linux.sh"
  else
    TARGET_GOOS="${target_goos}" TARGET_GOARCH="${target_goarch}" \
      bash "${ROOT}/scripts/build/build.sh" factor-engine
  fi
  binary="${binary:-${ROOT}/bin/moox-factor-engine}"
fi
[[ -n "${binary}" ]] || binary="${ROOT}/bin/moox-factor-engine"
[[ -f "${binary}" && -x "${binary}" ]] || die "engine binary is missing: ${binary}"

stage="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-engine-pkg.XXXXXX")"
cleanup() { rm -rf -- "${stage}"; }
trap cleanup EXIT

mkdir -p "${stage}/bin" "${stage}/config" "${stage}/pyworker" "${stage}/python-runtime"
install -m 0755 "${binary}" "${stage}/bin/moox-factor-engine"
install -m 0644 "${ROOT}/modules/factor/config/engine-app.yaml" "${stage}/config/engine-app.yaml"
install -m 0644 "${ROOT}/modules/factor/config/engine-trpc.yaml" "${stage}/config/engine-trpc.yaml"
install -m 0755 "${ROOT}/scripts/deploy/factor-engine/start.sh" "${stage}/start.sh"
install -m 0755 "${ROOT}/scripts/deploy/factor-engine/stop.sh" "${stage}/stop.sh"
install -m 0755 "${ROOT}/scripts/deploy/factor-engine/healthcheck.sh" "${stage}/healthcheck.sh"
cp -R "${ROOT}/modules/factor/pyworker/." "${stage}/pyworker/"
find "${stage}/pyworker" -type d -name __pycache__ -prune -exec rm -rf {} +
find "${stage}/pyworker" -type f -name '*.pyc' -delete
mkdir -p "${stage}/pyworker/wheels"
downloaded=0
for pyver in 3.10 3.11 3.12; do
  if python3 -m pip download \
    -r "${ROOT}/modules/factor/pyworker/runtime-requirements.txt" \
    -d "${stage}/pyworker/wheels" \
    --only-binary=:all: \
    --python-version "${pyver}" \
    --platform manylinux2014_x86_64 \
    --implementation cp; then
    downloaded=1
  fi
done
[[ "${downloaded}" -eq 1 ]] || die "failed to download Linux Python wheels for numpy/pandas"
shopt -s nullglob
wheel_files=("${stage}/pyworker/wheels"/*.whl)
shopt -u nullglob
((${#wheel_files[@]} > 0)) || die "engine package wheels directory is empty"
cp -R "${ROOT}/packages/pyruntime/python/." "${stage}/python-runtime/"
find "${stage}/python-runtime" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
find "${stage}/python-runtime" -type f -name '*.pyc' -delete

bash "${ROOT}/scripts/build/package-service.sh" --service-dir "${stage}" --output "${output}"
