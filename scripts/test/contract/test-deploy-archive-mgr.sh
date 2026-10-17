#!/usr/bin/env bash
# Contract: deploy-archive-mgr.sh validates its inputs and renders the install tree and the service definition
# (--render-only needs no SSH).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/scripts/deploy/deploy-archive-mgr.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fail() { echo "test-deploy-archive-mgr: $*" >&2; exit 1; }

printf '#!/bin/sh\nexit 0\n' >"${tmp}/moox-archive-mgr"
chmod 0755 "${tmp}/moox-archive-mgr"
printf 'k%.0s' {1..40} >"${tmp}/caller.key"
chmod 0600 "${tmp}/caller.key"

if bash "${SCRIPT}" --render-only --binary "${tmp}/moox-archive-mgr" --key-id 'bad id' --key-file "${tmp}/caller.key" >"${tmp}/bad1.log" 2>&1; then fail "a key id with spaces must be rejected"; fi
if bash "${SCRIPT}" --render-only --binary "${tmp}/missing" --key-id abc123 --key-file "${tmp}/caller.key" >"${tmp}/bad2.log" 2>&1; then fail "a missing binary must be rejected"; fi
if bash "${SCRIPT}" --render-only --dir relative --binary "${tmp}/moox-archive-mgr" --key-id abc123 --key-file "${tmp}/caller.key" >"${tmp}/bad3.log" 2>&1; then fail "a relative --dir must be rejected"; fi
if bash "${SCRIPT}" --binary "${tmp}/moox-archive-mgr" --key-id abc123 --key-file "${tmp}/caller.key" >"${tmp}/bad4.log" 2>&1; then fail "a real deploy without --target must be rejected"; fi

stage="${tmp}/stage"
bash "${SCRIPT}" --render-only --stage-dir "${stage}" --dir /data/moox/prod/archive --binary "${tmp}/moox-archive-mgr" --key-id abc123 --key-file "${tmp}/caller.key" >"${tmp}/ok.log" 2>&1 \
  || { cat "${tmp}/ok.log" >&2; fail "render failed"; }

[[ -x "${stage}/tree/bin/moox-archive-mgr" ]] || fail "binary was not staged"
config="${stage}/tree/archive-mgr/config/app.yaml"
grep -q 'key_id: "abc123"' "${config}" || fail "key_id was not rendered"
grep -q 'caller: archive-mgr' "${config}" || fail "caller missing"
grep -q 'key_file: ../../secrets/caller-archive-mgr.key' "${config}" || fail "key_file must stay relative to the config directory"
[[ -s "${stage}/tree/archive-mgr/config/trpc_go.yaml" ]] || fail "trpc_go.yaml was not staged"
unit="${stage}/moox-archive-mgr.service"
for want in 'WorkingDirectory=/data/moox/prod/archive/archive-mgr' 'EnvironmentFile=/data/moox/prod/archive/secrets/archive-mgr.env' \
  'ExecStart=/data/moox/prod/archive/bin/moox-archive-mgr -config /data/moox/prod/archive/archive-mgr/config/app.yaml -conf /data/moox/prod/archive/archive-mgr/config/trpc_go.yaml'; do
  grep -qF -- "${want}" "${unit}" || fail "service definition misses: ${want}"
done
grep -q '__[A-Z_]*__' "${unit}" && fail "unrendered placeholder"
echo "deploy-archive-mgr contract passed"
