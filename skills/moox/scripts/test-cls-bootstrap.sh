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
    "${stage}/ignored/config"
  cp "${ROOT}/modules/factor/config/trpc_go.yaml" "${stage}/factor/config/trpc_go.yaml"
  cp "${ROOT}/modules/storage/config/trpc_go.yaml" "${stage}/storage/config/trpc_go-prod.yaml"
  printf 'leave me alone\n' >"${stage}/ignored/config/not-trpc.yaml"
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
printf "MOOX_CLS_SECRET_ID='%s-id'\nMOOX_CLS_SECRET_KEY='%s-key'\nMOOX_CLS_REGION='ap-guangzhou'\nMOOX_CLS_HOST='ap-guangzhou.cls.tencentyun.com'\nMOOX_CLS_AUTO_BOOTSTRAP=0\n" \
  "${secret_prefix}" "${secret_prefix}" >"${output}"
chmod 0600 "${output}"
case "$(cat "${MOOX_TEST_MODE}")" in
  success)
    printf '{"status":"configured","resources":{"account_id":"acct-first","topic_id":"topic-fixed"}}\n'
    ;;
  missing-topic)
    printf '{"status":"configured","resources":{"account_id":"acct-first"}}\n'
    ;;
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
grep -q '^leave me alone$' "${STAGE}/ignored/config/not-trpc.yaml"
[[ $(file_mode "${DEPLOY}/secrets/cls.env") == 600 ]]
[[ ! -e "${DEPLOY}/secrets/cls.env.next" ]]
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
printf 'missing-topic\n' >"${mode_file}"
if MOOX_TEST_CALLS="${calls}" MOOX_TEST_MODE="${mode_file}" \
  MOOX_TEST_EXPECT_ACCOUNT=__not_set__ \
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
  --admin-url http://127.0.0.1:11002 >>"${output}" 2>&1; then
  echo 'missing topic unexpectedly succeeded' >&2
  exit 1
fi
[[ "${before_stage}" == "$(tree_digest "${STAGE}")" ]]
[[ "${before_env}" == "$(shasum -a 256 "${DEPLOY}/secrets/cls.env")" ]]
[[ ! -e "${DEPLOY}/secrets/cls.env.next" ]]

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
src=$1; destination=$2
remote_path=${destination#*:}
printf '%s\n' "${remote_path}" >>"${MOOX_TEST_REMOTE_PATHS}"
cp "${src}" "${remote_path}"
SCP
cat >"${FAKE_BIN}/ssh" <<'SSH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == -o ]]; then shift 2; fi
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
assert_no_secrets "${remote_output}" "${REMOTE_STAGE}"

remote_stage_before=$(tree_digest "${REMOTE_STAGE}")
remote_env_before=$(shasum -a 256 "${REMOTE_HOME}/moox/secrets/cls.env")
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
assert_no_secrets "${remote_output}" "${REMOTE_STAGE}"

echo 'CLS Skill bootstrap contract passed'
