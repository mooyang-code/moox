#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${1:-all}"
MODULES=(cli admin collector factor order account storage)
SELECTED_MODULES=""

check_workspace() {
  echo "==> check go workspace"
  if [[ ! -f "${ROOT}/go.work" ]]; then
    echo "go.work not found under ${ROOT}" >&2
    exit 1
  fi
}

run_boundaries() {
  "${ROOT}/scripts/check-module-boundaries.sh"
}

run_module_tests() {
  local module="$1"
  case "${module}" in
    storage)
      echo "==> test modules/storage"
      (cd "${ROOT}/modules/storage" && make test)
      ;;
    cli|admin|collector|factor|order|account)
      echo "==> test modules/${module}"
      (cd "${ROOT}/modules/${module}" && go test ./...)
      ;;
    *)
      echo "unknown test target: ${module}" >&2
      exit 1
      ;;
  esac
}

run_all_tests() {
  run_boundaries
  for module in "${MODULES[@]}"; do
    run_module_tests "${module}"
  done
}

changed_files() {
  if [[ -n "${BASE_REF:-}" ]]; then
    git -C "${ROOT}" diff --name-only "${BASE_REF}...HEAD" 2>/dev/null || true
  else
    git -C "${ROOT}" diff --name-only HEAD 2>/dev/null || true
  fi
  git -C "${ROOT}" ls-files --others --exclude-standard
}

has_selected_module() {
  local module="$1"
  [[ " ${SELECTED_MODULES} " == *" ${module} "* ]]
}

add_selected_module() {
  local module="$1"
  if ! has_selected_module "${module}"; then
    SELECTED_MODULES="${SELECTED_MODULES} ${module}"
  fi
}

run_changed_tests() {
  local files
  files="$(changed_files | sort -u)"
  if [[ -z "${files}" ]]; then
    echo "==> no changed files; running boundary check only"
    run_boundaries
    return
  fi

  SELECTED_MODULES=""
  local needs_all=0
  local file module

  while IFS= read -r file; do
    [[ -z "${file}" ]] && continue
    case "${file}" in
      Makefile|go.work|go.work.sum|scripts/*)
        needs_all=1
        ;;
      modules/*)
        module="${file#modules/}"
        module="${module%%/*}"
        add_selected_module "${module}"
        case "${file}" in
          modules/admin/proto/*)
            add_selected_module "cli"
            ;;
          modules/storage/proto/*)
            add_selected_module "cli"
            ;;
        esac
        ;;
    esac
  done <<< "${files}"

  if (( needs_all == 1 )); then
    echo "==> root scripts/workspace changed; running all module tests"
    run_all_tests
    return
  fi

  run_boundaries
  local ran=0
  for module in "${MODULES[@]}"; do
    if has_selected_module "${module}"; then
      run_module_tests "${module}"
      ran=1
    fi
  done
  if (( ran == 0 )); then
    echo "==> no module changes detected; boundary check passed"
  fi
}

check_workspace

case "${TARGET}" in
  all)
    run_all_tests
    ;;
  changed)
    run_changed_tests
    ;;
  boundaries)
    run_boundaries
    ;;
  cli|admin|collector|factor|order|account|storage)
    run_boundaries
    run_module_tests "${TARGET}"
    ;;
  *)
    echo "unknown test target: ${TARGET}" >&2
    echo "usage: scripts/test.sh [all|changed|boundaries|cli|admin|collector|factor|order|account|storage]" >&2
    exit 1
    ;;
esac

echo "==> all tests passed"
