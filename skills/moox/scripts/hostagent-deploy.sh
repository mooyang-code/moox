#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: hostagent-deploy.sh user@host release.tar.gz --eventbus-file path --ca-file path --health-auth-file path" >&2
  echo "       hostagent-deploy.sh user@host --credentials-only --eventbus-file path --ca-file path [--health-auth-file path]" >&2
  exit 2
}

TARGET="${1:-}"
[[ -n "${TARGET}" ]] || usage
shift

ARCHIVE=""
CREDENTIALS_ONLY=0
EVENTBUS_FILE=""
CA_FILE=""
HEALTH_AUTH_FILE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --credentials-only) CREDENTIALS_ONLY=1; shift ;;
    --eventbus-file) EVENTBUS_FILE="${2:-}"; shift 2 ;;
    --ca-file) CA_FILE="${2:-}"; shift 2 ;;
    --health-auth-file) HEALTH_AUTH_FILE="${2:-}"; shift 2 ;;
    --*) echo "unknown option: $1" >&2; usage ;;
    *)
      if [[ -z "${ARCHIVE}" ]]; then
        ARCHIVE="$1"
        shift
      else
        echo "unexpected argument: $1" >&2
        usage
      fi
      ;;
  esac
done

[[ -n "${EVENTBUS_FILE}" && -f "${EVENTBUS_FILE}" ]] || { echo "--eventbus-file must name a regular file" >&2; exit 2; }
[[ -n "${CA_FILE}" && -f "${CA_FILE}" ]] || { echo "--ca-file must name a regular file" >&2; exit 2; }
if [[ -n "${HEALTH_AUTH_FILE}" && ! -f "${HEALTH_AUTH_FILE}" ]]; then
  echo "--health-auth-file must name a regular file" >&2
  exit 2
fi
if [[ "${CREDENTIALS_ONLY}" -eq 1 ]]; then
  [[ -z "${ARCHIVE}" ]] || { echo "--credentials-only does not take a release archive" >&2; exit 2; }
else
  [[ -n "${ARCHIVE}" && -f "${ARCHIVE}" ]] || { echo "release archive not found: ${ARCHIVE:-}" >&2; exit 1; }
  [[ -n "${HEALTH_AUTH_FILE}" ]] || { echo "--health-auth-file is required unless --credentials-only" >&2; exit 2; }
fi

ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" 'test "$(id -u)" -ne 0'
REMOTE_TMP="$(ssh "${TARGET}" 'mktemp -d "${TMPDIR:-/tmp}/moox-hostagent.XXXXXX"')"
trap 'ssh "${TARGET}" "rm -rf ${REMOTE_TMP}"' EXIT
scp "${EVENTBUS_FILE}" "${TARGET}:${REMOTE_TMP}/eventbus.yaml"
scp "${CA_FILE}" "${TARGET}:${REMOTE_TMP}/ca.pem"
if [[ -n "${HEALTH_AUTH_FILE}" ]]; then
  scp "${HEALTH_AUTH_FILE}" "${TARGET}:${REMOTE_TMP}/health-auth.env"
fi

if [[ "${CREDENTIALS_ONLY}" -eq 1 ]]; then
  ssh "${TARGET}" "set -eu; export XDG_RUNTIME_DIR=/run/user/\$(id -u); test -x \"\$HOME/.local/lib/moox/hostagent/current/bin/moox-host-agent\"; mkdir -p ~/.config/moox/hostagent ~/.config/systemd/user/moox-host-agent.service.d; chmod 700 ~/.config/moox ~/.config/moox/hostagent; install -m 0600 ${REMOTE_TMP}/eventbus.yaml ~/.config/moox/hostagent/eventbus.yaml; install -m 0600 ${REMOTE_TMP}/ca.pem ~/.config/moox/hostagent/ca.pem; if [ -f ${REMOTE_TMP}/health-auth.env ]; then install -m 0600 ${REMOTE_TMP}/health-auth.env ~/.config/moox/hostagent/health-auth.env; printf '[Service]\\nEnvironmentFile=%%h/.config/moox/hostagent/health-auth.env\\nEnvironment=MOOX_HOST_AGENT_HEALTH_ADDR=0.0.0.0:11425\\n' > ~/.config/systemd/user/moox-host-agent.service.d/health.conf; chmod 0600 ~/.config/systemd/user/moox-host-agent.service.d/health.conf; fi; systemctl --user daemon-reload; systemctl --user restart moox-host-agent.service"
  echo "updated EventBus credentials on ${TARGET}"
  exit 0
fi

VERSION_DIR="$(tar -tzf "${ARCHIVE}" | awk '{entry=$0; sub(/\/$/, "", entry); if (entry != "" && entry !~ /\//) {print entry; exit}}')"
[[ -n "${VERSION_DIR}" ]] || { echo "invalid host-agent archive" >&2; exit 1; }
scp "${ARCHIVE}" "${TARGET}:${REMOTE_TMP}/release.tar.gz"
ssh "${TARGET}" "set -eu; export XDG_RUNTIME_DIR=/run/user/\$(id -u); mkdir -p ~/.local/lib/moox/hostagent/releases ~/.config/moox/hostagent ~/.config/systemd/user/moox-host-agent.service.d; tar -xzf ${REMOTE_TMP}/release.tar.gz -C ${REMOTE_TMP}; rm -rf ~/.local/lib/moox/hostagent/releases/${VERSION_DIR}; mv ${REMOTE_TMP}/${VERSION_DIR} ~/.local/lib/moox/hostagent/releases/${VERSION_DIR}; ln -sfn releases/${VERSION_DIR} ~/.local/lib/moox/hostagent/current; cp ~/.local/lib/moox/hostagent/current/systemd/user/moox-host-agent.service ~/.config/systemd/user/; chmod 700 ~/.config/moox ~/.config/moox/hostagent; install -m 0600 ${REMOTE_TMP}/eventbus.yaml ~/.config/moox/hostagent/eventbus.yaml; install -m 0600 ${REMOTE_TMP}/ca.pem ~/.config/moox/hostagent/ca.pem; install -m 0600 ${REMOTE_TMP}/health-auth.env ~/.config/moox/hostagent/health-auth.env; printf '[Service]\\nEnvironmentFile=%%h/.config/moox/hostagent/health-auth.env\\nEnvironment=MOOX_HOST_AGENT_HEALTH_ADDR=0.0.0.0:11425\\n' > ~/.config/systemd/user/moox-host-agent.service.d/health.conf; chmod 0600 ~/.config/systemd/user/moox-host-agent.service.d/health.conf; systemctl --user daemon-reload; systemctl --user enable moox-host-agent.service; systemctl --user restart moox-host-agent.service"
echo "deployed ${VERSION_DIR} to ${TARGET}"
