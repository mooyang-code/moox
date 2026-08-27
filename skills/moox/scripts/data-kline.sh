#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "${BASH_SOURCE[0]%/*}" && pwd -P)"
SKILL_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
CONFIG="${SKILL_ROOT}/config/data-access.yaml"

for arg in "$@"; do
  case "${arg}" in
    --config|--config=*)
      echo "caller must not override --config" >&2
      exit 2
      ;;
  esac
done

REPO_CLI="${SKILL_ROOT}/../../bin/moox-cli"
if [[ -x "${REPO_CLI}" ]]; then
  CLI="$(cd "${SKILL_ROOT}/../../bin" && pwd -P)/moox-cli"
else
  CLI="$(command -v moox-cli || true)"
  if [[ -z "${CLI}" || ! -x "${CLI}" ]]; then
    echo "moox-cli not found: build repository bin/moox-cli or install it on PATH" >&2
    exit 1
  fi
fi

if [[ ! -f "${CONFIG}" || -L "${CONFIG}" ]]; then
  echo "packaged data-access config is missing or unsafe" >&2
  exit 1
fi
config_mode="$(stat -f '%Lp' "${CONFIG}" 2>/dev/null || stat -c '%a' "${CONFIG}")"
if [[ "${config_mode}" != 600 ]]; then
  echo "packaged data-access config must have permission 0600" >&2
  exit 1
fi

exec "${CLI}" data kline get --config "${CONFIG}" "$@"
