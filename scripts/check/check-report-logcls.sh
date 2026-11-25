#!/usr/bin/env bash
# 契约检查：凡是依赖 packages/report 的组件 main，都必须空白引入 packages/clsreporter/logcls。
# 没有引入时 logsink.Enabled() 为假，report 不接日志，该组件的重要日志（含 process_started）不会进 Monitor。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

REPORT='github.com/mooyang-code/moox/packages/report'
LOGCLS='github.com/mooyang-code/moox/packages/clsreporter/logcls'

# 例外：这些 main 只借用 report 的类型或单独上报，不是常驻组件。
#   moox-cli：命令行工具；collector scf/market_data：云函数，日志走采集 CLS；trade testnet-smoke：一次性冒烟工具。
EXEMPT=(
    'github.com/mooyang-code/moox/modules/cli/cmd/moox-cli'
    'github.com/mooyang-code/moox/modules/collector/cmd/scf/market_data'
    'github.com/mooyang-code/moox/modules/trade/cmd/testnet-smoke'
)

patterns=()
for dir in modules/*/cmd; do
    patterns+=("./${dir}/...")
done

missing=()
while read -r pkg deps; do
    [[ -z "${pkg}" ]] && continue
    case " ${deps} " in *" ${REPORT} "*) ;; *) continue ;; esac
    case " ${deps} " in *" ${LOGCLS} "*) continue ;; esac
    exempt=0
    for item in "${EXEMPT[@]}"; do
        [[ "${pkg}" == "${item}" ]] && exempt=1
    done
    [[ "${exempt}" == 1 ]] || missing+=("${pkg}")
done < <(GOTOOLCHAIN=local go list -f '{{if eq .Name "main"}}{{.ImportPath}} {{join .Deps " "}}{{end}}' "${patterns[@]}")

if ((${#missing[@]} > 0)); then
    printf '以下组件 main 依赖 packages/report 但没有空白引入 %s：\n' "${LOGCLS}" >&2
    printf '  %s\n' "${missing[@]}" >&2
    exit 1
fi
echo 'report logcls contract passed'
