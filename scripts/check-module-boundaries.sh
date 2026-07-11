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

  if [[ "${file}" == *_test.go && "${target_rest}" == "testkit" ]]; then
    continue
  fi

  if [[ "${source_module}" == "admin" ]]; then
    violations+=("${file}:${line}: admin must not import ${target_module}/${target_rest}; route via gateway/service deployments instead")
    continue
  fi

  case "${target_rest}" in
    proto/*)
      continue
      ;;
  esac

  violations+=("${file}:${line}: ${source_module} must not import ${target_module}/${target_rest}")
done < <(rg -n "\"${IMPORT_PREFIX}[^\"]+\"" modules --glob '*.go' || true)

while IFS= read -r match; do
  file="${match%%:*}"
  rest="${match#*:}"
  line="${rest%%:*}"
  text="${rest#*:}"

  if [[ ! "${file}" =~ ^modules/([^/]+)/go\.mod$ ]]; then
    continue
  fi
  source_module="${BASH_REMATCH[1]}"

  if [[ ! "${text}" =~ ${IMPORT_PREFIX}([^[:space:]]+) ]]; then
    continue
  fi
  import_path="${BASH_REMATCH[1]}"
  target_module="${import_path%%/*}"
  target_rest="${import_path#*/}"

  if [[ "${source_module}" == "${target_module}" ]]; then
    continue
  fi

  if [[ "${target_rest}" == "${target_module}" ]] && rg -q "\"${IMPORT_PREFIX}${target_module}/testkit\"" "modules/${source_module}" --glob '*_test.go'; then
    continue
  fi

  if [[ "${source_module}" == "admin" ]]; then
    violations+=("${file}:${line}: admin go.mod must not depend on ${target_module}/${target_rest}; route via gateway/service deployments instead")
    continue
  fi

  case "${target_rest}" in
    proto/*)
      continue
      ;;
  esac

  violations+=("${file}:${line}: ${source_module} go.mod must not depend on ${target_module}/${target_rest}")
done < <(rg -n "${IMPORT_PREFIX}[^[:space:]]+" modules/*/go.mod || true)

if [[ -d packages ]]; then
  while IFS= read -r match; do
    file="${match%%:*}"
    rest="${match#*:}"
    line="${rest%%:*}"
    text="${rest#*:}"

    if [[ ! "${text}" =~ \"${IMPORT_PREFIX}([^\"]+)\" ]]; then
      continue
    fi

    violations+=("${file}:${line}: root packages must not import business modules (${text})")
  done < <(rg -n "\"${IMPORT_PREFIX}[^\"]+\"" packages --glob '*.go' || true)

  while IFS= read -r match; do
    file="${match%%:*}"
    rest="${match#*:}"
    line="${rest%%:*}"
    text="${rest#*:}"

    violations+=("${file}:${line}: root package go.mod must not depend on business modules (${text})")
  done < <(rg -n "${IMPORT_PREFIX}[^[:space:]]+" packages/*/go.mod 2>/dev/null || true)
fi

if (( ${#violations[@]} > 0 )); then
  {
    echo "module boundary violations:"
    printf '  %s\n' "${violations[@]}"
    echo
    echo "Allowed cross-module imports:"
    echo "  - generated protocol packages under modules/<name>/proto/..."
    echo "  - generated protocol module dependencies under modules/<name>/proto/..."
    echo "  - business modules may import stable root packages under packages/..."
    echo "  - root packages must not import modules/..."
    echo
    echo "Move shared stable code to a root packages/ module before importing it across business modules."
  } >&2
  exit 1
fi

echo "==> module boundaries passed"
