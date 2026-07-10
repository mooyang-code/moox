#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VERSION=test TARGET_GOARCH=amd64 "${ROOT}/skills/moox/scripts/hostagent-release.sh" >/tmp/moox-hostagent-release.out
ARCHIVE="$(tail -1 /tmp/moox-hostagent-release.out)"
tar -tzf "${ARCHIVE}" | grep -q 'bin/moox-host-agent$'
tar -tzf "${ARCHIVE}" | grep -q 'SHA256SUMS$'
echo "host-agent release smoke test passed"
