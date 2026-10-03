#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
SPACE_ID_HELPER="${REPO_ROOT}/scripts/test/e2e/resolve-series-tag-space-id.sh"

normalized_space_id="$(MOOX_FACTOR_STORAGE_E2E_SPACE_ID=' factor_e2e ' bash "${SPACE_ID_HELPER}")"
if [[ "${normalized_space_id}" != "factor_e2e" ]]; then
  printf 'Space ID normalization returned %q, want factor_e2e\n' "${normalized_space_id}" >&2
  exit 1
fi

for invalid_space_id in '   ' 'alpha,beta' 'alpha:beta'; do
  if output="$(MOOX_FACTOR_STORAGE_E2E_SPACE_ID="${invalid_space_id}" bash "${SPACE_ID_HELPER}" 2>&1)"; then
    printf 'expected invalid Space ID %q to fail validation\n' "${invalid_space_id}" >&2
    exit 1
  fi
  case "${output}" in
    *"must use 1-64 letters, digits, underscores, or hyphens"*) ;;
    *)
      printf 'unexpected error for invalid Space ID %q: %s\n' "${invalid_space_id}" "${output}" >&2
      exit 1
      ;;
  esac
done

printf 'series-tag E2E Space ID validation passed\n'
