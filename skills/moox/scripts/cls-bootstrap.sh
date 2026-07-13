#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TARGET=localhost
DEPLOY_DIR="~/moox"
STAGE_DIR="${ROOT}/release/deploy-stage/moox"
ADMIN_URL=http://127.0.0.1:11002
CLOUD_ACCOUNT_ID=""
NEXT_PATH=""
REMOTE_CLI=""
CONFIG_TEMPS=()
CONFIG_PATHS=()

fail() {
  printf '[cls-bootstrap] ERROR: %s\n' "$*" >&2
  exit 1
}

is_local() {
  [[ "${TARGET}" == localhost || "${TARGET}" == 127.0.0.1 || "${TARGET}" == ::1 ]]
}

expand_local() {
  case "$1" in
    "~") printf '%s\n' "${HOME}" ;;
    "~/"*) printf '%s/%s\n' "${HOME}" "${1#\~/}" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

require_value() {
  [[ $# -ge 2 && -n "$2" ]] || fail "$1 requires a value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) require_value "$@"; TARGET=$2; shift 2 ;;
    --deploy-dir) require_value "$@"; DEPLOY_DIR=$2; shift 2 ;;
    --stage-dir) require_value "$@"; STAGE_DIR=$2; shift 2 ;;
    --admin-url) require_value "$@"; ADMIN_URL=$2; shift 2 ;;
    --cloud-account-id) require_value "$@"; CLOUD_ACCOUNT_ID=$2; shift 2 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ -d "${STAGE_DIR}" ]] || fail "missing stage directory: ${STAGE_DIR}"
[[ -x "${STAGE_DIR}/bin/moox-cli" ]] || fail "missing staged moox-cli: ${STAGE_DIR}/bin/moox-cli"

cleanup_remote() {
  [[ -n "${REMOTE_CLI}" ]] || return 0
  ssh -o BatchMode=yes "${TARGET}" bash -s -- "${REMOTE_CLI}" "${DEPLOY_DIR}" <<'REMOTE' >/dev/null 2>&1 || true
set -euo pipefail
cli=$1
deploy=$2
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
rm -f "${cli}" "${deploy}/secrets/cls.env.next"
REMOTE
}

cleanup() {
  local path
  [[ -z "${NEXT_PATH}" ]] || rm -f "${NEXT_PATH}"
  if (( ${#CONFIG_TEMPS[@]} > 0 )); then
    for path in "${CONFIG_TEMPS[@]}"; do
      rm -f "${path}"
    done
  fi
  if ! is_local; then
    cleanup_remote
  fi
}
trap cleanup EXIT

run_local_prepare() {
  local deploy=$1
  [[ -r "${deploy}/secrets/service-auth.env" ]] || fail "missing service-auth.env"
  mkdir -p "${deploy}/secrets"
  rm -f "${NEXT_PATH}"
  set -a
  # shellcheck disable=SC1090
  source "${deploy}/secrets/service-auth.env"
  set +a
  if [[ -n "${CLOUD_ACCOUNT_ID}" ]]; then
    "${STAGE_DIR}/bin/moox-cli" ops tencent cls prepare \
      --control-url "${ADMIN_URL}" --credentials-output "${NEXT_PATH}" \
      --cloud-account-id "${CLOUD_ACCOUNT_ID}"
  else
    "${STAGE_DIR}/bin/moox-cli" ops tencent cls prepare \
      --control-url "${ADMIN_URL}" --credentials-output "${NEXT_PATH}"
  fi
}

run_remote_prepare() {
  scp -q "${STAGE_DIR}/bin/moox-cli" "${TARGET}:${REMOTE_CLI}"
  ssh -o BatchMode=yes "${TARGET}" bash -s -- \
    "${REMOTE_CLI}" "${DEPLOY_DIR}" "${ADMIN_URL}" "${CLOUD_ACCOUNT_ID}" <<'REMOTE'
set -euo pipefail
cli=$1
deploy=$2
admin_url=$3
account_id=$4
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
mkdir -p "${deploy}/secrets"
next="${deploy}/secrets/cls.env.next"
trap 'status=$?; if [[ ${status} -ne 0 ]]; then rm -f "${next}"; fi; exit ${status}' EXIT
[[ -r "${deploy}/secrets/service-auth.env" ]]
set -a
source "${deploy}/secrets/service-auth.env"
set +a
chmod 0700 "${cli}"
rm -f "${next}"
if [[ -n "${account_id}" ]]; then
  "${cli}" ops tencent cls prepare --control-url "${admin_url}" \
    --credentials-output "${next}" --cloud-account-id "${account_id}"
else
  "${cli}" ops tencent cls prepare --control-url "${admin_url}" \
    --credentials-output "${next}"
fi
REMOTE
}

extract_topic_id() {
  local result=$1 topic
  topic=$(printf '%s\n' "${result}" | \
    sed -n 's/.*"topic_id"[[:space:]]*:[[:space:]]*"\([^"[:space:]]*\)".*/\1/p' | tail -n 1)
  [[ -n "${topic}" ]] || return 1
  case "${topic}" in
    *[!A-Za-z0-9._:-]*) return 1 ;;
  esac
  printf '%s\n' "${topic}"
}

strip_managed_block() {
  awk '
    $0 == "# BEGIN MOOX MANAGED CLS" { skip=1; next }
    $0 == "# END MOOX MANAGED CLS" { skip=0; next }
    !skip { print }
  ' "$1" >"$2"
}

has_log_default() {
  awk '
    /^plugins:[[:space:]]*$/ { plugins=1; next }
    plugins && /^[^[:space:]#]/ { plugins=0; in_log=0 }
    plugins && /^  log:[[:space:]]*$/ { in_log=1; next }
    in_log && /^  [^[:space:]#][^:]*:/ { in_log=0 }
    in_log && /^    default:[[:space:]]*$/ { found=1 }
    END { exit(found ? 0 : 1) }
  ' "$1"
}

render_config() {
  local config=$1 output=$2 topic_id=$3 cleaned
  cleaned="${output}.clean"
  strip_managed_block "${config}" "${cleaned}"
  if has_log_default "${cleaned}"; then
    awk -v topic_id="${topic_id}" '
      function block() {
        print "# BEGIN MOOX MANAGED CLS"
        print "      - writer: cls"
        print "        level: warn"
        print "        remote_config:"
        print "          topic_id: " topic_id
        print "          host: ${MOOX_CLS_HOST}"
        print "          secret_id: ${MOOX_CLS_SECRET_ID}"
        print "          secret_key: ${MOOX_CLS_SECRET_KEY}"
        print "          max_block_sec: 0"
        print "# END MOOX MANAGED CLS"
      }
      {
        print
        if ($0 ~ /^plugins:[[:space:]]*$/) { plugins=1; next }
        if (plugins && $0 ~ /^[^[:space:]#]/) { plugins=0; in_log=0 }
        if (plugins && $0 ~ /^  log:[[:space:]]*$/) { in_log=1; next }
        if (in_log && $0 ~ /^  [^[:space:]#][^:]*:/) { in_log=0 }
        if (in_log && $0 ~ /^    default:[[:space:]]*$/ && !done) {
          block()
          done=1
        }
      }
    ' "${cleaned}" >"${output}"
  elif grep -q '^plugins:[[:space:]]*$' "${cleaned}"; then
    awk -v topic_id="${topic_id}" '
      function block() {
        print "# BEGIN MOOX MANAGED CLS"
        print "  log:"
        print "    default:"
        print "      - writer: cls"
        print "        level: warn"
        print "        remote_config:"
        print "          topic_id: " topic_id
        print "          host: ${MOOX_CLS_HOST}"
        print "          secret_id: ${MOOX_CLS_SECRET_ID}"
        print "          secret_key: ${MOOX_CLS_SECRET_KEY}"
        print "          max_block_sec: 0"
        print "# END MOOX MANAGED CLS"
      }
      { print }
      /^plugins:[[:space:]]*$/ && !done { block(); done=1 }
    ' "${cleaned}" >"${output}"
  else
    cp "${cleaned}" "${output}"
    cat >>"${output}" <<EOF
plugins:
# BEGIN MOOX MANAGED CLS
  log:
    default:
      - writer: cls
        level: warn
        remote_config:
          topic_id: ${topic_id}
          host: \${MOOX_CLS_HOST}
          secret_id: \${MOOX_CLS_SECRET_ID}
          secret_key: \${MOOX_CLS_SECRET_KEY}
          max_block_sec: 0
# END MOOX MANAGED CLS
EOF
  fi
  rm -f "${cleaned}"
}

if is_local; then
  deploy=$(expand_local "${DEPLOY_DIR}")
  NEXT_PATH="${deploy}/secrets/cls.env.next"
  if ! result=$(run_local_prepare "${deploy}"); then
    fail "CLS prepare failed"
  fi
else
  REMOTE_CLI="/tmp/moox-cli-cls-${RANDOM}.$$"
  if ! result=$(run_remote_prepare); then
    fail "remote CLS prepare failed"
  fi
fi

topic_id=$(extract_topic_id "${result}") || fail "CLS prepare returned invalid sanitized JSON or no topic_id"

while IFS= read -r -d '' config; do
  tmp="${config}.cls.${RANDOM}.$$.tmp"
  render_config "${config}" "${tmp}" "${topic_id}"
  CONFIG_PATHS+=("${config}")
  CONFIG_TEMPS+=("${tmp}")
done < <(find "${STAGE_DIR}" -path '*/config/trpc_go*.yaml' -type f -print0)

if is_local; then
  [[ -f "${NEXT_PATH}" ]] || fail "CLS prepare did not write credentials"
  chmod 0600 "${NEXT_PATH}"
  mv -f "${NEXT_PATH}" "$(dirname "${NEXT_PATH}")/cls.env"
  NEXT_PATH=""
else
  ssh -o BatchMode=yes "${TARGET}" bash -s -- "${DEPLOY_DIR}" <<'REMOTE'
set -euo pipefail
deploy=$1
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
next="${deploy}/secrets/cls.env.next"
[[ -f "${next}" ]]
chmod 0600 "${next}"
mv -f "${next}" "${deploy}/secrets/cls.env"
REMOTE
fi

for ((i = 0; i < ${#CONFIG_PATHS[@]}; i++)); do
  mv -f "${CONFIG_TEMPS[$i]}" "${CONFIG_PATHS[$i]}"
done

printf '[cls-bootstrap] account and CLS resource verified; topic_id=%s\n' "${topic_id}"
