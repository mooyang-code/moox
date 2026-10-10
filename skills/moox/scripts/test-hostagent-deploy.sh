#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/skills/moox/scripts/hostagent-deploy.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/hostagent-deploy-test.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

mkdir -p "${TMP}/bin" "${TMP}/release/v-test/systemd/user"
printf '[Unit]\nDescription=test\n' >"${TMP}/release/v-test/systemd/user/moox-host-agent.service"
tar -czf "${TMP}/release.tar.gz" -C "${TMP}/release" v-test
printf 'urls: [tls://eventbus:4222]\n' >"${TMP}/eventbus.yaml"
printf 'certificate\n' >"${TMP}/ca.pem"
printf 'MOOX_HEALTH_AUTH_SECRET_KEY=test\n' >"${TMP}/health-auth.env"

cat >"${TMP}/bin/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'ssh %q ' "$@" >>"${HOSTAGENT_TEST_LOG}"
printf '\n' >>"${HOSTAGENT_TEST_LOG}"
command_line="${*: -1}"
if [[ "${command_line}" == *"mktemp -d"* ]]; then
  printf '/tmp/moox-hostagent.test\n'
fi
EOF
cat >"${TMP}/bin/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'scp %q %q\n' "$1" "$2" >>"${HOSTAGENT_TEST_LOG}"
EOF
chmod +x "${TMP}/bin/ssh" "${TMP}/bin/scp"

export HOSTAGENT_TEST_LOG="${TMP}/calls.log"
PATH="${TMP}/bin:${PATH}" bash "${SCRIPT}" user@example "${TMP}/release.tar.gz" \
  --eventbus-file "${TMP}/eventbus.yaml" \
  --ca-file "${TMP}/ca.pem" \
  --health-auth-file "${TMP}/health-auth.env"

grep -q 'scp .*release.tar.gz.*release.tar.gz' "${HOSTAGENT_TEST_LOG}"
grep -q 'scp .*eventbus.yaml.*eventbus.yaml' "${HOSTAGENT_TEST_LOG}"
grep -q 'scp .*ca.pem.*ca.pem' "${HOSTAGENT_TEST_LOG}"
grep -q 'scp .*health-auth.env.*health-auth.env' "${HOSTAGENT_TEST_LOG}"
grep -q 'install\\ -m\\ 0600.*eventbus.yaml' "${HOSTAGENT_TEST_LOG}"
grep -q 'MOOX_HOST_AGENT_HEALTH_ADDR=0.0.0.0:11425' "${HOSTAGENT_TEST_LOG}"
grep -q 'systemctl\\ --user\\ enable\\ moox-host-agent.service' "${HOSTAGENT_TEST_LOG}"
grep -q 'systemctl\\ --user\\ restart\\ moox-host-agent.service' "${HOSTAGENT_TEST_LOG}"

: >"${HOSTAGENT_TEST_LOG}"
if PATH="${TMP}/bin:${PATH}" bash "${SCRIPT}" user@example "${TMP}/release.tar.gz" \
  --eventbus-file "${TMP}/eventbus.yaml" --ca-file "${TMP}/ca.pem" 2>/dev/null; then
  echo "missing --health-auth-file unexpectedly succeeded" >&2
  exit 1
fi
[[ ! -s "${HOSTAGENT_TEST_LOG}" ]] || {
  echo "deployment contacted the remote host before validating required files" >&2
  exit 1
}

: >"${HOSTAGENT_TEST_LOG}"
PATH="${TMP}/bin:${PATH}" bash "${SCRIPT}" user@example --credentials-only \
  --eventbus-file "${TMP}/eventbus.yaml" \
  --ca-file "${TMP}/ca.pem"
if grep -q 'release.tar.gz' "${HOSTAGENT_TEST_LOG}"; then
  echo "credentials-only deploy uploaded a release archive" >&2
  exit 1
fi
grep -q 'scp .*eventbus.yaml.*eventbus.yaml' "${HOSTAGENT_TEST_LOG}"
grep -q 'scp .*ca.pem.*ca.pem' "${HOSTAGENT_TEST_LOG}"
if ! grep -Eq 'test( |\\ )-x' "${HOSTAGENT_TEST_LOG}"; then
  echo "credentials-only deploy must refuse hosts without an installed Host Agent" >&2
  exit 1
fi
if grep -q 'systemctl\\ --user\\ enable\\ moox-host-agent.service' "${HOSTAGENT_TEST_LOG}"; then
  echo "credentials-only deploy must not re-enable the unit" >&2
  exit 1
fi
grep -q 'systemctl\\ --user\\ restart\\ moox-host-agent.service' "${HOSTAGENT_TEST_LOG}"

: >"${HOSTAGENT_TEST_LOG}"
if PATH="${TMP}/bin:${PATH}" bash "${SCRIPT}" user@example --credentials-only \
  --eventbus-file "${TMP}/eventbus.yaml" --ca-file "${TMP}/ca.pem" \
  "${TMP}/release.tar.gz" 2>/dev/null; then
  echo "credentials-only unexpectedly accepted a release archive" >&2
  exit 1
fi

bash -n "${SCRIPT}"
if grep -q 'sudo' "${SCRIPT}"; then
  echo "host-agent deploy must remain rootless" >&2
  exit 1
fi
echo "host-agent deploy contract passed"
