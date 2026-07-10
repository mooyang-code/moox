#!/usr/bin/env bash
set -euo pipefail

TARGET="${1:-}"
ARCHIVE="${2:-}"
EVENTBUS_FILE=""
CA_FILE=""
shift 2 2>/dev/null || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --eventbus-file) EVENTBUS_FILE="${2:-}"; shift 2 ;;
    --ca-file) CA_FILE="${2:-}"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ -n "${TARGET}" && -n "${ARCHIVE}" ]] || { echo "usage: hostagent-deploy.sh user@host release.tar.gz [--eventbus-file path --ca-file path]" >&2; exit 2; }
[[ -f "${ARCHIVE}" ]] || { echo "release archive not found: ${ARCHIVE}" >&2; exit 1; }
ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" 'test "$(id -u)" -ne 0'
VERSION_DIR="$(tar -tzf "${ARCHIVE}" | awk '{entry=$0; sub(/\/$/, "", entry); if (entry != "" && entry !~ /\//) {print entry; exit}}')"
[[ -n "${VERSION_DIR}" ]] || { echo "invalid host-agent archive" >&2; exit 1; }
REMOTE_TMP="$(ssh "${TARGET}" 'mktemp -d "${TMPDIR:-/tmp}/moox-hostagent.XXXXXX"')"
trap 'ssh "${TARGET}" "rm -rf ${REMOTE_TMP}"' EXIT
scp "${ARCHIVE}" "${TARGET}:${REMOTE_TMP}/release.tar.gz"
if [[ -n "${EVENTBUS_FILE}" ]]; then scp "${EVENTBUS_FILE}" "${TARGET}:${REMOTE_TMP}/eventbus.yaml"; fi
if [[ -n "${CA_FILE}" ]]; then scp "${CA_FILE}" "${TARGET}:${REMOTE_TMP}/ca.pem"; fi
ssh "${TARGET}" "set -eu; mkdir -p ~/.local/lib/moox/hostagent/releases ~/.config/moox/hostagent ~/.config/systemd/user; tar -xzf ${REMOTE_TMP}/release.tar.gz -C ${REMOTE_TMP}; rm -rf ~/.local/lib/moox/hostagent/releases/${VERSION_DIR}; mv ${REMOTE_TMP}/${VERSION_DIR} ~/.local/lib/moox/hostagent/releases/${VERSION_DIR}; ln -sfn releases/${VERSION_DIR} ~/.local/lib/moox/hostagent/current; cp ~/.local/lib/moox/hostagent/current/systemd/user/moox-host-agent.service ~/.config/systemd/user/; chmod 700 ~/.config/moox ~/.config/moox/hostagent; if [[ -f ${REMOTE_TMP}/eventbus.yaml ]]; then install -m 0600 ${REMOTE_TMP}/eventbus.yaml ~/.config/moox/hostagent/eventbus.yaml; fi; if [[ -f ${REMOTE_TMP}/ca.pem ]]; then install -m 0600 ${REMOTE_TMP}/ca.pem ~/.config/moox/hostagent/ca.pem; fi; test -f ~/.config/moox/hostagent/eventbus.yaml; test -f ~/.config/moox/hostagent/ca.pem; systemctl --user daemon-reload; systemctl --user enable --now moox-host-agent.service"
echo "deployed ${VERSION_DIR} to ${TARGET}"
