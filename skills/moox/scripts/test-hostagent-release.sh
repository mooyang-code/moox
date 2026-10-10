#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-hostagent-release-test.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

for arch in amd64 arm64; do
  VERSION=test TARGET_GOARCH="${arch}" "${ROOT}/skills/moox/scripts/hostagent-release.sh" >"${TMP}/release-${arch}.out"
  ARCHIVE="$(tail -1 "${TMP}/release-${arch}.out")"
  tar -tzf "${ARCHIVE}" | grep -q 'bin/moox-host-agent$'
  tar -tzf "${ARCHIVE}" | grep -q 'config/trpc_go.yaml$'
  tar -tzf "${ARCHIVE}" | grep -q 'SHA256SUMS$'
  ROOT_ENTRY="$(tar -tzf "${ARCHIVE}" | awk '{entry=$0; sub(/\/$/, "", entry); if (entry != "" && entry !~ /\//) {print entry; exit}}')"
  [[ -n "${ROOT_ENTRY}" ]] || { echo "release archive has no root directory" >&2; exit 1; }
  tar -tzf "${ARCHIVE}" | grep -q "${ROOT_ENTRY}/systemd/user/moox-host-agent.service$"
  tar -xzf "${ARCHIVE}" -C "${TMP}"
  (cd "${TMP}/${ROOT_ENTRY}" && sha256sum -c SHA256SUMS)
  [[ -x "${TMP}/${ROOT_ENTRY}/bin/moox-host-agent" ]]
  file "${TMP}/${ROOT_ENTRY}/bin/moox-host-agent" | grep -q "ELF 64-bit"
  file "${TMP}/${ROOT_ENTRY}/bin/moox-host-agent" | grep -q "$([[ "${arch}" == amd64 ]] && printf 'x86-64' || printf 'ARM aarch64')"
done
grep -q 'sub(/\\/$/' "${ROOT}/skills/moox/scripts/hostagent-deploy.sh"
echo "host-agent release smoke test passed"
