#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/data-query.md"
WRAPPER_SOURCE="${ROOT}/skills/moox/scripts/data-kline.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-data-kline-test.XXXXXX")"
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${REFERENCE}" ]] || fail "data query reference is missing"
[[ -x "${WRAPPER_SOURCE}" ]] || fail "data-kline wrapper is missing or not executable"
grep -Fq 'references/data-query.md' "${SKILL}" || fail "SKILL.md does not route data queries to the reference"
description="$(sed -n 's/^description: //p' "${SKILL}" | head -1)"
for trigger in '采集数据' 'K-line' 'K线' '行情' 'BTC-USDT' 'crypto market' 'queries'; do
  grep -Fiq "${trigger}" <<<"${description}" || fail "skill description is missing the ${trigger} trigger"
done

grep -Fq 'loaded `SKILL.md`' "${REFERENCE}" || fail "reference does not derive the wrapper path from the loaded skill"
grep -Fq '/scripts/data-kline.sh' "${REFERENCE}" || fail "reference does not use the bundled wrapper"
if grep -Eq '"\$(CLI|CONFIG)"|(^|[[:space:]])moox-cli data kline get' "${REFERENCE}"; then
  fail "reference still relies on cross-block CLI/config shell variables"
fi
query_examples="$(grep -E '^[[:space:]]*/[^[:space:]]*/scripts/data-kline\.sh([[:space:]]|$)' "${REFERENCE}" || true)"
[[ "$(wc -l <<<"${query_examples}" | tr -d ' ')" -ge 3 ]] || fail "reference lacks direct absolute wrapper examples"
for summary_field in 'data type' 'exchange' 'symbol' 'interval' 'row count' 'returned time range'; do
  grep -Fq "${summary_field}" "${REFERENCE}" || fail "reference summary is missing ${summary_field}"
done
grep -Fq 'Never include Gateway secrets' "${REFERENCE}" || fail "reference does not prohibit credential disclosure"

SKILL_ROOT="${TEST_ROOT}/install/skills/moox"
mkdir -p "${SKILL_ROOT}/scripts" "${SKILL_ROOT}/config" "${TEST_ROOT}/path-bin"
cp "${WRAPPER_SOURCE}" "${SKILL_ROOT}/scripts/data-kline.sh"
chmod +x "${SKILL_ROOT}/scripts/data-kline.sh"
CONFIG="${SKILL_ROOT}/config/data-access.yaml"
SECRET_SENTINEL='WRAPPER_TEST_SECRET_DO_NOT_PRINT_9vQ3'
printf 'gateway:\n  secret: %s\n' "${SECRET_SENTINEL}" >"${CONFIG}"
chmod 0600 "${CONFIG}"

cat >"${TEST_ROOT}/path-bin/moox-cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${CLI_MARKER:?}" >"${CLI_MARKER_LOG:?}"
printf '%s\n' "$@" >"${CLI_ARGS_LOG:?}"
EOF
chmod +x "${TEST_ROOT}/path-bin/moox-cli"

CLI_ARGS_LOG="${TEST_ROOT}/path.args" CLI_MARKER_LOG="${TEST_ROOT}/path.marker" CLI_MARKER=path \
  PATH="${TEST_ROOT}/path-bin:${PATH}" "${SKILL_ROOT}/scripts/data-kline.sh" \
  --data-type crypto --exchange binance --symbol BTC-USDT --interval 1m --limit 20
expected_config="$(cd "${SKILL_ROOT}/config" && pwd -P)/data-access.yaml"
[[ "$(sed -n '1p' "${TEST_ROOT}/path.args")" == data ]] || fail "wrapper did not invoke data command"
[[ "$(sed -n '2p' "${TEST_ROOT}/path.args")" == kline ]] || fail "wrapper did not invoke kline command"
[[ "$(sed -n '3p' "${TEST_ROOT}/path.args")" == get ]] || fail "wrapper did not invoke get command"
[[ "$(sed -n '4p' "${TEST_ROOT}/path.args")" == --config ]] || fail "wrapper did not inject --config"
[[ "$(sed -n '5p' "${TEST_ROOT}/path.args")" == "${expected_config}" ]] || fail "wrapper config path is not absolute"
grep -qx -- '--data-type' "${TEST_ROOT}/path.args" || fail "wrapper did not forward data type"
grep -qx -- 'BTC-USDT' "${TEST_ROOT}/path.args" || fail "wrapper did not preserve symbol"

mkdir -p "${TEST_ROOT}/install/bin"
cat >"${TEST_ROOT}/install/bin/moox-cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' repo >"${CLI_MARKER_LOG:?}"
printf '%s\n' "$@" >"${CLI_ARGS_LOG:?}"
EOF
chmod +x "${TEST_ROOT}/install/bin/moox-cli"
CLI_ARGS_LOG="${TEST_ROOT}/repo.args" CLI_MARKER_LOG="${TEST_ROOT}/repo.marker" CLI_MARKER=path \
  PATH="${TEST_ROOT}/path-bin:${PATH}" "${SKILL_ROOT}/scripts/data-kline.sh" \
  --data-type crypto --symbol BTC-USDT --interval 1m
[[ "$(cat "${TEST_ROOT}/repo.marker")" == repo ]] || fail "wrapper did not prefer repository CLI"

rm "${TEST_ROOT}/install/bin/moox-cli"
if output="$(PATH=/nonexistent /bin/bash "${SKILL_ROOT}/scripts/data-kline.sh" --data-type crypto --symbol BTC-USDT 2>&1)"; then
  fail "wrapper accepted a missing CLI"
fi
grep -Fq 'moox-cli not found' <<<"${output}" || fail "missing CLI error is unclear"
grep -Fq "${SECRET_SENTINEL}" <<<"${output}" && fail "missing CLI error leaked config content"

if output="$(PATH="${TEST_ROOT}/path-bin:${PATH}" CLI_ARGS_LOG="${TEST_ROOT}/override.args" CLI_MARKER_LOG="${TEST_ROOT}/override.marker" CLI_MARKER=path \
  "${SKILL_ROOT}/scripts/data-kline.sh" --config /tmp/evil --data-type crypto --symbol BTC-USDT 2>&1)"; then
  fail "wrapper accepted caller --config"
fi
grep -Fq 'caller must not override --config' <<<"${output}" || fail "--config rejection is unclear"

if output="$(PATH="${TEST_ROOT}/path-bin:${PATH}" CLI_ARGS_LOG="${TEST_ROOT}/override-eq.args" CLI_MARKER_LOG="${TEST_ROOT}/override-eq.marker" CLI_MARKER=path \
  "${SKILL_ROOT}/scripts/data-kline.sh" --config=/tmp/evil --data-type crypto --symbol BTC-USDT 2>&1)"; then
  fail "wrapper accepted caller --config=value"
fi
grep -Fq 'caller must not override --config' <<<"${output}" || fail "--config=value rejection is unclear"

rm "${CONFIG}"
if output="$(PATH="${TEST_ROOT}/path-bin:${PATH}" "${SKILL_ROOT}/scripts/data-kline.sh" --data-type crypto --symbol BTC-USDT 2>&1)"; then
  fail "wrapper accepted a missing packaged config"
fi
grep -Fq 'packaged data-access config is missing or unsafe' <<<"${output}" || fail "missing config error is unclear"
grep -Fq "${SECRET_SENTINEL}" <<<"${output}" && fail "config error leaked credential content"

printf 'gateway:\n  secret: %s\n' "${SECRET_SENTINEL}" >"${CONFIG}"
chmod 0644 "${CONFIG}"
if output="$(PATH="${TEST_ROOT}/path-bin:${PATH}" "${SKILL_ROOT}/scripts/data-kline.sh" --data-type crypto --symbol BTC-USDT 2>&1)"; then
  fail "wrapper accepted a non-0600 packaged config"
fi
grep -Fq 'packaged data-access config must have permission 0600' <<<"${output}" || fail "config mode error is unclear"
grep -Fq "${SECRET_SENTINEL}" <<<"${output}" && fail "config mode error leaked credential content"

rm "${CONFIG}"
printf 'gateway:\n  secret: %s\n' "${SECRET_SENTINEL}" >"${TEST_ROOT}/linked-config.yaml"
chmod 0600 "${TEST_ROOT}/linked-config.yaml"
ln -s "${TEST_ROOT}/linked-config.yaml" "${CONFIG}"
if output="$(PATH="${TEST_ROOT}/path-bin:${PATH}" "${SKILL_ROOT}/scripts/data-kline.sh" --data-type crypto --symbol BTC-USDT 2>&1)"; then
  fail "wrapper accepted a symlinked packaged config"
fi
grep -Fq 'packaged data-access config is missing or unsafe' <<<"${output}" || fail "symlink config error is unclear"
grep -Fq "${SECRET_SENTINEL}" <<<"${output}" && fail "symlink config error leaked credential content"

help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli data kline get --help)"
for flag in --config --data-type --exchange --symbol --interval --limit --start-time --end-time --timeout --output; do
  grep -Fq -- "${flag}" <<<"${help}" || fail "CLI help is missing ${flag}"
done

if missing_data_type="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli data kline get --symbol BTC-USDT 2>&1)"; then
  fail "CLI accepted a query without data-type"
fi
grep -Fq 'required flag(s) "data-type" not set' <<<"${missing_data_type}" || fail "CLI did not require data-type"

if missing_symbol="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli data kline get --data-type crypto 2>&1)"; then
  fail "CLI accepted a query without symbol"
fi
grep -Fq 'required flag(s) "symbol" not set' <<<"${missing_symbol}" || fail "CLI did not require symbol"

echo "PASS: skill data query contract"
