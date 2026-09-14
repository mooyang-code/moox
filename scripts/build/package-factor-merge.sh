#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build/package-factor-merge.sh --output FILE [--binary PATH] [--skip-build]

Assemble a deploy-service ZIP for moox-factor-merge. The package contains the
merge binary, merge configs and lifecycle scripts. Credentials are never included.
EOF
}

die() {
  printf 'package-factor-merge: %s\n' "$1" >&2
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
    [[ "${target_goos}" == linux ]] || die "cross-platform merge build supports only Linux targets"
    config="${MOOX_CONFIG:-${ROOT}/moox.toml}"
    if [[ ! -f "${config}" && -f "${ROOT}/../../moox.toml" ]]; then
      config="${ROOT}/../../moox.toml"
    fi
    if [[ ! -x "${ROOT}/bin/moox-cli" ]]; then
      bash "${ROOT}/scripts/build/build.sh" cli
    fi
    CONFIG="${config}" MOOX_LINUX_CGO_TARGET=factor-merge \
      bash "${ROOT}/scripts/build/build-storage-linux.sh"
  else
    TARGET_GOOS="${target_goos}" TARGET_GOARCH="${target_goarch}" \
      bash "${ROOT}/scripts/build/build.sh" factor-merge
  fi
  binary="${binary:-${ROOT}/bin/moox-factor-merge}"
fi
[[ -n "${binary}" ]] || binary="${ROOT}/bin/moox-factor-merge"
[[ -f "${binary}" && -x "${binary}" ]] || die "merge binary is missing: ${binary}"

stage="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-merge-pkg.XXXXXX")"
cleanup() { rm -rf -- "${stage}"; }
trap cleanup EXIT

mkdir -p "${stage}/bin" "${stage}/config"
install -m 0755 "${binary}" "${stage}/bin/moox-factor-merge"
install -m 0644 "${ROOT}/modules/factor/config/merge-app.yaml" "${stage}/config/merge-app.yaml"
install -m 0644 "${ROOT}/modules/factor/config/merge-trpc.yaml" "${stage}/config/merge-trpc.yaml"
install -m 0755 "${ROOT}/scripts/deploy/factor-merge/start.sh" "${stage}/start.sh"
install -m 0755 "${ROOT}/scripts/deploy/factor-merge/stop.sh" "${stage}/stop.sh"
install -m 0755 "${ROOT}/scripts/deploy/factor-merge/healthcheck.sh" "${stage}/healthcheck.sh"

bash "${ROOT}/scripts/build/package-service.sh" --service-dir "${stage}" --output "${output}"
