#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TARGET=localhost
DEPLOY_DIR="~/moox"
STAGE_DIR="${ROOT}/release/deploy-stage/moox"
ADMIN_URL=http://127.0.0.1:11002
CLOUD_ACCOUNT_ID=""
NEXT_PATH=""
CREDENTIAL_BACKUP=""
CREDENTIAL_HAD_OLD=0
REMOTE_CLI=""
CONFIG_TEMPS=()
CONFIG_PATHS=()
CONFIG_BACKUPS=()
COMMITTED_COUNT=0
TOKEN="$$.${RANDOM}.${RANDOM}"

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

valid_remote_target() {
  local target=$1 user host label old_ifs
  local -a labels
  [[ "${target}" != -* ]] || return 1
  case "${target}" in
    *@*)
      user=${target%%@*}
      host=${target#*@}
      [[ "${host}" != *@* && "${user}" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || return 1
      ;;
    *) host=${target} ;;
  esac
  if [[ "${host}" == \[*\] ]]; then
    [[ "${host}" =~ ^\[[0-9A-Fa-f:]*:[0-9A-Fa-f:]*\]$ ]]
    return
  fi
  [[ ${#host} -le 253 ]] || return 1
  case "${host}" in
    .*|*.|*..*) return 1 ;;
  esac
  old_ifs=${IFS}
  IFS=.
  read -r -a labels <<<"${host}"
  IFS=${old_ifs}
  (( ${#labels[@]} > 0 )) || return 1
  for label in "${labels[@]}"; do
    [[ ${#label} -le 63 && "${label}" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
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
command -v python3 >/dev/null 2>&1 || fail "python3 is required to parse CLS prepare output"

if ! is_local; then
  valid_remote_target "${TARGET}" || fail "invalid remote target"
fi

cleanup_remote() {
  [[ -n "${REMOTE_CLI}" ]] || return 0
  ssh -o BatchMode=yes -- "${TARGET}" bash -s -- \
    "${REMOTE_CLI}" "${DEPLOY_DIR}" "${TOKEN}" <<'REMOTE' >/dev/null 2>&1 || true
set -euo pipefail
cli=$1
deploy=$2
token=$3
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
rm -f "${cli}" "${deploy}/secrets/.cls.env.${token}.next" \
  "${deploy}/secrets/.cls.env.${token}.backup"
REMOTE
}

cleanup() {
  local path
  if (( COMMITTED_COUNT > 0 )); then
    rollback_stage || true
  fi
  [[ -z "${NEXT_PATH}" ]] || rm -f "${NEXT_PATH}"
  [[ -z "${CREDENTIAL_BACKUP}" ]] || rm -f "${CREDENTIAL_BACKUP}"
  if (( ${#CONFIG_TEMPS[@]} > 0 )); then
    for path in "${CONFIG_TEMPS[@]}"; do
      rm -f "${path}"
    done
  fi
  if (( ${#CONFIG_BACKUPS[@]} > 0 )); then
    for path in "${CONFIG_BACKUPS[@]}"; do
      rm -f "${path}"
    done
  fi
  if ! is_local; then
    cleanup_remote
  fi
}

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
    if ! "${STAGE_DIR}/bin/moox-cli" ops tencent cls prepare \
      --control-url "${ADMIN_URL}" --credentials-output "${NEXT_PATH}" \
      --cloud-account-id "${CLOUD_ACCOUNT_ID}"; then
      return 1
    fi
  else
    if ! "${STAGE_DIR}/bin/moox-cli" ops tencent cls prepare \
      --control-url "${ADMIN_URL}" --credentials-output "${NEXT_PATH}"; then
      return 1
    fi
  fi
  [[ -f "${NEXT_PATH}" ]] || return 1
  chmod 0600 "${NEXT_PATH}"
}

run_remote_prepare() {
  scp -q -- "${STAGE_DIR}/bin/moox-cli" "${TARGET}:${REMOTE_CLI}"
  ssh -o BatchMode=yes -- "${TARGET}" bash -s -- \
    "${REMOTE_CLI}" "${DEPLOY_DIR}" "${ADMIN_URL}" "${CLOUD_ACCOUNT_ID}" "${TOKEN}" <<'REMOTE'
set -euo pipefail
cli=$1
deploy=$2
admin_url=$3
account_id=$4
token=$5
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
mkdir -p "${deploy}/secrets"
next="${deploy}/secrets/.cls.env.${token}.next"
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
[[ -f "${next}" ]]
chmod 0600 "${next}"
REMOTE
}

extract_topic_id() {
  local result=$1 topic
  topic=$(printf '%s' "${result}" | python3 -c '
import json
import sys

try:
    data = json.load(sys.stdin)
except (ValueError, UnicodeDecodeError):
    raise SystemExit(1)
if not isinstance(data, dict) or data.get("status") != "configured":
    raise SystemExit(1)
resources = data.get("resources")
if not isinstance(resources, dict):
    raise SystemExit(1)
topic = resources.get("topic_id")
if not isinstance(topic, str) or not topic:
    raise SystemExit(1)
print(topic)
') || return 1
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

validate_rendered_config() {
  local config=$1 topic_id=$2
  [[ -s "${config}" ]] || return 1
  [[ $(grep -c '^# BEGIN MOOX MANAGED CLS$' "${config}") == 1 ]] || return 1
  [[ $(grep -c '^# END MOOX MANAGED CLS$' "${config}") == 1 ]] || return 1
  grep -q "^          topic_id: ${topic_id}$" "${config}" || return 1
  awk '
    /^plugins:[[:space:]]*$/ { plugins=1; next }
    plugins && /^[^[:space:]#]/ { plugins=0; in_log=0 }
    plugins && /^  log:[[:space:]]*$/ { in_log=1; next }
    in_log && /^  [^[:space:]#][^:]*:/ { in_log=0 }
    in_log && /^      - writer: cls[[:space:]]*$/ { found++ }
    END { exit(found == 1 ? 0 : 1) }
  ' "${config}"
}

rollback_stage() {
  local index
  while (( COMMITTED_COUNT > 0 )); do
    index=$((COMMITTED_COUNT - 1))
    if ! mv -f "${CONFIG_BACKUPS[$index]}" "${CONFIG_PATHS[$index]}"; then
      cp -p "${CONFIG_BACKUPS[$index]}" "${CONFIG_PATHS[$index]}" || return 1
      rm -f "${CONFIG_BACKUPS[$index]}"
    fi
    COMMITTED_COUNT=$index
  done
}

commit_stage() {
  local index
  for ((index = 0; index < ${#CONFIG_PATHS[@]}; index++)); do
    if ! mv -f "${CONFIG_TEMPS[$index]}" "${CONFIG_PATHS[$index]}"; then
      rollback_stage || true
      return 1
    fi
    COMMITTED_COUNT=$((COMMITTED_COUNT + 1))
  done
}

prepare_local_credential_backup() {
  local destination
  destination="$(dirname "${NEXT_PATH}")/cls.env"
  CREDENTIAL_BACKUP="$(dirname "${NEXT_PATH}")/.cls.env.${TOKEN}.backup"
  rm -f "${CREDENTIAL_BACKUP}"
  if [[ -e "${destination}" ]]; then
    cp -p "${destination}" "${CREDENTIAL_BACKUP}"
    CREDENTIAL_HAD_OLD=1
  else
    CREDENTIAL_HAD_OLD=0
  fi
}

commit_local_credentials() {
  local destination
  destination="$(dirname "${NEXT_PATH}")/cls.env"
  chmod 0600 "${NEXT_PATH}" || return 1
  if mv -f "${NEXT_PATH}" "${destination}"; then
    NEXT_PATH=""
    rm -f "${CREDENTIAL_BACKUP}"
    CREDENTIAL_BACKUP=""
    return 0
  fi
  if (( CREDENTIAL_HAD_OLD )); then
    mv -f "${CREDENTIAL_BACKUP}" "${destination}" || \
      cp -p "${CREDENTIAL_BACKUP}" "${destination}" || true
  else
    rm -f "${destination}"
  fi
  return 1
}

commit_remote_credentials() {
  ssh -o BatchMode=yes -- "${TARGET}" bash -s -- "${DEPLOY_DIR}" "${TOKEN}" <<'REMOTE'
set -euo pipefail
deploy=$1
token=$2
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
next="${deploy}/secrets/.cls.env.${token}.next"
destination="${deploy}/secrets/cls.env"
backup="${deploy}/secrets/.cls.env.${token}.backup"
had_old=0
trap 'rm -f "${next}" "${backup}"' EXIT
[[ -f "${next}" ]]
chmod 0600 "${next}"
if [[ -e "${destination}" ]]; then
  cp -p "${destination}" "${backup}"
  had_old=1
fi
if mv -f "${next}" "${destination}"; then
  exit 0
fi
if (( had_old )); then
  mv -f "${backup}" "${destination}" || cp -p "${backup}" "${destination}"
else
  rm -f "${destination}"
fi
exit 1
REMOTE
}

trap cleanup EXIT

if is_local; then
  deploy=$(expand_local "${DEPLOY_DIR}")
  NEXT_PATH="${deploy}/secrets/.cls.env.${TOKEN}.next"
  if ! result=$(run_local_prepare "${deploy}"); then
    fail "CLS prepare failed"
  fi
else
  REMOTE_CLI="/tmp/moox-cli-cls-${TOKEN}"
  if ! result=$(run_remote_prepare); then
    fail "remote CLS prepare failed"
  fi
fi

topic_id=$(extract_topic_id "${result}") || fail "CLS prepare returned invalid sanitized JSON or no topic_id"

while IFS= read -r -d '' config; do
  tmp="${config}.cls.${TOKEN}.tmp"
  backup="${config}.cls.${TOKEN}.backup"
  render_config "${config}" "${tmp}" "${topic_id}"
  validate_rendered_config "${tmp}" "${topic_id}" || fail "invalid rendered CLS config: ${config}"
  cp -p "${config}" "${backup}"
  CONFIG_PATHS+=("${config}")
  CONFIG_TEMPS+=("${tmp}")
  CONFIG_BACKUPS+=("${backup}")
done < <(find "${STAGE_DIR}" -path '*/config/trpc_go*.yaml' -type f -print0)

if is_local; then
  [[ -f "${NEXT_PATH}" ]] || fail "CLS prepare did not write credentials"
  prepare_local_credential_backup
fi

commit_stage || fail "failed to commit staged CLS configuration"
if is_local; then
  if ! commit_local_credentials; then
    rollback_stage || true
    fail "failed to commit CLS credentials"
  fi
else
  if ! commit_remote_credentials; then
    rollback_stage || true
    fail "failed to commit remote CLS credentials"
  fi
fi

COMMITTED_COUNT=0
if (( ${#CONFIG_BACKUPS[@]} > 0 )); then
  for backup in "${CONFIG_BACKUPS[@]}"; do
    rm -f "${backup}"
  done
fi

printf '[cls-bootstrap] account and CLS resource verified; topic_id=%s\n' "${topic_id}"
