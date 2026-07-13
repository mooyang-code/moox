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
CONFIG_CLEANS=()
CONFIG_PATHS=()
CONFIG_BACKUPS=()
COMMITTED_COUNT=0
TOKEN="$$.${RANDOM}.${RANDOM}"
STAGE_LOCK=""
DEPLOY_LOCK=""
STAGE_LOCK_HELD=0
DEPLOY_LOCK_HELD=0
REMOTE_DEPLOY_LOCK_HELD=0
LOCK_LEASE_SECONDS="${MOOX_CLS_LOCK_LEASE_SECONDS:-3600}"
GUARD_LEASE_SECONDS="${MOOX_CLS_GUARD_LEASE_SECONDS:-60}"
LOCK_HOST="$(hostname)"
LOCK_STEALS=()
ACTIVE_GUARD=""
ACTIVE_GUARD_CLAIM=""
OWNER_TOKEN=""
OWNER_HOST=""
OWNER_PID=""
OWNER_CREATED_AT=""

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

STAGE_LOCK="${STAGE_DIR}/.cls-bootstrap.lock"

[[ "${LOCK_LEASE_SECONDS}" =~ ^[0-9]+$ ]] && \
  (( LOCK_LEASE_SECONDS >= 1 && LOCK_LEASE_SECONDS <= 86400 )) || \
  fail "MOOX_CLS_LOCK_LEASE_SECONDS must be 1..86400"
[[ "${GUARD_LEASE_SECONDS}" =~ ^[0-9]+$ ]] && \
  (( GUARD_LEASE_SECONDS >= 1 && GUARD_LEASE_SECONDS <= 600 )) || \
  fail "MOOX_CLS_GUARD_LEASE_SECONDS must be 1..600"
[[ "${LOCK_HOST}" =~ ^[A-Za-z0-9_.-]+$ ]] || fail "hostname is not safe for lock metadata"

[[ -d "${STAGE_DIR}" ]] || fail "missing stage directory: ${STAGE_DIR}"
[[ -x "${STAGE_DIR}/bin/moox-cli" ]] || fail "missing staged moox-cli: ${STAGE_DIR}/bin/moox-cli"
command -v python3 >/dev/null 2>&1 || fail "python3 is required to parse CLS prepare output"
python3 -c 'import yaml' >/dev/null 2>&1 || fail "PyYAML is required to validate staged configuration"

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

acquire_owned_lock() {
  local lock=$1 acquired=1
  acquire_takeover_guard "${lock}.takeover" || return 1
  ACTIVE_GUARD="${lock}.takeover"
  if mkdir "${lock}" 2>/dev/null; then
    write_owned_lock_metadata "${lock}" && acquired=0
  elif lock_is_stale_local "${lock}" "${LOCK_LEASE_SECONDS}" && steal_owned_lock "${lock}"; then
    acquired=0
  fi
  release_owned_lock "${ACTIVE_GUARD}"
  ACTIVE_GUARD=""
  return "${acquired}"
}

release_owned_lock() {
  local lock=$1
  read_lock_metadata "${lock}/owner" || return 0
  [[ "${OWNER_TOKEN}" == "${TOKEN}" ]] || return 0
  rm -f "${lock}/owner"
  rmdir "${lock}" 2>/dev/null || true
}

write_owned_lock_metadata() {
  local lock=$1 created_at
  created_at=$(date +%s)
  if ! (umask 077; printf 'token=%s\nhost=%s\npid=%s\ncreated_at=%s\n' \
    "${TOKEN}" "${LOCK_HOST}" "$$" "${created_at}" >"${lock}/owner"); then
    rm -f "${lock}/owner"
    rmdir "${lock}" 2>/dev/null || true
    return 1
  fi
}

replace_owned_lock_metadata() {
  local lock=$1 expected=$2 created_at next
  read_lock_metadata "${lock}/owner" || return 1
  [[ "${OWNER_TOKEN}" == "${expected}" ]] || return 1
  created_at=$(date +%s)
  next="${lock}/owner.next.${TOKEN}"
  if ! (umask 077; printf 'token=%s\nhost=%s\npid=%s\ncreated_at=%s\n' \
    "${TOKEN}" "${LOCK_HOST}" "$$" "${created_at}" >"${next}"); then
    rm -f "${next}"
    return 1
  fi
  read_lock_metadata "${lock}/owner" || { rm -f "${next}"; return 1; }
  if [[ "${OWNER_TOKEN}" != "${expected}" ]]; then
    rm -f "${next}"
    return 1
  fi
  mv "${next}" "${lock}/owner"
}

read_lock_metadata() {
  local owner=$1 key value token_count=0 host_count=0 pid_count=0 created_count=0
  OWNER_TOKEN=""
  OWNER_HOST=""
  OWNER_PID=""
  OWNER_CREATED_AT=""
  [[ -f "${owner}" ]] || return 1
  while IFS='=' read -r key value; do
    case "${key}" in
      token) OWNER_TOKEN=${value}; token_count=$((token_count + 1)) ;;
      host) OWNER_HOST=${value}; host_count=$((host_count + 1)) ;;
      pid) OWNER_PID=${value}; pid_count=$((pid_count + 1)) ;;
      created_at) OWNER_CREATED_AT=${value}; created_count=$((created_count + 1)) ;;
      *) return 1 ;;
    esac
  done <"${owner}"
  [[ ${token_count} -eq 1 && ${host_count} -eq 1 && ${pid_count} -eq 1 && ${created_count} -eq 1 ]] || return 1
  [[ "${OWNER_TOKEN}" =~ ^[A-Za-z0-9._-]+$ ]] || return 1
  [[ "${OWNER_HOST}" =~ ^[A-Za-z0-9_.-]+$ ]] || return 1
  [[ "${OWNER_PID}" =~ ^[0-9]+$ && "${OWNER_CREATED_AT}" =~ ^[0-9]+$ ]] || return 1
  [[ ${#OWNER_TOKEN} -le 128 && ${#OWNER_HOST} -le 255 && ${#OWNER_PID} -le 10 && ${#OWNER_CREATED_AT} -le 10 ]] || return 1
  (( OWNER_PID > 0 )) || return 1
}

lock_is_stale_local() {
  local lock=$1 lease=${2:-${LOCK_LEASE_SECONDS}} now age
  read_lock_metadata "${lock}/owner" || return 1
  if [[ "${OWNER_HOST}" == "${LOCK_HOST}" ]]; then
    kill -0 "${OWNER_PID}" 2>/dev/null && return 1
    return 0
  fi
  now=$(date +%s)
  (( OWNER_CREATED_AT <= now )) || return 1
  age=$((now - OWNER_CREATED_AT))
  (( age >= lease ))
}

acquire_takeover_guard() {
  local guard=$1 observed claim acquired=1
  if mkdir "${guard}" 2>/dev/null; then
    write_owned_lock_metadata "${guard}"
    return
  fi
  lock_is_stale_local "${guard}" "${GUARD_LEASE_SECONDS}" || return 1
  observed=${OWNER_TOKEN}
  claim="${guard}.claim.${observed}"
  if mkdir "${claim}" 2>/dev/null; then
    write_owned_lock_metadata "${claim}" || return 1
  else
    lock_is_stale_local "${claim}" "${GUARD_LEASE_SECONDS}" || return 1
    steal_owned_lock "${claim}" || return 1
  fi
  ACTIVE_GUARD_CLAIM=${claim}
  read_lock_metadata "${guard}/owner" || return 1
  if [[ "${OWNER_TOKEN}" == "${observed}" ]] && replace_owned_lock_metadata "${guard}" "${observed}"; then
    acquired=0
  fi
  release_owned_lock "${claim}"
  ACTIVE_GUARD_CLAIM=""
  return "${acquired}"
}

remove_owned_steal() {
  local steal=$1
  rm -f "${steal}/owner"
  rmdir "${steal}" 2>/dev/null || true
}

steal_owned_lock() {
  local lock=$1 steal="${lock}.steal.${TOKEN}"
  LOCK_STEALS+=("${steal}")
  mv "${lock}" "${steal}" 2>/dev/null || return 1
  if ! mkdir "${lock}" 2>/dev/null; then
    remove_owned_steal "${steal}"
    return 1
  fi
  if ! write_owned_lock_metadata "${lock}"; then
    remove_owned_steal "${steal}"
    return 1
  fi
  remove_owned_steal "${steal}"
}

acquire_remote_deploy_lock() {
  ssh -o BatchMode=yes -- "${TARGET}" bash -s -- \
    "${DEPLOY_DIR}" "${TOKEN}" "${LOCK_HOST}" "$$" "$(date +%s)" \
    "${LOCK_LEASE_SECONDS}" "${GUARD_LEASE_SECONDS}" <<'REMOTE'
set -euo pipefail
deploy=$1
token=$2
controller_host=$3
controller_pid=$4
created_at=$5
lease_seconds=$6
guard_lease_seconds=$7
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
secrets="${deploy}/secrets"
lock="${secrets}/.cls-bootstrap.lock"
[[ -d "${secrets}" ]]
main_lock=${lock}
main_steal="${main_lock}.steal.${token}"
guard="${main_lock}.takeover"
guard_steal="${guard}.steal.${token}"
claim=
claim_steal=

write_owner() {
  if ! (umask 077; printf 'token=%s\nhost=%s\npid=%s\ncreated_at=%s\n' \
    "${token}" "${controller_host}" "${controller_pid}" "${created_at}" >"${lock}/owner"); then
    rm -f "${lock}/owner"
    rmdir "${lock}" 2>/dev/null || true
    return 1
  fi
}

read_owner() {
  local key value token_count=0 host_count=0 pid_count=0 created_count=0
  owner_token= owner_host= owner_pid= owner_created=
  [[ -f "${lock}/owner" ]] || return 1
  while IFS='=' read -r key value; do
    case "${key}" in
      token) owner_token=${value}; token_count=$((token_count + 1)) ;;
      host) owner_host=${value}; host_count=$((host_count + 1)) ;;
      pid) owner_pid=${value}; pid_count=$((pid_count + 1)) ;;
      created_at) owner_created=${value}; created_count=$((created_count + 1)) ;;
      *) return 1 ;;
    esac
  done <"${lock}/owner"
  [[ ${token_count} -eq 1 && ${host_count} -eq 1 && ${pid_count} -eq 1 && ${created_count} -eq 1 ]] || return 1
  [[ "${owner_token}" =~ ^[A-Za-z0-9._-]+$ && "${owner_host}" =~ ^[A-Za-z0-9_.-]+$ ]] || return 1
  [[ "${owner_pid}" =~ ^[0-9]+$ && "${owner_created}" =~ ^[0-9]+$ ]] || return 1
  [[ ${#owner_token} -le 128 && ${#owner_host} -le 255 && ${#owner_pid} -le 10 && ${#owner_created} -le 10 ]] || return 1
  (( owner_pid > 0 ))
}

remove_steal() {
  local path=$1
  rm -f "${path}/owner"
  rmdir "${path}" 2>/dev/null || true
}

release_owned() {
  local path=$1
  [[ -f "${path}/owner" ]] || return 0
  [[ $(grep -c '^token=' "${path}/owner") -eq 1 ]] || return 0
  grep -Fqx "token=${token}" "${path}/owner" || return 0
  rm -f "${path}/owner"
  rmdir "${path}" 2>/dev/null || true
}

replace_owner() {
  local path=$1 expected=$2 next="${path}/owner.next.${token}"
  lock=${path}
  read_owner || return 1
  [[ "${owner_token}" == "${expected}" ]] || return 1
  if ! (umask 077; printf 'token=%s\nhost=%s\npid=%s\ncreated_at=%s\n' \
    "${token}" "${controller_host}" "${controller_pid}" "${created_at}" >"${next}"); then
    rm -f "${next}"
    return 1
  fi
  read_owner || { rm -f "${next}"; return 1; }
  if [[ "${owner_token}" != "${expected}" ]]; then
    rm -f "${next}"
    return 1
  fi
  mv "${next}" "${path}/owner"
}

cleanup_acquire() {
  remove_steal "${main_steal}"
  remove_steal "${guard_steal}"
  [[ -z "${claim_steal}" ]] || remove_steal "${claim_steal}"
  [[ -z "${claim}" ]] || release_owned "${claim}"
  release_owned "${guard}"
}
trap cleanup_acquire EXIT

lock=${guard}
if mkdir "${guard}" 2>/dev/null; then
  write_owner
else
  read_owner || exit 73
  now=$(date +%s)
  (( owner_created <= now && now - owner_created >= guard_lease_seconds )) || exit 73
  observed=${owner_token}
  claim="${guard}.claim.${observed}"
  claim_steal="${claim}.steal.${token}"
  lock=${claim}
  if mkdir "${claim}" 2>/dev/null; then
    write_owner
  else
    read_owner || exit 73
    now=$(date +%s)
    (( owner_created <= now && now - owner_created >= guard_lease_seconds )) || exit 73
    claim_observed=${owner_token}
    read_owner || exit 73
    [[ "${owner_token}" == "${claim_observed}" ]] || exit 73
    mv "${claim}" "${claim_steal}" 2>/dev/null || exit 73
    mkdir "${claim}" 2>/dev/null || exit 73
    write_owner
  fi
  lock=${guard}
  read_owner || exit 73
  [[ "${owner_token}" == "${observed}" ]] || exit 73
  replace_owner "${guard}" "${observed}" || exit 73
fi

lock=${main_lock}
if mkdir "${main_lock}" 2>/dev/null; then
  write_owner
  exit 0
fi
read_owner || exit 73
now=$(date +%s)
(( owner_created <= now && now - owner_created >= lease_seconds )) || exit 73
observed=${owner_token}
read_owner || exit 73
[[ "${owner_token}" == "${observed}" ]] || exit 73
mv "${main_lock}" "${main_steal}" 2>/dev/null || exit 73
mkdir "${main_lock}" 2>/dev/null || exit 73
write_owner
REMOTE
}

release_remote_deploy_lock() {
  (( REMOTE_DEPLOY_LOCK_HELD )) || return 0
  ssh -o BatchMode=yes -- "${TARGET}" bash -s -- "${DEPLOY_DIR}" "${TOKEN}" <<'REMOTE' >/dev/null 2>&1 || true
set -euo pipefail
deploy=$1
token=$2
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#\~/}" ;;
esac
lock="${deploy}/secrets/.cls-bootstrap.lock"
steal="${lock}.steal.${token}"
rm -f "${steal}/owner"
rmdir "${steal}" 2>/dev/null || true
guard="${lock}.takeover"
guard_steal="${guard}.steal.${token}"
rm -f "${guard_steal}/owner"
rmdir "${guard_steal}" 2>/dev/null || true
if [[ -f "${guard}/owner" && $(grep -c '^token=' "${guard}/owner") -eq 1 ]] && \
  grep -Fqx "token=${token}" "${guard}/owner"; then
  rm -f "${guard}/owner"
  rmdir "${guard}" 2>/dev/null || true
fi
[[ -f "${lock}/owner" ]] || exit 0
[[ $(grep -c '^token=' "${lock}/owner") -eq 1 ]] || exit 0
grep -Fqx "token=${token}" "${lock}/owner" || exit 0
rm -f "${lock}/owner"
rmdir "${lock}" 2>/dev/null || true
REMOTE
  REMOTE_DEPLOY_LOCK_HELD=0
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
  if (( ${#CONFIG_CLEANS[@]} > 0 )); then
    for path in "${CONFIG_CLEANS[@]}"; do
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
  if [[ -n "${ACTIVE_GUARD_CLAIM}" ]]; then
    release_owned_lock "${ACTIVE_GUARD_CLAIM}"
    ACTIVE_GUARD_CLAIM=""
  fi
  if [[ -n "${ACTIVE_GUARD}" ]]; then
    release_owned_lock "${ACTIVE_GUARD}"
    ACTIVE_GUARD=""
  fi
  if is_local; then
    if (( DEPLOY_LOCK_HELD )); then
      release_owned_lock "${DEPLOY_LOCK}"
      DEPLOY_LOCK_HELD=0
    fi
  else
    release_remote_deploy_lock
  fi
  if (( STAGE_LOCK_HELD )); then
    release_owned_lock "${STAGE_LOCK}"
    STAGE_LOCK_HELD=0
  fi
  if (( ${#LOCK_STEALS[@]} > 0 )); then
    for path in "${LOCK_STEALS[@]}"; do
      remove_owned_steal "${path}"
    done
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
  scp -q -o BatchMode=yes -- "${STAGE_DIR}/bin/moox-cli" "${TARGET}:${REMOTE_CLI}"
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

validate_managed_markers() {
  awk '
    $0 == "# BEGIN MOOX MANAGED CLS" {
      if (open) invalid=1
      open=1
      next
    }
    $0 == "# END MOOX MANAGED CLS" {
      if (!open) invalid=1
      open=0
      next
    }
    END { exit(invalid || open ? 1 : 0) }
  ' "$1"
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
  local config=$1 output=$2 topic_id=$3 cleaned=$4
  validate_managed_markers "${config}" || return 1
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
  python3 - "${config}" "${topic_id}" <<'PY' >/dev/null 2>&1
import sys
import yaml
from yaml.constructor import ConstructorError
from yaml.nodes import MappingNode, ScalarNode, SequenceNode


def node_fingerprint(node):
    if isinstance(node, ScalarNode):
        return ("scalar", node.tag, node.value)
    if isinstance(node, SequenceNode):
        return ("sequence", node.tag, tuple(node_fingerprint(item) for item in node.value))
    if isinstance(node, MappingNode):
        return (
            "mapping",
            node.tag,
            frozenset((node_fingerprint(key), node_fingerprint(value)) for key, value in node.value),
        )
    return (type(node).__name__, node.tag)


class UniqueKeySafeLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        if not isinstance(node, MappingNode):
            return super().construct_mapping(node, deep=deep)
        seen_nodes = set()
        seen_values = set()
        for key_node, _ in node.value:
            structural = node_fingerprint(key_node)
            if structural in seen_nodes:
                raise ConstructorError(
                    "while constructing a mapping",
                    node.start_mark,
                    "found duplicate mapping key",
                    key_node.start_mark,
                )
            seen_nodes.add(structural)
            try:
                key = self.construct_object(key_node, deep=True)
                signature = (key_node.tag, key)
                hash(signature)
            except TypeError:
                continue
            if signature in seen_values:
                raise ConstructorError(
                    "while constructing a mapping",
                    node.start_mark,
                    "found duplicate mapping key",
                    key_node.start_mark,
                )
            seen_values.add(signature)
        return super().construct_mapping(node, deep=deep)

path, expected_topic = sys.argv[1:]
with open(path, encoding="utf-8") as stream:
    document = yaml.load(stream, Loader=UniqueKeySafeLoader)
if not isinstance(document, dict):
    raise SystemExit(1)
plugins = document.get("plugins")
log = plugins.get("log") if isinstance(plugins, dict) else None
default = log.get("default") if isinstance(log, dict) else None
if not isinstance(default, list):
    raise SystemExit(1)
managed = [item for item in default if isinstance(item, dict) and item.get("writer") == "cls"]
if len(managed) != 1:
    raise SystemExit(1)
writer = managed[0]
remote = writer.get("remote_config")
if writer.get("level") != "warn" or not isinstance(remote, dict):
    raise SystemExit(1)
expected_remote = {
    "topic_id": expected_topic,
    "host": "${MOOX_CLS_HOST}",
    "secret_id": "${MOOX_CLS_SECRET_ID}",
    "secret_key": "${MOOX_CLS_SECRET_KEY}",
    "max_block_sec": 0,
}
if any(remote.get(key) != value for key, value in expected_remote.items()):
    raise SystemExit(1)

def cls_writers(value):
    if isinstance(value, dict):
        return (1 if value.get("writer") == "cls" else 0) + sum(cls_writers(item) for item in value.values())
    if isinstance(value, list):
        return sum(cls_writers(item) for item in value)
    return 0

if cls_writers(document) != 1:
    raise SystemExit(1)
PY
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

if ! acquire_owned_lock "${STAGE_LOCK}"; then
  fail "CLS bootstrap is already running for this stage"
fi
STAGE_LOCK_HELD=1

if is_local; then
  deploy=$(expand_local "${DEPLOY_DIR}")
  [[ -d "${deploy}/secrets" ]] || fail "missing deploy secrets directory"
  DEPLOY_LOCK="${deploy}/secrets/.cls-bootstrap.lock"
  if ! acquire_owned_lock "${DEPLOY_LOCK}"; then
    fail "CLS bootstrap is already running for this deployment"
  fi
  DEPLOY_LOCK_HELD=1
  NEXT_PATH="${deploy}/secrets/.cls.env.${TOKEN}.next"
  if ! result=$(run_local_prepare "${deploy}"); then
    fail "CLS prepare failed"
  fi
else
  if ! acquire_remote_deploy_lock; then
    fail "CLS bootstrap is already running for the remote deployment"
  fi
  REMOTE_DEPLOY_LOCK_HELD=1
  REMOTE_CLI="/tmp/moox-cli-cls-${TOKEN}"
  if ! result=$(run_remote_prepare); then
    fail "remote CLS prepare failed"
  fi
fi

topic_id=$(extract_topic_id "${result}") || fail "CLS prepare returned invalid sanitized JSON or no topic_id"

while IFS= read -r -d '' config; do
  tmp="${config}.cls.${TOKEN}.tmp"
  clean="${tmp}.clean"
  backup="${config}.cls.${TOKEN}.backup"
  CONFIG_PATHS+=("${config}")
  CONFIG_TEMPS+=("${tmp}")
  CONFIG_CLEANS+=("${clean}")
  CONFIG_BACKUPS+=("${backup}")
  render_config "${config}" "${tmp}" "${topic_id}" "${clean}"
  validate_rendered_config "${tmp}" "${topic_id}" || fail "invalid rendered CLS config: ${config}"
  cp -p "${config}" "${backup}"
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
