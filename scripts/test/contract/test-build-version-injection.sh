#!/usr/bin/env bash
# 版本注入的契约检查：发布构建（VERSION=vX.Y.Z）出来的每个服务组件都要能上报这个版本，而不是 dev。
#   1. build.sh 的 LDFLAGS 同时写 main.Version 和 packages/report.BuildVersion；
#   2. 用 build.sh 里的变量路径真的注入一次，packages/report 读得到（变量改名、包路径变化会在这里失败）；
#   3. build.sh 里每个服务目标要么链接了 packages/report（经它上报版本），要么 main 包自己有 Version 变量。
#      命令行工具（*-cli）不上报，不检查。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BUILD="${ROOT}/scripts/build/build.sh"
fail() { echo "FAIL: $*" >&2; exit 1; }

go_version="$(tr -d '[:space:]' <"${ROOT}/.go-version")"
if [[ -d "${HOME}/.local/go${go_version}/bin" ]]; then export PATH="${HOME}/.local/go${go_version}/bin:${PATH}"; fi
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly

report_var="$(sed -n 's/^REPORT_VERSION_VAR="\(.*\)"$/\1/p' "${BUILD}")"
[[ -n "${report_var}" ]] || fail "build.sh 里没有 REPORT_VERSION_VAR"
grep -Fq -- '-X main.Version=${VERSION}' "${BUILD}" || fail "build.sh 的 LDFLAGS 没有写 main.Version"
grep -Fq -- '-X ${REPORT_VERSION_VAR}=${VERSION}' "${BUILD}" || fail "build.sh 的 LDFLAGS 没有写 ${report_var}"

report_pkg="${report_var%.*}"
(cd "${ROOT}/packages/report" && MOOX_EXPECT_BUILD_VERSION=v9.9.9 \
  go test -count=1 -ldflags "-X ${report_var}=v9.9.9" -run '^TestBuildVersionInjectedByLdflags$' . >/dev/null) \
  || fail "按 build.sh 的写法注入版本号后 packages/report 读不到：${report_var}"

checked=0
while read -r module package output; do
  case "${output}" in *-cli | moox-cli) continue ;; esac
  checked=$((checked + 1))
  if (cd "${ROOT}/${module}" && go list -deps "${package}" 2>/dev/null | grep -Fxq "${report_pkg}"); then continue; fi
  # go list 在缺依赖缓存或关了 cgo 的机器上会失败（Storage 依赖 cgo）：退回按源码里的 import 判断。
  if grep -rIlF --include='*.go' --exclude='*_test.go' "\"${report_pkg}\"" "${ROOT}/${module}" >/dev/null 2>&1; then continue; fi
  grep -Eqs '^[[:space:]]*(var[[:space:]]+)?Version[[:space:]]*=[[:space:]]*"dev"' "${ROOT}/${module}/${package#./}"/*.go \
    || fail "${output}（${module} ${package}）既没有链接 ${report_pkg}，main 包里也没有 Version 变量：发布后会上报 dev"
done < <(
  {
    grep -E '^[[:space:]]*build_go modules/' "${BUILD}" | awk '{print $2, $3, $4}'
    # Storage 的两个进程不走 build_go，同一个 main 包。
    echo "modules/storage ./cmd/server moox-storage"
  } | sort -u
)
[[ "${checked}" -ge 15 ]] || fail "只检查到 ${checked} 个服务目标，build.sh 的写法可能变了"

echo "build version injection contract passed (${checked} 个服务目标)"
