#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
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

  if [[ "${source_module}" == "admin" ]]; then
    violations+=("${file}:${line}: admin must not import ${target_module}/${target_rest}; route via gateway/service deployments instead")
    continue
  fi

  case "${target_rest}" in
    proto/*)
      continue
      ;;
  esac

  # cli 预检需要按内嵌 SQLite schema 计算期望数据格式，允许引入各模块纯 embed 的 schema 包。
  if [[ "${source_module}" == "cli" && "${target_rest}" == "schema" ]]; then
    continue
  fi

  violations+=("${file}:${line}: ${source_module} must not import ${target_module}/${target_rest}")
done < <(rg -n "\"${IMPORT_PREFIX}[^\"]+\"" modules --glob '*.go' || true)

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
fi

# 仓库已收敛为根目录唯一 go.mod，禁止新增 go.work 或子目录 go.mod（排除 .toolchain/ 与 web/）。
while IFS= read -r file; do
  [[ -n "${file}" && -e "${file}" ]] || continue
  case "${file}" in
    go.mod|.toolchain/*|web/*)
      continue
      ;;
  esac
  violations+=("${file}: single root go.mod is required; go.work and nested go.mod are forbidden")
done < <(
  if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git ls-files --cached --others --exclude-standard -- 'go.work' '**/go.work' '**/go.mod'
  else
    find . \( -path './.git' -o -path './.claude' -o -path './.toolchain' -o -path './web' -o -path './docs/计划' -o -path './modules/*/proto/*/tmp-*' \) -prune -o \
      \( -name 'go.work' -o -name 'go.mod' \) -print | sed 's|^\./||'
  fi
)

if (( ${#violations[@]} > 0 )); then
  {
    echo "module boundary violations:"
    printf '  %s\n' "${violations[@]}"
    echo
    echo "Allowed cross-module imports:"
    echo "  - generated protocol packages under modules/<name>/proto/..."
    echo "  - cli may import embedded schema packages under modules/<name>/schema"
    echo "  - business modules may import stable root packages under packages/..."
    echo "  - root packages must not import modules/..."
    echo "  - repository must maintain a single root go.mod (no go.work or nested go.mod)"
    echo
    echo "Move shared stable code to packages/ before importing it across business modules."
  } >&2
  exit 1
fi

echo "==> module boundaries passed"
