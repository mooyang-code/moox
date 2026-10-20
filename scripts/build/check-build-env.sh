#!/usr/bin/env bash
# 检查本机能不能构建 MooX：逐项列出已有的和缺的工具，并给出本机系统（macOS / Linux / Windows 的 Git Bash 与 WSL）上的安装命令。
# 设计给人和 Agent 一起用：先跑它，再按“修复”一列补齐缺的，然后重跑直到全部通过。完整说明见 docs/构建手册.md。
#
# 用法：check-build-env.sh [--profile <场景>] [--json]
#   场景（默认 all）：
#     build    构建纯 Go 的 Linux 二进制（交叉编译）：git、固定版本的 Go
#     cgo      build + 构建 Storage（DuckDB，需要 CGO）：Docker 在运行
#     web      build + 构建前端与 Web Host：Node、pnpm
#     verify   build + 推送前本地预检（scripts/ci/local-verify.sh）：make、rg、python3 + PyYAML、jq、rsync、Node、pnpm
#     release  cgo + 发布到线上主机：ssh、scp
#     all      以上全部
#   --json     输出 JSON，方便 Agent 解析；退出码仍表示有没有缺项。
# 退出码：0 全部满足；1 有缺项；2 参数错误。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROFILE=all
JSON=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile) PROFILE="${2:-}"; shift 2 ;;
    --json) JSON=1; shift ;;
    -h|--help) sed -n '2,17p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（--help 查看用法）" >&2; exit 2 ;;
  esac
done
case "${PROFILE}" in build|cgo|web|verify|release|all) ;; *) echo "未知场景：${PROFILE}" >&2; exit 2 ;; esac

# ---------------------------------------------------------------- 本机系统
# MOOX_BUILD_ENV_OS / MOOX_BUILD_ENV_PM 只给测试用，用来在一台机器上演示别的系统的安装建议。
detect_os() {
  if [[ -n "${MOOX_BUILD_ENV_OS:-}" ]]; then printf '%s' "${MOOX_BUILD_ENV_OS}"; return; fi
  case "$(uname -s)" in
    Darwin) printf macos ;;
    Linux) if grep -qi microsoft /proc/version 2>/dev/null; then printf wsl; else printf linux; fi ;;
    MINGW*|MSYS*|CYGWIN*) printf windows ;;
    *) printf unknown ;;
  esac
}
detect_pm() {
  if [[ -n "${MOOX_BUILD_ENV_PM:-}" ]]; then printf '%s' "${MOOX_BUILD_ENV_PM}"; return; fi
  case "${OS}" in
    macos) printf brew ;;
    windows) printf winget ;;
    *)
      local pm
      for pm in apt-get dnf yum pacman zypper apk; do command -v "${pm}" >/dev/null 2>&1 && { printf '%s' "${pm}"; return; }; done
      printf unknown ;;
  esac
}
detect_arch() {
  case "$(uname -m)" in x86_64|amd64|AMD64) printf amd64 ;; arm64|aarch64) printf arm64 ;; *) uname -m ;; esac
}
OS="$(detect_os)"
PM="$(detect_pm)"
ARCH="$(detect_arch)"

# 固定版本的 Go：构建脚本优先用 ~/.local/go<版本>/bin（与 CI 一致）。
GO_WANT="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
GO_HOME_BIN="${HOME}/.local/go${GO_WANT}/bin"
NODE_RANGE='^20.19.0 || ^22.13.0 || >=24.0.0'

# ---------------------------------------------------------------- 安装建议
# install_hint <工具>：按本机包管理器给出安装命令；没有把握的写成指引而不是编造命令。
install_hint() {
  local tool="$1"
  case "${tool}" in
    go) printf 'bash scripts/build/install-go.sh   # 下载固定版本 %s 到 ~/.local/go%s（官方安装包，校验 SHA-256）' "${GO_WANT}" "${GO_WANT}" ;;
    git)
      case "${PM}" in brew) printf 'brew install git' ;; apt-get) printf 'sudo apt-get install -y git' ;; dnf|yum) printf 'sudo %s install -y git' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm git' ;; zypper) printf 'sudo zypper install -y git' ;; apk) printf 'sudo apk add git' ;; winget) printf 'winget install --id Git.Git -e   # 同时带 Git Bash，构建脚本要在 Git Bash 或 WSL 里运行' ;; *) printf '安装 git：https://git-scm.com/downloads' ;; esac ;;
    make)
      case "${PM}" in brew) printf 'xcode-select --install' ;; apt-get) printf 'sudo apt-get install -y make' ;; dnf|yum) printf 'sudo %s install -y make' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm make' ;; zypper) printf 'sudo zypper install -y make' ;; apk) printf 'sudo apk add make' ;; winget) printf '在 WSL 里安装（sudo apt-get install -y make），或用 winget install ezwinports.make' ;; *) printf '安装 GNU make' ;; esac ;;
    rg)
      case "${PM}" in brew) printf 'brew install ripgrep' ;; apt-get) printf 'sudo apt-get install -y ripgrep' ;; dnf|yum) printf 'sudo %s install -y ripgrep' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm ripgrep' ;; zypper) printf 'sudo zypper install -y ripgrep' ;; apk) printf 'sudo apk add ripgrep' ;; winget) printf 'winget install --id BurntSushi.ripgrep.MSVC -e' ;; *) printf '安装 ripgrep：https://github.com/BurntSushi/ripgrep#installation' ;; esac ;;
    jq)
      case "${PM}" in brew) printf 'brew install jq' ;; apt-get) printf 'sudo apt-get install -y jq' ;; dnf|yum) printf 'sudo %s install -y jq' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm jq' ;; zypper) printf 'sudo zypper install -y jq' ;; apk) printf 'sudo apk add jq' ;; winget) printf 'winget install --id jqlang.jq -e' ;; *) printf '安装 jq：https://jqlang.github.io/jq/download/' ;; esac ;;
    rsync)
      case "${PM}" in brew) printf 'brew install rsync   # 系统自带的版本也可用' ;; apt-get) printf 'sudo apt-get install -y rsync' ;; dnf|yum) printf 'sudo %s install -y rsync' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm rsync' ;; zypper) printf 'sudo zypper install -y rsync' ;; apk) printf 'sudo apk add rsync' ;; winget) printf 'Git Bash 没有 rsync：改在 WSL 里运行' ;; *) printf '安装 rsync' ;; esac ;;
    python3)
      case "${PM}" in brew) printf 'brew install python' ;; apt-get) printf 'sudo apt-get install -y python3 python3-pip' ;; dnf|yum) printf 'sudo %s install -y python3 python3-pip' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm python python-pip' ;; zypper) printf 'sudo zypper install -y python3 python3-pip' ;; apk) printf 'sudo apk add python3 py3-pip' ;; winget) printf 'winget install --id Python.Python.3.12 -e' ;; *) printf '安装 Python 3：https://www.python.org/downloads/' ;; esac ;;
    pyyaml) printf 'python3 -m pip install --user pyyaml   # 系统包管理器的 python3-yaml 也可以' ;;
    node)
      case "${PM}" in brew) printf 'brew install node@24 && brew link --overwrite node@24   # 需要 Node %s' "${NODE_RANGE}" ;; apt-get|dnf|yum|pacman|zypper|apk) printf '用 nvm 或 NodeSource 装 Node 24：https://nodejs.org/en/download （发行版自带的版本通常太旧，需要 %s）' "${NODE_RANGE}" ;; winget) printf 'winget install --id OpenJS.NodeJS -e   # 需要 Node %s' "${NODE_RANGE}" ;; *) printf '安装 Node：https://nodejs.org/en/download （需要 %s）' "${NODE_RANGE}" ;; esac ;;
    pnpm) printf 'corepack enable && corepack prepare pnpm@10.28.2 --activate   # 或 npm install -g pnpm@10.28.2' ;;
    docker)
      case "${OS}" in
        macos) printf 'brew install colima docker && colima start --vm-type vz --cpu 4 --memory 8   # 也可以装 Docker Desktop 或 OrbStack' ;;
        windows) printf '安装 Docker Desktop（WSL2 后端）：winget install --id Docker.DockerDesktop -e；构建脚本在 WSL 或 Git Bash 里运行' ;;
        wsl) printf '在 Windows 安装 Docker Desktop 并在其设置里打开本发行版的 WSL 集成；或在 WSL 内装 docker.io' ;;
        *)
          case "${PM}" in apt-get) printf 'sudo apt-get install -y docker.io && sudo usermod -aG docker "$USER"   # 重新登录后生效' ;; dnf|yum) printf 'sudo %s install -y docker && sudo systemctl enable --now docker && sudo usermod -aG docker "$USER"' "${PM}" ;; pacman) printf 'sudo pacman -S --noconfirm docker && sudo systemctl enable --now docker && sudo usermod -aG docker "$USER"' ;; *) printf '安装 Docker Engine：https://docs.docker.com/engine/install/' ;; esac ;;
      esac ;;
    docker-running)
      case "${OS}" in
        macos) printf 'colima start --vm-type vz --cpu 4 --memory 8   # 用 Docker Desktop / OrbStack 的话改为启动它们' ;;
        windows|wsl) printf '启动 Docker Desktop，并确认已打开 WSL 集成' ;;
        *) printf 'sudo systemctl enable --now docker；当前用户要在 docker 组里（sudo usermod -aG docker "$USER" 后重新登录）' ;;
      esac ;;
    ssh) case "${PM}" in apt-get) printf 'sudo apt-get install -y openssh-client' ;; dnf|yum) printf 'sudo %s install -y openssh-clients' "${PM}" ;; winget) printf 'Windows 自带 OpenSSH 客户端；Git Bash 里也有 ssh/scp' ;; *) printf '安装 OpenSSH 客户端' ;; esac ;;
    sha256) printf 'macOS 自带 shasum；Linux 的 coreutils 自带 sha256sum' ;;
    *) printf '' ;;
  esac
}

# ---------------------------------------------------------------- 检查项
RESULTS=""
FAILED=0
# 每行用 \x1f（单元分隔符）分隔字段：id、标签、状态(ok/missing/outdated/not_running)、发现的、需要的、修复。
# 不用 | 是因为 Node 的版本范围里就有 |。
SEP=$'\x1f'
record() {
  local id="$1" label="$2" status="$3" found="$4" need="$5" fix="$6"
  RESULTS="${RESULTS}${id}${SEP}${label}${SEP}${status}${SEP}${found}${SEP}${need}${SEP}${fix}"$'\n'
  [[ "${status}" == ok ]] || FAILED=1
}
has() { command -v "$1" >/dev/null 2>&1; }

# 场景是否包含某个要求。
wants() {
  local group="$1"
  case "${PROFILE}" in
    all) return 0 ;;
    build) [[ "${group}" == build ]] ;;
    cgo) [[ "${group}" == build || "${group}" == cgo ]] ;;
    web) [[ "${group}" == build || "${group}" == web ]] ;;
    verify) [[ "${group}" == build || "${group}" == verify || "${group}" == web ]] ;;
    release) [[ "${group}" == build || "${group}" == cgo || "${group}" == release ]] ;;
  esac
}

simple_tool() { # id 标签 命令 版本命令
  local id="$1" label="$2" cmd="$3" version_cmd="${4:-}" found=""
  if has "${cmd}"; then
    [[ -n "${version_cmd}" ]] && found="$(eval "${version_cmd}" 2>/dev/null | head -n 1 || true)"
    record "${id}" "${label}" ok "${found:-已安装}" "任意版本" ""
  else
    record "${id}" "${label}" missing "" "任意版本" "$(install_hint "${id}")"
  fi
}

# 版本比较：version_ge <a> <b> 即 a >= b（点分数字）。
version_ge() {
  local IFS=. i a b
  read -r -a a <<<"$1"; read -r -a b <<<"$2"
  for i in 0 1 2; do
    if (( ${a[$i]:-0} > ${b[$i]:-0} )); then return 0; fi
    if (( ${a[$i]:-0} < ${b[$i]:-0} )); then return 1; fi
  done
  return 0
}

check_go() {
  local go_bin="" found=""
  if [[ -x "${GO_HOME_BIN}/go" ]]; then go_bin="${GO_HOME_BIN}/go"; elif has go; then go_bin="$(command -v go)"; fi
  if [[ -z "${go_bin}" ]]; then
    record go "Go ${GO_WANT}" missing "" "go${GO_WANT}" "$(install_hint go)"
    return
  fi
  found="$(GOTOOLCHAIN=local "${go_bin}" env GOVERSION 2>/dev/null || true)"
  if [[ "${found}" == "go${GO_WANT}" ]]; then
    record go "Go ${GO_WANT}" ok "${found}（${go_bin}）" "go${GO_WANT}" ""
  else
    record go "Go ${GO_WANT}" outdated "${found:-未知}（${go_bin}）" "go${GO_WANT}（构建脚本要求与 .go-version 完全一致）" "$(install_hint go)"
  fi
}

check_sha256() {
  if has sha256sum || has shasum; then record sha256 "SHA-256 工具" ok "已安装" "sha256sum 或 shasum" ""
  else record sha256 "SHA-256 工具" missing "" "sha256sum 或 shasum" "$(install_hint sha256)"; fi
}

check_node() {
  if ! has node; then record node "Node.js" missing "" "${NODE_RANGE}" "$(install_hint node)"; return; fi
  local v major minor ok=0
  v="$(node -p 'process.versions.node' 2>/dev/null || true)"
  major="${v%%.*}"; minor="${v#*.}"; minor="${minor%%.*}"
  if [[ "${major}" =~ ^[0-9]+$ && "${minor}" =~ ^[0-9]+$ ]]; then
    if (( major >= 24 )) || { (( major == 22 )) && (( minor >= 13 )); } || { (( major == 20 )) && (( minor >= 19 )); }; then ok=1; fi
  fi
  if (( ok )); then record node "Node.js" ok "v${v}" "${NODE_RANGE}" ""
  else record node "Node.js" outdated "v${v:-未知}" "${NODE_RANGE}" "$(install_hint node)"; fi
}

check_pnpm() {
  if has pnpm; then record pnpm "pnpm" ok "$(pnpm --version 2>/dev/null | head -n 1)" "pnpm >= 8.7（仓库固定 10.28.2）" ""
  else record pnpm "pnpm" missing "" "pnpm >= 8.7（仓库固定 10.28.2）" "$(install_hint pnpm)"; fi
}

check_rg() {
  # 必须是真正的 ripgrep 可执行文件：shell 里的 rg 别名/函数不会被 make 和脚本继承。
  local path
  path="$(type -P rg 2>/dev/null || true)"
  if [[ -n "${path}" ]]; then record rg "ripgrep" ok "$("${path}" --version 2>/dev/null | head -n 1)" "可执行文件 rg" ""
  else record rg "ripgrep" missing "" "PATH 里有可执行文件 rg（shell 别名不算）" "$(install_hint rg)"; fi
}

check_pyyaml() {
  if ! has python3; then record pyyaml "PyYAML" missing "" "python3 -c 'import yaml'" "$(install_hint pyyaml)"; return; fi
  if python3 -c 'import yaml' >/dev/null 2>&1; then record pyyaml "PyYAML" ok "$(python3 -c 'import yaml; print(yaml.__version__)' 2>/dev/null)" "python3 -c 'import yaml'" ""
  else record pyyaml "PyYAML" missing "" "python3 -c 'import yaml'" "$(install_hint pyyaml)"; fi
}

check_docker() {
  if ! has docker; then
    record docker "Docker" missing "" "docker 命令" "$(install_hint docker)"
    return
  fi
  local version
  version="$(docker --version 2>/dev/null | head -n 1 || true)"
  if ! docker info >/dev/null 2>&1; then
    record docker "Docker" not_running "${version}（没有在运行或当前用户无权访问）" "docker info 成功" "$(install_hint docker-running)"
    return
  fi
  local mem_bytes mem_gb
  mem_bytes="$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)"
  mem_gb=$(( ${mem_bytes:-0} / 1073741824 ))
  if (( mem_gb < 4 )); then
    record docker "Docker" outdated "${version}，可用内存 ${mem_gb} GB" "Docker 可用内存 >= 4 GB（DuckDB 链接较吃内存）" "调大 Docker 内存（colima: colima stop && colima start --memory 8；Docker Desktop: Settings → Resources）"
  else
    record docker "Docker" ok "${version}，可用内存 ${mem_gb} GB" "docker info 成功，可用内存 >= 4 GB" ""
  fi
}

check_windows_shell() {
  case "${OS}" in
    windows) record shell "运行环境" ok "Git Bash / MSYS" "bash（Git Bash 或 WSL）" "" ;;
    unknown) record shell "运行环境" outdated "未识别的系统 $(uname -s)" "macOS、Linux、WSL 或 Windows 的 Git Bash" "见 docs/构建手册.md" ;;
  esac
}

check_windows_shell
if wants build; then
  simple_tool git "Git" git 'git --version'
  check_go
  check_sha256
fi
if wants cgo; then check_docker; fi
if wants web; then check_node; check_pnpm; fi
if wants verify; then
  simple_tool make "make" make 'make --version'
  check_rg
  simple_tool python3 "Python 3" python3 'python3 --version'
  check_pyyaml
  simple_tool jq "jq" jq 'jq --version'
  simple_tool rsync "rsync" rsync 'rsync --version'
fi
if wants release; then
  simple_tool ssh "ssh" ssh 'ssh -V 2>&1'
  simple_tool scp "scp" scp ''
fi

# ---------------------------------------------------------------- 输出
json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"; s="${s//\"/\\\"}"; s="${s//$'\t'/\\t}"
  printf '%s' "${s}"
}

if (( JSON )); then
  printf '{"os":"%s","arch":"%s","package_manager":"%s","profile":"%s","go_version":"%s","ok":%s,"checks":[' \
    "${OS}" "${ARCH}" "${PM}" "${PROFILE}" "${GO_WANT}" "$([[ ${FAILED} -eq 0 ]] && echo true || echo false)"
  first=1
  while IFS="${SEP}" read -r id label status found need fix; do
    [[ -n "${id}" ]] || continue
    (( first )) || printf ','
    first=0
    printf '{"id":"%s","label":"%s","status":"%s","found":"%s","need":"%s","fix":"%s"}' \
      "$(json_escape "${id}")" "$(json_escape "${label}")" "${status}" "$(json_escape "${found}")" "$(json_escape "${need}")" "$(json_escape "${fix}")"
  done <<<"${RESULTS}"
  printf ']}\n'
else
  printf '系统：%s/%s，包管理器：%s，场景：%s，固定 Go 版本：%s\n\n' "${OS}" "${ARCH}" "${PM}" "${PROFILE}" "go${GO_WANT}"
  while IFS="${SEP}" read -r id label status found need fix; do
    [[ -n "${id}" ]] || continue
    case "${status}" in
      ok) printf '  [通过]   %-14s %s\n' "${label}" "${found}" ;;
      missing) printf '  [缺少]   %-14s 需要：%s\n' "${label}" "${need}" ;;
      outdated) printf '  [不符]   %-14s 发现：%s；需要：%s\n' "${label}" "${found}" "${need}" ;;
      not_running) printf '  [未运行] %-14s %s\n' "${label}" "${found}" ;;
    esac
    [[ -z "${fix}" ]] || printf '           修复：%s\n' "${fix}"
  done <<<"${RESULTS}"
  echo
  if (( FAILED )); then echo "有缺项：按“修复”一列处理后重新运行本脚本，直到全部通过。"; else echo "环境满足：可以构建（场景 ${PROFILE}）。"; fi
fi
exit "${FAILED}"
