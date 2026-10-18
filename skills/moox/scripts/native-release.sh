#!/usr/bin/env bash
# MooX 原生部署的二进制发布助手：在构建机上构建、取回、逐台安装并重启、核对 sha、写发布台账、回滚。
# 流程、顺序与注意事项见 skills/moox/references/native-release.md；这里只固化其中机械且容易出错的步骤。
#
# 只用 SSH 公钥（BatchMode，禁用密码登录）；不读取、不使用 moox.toml；不处理任何凭据。
set -euo pipefail

CONFIG_FILE="${MOOX_RELEASE_ENV:-${HOME}/.moox-release.env}"
# shellcheck disable=SC1090
[[ -f "${CONFIG_FILE}" ]] && source "${CONFIG_FILE}"

DEPLOY_ROOT="${MOOX_RELEASE_ROOT:-/data/moox}"
BUILD_DIR="${MOOX_RELEASE_BUILD_DIR:-moox-build}"
OUT_ROOT="${MOOX_RELEASE_OUT:-${TMPDIR:-/tmp}/moox-release}"
SSH_OPTS=(-o BatchMode=yes -o PasswordAuthentication=no)
LEDGER_REL="run/release-ledger.log"

usage() {
  cat >&2 <<'EOF'
用法：native-release.sh <命令> [选项]

  build   [--allow-unpushed] [--builder user@host] <构建目标>...
          把当前（干净且已推送的）提交同步到构建机、构建、取回到本地并校验 sha。
          构建目标即 scripts/build/build.sh 的目标，例如 host-gateway admin monitor collector storage-primary。
  install --host <别名> --unit <单元目录> --file <本地二进制> [--name moox-xxx]
          (--restart <组件id>... | --no-restart) [--label <备份标签>]
          上传、核对 sha、备份旧二进制、原子替换、重启、核对重启后的 sha 与就绪状态，并写发布台账。
  rollback --host <别名> --unit <单元目录> --backup <备份目录> (--restart <组件id>... | --no-restart)
          把备份目录里的 moox-* 二进制装回去并重启。
  status  --host <别名>            各单元组件的状态、就绪与二进制 sha。
  ledger  --host <别名> [--tail N]  该主机的发布台账（谁在什么时候装了哪个提交）。
  catalog-changed (--since <提交> | --host <别名>)
          服务目录 packages/servicecatalog/catalog.yaml 自上次发布以来是否变化；变化时退出码 10，必须整体发布。

主机别名对应环境变量 MOOX_RELEASE_HOST_<别名大写，- 换成 _>，值为 user@host，例如
MOOX_RELEASE_HOST_CONTROL、MOOX_RELEASE_HOST_STORAGE、MOOX_RELEASE_HOST_OVERSEAS；构建机用 MOOX_RELEASE_BUILDER。
这些从 ~/.moox-release.env（或 MOOX_RELEASE_ENV 指定的文件）读取，不入库，示例见 skills/moox/references/release-hosts.env.example。
EOF
  exit 2
}

die() { echo "错误：$*" >&2; exit 1; }

sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

repo_root() {
  if [[ -n "${MOOX_RELEASE_REPO:-}" ]]; then printf '%s\n' "${MOOX_RELEASE_REPO}"; else git rev-parse --show-toplevel; fi
}

# 别名 -> user@host；只接受 user@host 形式，不接受带密码的写法。
host_target() {
  local alias="$1" var value
  [[ "${alias}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "主机别名 ${alias} 不合法"
  var="MOOX_RELEASE_HOST_$(printf '%s' "${alias}" | tr 'a-z-' 'A-Z_')"
  value="${!var:-}"
  [[ -n "${value}" ]] || die "没有配置 ${var}（在 ${CONFIG_FILE} 里写 ${var}=user@host）"
  [[ "${value}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._:-]+$ ]] || die "${var} 必须是 user@host 形式"
  printf '%s\n' "${value}"
}

# 单元就是部署根下的目录名：控制机的 prod、Storage 主机的 storage、每台主机的 host，以及 access、trade、egress-proxy 等。
unit_link() {
  [[ "$1" =~ ^[a-z][a-z0-9-]*$ ]] || die "--unit 必须是部署根下的单元目录名（如 prod、storage、host、trade），不能是 $1"
  printf '%s/%s/current\n' "${DEPLOY_ROOT}" "$1"
}

# 就绪等待的次数与间隔（秒），默认 30 次 × 2 秒；测试里调小。
ready_env_assignments() {
  local attempts="${MOOX_RELEASE_READY_ATTEMPTS:-30}" interval="${MOOX_RELEASE_READY_INTERVAL:-2}"
  [[ "${attempts}" =~ ^[0-9]+$ && "${interval}" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "MOOX_RELEASE_READY_ATTEMPTS / MOOX_RELEASE_READY_INTERVAL 必须是数字"
  printf "READY_ATTEMPTS='%s' READY_INTERVAL='%s'" "${attempts}" "${interval}"
}

check_component_ids() {
  local id
  for id in "$@"; do [[ "${id}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "组件 id ${id} 不合法"; done
}

# ---------------------------------------------------------------- build
# 构建机上的脚本：只认这次构建新产出的文件。放在变量里再经管道交给 ssh（macOS 自带的 bash 3.2 解析不了 $(...) 里嵌 heredoc）。
read -r -d '' REMOTE_BUILD_BODY <<'REMOTE' || true
set -euo pipefail
cd "${BUILD_DIR}"
export PATH="${HOME}/.local/go$(tr -d '[:space:]' < .go-version)/bin:${PATH}" GOTOOLCHAIN=local
mkdir -p bin .gotmp
touch .release-marker
for target in ${TARGETS}; do
  cgo=0
  # Storage 依赖 DuckDB，必须在 Linux 构建机上开 CGO；其余组件不开。
  case "${target}" in storage|storage-cgo|storage-primary|storage-node) cgo=1 ;; esac
  GOTMPDIR="${PWD}/.gotmp" GOFLAGS=-buildvcs=false VERSION="${VERSION}" GIT_COMMIT="${VERSION}" \
    CGO_ENABLED="${cgo}" TARGET_GOOS=linux TARGET_GOARCH=amd64 bash scripts/build/build.sh "${target}" >&2
done
# 只认这次构建新产出的文件，旧产物不算。
find bin -maxdepth 1 -type f -newer .release-marker | sort | while read -r file; do
  printf 'BUILT:%s:%s\n' "$(basename "${file}")" "$(sha256sum "${file}" | cut -d' ' -f1)"
done
REMOTE

cmd_build() {
  local allow_unpushed=0 builder="${MOOX_RELEASE_BUILDER:-}" targets=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --allow-unpushed) allow_unpushed=1; shift ;;
      --builder) builder="${2:-}"; shift 2 ;;
      --*) die "未知选项 $1" ;;
      *) targets+=("$1"); shift ;;
    esac
  done
  [[ ${#targets[@]} -gt 0 ]] || usage
  [[ -n "${builder}" ]] || die "没有配置构建机（MOOX_RELEASE_BUILDER=user@host，或 --builder）"
  [[ "${builder}" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._:-]+$ ]] || die "构建机必须是 user@host 形式"
  local target
  for target in "${targets[@]}"; do
    [[ "${target}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "构建目标 ${target} 不合法"
  done

  local repo version
  repo="$(repo_root)"
  # 构建的是工作区同步过去的内容，必须和提交一一对应，否则线上二进制对不上任何提交。
  if [[ -n "$(git -C "${repo}" status --porcelain --untracked-files=no)" ]]; then
    die "工作区有未提交的改动；先提交（构建机上的二进制要能对应到一个提交）"
  fi
  version="$(git -C "${repo}" rev-parse --short=8 HEAD)"
  if [[ ${allow_unpushed} -ne 1 ]] && [[ -z "$(git -C "${repo}" branch -r --contains HEAD 2>/dev/null)" ]]; then
    die "提交 ${version} 还没有推送到远端；先 git push（只有用户明确要求本地试验时才加 --allow-unpushed）"
  fi

  # .gotmp 必须保留：rsync --delete 会把它删掉，go 就会因为创建不了工作目录而失败并沿用旧产物。
  rsync -az --delete \
    --exclude .git --exclude moox.toml --exclude bin --exclude .gotmp --exclude node_modules --exclude web/dist \
    --exclude release --exclude artifacts --exclude .worktrees --exclude .tmp --exclude '*.test' \
    -e "ssh ${SSH_OPTS[*]}" "${repo}/" "${builder}:${BUILD_DIR}/"

  local listing line name sha
  listing="$(printf '%s\n' "${REMOTE_BUILD_BODY}" | ssh "${SSH_OPTS[@]}" "${builder}" "BUILD_DIR='${BUILD_DIR}' VERSION='${version}' TARGETS='${targets[*]}' bash -s")"
  local out_dir="${OUT_ROOT}/${version}" count=0
  mkdir -p "${out_dir}"
  while IFS= read -r line; do
    [[ "${line}" == BUILT:* ]] || continue
    name="$(printf '%s' "${line}" | cut -d: -f2)"
    sha="$(printf '%s' "${line}" | cut -d: -f3)"
    [[ "${name}" =~ ^moox-[a-z0-9-]+$ ]] || die "构建机返回了不认识的文件名 ${name}"
    scp -O -q -C "${SSH_OPTS[@]}" "${builder}:${BUILD_DIR}/bin/${name}" "${out_dir}/${name}"
    [[ "$(sha_of "${out_dir}/${name}")" == "${sha}" ]] || die "${name} 取回后的 sha 与构建机上的不一致"
    printf 'BUILT %s %s %s\n' "${name}" "${sha}" "${out_dir}/${name}"
    count=$((count + 1))
  done <<<"${listing}"
  [[ ${count} -gt 0 ]] || die "构建没有产出新的二进制：常见原因是构建机上缺 .gotmp（go 创建工作目录失败却沿用了旧产物）、Go 版本不对或目标名写错"
  echo "提交 ${version} 的 ${count} 个二进制已取回到 ${out_dir}"
}

# -------------------------------------------------------------- install
# 远端：重启组件后核对返回的二进制 sha，并等它就绪。install 与 rollback 共用。
REMOTE_RESTART_FUNCS='
wait_ready() {
  # 重启返回时可能还没就绪：默认最多等 60 秒（30 次，每次 2 秒）。
  local c="$1" i
  for i in $(seq 1 "${READY_ATTEMPTS:-30}"); do
    if "${LINK}/status.sh" 2>/dev/null | tail -n 1 | C="${c}" python3 -c "
import json, os, sys
cid = os.environ[\"C\"]
sys.exit(0 if any(x[\"id\"] == cid and x[\"ready\"] for x in json.loads(sys.stdin.read())[\"components\"]) else 1)
"; then echo "READY:${c}"; return 0; fi
    sleep "${READY_INTERVAL:-2}"
  done
  echo "组件 ${c} 重启后等了 ${READY_ATTEMPTS:-30} 次仍没有就绪" >&2
  return 6
}

restart_and_verify() {
  # $1 组件 id；$2 期望的二进制 sha（空表示不核对，回滚时用）
  local c="$1" want="${2:-}" out
  echo "RESTART:${c}"
  out="$("${LINK}/restart.sh" --components "${c}" 2>&1 | tail -n 1)" || { echo "restart.sh 执行失败" >&2; return 4; }
  # 返回里的 binary_sha256 必须等于刚装的：不等说明装错了文件名，或这个组件根本不用这个二进制。
  printf "%s" "${out}" | C="${c}" WANT="${want}" python3 -c "
import json, os, sys
cid, want = os.environ[\"C\"], os.environ[\"WANT\"]
data = json.loads(sys.stdin.read())
for item in data[\"components\"]:
    if item[\"id\"] == cid:
        print(\"RESULT:%s:%s:ready=%s:sha=%s\" % (cid, item[\"state\"], item[\"ready\"], item[\"binary_sha256\"][:16]))
        if want and item[\"binary_sha256\"] != want:
            print(\"重启后的二进制 sha 与刚安装的不一致\", file=sys.stderr); sys.exit(4)
        sys.exit(0)
print(\"重启结果里没有组件 \" + cid, file=sys.stderr); sys.exit(4)
" || return 4
  wait_ready "${c}"
}
'

# 远端安装脚本：校验上传、备份、原子替换、写台账、重启并核对。
read -r -d '' REMOTE_INSTALL_BODY <<'REMOTE' || true
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
REL="$(readlink -f "${LINK}")"
[ -d "${REL}/bin" ] || { echo "发布目录 ${REL}/bin 不存在" >&2; rm -f "${REMOTE_TMP}"; exit 5; }
[ "$(sha "${REMOTE_TMP}")" = "${SHA}" ] || { echo "上传后的 sha 与本地不一致，已放弃，未改动任何文件" >&2; rm -f "${REMOTE_TMP}"; exit 3; }
BK_BASE="$(dirname "${LINK}")/binary-backup-${LABEL}-${TS}"
# 备份目录必须是这次新建的：同一秒内连续安装时不能写进上一次的备份目录，否则会把更早备份的旧二进制覆盖掉。
BK="${BK_BASE}"; n=0
while ! mkdir "${BK}" 2>/dev/null; do
  n=$((n + 1)); [ "${n}" -lt 100 ] || { echo "无法创建备份目录 ${BK_BASE}" >&2; rm -f "${REMOTE_TMP}"; exit 5; }
  BK="${BK_BASE}-${n}"
done
if [ -f "${REL}/bin/${NAME}" ]; then cp -p "${REL}/bin/${NAME}" "${BK}/"; fi
install -m 0755 "${REMOTE_TMP}" "${REL}/bin/.${NAME}.new"
mv -f "${REL}/bin/.${NAME}.new" "${REL}/bin/${NAME}"
rm -f "${REMOTE_TMP}"
[ "$(sha "${REL}/bin/${NAME}")" = "${SHA}" ] || { echo "安装后的 sha 不一致" >&2; exit 3; }
echo "BACKUP:${BK}"
mkdir -p "$(dirname "${LEDGER}")"
printf '%s %s %s %s %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${UNIT}" "${NAME}" "${COMMIT}" "${SHA}" "${COMPONENTS:-none}" >> "${LEDGER}"
for c in ${COMPONENTS}; do restart_and_verify "${c}" "${SHA}" || exit $?; done
REMOTE

# 远端回滚脚本：把备份目录里的 moox-* 装回去并重启。
read -r -d '' REMOTE_ROLLBACK_BODY <<'REMOTE' || true
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
REL="$(readlink -f "${LINK}")"
[ -d "${BACKUP}" ] || { echo "备份目录 ${BACKUP} 不存在" >&2; exit 5; }
restored=""
for f in "${BACKUP}"/moox-*; do
  [ -f "${f}" ] || continue
  n="$(basename "${f}")"
  install -m 0755 "${f}" "${REL}/bin/.${n}.new"
  mv -f "${REL}/bin/.${n}.new" "${REL}/bin/${n}"
  [ "$(sha "${REL}/bin/${n}")" = "$(sha "${f}")" ] || { echo "回滚后 ${n} 的 sha 不一致" >&2; exit 3; }
  echo "RESTORED:${n}:$(sha "${f}" | cut -c1-16)"
  mkdir -p "$(dirname "${LEDGER}")"
  printf '%s %s %s rollback:%s %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${UNIT}" "${n}" "$(basename "${BACKUP}")" "$(sha "${f}")" "${COMPONENTS:-none}" >> "${LEDGER}"
  restored="${restored} ${n}"
done
[ -n "${restored}" ] || { echo "备份目录里没有 moox-* 二进制" >&2; exit 5; }
# 回滚时不知道哪个文件对应哪个组件，所以只核对组件重启后就绪；二进制 sha 以 RESTORED 行为准。
for c in ${COMPONENTS}; do restart_and_verify "${c}" "" || exit $?; done
REMOTE

cmd_install() {
  local host="" unit="" file="" name="" label="" norestart=0 components=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --host) host="${2:-}"; shift 2 ;;
      --unit) unit="${2:-}"; shift 2 ;;
      --file) file="${2:-}"; shift 2 ;;
      --name) name="${2:-}"; shift 2 ;;
      --label) label="${2:-}"; shift 2 ;;
      --restart) components+=("${2:-}"); shift 2 ;;
      --no-restart) norestart=1; shift ;;
      *) die "未知参数 $1" ;;
    esac
  done
  [[ -n "${host}" && -n "${unit}" && -n "${file}" ]] || usage
  [[ -f "${file}" ]] || die "找不到文件 ${file}"
  [[ ${norestart} -eq 1 || ${#components[@]} -gt 0 ]] || die "必须用 --restart <组件id> 指明要重启的组件，或显式 --no-restart"
  [[ ${norestart} -eq 0 || ${#components[@]} -eq 0 ]] || die "--restart 与 --no-restart 不能同时使用"
  [[ -n "${name}" ]] || name="$(basename "${file}")"
  [[ "${name}" =~ ^moox-[a-z0-9-]+$ ]] || die "安装名 ${name} 必须形如 moox-xxx（用 --name 指定）"
  [[ -n "${label}" ]] || label="${name#moox-}"
  [[ "${label}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "备份标签 ${label} 不合法"
  if [[ ${#components[@]} -gt 0 ]]; then check_component_ids "${components[@]}"; fi
  local target link sha ts commit remote_tmp comps="" ready_env
  target="$(host_target "${host}")"
  link="$(unit_link "${unit}")"
  sha="$(sha_of "${file}")"
  ts="$(date +%Y%m%d%H%M%S)"
  commit="$(basename "$(dirname "${file}")")"
  [[ "${commit}" =~ ^[0-9a-f]{7,40}$ ]] || commit="unknown"
  remote_tmp="/tmp/moox-release-${ts}-${name}"
  if [[ ${#components[@]} -gt 0 ]]; then comps="${components[*]}"; fi
  ready_env="$(ready_env_assignments)"

  scp -O -q -C "${SSH_OPTS[@]}" "${file}" "${target}:${remote_tmp}"
  printf '%s\n' 'set -euo pipefail' "${REMOTE_RESTART_FUNCS}" "${REMOTE_INSTALL_BODY}" |
    ssh "${SSH_OPTS[@]}" "${target}" "DEPLOY_ROOT='${DEPLOY_ROOT}' UNIT='${unit}' LINK='${link}' NAME='${name}' SHA='${sha}' TS='${ts}' LABEL='${label}' COMMIT='${commit}' REMOTE_TMP='${remote_tmp}' COMPONENTS='${comps}' LEDGER='${DEPLOY_ROOT}/${LEDGER_REL}' ${ready_env} bash -s"
}

# ------------------------------------------------------------- rollback
cmd_rollback() {
  local host="" unit="" backup="" norestart=0 components=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --host) host="${2:-}"; shift 2 ;;
      --unit) unit="${2:-}"; shift 2 ;;
      --backup) backup="${2:-}"; shift 2 ;;
      --restart) components+=("${2:-}"); shift 2 ;;
      --no-restart) norestart=1; shift ;;
      *) die "未知参数 $1" ;;
    esac
  done
  [[ -n "${host}" && -n "${unit}" && -n "${backup}" ]] || usage
  [[ ${norestart} -eq 1 || ${#components[@]} -gt 0 ]] || die "必须用 --restart <组件id> 指明要重启的组件，或显式 --no-restart"
  [[ ${norestart} -eq 0 || ${#components[@]} -eq 0 ]] || die "--restart 与 --no-restart 不能同时使用"
  if [[ ${#components[@]} -gt 0 ]]; then check_component_ids "${components[@]}"; fi
  local target link comps="" parent ready_env
  ready_env="$(ready_env_assignments)"
  target="$(host_target "${host}")"
  link="$(unit_link "${unit}")"
  parent="$(dirname "${link}")"
  # 备份目录只能是这个单元下 install 生成的 binary-backup-*，不接受任意路径。
  [[ "${backup}" == "${parent}"/binary-backup-* && "${backup}" != *..* ]] || die "备份目录必须是 ${parent}/binary-backup-*"
  if [[ ${#components[@]} -gt 0 ]]; then comps="${components[*]}"; fi
  printf '%s\n' 'set -euo pipefail' "${REMOTE_RESTART_FUNCS}" "${REMOTE_ROLLBACK_BODY}" |
    ssh "${SSH_OPTS[@]}" "${target}" "UNIT='${unit}' LINK='${link}' BACKUP='${backup}' COMPONENTS='${comps}' LEDGER='${DEPLOY_ROOT}/${LEDGER_REL}' ${ready_env} bash -s"
}

# --------------------------------------------------------------- status
cmd_status() {
  local host=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --host) host="${2:-}"; shift 2 ;;
      *) die "未知参数 $1" ;;
    esac
  done
  [[ -n "${host}" ]] || usage
  local target
  target="$(host_target "${host}")"
  ssh "${SSH_OPTS[@]}" "${target}" "DEPLOY_ROOT='${DEPLOY_ROOT}' bash -s" <<'EOF'
set -euo pipefail
for link in "${DEPLOY_ROOT}"/*/current; do
  unit="$(basename "$(dirname "${link}")")"
  # 名字里带点的是切换前保留的旧目录（如 *.pre-cutover-*），不是当前单元。
  case "${unit}" in *.*) continue ;; esac
  [ -x "${link}/status.sh" ] || continue
  echo "== ${unit}"
  "${link}/status.sh" 2>/dev/null | tail -n 1 | python3 -c '
import json, sys
for c in json.loads(sys.stdin.read())["components"]:
    print("%-16s %-8s ready=%-5s sha=%s" % (c["id"], c["state"], c["ready"], c["binary_sha256"][:16]))
'
done
EOF
}

# --------------------------------------------------------------- ledger
cmd_ledger() {
  local host="" tail_n=20
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --host) host="${2:-}"; shift 2 ;;
      --tail) tail_n="${2:-}"; shift 2 ;;
      *) die "未知参数 $1" ;;
    esac
  done
  [[ -n "${host}" ]] || usage
  [[ "${tail_n}" =~ ^[0-9]+$ ]] || die "--tail 必须是数字"
  local target
  target="$(host_target "${host}")"
  ssh "${SSH_OPTS[@]}" "${target}" "tail -n ${tail_n} '${DEPLOY_ROOT}/${LEDGER_REL}' 2>/dev/null || echo '（还没有发布台账：只有用 native-release.sh install 装的二进制才会记录）'"
}

# ------------------------------------------------------ catalog-changed
cmd_catalog_changed() {
  local since="" host=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --since) since="${2:-}"; shift 2 ;;
      --host) host="${2:-}"; shift 2 ;;
      *) die "未知参数 $1" ;;
    esac
  done
  local repo
  repo="$(repo_root)"
  if [[ -z "${since}" && -n "${host}" ]]; then
    # 台账里最近一次装 moox-admin 的提交就是目录上次随之发布的版本。
    local target
    target="$(host_target "${host}")"
    since="$(ssh "${SSH_OPTS[@]}" "${target}" "awk '\$3==\"moox-admin\" {c=\$4} END {print c}' '${DEPLOY_ROOT}/${LEDGER_REL}' 2>/dev/null || true")"
    [[ -n "${since}" && "${since}" != "unknown" && "${since}" != rollback:* ]] || die "${host} 的发布台账里没有 moox-admin 的可用记录（没有记录、提交未知或最近一次是回滚），无法确定上次发布的提交；请用 --since 指定"
  fi
  [[ -n "${since}" ]] || usage
  [[ "${since}" =~ ^[0-9a-f]{7,40}$ ]] || die "--since 必须是提交 id"
  git -C "${repo}" cat-file -e "${since}^{commit}" 2>/dev/null || die "本地仓库里没有提交 ${since}"
  if git -C "${repo}" diff --quiet "${since}" HEAD -- packages/servicecatalog/catalog.yaml; then
    echo "服务目录自 ${since} 以来没有变化"
    return 0
  fi
  echo "服务目录自 ${since} 以来有变化：必须整体发布——三台主机的 host-gateway（server+cli）→ Admin → Monitor（Admin 与 Monitor 连着装）" >&2
  return 10
}

command="${1:-}"
[[ -n "${command}" ]] || usage
shift
case "${command}" in
  build) cmd_build "$@" ;;
  install) cmd_install "$@" ;;
  rollback) cmd_rollback "$@" ;;
  status) cmd_status "$@" ;;
  ledger) cmd_ledger "$@" ;;
  catalog-changed) cmd_catalog_changed "$@" ;;
  *) usage ;;
esac
