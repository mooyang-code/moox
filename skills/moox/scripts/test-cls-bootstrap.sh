#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-cls-skill.XXXXXX")"
trap '[[ -n "${MOOX_TEST_KEEP_TMP:-}" ]] || rm -rf "${TMP}"' EXIT

mode_file="${TMP}/mode"
calls="${TMP}/calls"
printf 'success\n' >"${mode_file}"

new_stage() {
  local stage=$1
  mkdir -p "${stage}/bin" "${stage}/factor/config" "${stage}/storage/config" \
    "${stage}/nolog/config" "${stage}/bare/config" "${stage}/ignored/config"
  cp "${ROOT}/modules/factor/config/trpc_go.yaml" "${stage}/factor/config/trpc_go.yaml"
  cp "${ROOT}/modules/storage/config/trpc_go.yaml" "${stage}/storage/config/trpc_go-prod.yaml"
  printf 'leave me alone\n' >"${stage}/ignored/config/not-trpc.yaml"
  printf 'plugins:\n  metrics:\n    prometheus:\n      port: 1234\n' \
    >"${stage}/nolog/config/trpc_go.yaml"
  printf 'global:\n  namespace: Development\n' >"${stage}/bare/config/trpc_go-test.yaml"
  cat >"${stage}/bin/moox-cli" <<'CLI'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${MOOX_TEST_CALLS}"
[[ "${MOOX_SERVICE_AUTH_ACCESS_KEY:-}" == "svc""-ak" ]]
[[ "${MOOX_SERVICE_AUTH_SECRET_KEY:-}" == "svc""-sk" ]]
output=
cloud_account=__not_set__
while [[ $# -gt 0 ]]; do
  case "$1" in
    --credentials-output) output=$2; shift 2 ;;
    --cloud-account-id) cloud_account=$2; shift 2 ;;
    *) shift ;;
  esac
done
[[ -n "${output}" ]]
[[ "${cloud_account}" == "${MOOX_TEST_EXPECT_ACCOUNT}" ]]
secret_prefix=secret
topic=topic-fixed
case "${cloud_account}" in
  concurrent-a) secret_prefix=concurrent-a; topic=topic-a; sleep 1 ;;
  concurrent-b) secret_prefix=concurrent-b; topic=topic-b; sleep 1 ;;
esac
printf "MOOX_CLS_SECRET_ID='%s-id'\nMOOX_CLS_SECRET_KEY='%s-key'\nMOOX_CLS_REGION='ap-guangzhou'\nMOOX_CLS_HOST='ap-guangzhou.cls.tencentyun.com'\nMOOX_CLS_AUTO_BOOTSTRAP=0\n" \
  "${secret_prefix}" "${secret_prefix}" >"${output}"
chmod 0600 "${output}"
case "$(cat "${MOOX_TEST_MODE}")" in
  success)
    printf '{"status":"configured","resources":{"account_id":"acct-first","topic_id":"%s"}}\n' "${topic}"
    ;;
  missing-topic)
    printf '{"status":"configured","resources":{"account_id":"acct-first"}}\n'
    ;;
  malformed) printf '{not-json}\n' ;;
  leading-garbage) printf 'garbage {"status":"configured","resources":{"topic_id":"topic-fixed"}}\n' ;;
  trailing-garbage) printf '{"status":"configured","resources":{"topic_id":"topic-fixed"}} garbage\n' ;;
  multiple-json) printf '{"status":"configured","resources":{"topic_id":"topic-fixed"}}\n{}\n' ;;
  wrong-status) printf '{"status":"dry-run","resources":{"topic_id":"topic-fixed"}}\n' ;;
  wrong-topic-type) printf '{"status":"configured","resources":{"topic_id":123}}\n' ;;
  empty-topic) printf '{"status":"configured","resources":{"topic_id":""}}\n' ;;
  unsafe-topic) printf '{"status":"configured","resources":{"topic_id":"topic unsafe"}}\n' ;;
  failure)
    printf 'sanitized prepare failure\n' >&2
    exit 42
    ;;
esac
CLI
  chmod +x "${stage}/bin/moox-cli"
}

new_deploy() {
  local deploy=$1
  mkdir -p "${deploy}/secrets"
  printf 'MOOX_SERVICE_AUTH_ACCESS_KEY=svc-ak\nMOOX_SERVICE_AUTH_SECRET_KEY=svc-sk\n' \
    >"${deploy}/secrets/service-auth.env"
  printf 'old-cls-env\n' >"${deploy}/secrets/cls.env"
  chmod 0600 "${deploy}/secrets/service-auth.env" "${deploy}/secrets/cls.env"
}

file_mode() {
  stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"
}

tree_digest() {
  local dir=$1
  find "${dir}" -type f ! -path '*/bin/moox-cli' -print0 | LC_ALL=C sort -z | \
    xargs -0 shasum -a 256
}

assert_no_transaction_artifacts() {
  local stage=$1 deploy=$2
  [[ -z "$(find "${stage}" -type f -name '*.cls.*' -print -quit)" ]]
  [[ -z "$(find "${deploy}/secrets" -type f \( -name '.cls.env.*.next' -o -name '.cls.env.*.backup' \) -print -quit)" ]]
}

assert_no_secrets() {
  local output=$1 stage=$2
  ! grep -qE 'secret-id|secret-key|svc-ak|svc-sk' "${output}"
  ! grep -R -qE 'secret-id|secret-key|svc-ak|svc-sk' "${stage}"
}

assert_cls_log_writer() {
  awk '
    /^plugins:[[:space:]]*$/ { plugins=1; next }
    plugins && /^[^[:space:]#]/ { plugins=0; in_log=0 }
    plugins && /^  log:[[:space:]]*$/ { in_log=1; next }
    in_log && /^  [^[:space:]#][^:]*:/ { in_log=0 }
    in_log && /^      - writer: cls[[:space:]]*$/ { found++ }
    END { exit(found == 1 ? 0 : 1) }
  ' "$1"
}

write_lock_owner() {
  local lock=$1 token=$2 host=$3 pid=$4 created_at=$5
  mkdir -p "${lock}"
  printf 'token=%s\nhost=%s\npid=%s\ncreated_at=%s\n' \
    "${token}" "${host}" "${pid}" "${created_at}" >"${lock}/owner"
}

STAGE="${TMP}/stage"
DEPLOY="${TMP}/deploy"
new_stage "${STAGE}"
new_deploy "${DEPLOY}"

output="${TMP}/local-output"
MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
  MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
  --admin-url http://127.0.0.1:11002 >"${output}" 2>&1

grep -q -- '--control-url http://127.0.0.1:11002' "${calls}"
if grep -q -- '--cloud-account-id' "${calls}"; then
  echo 'default invocation unexpectedly supplied cloud-account-id' >&2
  exit 1
fi
grep -q 'writer: cls' "${STAGE}/factor/config/trpc_go.yaml"
grep -q 'topic_id: topic-fixed' "${STAGE}/factor/config/trpc_go.yaml"
grep -q 'topic_id: topic-fixed' "${STAGE}/storage/config/trpc_go-prod.yaml"
assert_cls_log_writer "${STAGE}/factor/config/trpc_go.yaml"
assert_cls_log_writer "${STAGE}/storage/config/trpc_go-prod.yaml"
assert_cls_log_writer "${STAGE}/nolog/config/trpc_go.yaml"
assert_cls_log_writer "${STAGE}/bare/config/trpc_go-test.yaml"
grep -q '^leave me alone$' "${STAGE}/ignored/config/not-trpc.yaml"
[[ $(file_mode "${DEPLOY}/secrets/cls.env") == 600 ]]
[[ ! -e "${DEPLOY}/secrets/cls.env.next" ]]
assert_no_transaction_artifacts "${STAGE}" "${DEPLOY}"
assert_no_secrets "${output}" "${STAGE}"

MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
  MOOX_TEST_EXPECT_ACCOUNT='explicit account' \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
  --admin-url http://127.0.0.1:11002 --cloud-account-id 'explicit account' >>"${output}" 2>&1
[[ $(grep -c 'writer: cls' "${STAGE}/factor/config/trpc_go.yaml") == 1 ]]
grep -q -- '--cloud-account-id explicit account' "${calls}"
[[ $(wc -l <"${calls}" | tr -d ' ') == 2 ]]
assert_no_secrets "${output}" "${STAGE}"

before_stage=$(tree_digest "${STAGE}")
before_env=$(shasum -a 256 "${DEPLOY}/secrets/cls.env")
for invalid_json_mode in missing-topic malformed leading-garbage trailing-garbage multiple-json wrong-status wrong-topic-type empty-topic unsafe-topic; do
  printf '%s\n' "${invalid_json_mode}" >"${mode_file}"
  if MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
    MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
    --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
    echo "${invalid_json_mode} unexpectedly succeeded" >&2
    exit 1
  fi
  [[ "${before_stage}" == "$(tree_digest "${STAGE}")" ]]
  [[ "${before_env}" == "$(shasum -a 256 "${DEPLOY}/secrets/cls.env")" ]]
  assert_no_transaction_artifacts "${STAGE}" "${DEPLOY}"
done

printf 'failure\n' >"${mode_file}"
if MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
  MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
  --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
  echo 'CLI failure unexpectedly succeeded' >&2
  exit 1
fi
[[ "${before_stage}" == "$(tree_digest "${STAGE}")" ]]
[[ "${before_env}" == "$(shasum -a 256 "${DEPLOY}/secrets/cls.env")" ]]
[[ ! -e "${DEPLOY}/secrets/cls.env.next" ]]
assert_no_secrets "${output}" "${STAGE}"

# Host strings are data, never OpenSSH options or shell syntax.
transport_marker="${TMP}/transport-called"
cat >"${TMP}/reject-scp" <<'EOF'
#!/usr/bin/env bash
touch "${MOOX_TEST_TRANSPORT_MARKER}"
exit 97
EOF
chmod +x "${TMP}/reject-scp"
mkdir -p "${TMP}/reject-bin"
ln -s "${TMP}/reject-scp" "${TMP}/reject-bin/scp"
ln -s "${TMP}/reject-scp" "${TMP}/reject-bin/ssh"
for hostile_target in '-oProxyCommand=touch /tmp/pwned' 'host with spaces' $'host\nnewline' 'user@@host' 'host..invalid'; do
  if PATH="${TMP}/reject-bin:${PATH}" MOOX_TEST_TRANSPORT_MARKER="${transport_marker}" \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target "${hostile_target}" \
    --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
    echo "hostile target unexpectedly succeeded: ${hostile_target}" >&2
    exit 1
  fi
done
[[ ! -e "${transport_marker}" ]]

# A failed acquisition never removes a lock owned by another transaction.
for foreign_lock_scope in stage deploy; do
  LOCK_STAGE="${TMP}/lock-stage-${foreign_lock_scope}"
  LOCK_DEPLOY="${TMP}/lock-deploy-${foreign_lock_scope}"
  new_stage "${LOCK_STAGE}"
  new_deploy "${LOCK_DEPLOY}"
  if [[ "${foreign_lock_scope}" == stage ]]; then
    foreign_lock="${LOCK_STAGE}/.cls-bootstrap.lock"
  else
    foreign_lock="${LOCK_DEPLOY}/secrets/.cls-bootstrap.lock"
  fi
  mkdir "${foreign_lock}"
  printf 'foreign-owner\n' >"${foreign_lock}/owner"
  if MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
    MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
    --deploy-dir "${LOCK_DEPLOY}" --stage-dir "${LOCK_STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
    echo "foreign ${foreign_lock_scope} lock unexpectedly acquired" >&2
    exit 1
  fi
  [[ $(cat "${foreign_lock}/owner") == foreign-owner ]]
  if [[ "${foreign_lock_scope}" == deploy ]]; then
    [[ ! -e "${LOCK_STAGE}/.cls-bootstrap.lock" ]]
  fi
done

local_host=$(hostname)
now=$(date +%s)
for owner_case in active-local fresh-foreign; do
  OWNER_STAGE="${TMP}/owner-stage-${owner_case}"
  OWNER_DEPLOY="${TMP}/owner-deploy-${owner_case}"
  new_stage "${OWNER_STAGE}"
  new_deploy "${OWNER_DEPLOY}"
  owner_lock="${OWNER_STAGE}/.cls-bootstrap.lock"
  if [[ "${owner_case}" == active-local ]]; then
    write_lock_owner "${owner_lock}" active-token "${local_host}" "$$" 1
  else
    write_lock_owner "${owner_lock}" fresh-token another-host 12345 "${now}"
  fi
  owner_before=$(shasum -a 256 "${owner_lock}/owner")
  if MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
    MOOX_TEST_EXPECT_ACCOUNT=__not_set__ MOOX_CLS_LOCK_LEASE_SECONDS=600 \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
    --deploy-dir "${OWNER_DEPLOY}" --stage-dir "${OWNER_STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
    echo "${owner_case} lock unexpectedly acquired" >&2
    exit 1
  fi
  [[ "${owner_before}" == "$(shasum -a 256 "${owner_lock}/owner")" ]]
done

DEAD_STAGE="${TMP}/dead-stage"
DEAD_DEPLOY="${TMP}/dead-deploy"
new_stage "${DEAD_STAGE}"
new_deploy "${DEAD_DEPLOY}"
write_lock_owner "${DEAD_STAGE}/.cls-bootstrap.lock" dead-token "${local_host}" 99999999 "${now}"
printf 'success\n' >"${mode_file}"
MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
  MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
  --deploy-dir "${DEAD_DEPLOY}" --stage-dir "${DEAD_STAGE}" \
  --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1
grep -q 'topic_id: topic-fixed' "${DEAD_STAGE}/factor/config/trpc_go.yaml"
[[ ! -e "${DEAD_STAGE}/.cls-bootstrap.lock" ]]

# A failure during either stage or credential commit restores the whole release.
REAL_MV=$(command -v mv)
FAIL_BIN="${TMP}/fail-bin"
mkdir -p "${FAIL_BIN}"
cat >"${FAIL_BIN}/mv" <<'MV'
#!/usr/bin/env bash
set -euo pipefail
destination=${!#}
if [[ "${destination}" == *"${MOOX_TEST_FAIL_MV_DEST}" && ! -e "${MOOX_TEST_FAIL_MV_ONCE}" ]]; then
  : >"${MOOX_TEST_FAIL_MV_ONCE}"
  exit 88
fi
exec "${MOOX_TEST_REAL_MV}" "$@"
MV
chmod +x "${FAIL_BIN}/mv"
for fail_destination in '/storage/config/trpc_go-prod.yaml' '/secrets/cls.env'; do
  TX_STAGE="${TMP}/tx-stage-${fail_destination##*/}"
  TX_DEPLOY="${TMP}/tx-deploy-${fail_destination##*/}"
  new_stage "${TX_STAGE}"
  new_deploy "${TX_DEPLOY}"
  tx_stage_before=$(tree_digest "${TX_STAGE}")
  tx_env_before=$(shasum -a 256 "${TX_DEPLOY}/secrets/cls.env")
  fail_once="${TMP}/fail-once-${fail_destination##*/}"
  printf 'success\n' >"${mode_file}"
  if PATH="${FAIL_BIN}:${PATH}" MOOX_TEST_REAL_MV="${REAL_MV}" \
    MOOX_TEST_FAIL_MV_DEST="${fail_destination}" MOOX_TEST_FAIL_MV_ONCE="${fail_once}" \
    MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
    MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
    --deploy-dir "${TX_DEPLOY}" --stage-dir "${TX_STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
    echo "commit failure unexpectedly succeeded: ${fail_destination}" >&2
    exit 1
  fi
  [[ "${tx_stage_before}" == "$(tree_digest "${TX_STAGE}")" ]]
  [[ "${tx_env_before}" == "$(shasum -a 256 "${TX_DEPLOY}/secrets/cls.env")" ]]
  assert_no_transaction_artifacts "${TX_STAGE}" "${TX_DEPLOY}"
done

# Render validation is structural and leaves no candidate or backup behind.
for invalid_config_case in malformed-yaml wrong-level duplicate-writer duplicate-mapping-key duplicate-nested-key complex-duplicate-key orphan-begin orphan-end nested-markers; do
  INVALID_STAGE="${TMP}/invalid-stage-${invalid_config_case}"
  INVALID_DEPLOY="${TMP}/invalid-deploy-${invalid_config_case}"
  new_stage "${INVALID_STAGE}"
  new_deploy "${INVALID_DEPLOY}"
  case "${invalid_config_case}" in
    malformed-yaml)
      printf 'broken: [\n' >>"${INVALID_STAGE}/factor/config/trpc_go.yaml"
      ;;
    wrong-level)
      cat >"${INVALID_STAGE}/factor/config/trpc_go.yaml" <<'YAML'
plugins:
  log:
    default:
      - writer: console
        level: info
    audit:
      - writer: cls
        remote_config:
          topic_id: unmanaged-wrong-level
YAML
      ;;
    duplicate-writer)
      cat >"${INVALID_STAGE}/factor/config/trpc_go.yaml" <<'YAML'
plugins:
  log:
    default:
      - writer: console
        level: info
      - writer: cls
        level: error
        remote_config:
          topic_id: unmanaged-duplicate
YAML
      ;;
    duplicate-mapping-key)
      cat >"${INVALID_STAGE}/factor/config/trpc_go.yaml" <<'YAML'
plugins:
  metrics:
    prometheus:
      port: 1234
plugins:
  log:
    default:
      - writer: console
        level: info
YAML
      ;;
    duplicate-nested-key)
      cat >"${INVALID_STAGE}/factor/config/trpc_go.yaml" <<'YAML'
plugins:
  metrics:
    prometheus:
      port: 1234
    prometheus:
      port: 5678
  log:
    default:
      - writer: console
        level: info
YAML
      ;;
    complex-duplicate-key)
      cat >"${INVALID_STAGE}/factor/config/trpc_go.yaml" <<'YAML'
? [one, two]
: first
? [one, two]
: second
plugins:
  log:
    default:
      - writer: console
        level: info
YAML
      ;;
    orphan-begin)
      printf '# BEGIN MOOX MANAGED CLS\n' >>"${INVALID_STAGE}/factor/config/trpc_go.yaml"
      ;;
    orphan-end)
      printf '# END MOOX MANAGED CLS\n' >>"${INVALID_STAGE}/factor/config/trpc_go.yaml"
      ;;
    nested-markers)
      cat >>"${INVALID_STAGE}/factor/config/trpc_go.yaml" <<'YAML'
# BEGIN MOOX MANAGED CLS
# BEGIN MOOX MANAGED CLS
# END MOOX MANAGED CLS
# END MOOX MANAGED CLS
YAML
      ;;
  esac
  invalid_stage_before=$(tree_digest "${INVALID_STAGE}")
  invalid_env_before=$(shasum -a 256 "${INVALID_DEPLOY}/secrets/cls.env")
  printf 'success\n' >"${mode_file}"
  if MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
    MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
    --deploy-dir "${INVALID_DEPLOY}" --stage-dir "${INVALID_STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
    echo "invalid rendered config unexpectedly succeeded: ${invalid_config_case}" >&2
    exit 1
  fi
  [[ "${invalid_stage_before}" == "$(tree_digest "${INVALID_STAGE}")" ]]
  [[ "${invalid_env_before}" == "$(shasum -a 256 "${INVALID_DEPLOY}/secrets/cls.env")" ]]
  assert_no_transaction_artifacts "${INVALID_STAGE}" "${INVALID_DEPLOY}"
done

# A shared stage and deploy are one transaction domain. Concurrent publishers
# may fail fast, but topic and credentials must always come from one account.
CONCURRENT_DEPLOY="${TMP}/concurrent-deploy"
CONCURRENT_STAGE="${TMP}/concurrent-stage"
new_deploy "${CONCURRENT_DEPLOY}"
new_stage "${CONCURRENT_STAGE}"
write_lock_owner "${CONCURRENT_STAGE}/.cls-bootstrap.lock" stale-race "${local_host}" 99999999 "${now}"
printf 'success\n' >"${mode_file}"
MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" MOOX_TEST_EXPECT_ACCOUNT=concurrent-a \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
  --deploy-dir "${CONCURRENT_DEPLOY}" --stage-dir "${CONCURRENT_STAGE}" \
  --admin-url http://127.0.0.1:11002 --cloud-account-id concurrent-a \
  >"${TMP}/concurrent-a.out" 2>&1 &
pid_a=$!
MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" MOOX_TEST_EXPECT_ACCOUNT=concurrent-b \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" --target localhost \
  --deploy-dir "${CONCURRENT_DEPLOY}" --stage-dir "${CONCURRENT_STAGE}" \
  --admin-url http://127.0.0.1:11002 --cloud-account-id concurrent-b \
  >"${TMP}/concurrent-b.out" 2>&1 &
pid_b=$!
set +e
wait "${pid_a}"; status_a=$?
wait "${pid_b}"; status_b=$?
set -e
if [[ ${status_a} -eq 0 ]]; then
  [[ ${status_b} -ne 0 ]]
else
  [[ ${status_b} -eq 0 ]]
fi
concurrent_env=$(cat "${CONCURRENT_DEPLOY}/secrets/cls.env")
if grep -q 'topic_id: topic-a' "${CONCURRENT_STAGE}/factor/config/trpc_go.yaml"; then
  [[ "${concurrent_env}" == *concurrent-a-id* && "${concurrent_env}" == *concurrent-a-key* ]]
else
  grep -q 'topic_id: topic-b' "${CONCURRENT_STAGE}/factor/config/trpc_go.yaml"
  [[ "${concurrent_env}" == *concurrent-b-id* && "${concurrent_env}" == *concurrent-b-key* ]]
fi
assert_cls_log_writer "${CONCURRENT_STAGE}/factor/config/trpc_go.yaml"
assert_no_transaction_artifacts "${CONCURRENT_STAGE}" "${CONCURRENT_DEPLOY}"
[[ ! -e "${CONCURRENT_STAGE}/.cls-bootstrap.lock" ]]
[[ ! -e "${CONCURRENT_DEPLOY}/secrets/.cls-bootstrap.lock" ]]

# Exercise the remote branch without requiring a host. The transport shims run
# the uploaded target-architecture CLI in an isolated HOME and record its path.
REMOTE_STAGE="${TMP}/remote-stage"
REMOTE_HOME="${TMP}/remote-home"
FAKE_BIN="${TMP}/fake-bin"
REMOTE_PATHS="${TMP}/remote-paths"
mkdir -p "${FAKE_BIN}"
new_stage "${REMOTE_STAGE}"
new_deploy "${REMOTE_HOME}/moox"
cat >"${FAKE_BIN}/scp" <<'SCP'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == -q ]]; shift
[[ "${1:-}" == -o ]]; shift
[[ "${1:-}" == BatchMode=yes ]]; shift
[[ "${1:-}" == -- ]]; shift
src=$1; destination=$2
remote_path=${destination#*:}
printf '%s\n' "${remote_path}" >>"${MOOX_TEST_REMOTE_PATHS}"
cp "${src}" "${remote_path}"
SCP
cat >"${FAKE_BIN}/ssh" <<'SSH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == -o ]]; then shift 2; fi
[[ "${1:-}" == -- ]]; shift
target=$1; shift
[[ "${target}" == deploy@example.test ]]
if [[ "${1:-}" == bash && "${2:-}" == -s ]]; then
  shift
  HOME="${MOOX_TEST_REMOTE_HOME}" /bin/bash "$@"
else
  HOME="${MOOX_TEST_REMOTE_HOME}" /bin/bash -c "$*"
fi
SSH
chmod +x "${FAKE_BIN}/scp" "${FAKE_BIN}/ssh"

printf 'success\n' >"${mode_file}"
remote_output="${TMP}/remote-output"
PATH="${FAKE_BIN}:${PATH}" MOOX_TEST_CALLS="${calls}" \
  MOOX_TEST_MODE="${mode_file}" MOOX_TEST_REMOTE_HOME="${REMOTE_HOME}" \
  MOOX_TEST_REMOTE_PATHS="${REMOTE_PATHS}" \
  MOOX_TEST_EXPECT_ACCOUNT='explicit account' \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target deploy@example.test --deploy-dir '~/moox' --stage-dir "${REMOTE_STAGE}" \
  --admin-url http://127.0.0.1:11002 --cloud-account-id 'explicit account' \
  >"${remote_output}" 2>&1
grep -q -- '--cloud-account-id explicit account' "${calls}"
grep -q 'topic_id: topic-fixed' "${REMOTE_STAGE}/factor/config/trpc_go.yaml"
[[ $(file_mode "${REMOTE_HOME}/moox/secrets/cls.env") == 600 ]]
[[ ! -e "${REMOTE_HOME}/moox/secrets/cls.env.next" ]]
while IFS= read -r remote_path; do [[ ! -e "${remote_path}" ]]; done <"${REMOTE_PATHS}"
assert_no_transaction_artifacts "${REMOTE_STAGE}" "${REMOTE_HOME}/moox"
assert_no_secrets "${remote_output}" "${REMOTE_STAGE}"

remote_expired_lock="${REMOTE_HOME}/moox/secrets/.cls-bootstrap.lock"
write_lock_owner "${remote_expired_lock}" expired-remote another-controller 12345 "$((now - 2))"
PATH="${FAKE_BIN}:${PATH}" MOOX_TEST_CALLS="${calls}" \
  MOOX_TEST_MODE="${mode_file}" MOOX_TEST_REMOTE_HOME="${REMOTE_HOME}" \
  MOOX_TEST_REMOTE_PATHS="${REMOTE_PATHS}" MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  MOOX_CLS_LOCK_LEASE_SECONDS=1 \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target deploy@example.test --deploy-dir '~/moox' --stage-dir "${REMOTE_STAGE}" \
  --admin-url http://127.0.0.1:11002 >>"${remote_output}" 2>&1
[[ ! -e "${remote_expired_lock}" ]]

remote_foreign_lock="${REMOTE_HOME}/moox/secrets/.cls-bootstrap.lock"
write_lock_owner "${remote_foreign_lock}" fresh-remote another-controller 12345 "${now}"
remote_fresh_before=$(shasum -a 256 "${remote_foreign_lock}/owner")
if PATH="${FAKE_BIN}:${PATH}" MOOX_TEST_CALLS="${calls}" \
  MOOX_TEST_MODE="${mode_file}" MOOX_TEST_REMOTE_HOME="${REMOTE_HOME}" \
  MOOX_TEST_REMOTE_PATHS="${REMOTE_PATHS}" MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target deploy@example.test --deploy-dir '~/moox' --stage-dir "${REMOTE_STAGE}" \
  --admin-url http://127.0.0.1:11002 >>"${remote_output}" 2>&1; then
  echo 'foreign remote deploy lock unexpectedly acquired' >&2
  exit 1
fi
[[ "${remote_fresh_before}" == "$(shasum -a 256 "${remote_foreign_lock}/owner")" ]]
[[ ! -e "${REMOTE_STAGE}/.cls-bootstrap.lock" ]]
rm -f "${remote_foreign_lock}/owner"
rmdir "${remote_foreign_lock}"

remote_stage_before=$(tree_digest "${REMOTE_STAGE}")
remote_env_before=$(shasum -a 256 "${REMOTE_HOME}/moox/secrets/cls.env")

printf 'success\n' >"${mode_file}"
remote_fail_once="${TMP}/remote-fail-once"
if PATH="${FAIL_BIN}:${FAKE_BIN}:${PATH}" MOOX_TEST_REAL_MV="${REAL_MV}" \
  MOOX_TEST_FAIL_MV_DEST='/secrets/cls.env' MOOX_TEST_FAIL_MV_ONCE="${remote_fail_once}" \
  MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
  MOOX_TEST_REMOTE_HOME="${REMOTE_HOME}" MOOX_TEST_REMOTE_PATHS="${REMOTE_PATHS}" \
  MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target deploy@example.test --deploy-dir '~/moox' --stage-dir "${REMOTE_STAGE}" \
  --admin-url http://127.0.0.1:11002 >>"${remote_output}" 2>&1; then
  echo 'remote credential commit failure unexpectedly succeeded' >&2
  exit 1
fi
[[ "${remote_stage_before}" == "$(tree_digest "${REMOTE_STAGE}")" ]]
[[ "${remote_env_before}" == "$(shasum -a 256 "${REMOTE_HOME}/moox/secrets/cls.env")" ]]
assert_no_transaction_artifacts "${REMOTE_STAGE}" "${REMOTE_HOME}/moox"

for failing_mode in missing-topic failure; do
  printf '%s\n' "${failing_mode}" >"${mode_file}"
  if PATH="${FAKE_BIN}:${PATH}" MOOX_TEST_CALLS="${calls}" \
    MOOX_TEST_MODE="${mode_file}" MOOX_TEST_REMOTE_HOME="${REMOTE_HOME}" \
    MOOX_TEST_REMOTE_PATHS="${REMOTE_PATHS}" \
    MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
    "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
    --target deploy@example.test --deploy-dir '~/moox' --stage-dir "${REMOTE_STAGE}" \
    --admin-url http://127.0.0.1:11002 >>"${remote_output}" 2>&1; then
    echo "remote ${failing_mode} unexpectedly succeeded" >&2
    exit 1
  fi
  [[ "${remote_stage_before}" == "$(tree_digest "${REMOTE_STAGE}")" ]]
  [[ "${remote_env_before}" == "$(shasum -a 256 "${REMOTE_HOME}/moox/secrets/cls.env")" ]]
  [[ ! -e "${REMOTE_HOME}/moox/secrets/cls.env.next" ]]
done
while IFS= read -r remote_path; do [[ ! -e "${remote_path}" ]]; done <"${REMOTE_PATHS}"
[[ $(wc -l <"${REMOTE_PATHS}" | tr -d ' ') == $(sort -u "${REMOTE_PATHS}" | wc -l | tr -d ' ') ]]
assert_no_transaction_artifacts "${REMOTE_STAGE}" "${REMOTE_HOME}/moox"
assert_no_secrets "${remote_output}" "${REMOTE_STAGE}"

echo 'CLS Skill bootstrap contract passed'
