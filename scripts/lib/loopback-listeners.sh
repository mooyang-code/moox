#!/usr/bin/env bash
set -euo pipefail

require_loopback_listener() {
  local name="$1" value="$2" port port_decimal
  if [[ ! "${value}" =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then
    printf '%s must use explicit IPv4 loopback form 127.0.0.1:port, got %s\n' "${name}" "${value}" >&2
    return 1
  fi
  port="${BASH_REMATCH[1]}"
  if [[ ${#port} -gt 5 ]]; then
    printf '%s contains an invalid port: %s\n' "${name}" "${value}" >&2
    return 1
  fi
  port_decimal=$((10#${port}))
  if (( port_decimal < 1 || port_decimal > 65535 )); then
    printf '%s contains an invalid port: %s\n' "${name}" "${value}" >&2
    return 1
  fi
}

validate_moox_loopback_listeners() {
  require_loopback_listener MOOX_WEB_HOST_ADDR "${MOOX_WEB_HOST_ADDR-127.0.0.1:9528}"
  require_loopback_listener MOOX_WEB_HOST_HEALTH_ADDR "${MOOX_WEB_HOST_HEALTH_ADDR-127.0.0.1:19527}"
  require_loopback_listener MOOX_ADMIN_CONTROL_ADDR "${MOOX_ADMIN_CONTROL_ADDR-127.0.0.1:11000}"
  require_loopback_listener MOOX_ADMIN_SERVICE_ADDR "${MOOX_ADMIN_SERVICE_ADDR-127.0.0.1:11002}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  validate_moox_loopback_listeners
fi
