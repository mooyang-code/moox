#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VERSION=test TARGET_GOARCH=amd64 "${ROOT}/skills/moox/scripts/hostagent-release.sh" >/tmp/moox-hostagent-release.out
ARCHIVE="$(tail -1 /tmp/moox-hostagent-release.out)"
tar -tzf "${ARCHIVE}" | grep -q 'bin/moox-host-agent$'
tar -tzf "${ARCHIVE}" | grep -q 'SHA256SUMS$'
ROOT_ENTRY="$(tar -tzf "${ARCHIVE}" | awk '{entry=$0; sub(/\/$/, "", entry); if (entry != "" && entry !~ /\//) {print entry; exit}}')"
[[ -n "${ROOT_ENTRY}" ]] || { echo "release archive has no root directory" >&2; exit 1; }
tar -tzf "${ARCHIVE}" | grep -q "${ROOT_ENTRY}/systemd/user/moox-host-agent.service$"
grep -q 'sub(/\\/$/' "${ROOT}/skills/moox/scripts/hostagent-deploy.sh"
echo "host-agent release smoke test passed"
