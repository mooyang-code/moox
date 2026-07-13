#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT}/scripts/deploy-moox.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-deploy-cls-contract.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

invalid_account_args=(
  --target localhost
  --dir "${TMP_ROOT}/deploy"
  --stage "${TMP_ROOT}/stage"
  --skip-build
  --no-start
  --no-storage
  --no-archive
  --no-eventbus
  --no-web-host
  --no-cloudnode
  --no-collector
  --no-factor
  --no-monitor
)
if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id "" 2>&1); then
  echo 'empty cloud account id unexpectedly accepted' >&2
  exit 1
fi
grep -q 'cloud-account-id cannot be empty' <<<"${output}"
if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id "   " 2>&1); then
  echo 'whitespace-only cloud account id unexpectedly accepted' >&2
  exit 1
fi
grep -q 'cloud-account-id cannot be empty' <<<"${output}"
for option_like in --enable-cls --no-start; do
  if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id "${option_like}" 2>&1); then
    echo "option-like cloud account id unexpectedly accepted: ${option_like}" >&2
    exit 1
  fi
  grep -q -- '--cloud-account-id requires a value' <<<"${output}"
done
if output=$("${SCRIPT}" "${invalid_account_args[@]}" --cloud-account-id 'acct;touch' 2>&1); then
  echo 'cloud account id with unsafe characters unexpectedly accepted' >&2
  exit 1
fi
grep -q 'cloud account ID must match' <<<"${output}"

grep -q -- '--cloud-account-id' "${SCRIPT}"
grep -q 'skills/moox/scripts/cls-bootstrap.sh' "${SCRIPT}"
grep -q 'prepare_cls_preflight' "${SCRIPT}"
if grep -q '^bootstrap_cls()' "${SCRIPT}"; then
  echo 'runtime start script still provisions CLS' >&2
  exit 1
fi

prepare_line=$(grep -n '^prepare_cls_preflight$' "${SCRIPT}" | tail -1 | cut -d: -f1)
sync_line=$(grep -n '^if is_local_target; then$' "${SCRIPT}" | tail -1 | cut -d: -f1)
[[ -n "${prepare_line}" && -n "${sync_line}" && ${prepare_line} -lt ${sync_line} ]]

grep -q 'ops tencent cls prepare' "${ROOT}/skills/moox/scripts/cls-bootstrap.sh"
echo 'CLS deployment ordering contract passed'
