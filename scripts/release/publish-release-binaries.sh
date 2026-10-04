#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${MOOX_ROOT:-}"
if [[ -z "${ROOT}" ]]; then
  candidate="${SCRIPT_DIR}"
  while [[ "${candidate}" != "/" ]]; do
    if [[ -d "${candidate}/.git" || -f "${candidate}/go.work" ]]; then
      ROOT="${candidate}"
      break
    fi
    candidate="$(dirname "${candidate}")"
  done
fi
ROOT="${ROOT:-$(pwd)}"

ARTIFACT="${MOOX_RELEASE_ARTIFACT:-${ROOT}/release/bin}"
TARGET=""
DEPLOY_DIR=""
RESTART=0
DRY_RUN=0
declare -a SELECTED_BINARIES=()

usage() {
  cat <<'EOF'
Usage: scripts/release/publish-release-binaries.sh [options]

Copy a binary bundle to an existing MooX deployment. Configuration, secrets,
data, logs, and unrelated files are never removed or overwritten.

Options:
  --artifact PATH       Release directory or its bin/ directory
  --target HOST         SSH target (user@host). Omit for local deployment
  --dir PATH            Existing deployment directory (required with --target)
  --binary NAME         Publish only this binary; repeat for several binaries
  --restart             Run the target deployment restart.sh after upload
  --dry-run             Validate and print the operation without copying
  -h, --help            Show this help

Examples:
  ./scripts/release/publish-release-binaries.sh --target user@host --dir /data/moox/prod
  ./scripts/release/publish-release-binaries.sh --artifact release/moox-binaries-v1-linux-amd64 \
    --target user@host --dir /data/moox/prod --binary moox-factor --restart
EOF
}

while (($# > 0)); do
  case "$1" in
    --artifact)
      (($# >= 2)) || { echo "--artifact requires a value" >&2; exit 2; }
      ARTIFACT="$2"
      shift 2
      ;;
    --target)
      (($# >= 2)) || { echo "--target requires a value" >&2; exit 2; }
      TARGET="$2"
      shift 2
      ;;
    --dir)
      (($# >= 2)) || { echo "--dir requires a value" >&2; exit 2; }
      DEPLOY_DIR="$2"
      shift 2
      ;;
    --binary)
      (($# >= 2)) || { echo "--binary requires a value" >&2; exit 2; }
      SELECTED_BINARIES+=("$2")
      shift 2
      ;;
    --restart)
      RESTART=1
      shift
      ;;
    --dry-run)
      DRY_RUN=1
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

if [[ -n "${TARGET}" && -z "${DEPLOY_DIR}" ]]; then
  echo "--dir is required when --target is provided" >&2
  exit 2
fi
if [[ -z "${DEPLOY_DIR}" ]]; then
  DEPLOY_DIR="${MOOX_DEPLOY_DIR:-${ROOT}}"
fi

if [[ -d "${ARTIFACT}/bin" ]]; then
  BIN_DIR="${ARTIFACT}/bin"
else
  BIN_DIR="${ARTIFACT}"
fi
[[ -d "${BIN_DIR}" ]] || { echo "binary directory does not exist: ${BIN_DIR}" >&2; exit 1; }

declare -a FILES=()
if ((${#SELECTED_BINARIES[@]})); then
  for name in "${SELECTED_BINARIES[@]}"; do
    file="${BIN_DIR}/${name}"
    [[ -f "${file}" ]] || file="${BIN_DIR}/${name}.exe"
    [[ -f "${file}" ]] || { echo "binary not found in artifact: ${name}" >&2; exit 1; }
    FILES+=("${file}")
  done
else
  shopt -s nullglob
  for file in "${BIN_DIR}"/moox-*; do
    [[ -f "${file}" ]] || continue
    FILES+=("${file}")
  done
  shopt -u nullglob
fi
((${#FILES[@]})) || { echo "no moox binaries found in ${BIN_DIR}" >&2; exit 1; }

quote_sh() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

names=()
for file in "${FILES[@]}"; do
  names+=("$(basename "${file}")")
done

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

if [[ -f "${ARTIFACT}/manifest.txt" ]]; then
  while IFS=$'\t' read -r binary_field sha_field _; do
    [[ "${binary_field}" == binary=* ]] || continue
    binary="${binary_field#binary=}"
    expected="${sha_field#sha256=}"
    selected=0
    for name in "${names[@]}"; do
      [[ "${name}" == "${binary}" ]] && selected=1 && break
    done
    ((selected)) || continue
    actual="$(sha256 "${BIN_DIR}/${binary}")"
    [[ "${actual}" == "${expected}" ]] || {
      echo "checksum mismatch for ${binary}: expected ${expected}, got ${actual}" >&2
      exit 1
    }
  done <"${ARTIFACT}/manifest.txt"
fi

echo "==> publish ${#FILES[@]} binaries to ${TARGET:-local}:${DEPLOY_DIR}/bin"
printf '    %s\n' "${names[@]}"
((DRY_RUN)) && exit 0

if [[ -z "${TARGET}" || "${TARGET}" == "localhost" || "${TARGET}" == "127.0.0.1" ]]; then
  mkdir -p "${DEPLOY_DIR}/bin"
  for file in "${FILES[@]}"; do
    cp "${file}" "${DEPLOY_DIR}/bin/"
    chmod 0755 "${DEPLOY_DIR}/bin/$(basename "${file}")"
  done
  if ((RESTART)); then
    [[ -x "${DEPLOY_DIR}/restart.sh" ]] || { echo "restart requested but ${DEPLOY_DIR}/restart.sh is missing" >&2; exit 1; }
    "${DEPLOY_DIR}/restart.sh"
  fi
  exit 0
fi

command -v ssh >/dev/null 2>&1 || { echo "ssh is required for remote publishing" >&2; exit 1; }
remote_dir="$(quote_sh "${DEPLOY_DIR}/bin")"
ssh "${TARGET}" "mkdir -p ${remote_dir}"
if command -v rsync >/dev/null 2>&1; then
  rsync -a "${FILES[@]}" "${TARGET}:${DEPLOY_DIR}/bin/"
else
  tar -C "${BIN_DIR}" -czf - "${names[@]}" | ssh "${TARGET}" "tar -xzf - -C ${remote_dir}"
fi

quoted_names=""
for name in "${names[@]}"; do
  quoted_names+=" $(quote_sh "${DEPLOY_DIR}/bin/${name}")"
done
ssh "${TARGET}" "chmod 0755${quoted_names}"
if ((RESTART)); then
  ssh "${TARGET}" "test -x $(quote_sh "${DEPLOY_DIR}/restart.sh") && $(quote_sh "${DEPLOY_DIR}/restart.sh")"
fi
echo "==> publish complete"
