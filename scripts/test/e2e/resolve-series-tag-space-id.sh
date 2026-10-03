#!/usr/bin/env bash
set -euo pipefail

raw_space_id="${MOOX_FACTOR_STORAGE_E2E_SPACE_ID:-crypto}"
space_id="${raw_space_id#"${raw_space_id%%[![:space:]]*}"}"
space_id="${space_id%"${space_id##*[![:space:]]}"}"
if [[ ! "${space_id}" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$ ]]; then
  printf 'series-tag E2E: MOOX_FACTOR_STORAGE_E2E_SPACE_ID must use 1-64 letters, digits, underscores, or hyphens\n' >&2
  exit 1
fi

printf '%s' "${space_id}"
