#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMPORT_PREFIX="github.com/mooyang-code/moox/modules/"

cd "${ROOT}"

violations=()

while IFS= read -r match; do
  file="${match%%:*}"
  rest="${match#*:}"
  line="${rest%%:*}"
  text="${rest#*:}"

  if [[ ! "${file}" =~ ^modules/([^/]+)/ ]]; then
    continue
  fi
  source_module="${BASH_REMATCH[1]}"

  if [[ ! "${text}" =~ \"${IMPORT_PREFIX}([^\"]+)\" ]]; then
    continue
  fi
  import_path="${BASH_REMATCH[1]}"
  target_module="${import_path%%/*}"
  target_rest="${import_path#*/}"

  if [[ "${source_module}" == "${target_module}" ]]; then
    continue
  fi

  case "${target_rest}" in
    proto/*)
      continue
      ;;
  esac

  violations+=("${file}:${line}: ${source_module} must not import ${target_module}/${target_rest}")
done < <(rg -n "\"${IMPORT_PREFIX}[^\"]+\"" modules --glob '*.go' || true)

if (( ${#violations[@]} > 0 )); then
  {
    echo "module boundary violations:"
    printf '  %s\n' "${violations[@]}"
    echo
    echo "Allowed cross-module imports:"
    echo "  - generated protocol packages under modules/<name>/proto/..."
    echo
    echo "Move shared stable code to a root packages/ module before importing it across business modules."
  } >&2
  exit 1
fi

echo "==> module boundaries passed"
