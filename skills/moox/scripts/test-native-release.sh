#!/usr/bin/env bash
# native-release.sh 的契约测试：用替身 ssh/scp/rsync 把“远端脚本”放在本机的假部署根里真正执行，
# 覆盖构建前的护栏、安装/回滚的校验与台账、就绪与 sha 核对、服务目录变更检测。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/skills/moox/scripts/native-release.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/native-release-test.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

STUBS="${TMP}/stubs"
BUILDER_HOME="${TMP}/builder-home"
FAKE_ROOT="${TMP}/data-moox"
CALLS="${TMP}/calls.log"
mkdir -p "${STUBS}" "${BUILDER_HOME}/moox-build/bin" "${BUILDER_HOME}/moox-build/scripts/build"
: >"${CALLS}"

# --- 替身：ssh 把命令放在本机执行（构建机则切到它的假 HOME），scp/rsync 只做本地拷贝或记录。
cat >"${STUBS}/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
while [[ "${1:-}" == -o ]]; do shift 2; done
host="$1"; shift
printf 'ssh %s %s\n' "${host}" "$*" >>"${STUB_CALLS}"
if [[ "${host}" == "${STUB_BUILDER}" ]]; then
  cd "${STUB_BUILDER_HOME}"
  export HOME="${STUB_BUILDER_HOME}"
fi
exec bash -c "$*"
EOF
cat >"${STUBS}/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args=()
while [[ $# -gt 0 ]]; do
  case "$1" in -o) shift 2 ;; -O|-q|-C) shift ;; *) args+=("$1"); shift ;; esac
done
src="${args[0]}"; dst="${args[1]}"
printf 'scp %s %s\n' "${src}" "${dst}" >>"${STUB_CALLS}"
if [[ "${src}" == "${STUB_BUILDER}:"* ]]; then src="${STUB_BUILDER_HOME}/${src#*:}"; fi
if [[ "${dst}" == *:* ]]; then dst="${dst#*:}"; fi
cp "${src}" "${dst}"
if [[ -n "${STUB_SCP_CORRUPT:-}" ]]; then printf 'x' >>"${dst}"; fi
EOF
cat >"${STUBS}/rsync" <<'EOF'
#!/usr/bin/env bash
printf 'rsync %s\n' "$*" >>"${STUB_CALLS}"
EOF
# 本机可能没有 GNU 的 sha256sum，构建机脚本要用它。
if ! command -v sha256sum >/dev/null 2>&1; then
  printf '#!/usr/bin/env bash\nexec shasum -a 256 "$@"\n' >"${STUBS}/sha256sum"
fi
chmod +x "${STUBS}"/*

# --- 假构建机：build.sh 把目标名写进 bin/moox-<目标>，并记录 CGO 与 GOTMPDIR。
printf '1.26.9\n' >"${BUILDER_HOME}/moox-build/.go-version"
cat >"${BUILDER_HOME}/moox-build/scripts/build/build.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ -d "${GOTMPDIR}" ]] || { echo "go: creating work dir: ${GOTMPDIR} 不存在" >&2; exit 1; }
printf '%s cgo=%s version=%s tmp=%s\n' "$1" "${CGO_ENABLED}" "${VERSION}" "${GOTMPDIR##*/}" >>"${PWD}/build-env.log"
[[ -e "${PWD}/noop" ]] && exit 0
sleep 0.05
printf 'binary-of-%s-%s\n' "$1" "${VERSION}" >"${PWD}/bin/moox-$1"
EOF
chmod +x "${BUILDER_HOME}/moox-build/scripts/build/build.sh"
mkdir -p "${BUILDER_HOME}/moox-build/bin"
printf 'stale\n' >"${BUILDER_HOME}/moox-build/bin/moox-stale"

# --- 假仓库：有远端、有服务目录文件。
REPO="${TMP}/repo"
git init -q "${REPO}"
git -C "${REPO}" config user.email t@example.com
git -C "${REPO}" config user.name test
mkdir -p "${REPO}/packages/servicecatalog"
printf 'catalog: 1\n' >"${REPO}/packages/servicecatalog/catalog.yaml"
printf 'x\n' >"${REPO}/main.go"
git -C "${REPO}" add -A && git -C "${REPO}" commit -q -m first
git init -q --bare "${TMP}/origin.git"
git -C "${REPO}" remote add origin "${TMP}/origin.git"
git -C "${REPO}" push -q origin HEAD
FIRST="$(git -C "${REPO}" rev-parse --short=8 HEAD)"

# --- 假部署根：control 单元有 admin 和 monitor 两个组件。
REL="${FAKE_ROOT}/prod/releases/r1"
mkdir -p "${REL}/bin" "${FAKE_ROOT}/run"
ln -s releases/r1 "${FAKE_ROOT}/prod/current"
printf 'old-admin\n' >"${REL}/bin/moox-admin"
printf 'old-monitor\n' >"${REL}/bin/moox-monitor"
cat >"${REL}/restart.sh" <<'EOF'
#!/usr/bin/env bash
dir="$(cd "$(dirname "$0")" && pwd)"
[[ "$1" == "--components" ]] || exit 2
c="$2"
echo "restarting ${c}"
sha="$(shasum -a 256 "${dir}/bin/moox-${c}" | cut -d' ' -f1)"
[[ -e "${dir}/wrong-sha" ]] && sha="$(printf '0%.0s' $(seq 1 64))"
ready=true; [[ -e "${dir}/not-ready" ]] && ready=false
printf '{"host_id":"control","operation":"restart","components":[{"id":"%s","state":"running","ready":%s,"binary_sha256":"%s"}]}\n' "${c}" "${ready}" "${sha}"
EOF
cat >"${REL}/status.sh" <<'EOF'
#!/usr/bin/env bash
dir="$(cd "$(dirname "$0")" && pwd)"
ready=true; [[ -e "${dir}/not-ready" ]] && ready=false
for c in admin monitor; do
  sha="$(shasum -a 256 "${dir}/bin/moox-${c}" | cut -d' ' -f1)"
  items="${items:+${items},}{\"id\":\"${c}\",\"state\":\"running\",\"ready\":${ready},\"binary_sha256\":\"${sha}\"}"
done
printf '{"host_id":"control","operation":"status","components":[%s]}\n' "${items}"
EOF
chmod +x "${REL}/restart.sh" "${REL}/status.sh"

cat >"${TMP}/release.env" <<EOF
MOOX_RELEASE_HOST_CONTROL=ubuntu@control.example
MOOX_RELEASE_BUILDER=mooyang@builder.example
EOF

export PATH="${STUBS}:${PATH}"
export STUB_CALLS="${CALLS}" STUB_BUILDER="mooyang@builder.example" STUB_BUILDER_HOME="${BUILDER_HOME}"
export MOOX_RELEASE_ENV="${TMP}/release.env" MOOX_RELEASE_REPO="${REPO}" MOOX_RELEASE_ROOT="${FAKE_ROOT}"
export MOOX_RELEASE_OUT="${TMP}/out" MOOX_RELEASE_READY_ATTEMPTS=2 MOOX_RELEASE_READY_INTERVAL=0

run() { bash "${SCRIPT}" "$@"; }
expect_fail() {
  local message="$1"; shift
  : >"${CALLS}"
  if run "$@" >"${TMP}/out.txt" 2>&1; then fail "${message}：本应失败却成功了"; fi
}
no_remote_calls() { [[ ! -s "${CALLS}" ]] || fail "$1：不该联系任何主机，实际调用了：$(cat "${CALLS}")"; }

# ---------------------------------------------------------------- 参数与护栏
expect_fail "install 没指明重启" install --host control --unit prod --file "${TMP}/stubs/ssh" --name moox-admin
no_remote_calls "install 没指明重启"
expect_fail "unit 不合法" install --host control --unit "../prod" --file "${STUBS}/ssh" --name moox-admin --no-restart
no_remote_calls "unit 不合法"
expect_fail "安装名不合法" install --host control --unit prod --file "${STUBS}/ssh" --name "../evil" --no-restart
no_remote_calls "安装名不合法"
expect_fail "组件 id 不合法" install --host control --unit prod --file "${STUBS}/ssh" --name moox-admin --restart "a;b"
no_remote_calls "组件 id 不合法"
expect_fail "主机别名没配置" install --host unknown --unit prod --file "${STUBS}/ssh" --name moox-admin --no-restart
no_remote_calls "主机别名没配置"
grep -q "MOOX_RELEASE_HOST_UNKNOWN" "${TMP}/out.txt" || fail "没配置的主机别名要点名缺哪个变量"
printf 'MOOX_RELEASE_HOST_BAD=user:password@host\n' >>"${TMP}/release.env"
expect_fail "带密码的主机写法" install --host bad --unit prod --file "${STUBS}/ssh" --name moox-admin --no-restart
no_remote_calls "带密码的主机写法"

# ---------------------------------------------------------------- build 的护栏
printf 'dirty\n' >>"${REPO}/main.go"
expect_fail "工作区有未提交改动" build admin
no_remote_calls "工作区有未提交改动"
git -C "${REPO}" checkout -q main.go
printf 'local-only\n' >>"${REPO}/main.go"
git -C "${REPO}" commit -q -am local-only
expect_fail "提交还没推送" build admin
no_remote_calls "提交还没推送"
grep -q "还没有推送" "${TMP}/out.txt" || fail "要说明提交还没推送"
git -C "${REPO}" push -q origin HEAD
HEAD1="$(git -C "${REPO}" rev-parse --short=8 HEAD)"

# ---------------------------------------------------------------- build 成功
: >"${CALLS}"
run build admin storage-primary >"${TMP}/out.txt" 2>&1 || { cat "${TMP}/out.txt" >&2; fail "build 应成功"; }
grep -q -- '--delete' "${CALLS}" || fail "rsync 要带 --delete"
for excluded in .git moox.toml bin .gotmp node_modules; do
  grep -q -- "--exclude ${excluded} " "${CALLS}" || fail "rsync 必须排除 ${excluded}（.gotmp 被删会让 go 沿用旧产物，moox.toml 不能上传）"
done
grep -q '^admin cgo=0 version='"${HEAD1}"' tmp=.gotmp' "${BUILDER_HOME}/moox-build/build-env.log" || fail "admin 应 CGO=0、VERSION 为提交、GOTMPDIR 在 .gotmp"
grep -q '^storage-primary cgo=1 ' "${BUILDER_HOME}/moox-build/build-env.log" || fail "storage-primary 要 CGO=1（DuckDB）"
for name in moox-admin moox-storage-primary; do
  [[ -f "${MOOX_RELEASE_OUT}/${HEAD1}/${name}" ]] || fail "没有取回 ${name}"
  grep -q "^BUILT ${name} $(sha "${MOOX_RELEASE_OUT}/${HEAD1}/${name}") " "${TMP}/out.txt" || fail "BUILT 行要带正确的 sha：${name}"
done
[[ ! -e "${MOOX_RELEASE_OUT}/${HEAD1}/moox-stale" ]] || fail "构建机上旧的产物不能被当成这次的"

# 取回的文件和构建机上的 sha 对不上（传输损坏）必须报错，不能把坏文件当成构建产物。
STUB_SCP_CORRUPT=1 expect_fail "取回的二进制损坏" build admin
grep -q "取回后的 sha 与构建机上的不一致" "${TMP}/out.txt" || fail "要说明取回后的 sha 不一致"

# 构建没有产出新文件（例如 .gotmp 缺失导致 go 失败而沿用旧产物）必须报错。
touch "${BUILDER_HOME}/moox-build/noop"
expect_fail "构建没有新产物" build admin
grep -q "没有产出新的二进制" "${TMP}/out.txt" || fail "要说明构建没有产出新的二进制"
rm -f "${BUILDER_HOME}/moox-build/noop"
expect_fail "构建目标不合法" build "admin;rm"
no_remote_calls "构建目标不合法"

# ---------------------------------------------------------------- install 成功
NEW="${MOOX_RELEASE_OUT}/${HEAD1}/moox-admin"
: >"${CALLS}"
run install --host control --unit prod --file "${NEW}" --restart admin >"${TMP}/out.txt" 2>&1 || { cat "${TMP}/out.txt" >&2; fail "install 应成功"; }
[[ "$(cat "${REL}/bin/moox-admin")" == "$(cat "${NEW}")" ]] || fail "二进制没有被替换"
BACKUP="$(sed -n 's/^BACKUP://p' "${TMP}/out.txt")"
[[ "${BACKUP}" == "${FAKE_ROOT}/prod/binary-backup-admin-"* ]] || fail "备份目录位置不对：${BACKUP}"
[[ "$(cat "${BACKUP}/moox-admin")" == "old-admin" ]] || fail "备份里应是旧二进制"
grep -q '^RESULT:admin:running:ready=True:sha='"$(sha "${NEW}" | cut -c1-16)" "${TMP}/out.txt" || fail "要打印重启后的状态与 sha"
grep -q '^READY:admin' "${TMP}/out.txt" || fail "要等到就绪"
grep -q " prod moox-admin ${HEAD1} $(sha "${NEW}") admin\$" "${FAKE_ROOT}/run/release-ledger.log" || fail "发布台账里要有提交与 sha"
[[ ! -e "${REL}/bin/.moox-admin.new" ]] || fail "不能留下 .new 临时文件"

# --no-restart（装 cli 之类没有组件的二进制）
printf 'new-cli\n' >"${TMP}/moox-admin-cli"
run install --host control --unit prod --file "${TMP}/moox-admin-cli" --no-restart >"${TMP}/out.txt" 2>&1 || fail "--no-restart 应成功"
grep -q RESTART "${TMP}/out.txt" && fail "--no-restart 不该重启" || true
[[ "$(cat "${REL}/bin/moox-admin-cli")" == "new-cli" ]] || fail "cli 没装上"

# ---------------------------------------------------------------- install 失败
cp "${REL}/bin/moox-admin" "${TMP}/admin-before"
printf 'candidate\n' >"${TMP}/candidate"
STUB_SCP_CORRUPT=1 expect_fail "上传损坏" install --host control --unit prod --file "${TMP}/candidate" --name moox-admin --restart admin
grep -q "sha 与本地不一致" "${TMP}/out.txt" || fail "上传损坏要说明 sha 不一致"
cmp -s "${REL}/bin/moox-admin" "${TMP}/admin-before" || fail "上传损坏时不能动线上二进制"

touch "${REL}/wrong-sha"
expect_fail "重启后 sha 不一致" install --host control --unit prod --file "${TMP}/candidate" --name moox-admin --restart admin
grep -q "sha 与刚安装的不一致" "${TMP}/out.txt" || fail "要说明重启后的 sha 不一致"
rm -f "${REL}/wrong-sha"

touch "${REL}/not-ready"
expect_fail "重启后不就绪" install --host control --unit prod --file "${TMP}/candidate" --name moox-admin --restart admin
grep -q "仍没有就绪" "${TMP}/out.txt" || fail "要说明组件没有就绪"
rm -f "${REL}/not-ready"

expect_fail "重启结果里没有这个组件" install --host control --unit prod --file "${TMP}/candidate" --name moox-admin --restart nonexistent
grep -q "不一致\|没有组件" "${TMP}/out.txt" || fail "组件对不上刚装的二进制要报错"

# 同一秒内连续安装不能写进同一个备份目录（否则更早备份的旧二进制会被覆盖）。
BACKUP_COUNT="$(ls -d "${FAKE_ROOT}"/prod/binary-backup-admin-* | wc -l | tr -d ' ')"
[[ "${BACKUP_COUNT}" -ge 5 ]] || fail "每次安装都应有自己的备份目录，实际只有 ${BACKUP_COUNT} 个"
[[ "$(cat "${BACKUP}/moox-admin")" == "old-admin" ]] || fail "第一次安装的备份被后面的安装覆盖了"

# 成功与失败路径都不能在远端 /tmp 留下上传的二进制。
if compgen -G "/tmp/moox-release-*-moox-*" >/dev/null; then fail "远端临时文件没有清理：$(ls /tmp/moox-release-*-moox-* | head -3)"; fi

# ---------------------------------------------------------------- rollback
run rollback --host control --unit prod --backup "${BACKUP}" --restart admin >"${TMP}/out.txt" 2>&1 || { cat "${TMP}/out.txt" >&2; fail "rollback 应成功"; }
[[ "$(cat "${REL}/bin/moox-admin")" == "old-admin" ]] || fail "回滚后应是旧二进制"
grep -q '^RESTORED:moox-admin:' "${TMP}/out.txt" || fail "要打印恢复了哪些文件"
grep -q "rollback:$(basename "${BACKUP}")" "${FAKE_ROOT}/run/release-ledger.log" || fail "回滚要写台账"
expect_fail "备份目录越界" rollback --host control --unit prod --backup "/etc" --no-restart
no_remote_calls "备份目录越界"
expect_fail "备份目录含 .." rollback --host control --unit prod --backup "${FAKE_ROOT}/prod/binary-backup-x/../../etc" --no-restart
no_remote_calls "备份目录含 .."

# ---------------------------------------------------------------- status / ledger
run status --host control >"${TMP}/out.txt" 2>&1 || fail "status 应成功"
grep -q "== prod" "${TMP}/out.txt" && grep -q "^admin .*ready=True" "${TMP}/out.txt" || fail "status 要列出组件状态"
run ledger --host control --tail 3 >"${TMP}/out.txt" 2>&1 || fail "ledger 应成功"
grep -q "moox-admin" "${TMP}/out.txt" || fail "ledger 要列出发布记录"

# ---------------------------------------------------------------- catalog-changed
git -C "${REPO}" rev-parse HEAD >/dev/null
printf 'catalog: 2\n' >"${REPO}/packages/servicecatalog/catalog.yaml"
git -C "${REPO}" commit -q -am "change catalog"
HEAD2="$(git -C "${REPO}" rev-parse --short=8 HEAD)"
set +e; run catalog-changed --since "${HEAD1}" >"${TMP}/out.txt" 2>&1; code=$?; set -e
[[ ${code} -eq 10 ]] || fail "目录变化时退出码应为 10，实际 ${code}"
grep -q "必须整体发布" "${TMP}/out.txt" || fail "要提示必须整体发布"
run catalog-changed --since "${HEAD2}" >"${TMP}/out.txt" 2>&1 || fail "目录没变化时应成功"
# 用台账里最近一次 moox-admin 的提交：台账里记的是 HEAD1 → 目录已变化。
printf '2026-01-01T00:00:00Z prod moox-admin %s deadbeef admin\n' "${HEAD1}" >>"${FAKE_ROOT}/run/release-ledger.log"
set +e; run catalog-changed --host control >"${TMP}/out.txt" 2>&1; code=$?; set -e
[[ ${code} -eq 10 ]] || fail "按台账判断目录变化时退出码应为 10，实际 ${code}"
# 最近一次是回滚时无法判断，必须让调用方显式给出。
printf '2026-01-02T00:00:00Z prod moox-admin rollback:binary-backup-admin-1 deadbeef admin\n' >>"${FAKE_ROOT}/run/release-ledger.log"
expect_fail "最近一次是回滚" catalog-changed --host control
grep -q "回滚" "${TMP}/out.txt" || fail "回滚后的台账不能用来判断目录"

echo "native-release contract ok"
