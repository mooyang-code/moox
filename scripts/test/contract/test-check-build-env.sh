#!/usr/bin/env bash
# check-build-env.sh 的契约测试：用假的 PATH 构造“什么都没有”“工具齐全”“版本不符”“Docker 没运行”等情形，
# 检查各场景的要求、退出码、JSON 输出与各系统的安装建议。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/scripts/build/check-build-env.sh"
TMP="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/moox-check-build-env.XXXXXX")" && pwd)"
trap 'rm -rf "${TMP}"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

GO_WANT="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
RUST_WANT="$(sed -n 's/^channel *= *"\(.*\)".*/\1/p' "${ROOT}/sdk/rust/rust-toolchain.toml")"
BASH_BIN="$(command -v bash)"
PY="$(command -v python3)"

# --- 假的 HOME 与工具目录。PATH 只含基础命令和这里造的工具，保证“缺什么”可控。
mkdir -p "${TMP}/home" "${TMP}/base" "${TMP}/tools"
for name in uname grep tr cat head sed awk dirname mkdir rm cut mktemp date wc sort basename pwd env; do
  src="$(command -v "${name}" 2>/dev/null || true)"
  [[ -n "${src}" && -x "${src}" ]] && ln -sf "${src}" "${TMP}/base/${name}"
done
ln -sf "${PY}" "${TMP}/base/python3"
ln -sf "${BASH_BIN}" "${TMP}/base/bash"   # 假工具的 #!/usr/bin/env bash 要用

make_tool() { # 名称 版本输出（打印到 stdout），退出码 0
  printf '#!/usr/bin/env bash\nprintf "%%s\\n" "%s"\n' "$2" >"${TMP}/tools/$1"
  chmod +x "${TMP}/tools/$1"
}
make_go() { # 版本，如 go1.27.2；GOTOOLCHAIN=local go env GOVERSION 应输出它
  mkdir -p "${TMP}/home/.local/go${GO_WANT}/bin"
  cat >"${TMP}/home/.local/go${GO_WANT}/bin/go" <<EOF
#!/usr/bin/env bash
[[ "\$1 \$2" == "env GOVERSION" ]] && { echo "$1"; exit 0; }
exit 0
EOF
  chmod +x "${TMP}/home/.local/go${GO_WANT}/bin/go"
}
run_check() { # 参数传给脚本；输出到 ${TMP}/out.txt，退出码返回
  set +e
  env -i HOME="${TMP}/home" PATH="${TMP}/tools:${TMP}/base" "${@:2}" "${BASH_BIN}" "${SCRIPT}" $1 >"${TMP}/out.txt" 2>"${TMP}/err.txt"
  local code=$?
  set -e
  return ${code}
}

# ---------------------------------------------------------------- 什么都没有：build 场景缺 git 和 Go，并给出安装建议
if run_check "--profile build" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get; then fail "什么都没有时应返回非 0"; fi
grep -q '\[缺少\] *Git' "${TMP}/out.txt" || fail "要报缺少 Git：$(cat "${TMP}/out.txt")"
grep -q 'sudo apt-get install -y git' "${TMP}/out.txt" || fail "Linux/apt 下 git 的安装建议不对"
grep -q "\[缺少\] *Go ${GO_WANT}" "${TMP}/out.txt" || fail "要报缺少固定版本的 Go"
grep -q 'bash scripts/build/install-go.sh' "${TMP}/out.txt" || fail "Go 的安装建议要指向 install-go.sh"

# ---------------------------------------------------------------- build 场景工具齐全
make_tool git "git version 2.45.0"; make_tool sha256sum "x"
make_go "go${GO_WANT}"
run_check "--profile build" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get || fail "build 场景齐全时应通过：$(cat "${TMP}/out.txt")"
grep -q '环境满足' "${TMP}/out.txt" || fail "要说明环境满足"
# build 场景不要求 Docker / Node。
! grep -q 'Docker\|Node' "${TMP}/out.txt" || fail "build 场景不该检查 Docker 或 Node"

# Go 版本不符：必须判为不符并给出安装建议（构建脚本要求与 .go-version 完全一致）。
make_go "go1.25.0"
if run_check "--profile build" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get; then fail "Go 版本不符应失败"; fi
grep -q "\[不符\] *Go ${GO_WANT}" "${TMP}/out.txt" || fail "要报 Go 版本不符：$(cat "${TMP}/out.txt")"
grep -q 'go1.25.0' "${TMP}/out.txt" || fail "要说出发现的版本"
make_go "go${GO_WANT}"

# ---------------------------------------------------------------- cgo 场景：Docker 缺失、没运行、内存不足、正常
if run_check "--profile cgo" MOOX_BUILD_ENV_OS=macos MOOX_BUILD_ENV_PM=brew; then fail "没有 Docker 应失败"; fi
grep -q 'brew install colima docker' "${TMP}/out.txt" || fail "macOS 下 Docker 的建议应是 colima"

cat >"${TMP}/tools/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  --version) echo "Docker version 29.0.0, build abc" ;;
  info)
    [[ -e "${DOCKER_DOWN:-/nonexistent}" ]] && exit 1
    if [[ "${2:-}" == "--format" ]]; then echo "${DOCKER_MEM:-8589934592}"; fi ;;
esac
EOF
chmod +x "${TMP}/tools/docker"
touch "${TMP}/docker-down"
if run_check "--profile cgo" MOOX_BUILD_ENV_OS=macos MOOX_BUILD_ENV_PM=brew DOCKER_DOWN="${TMP}/docker-down"; then fail "Docker 没运行应失败"; fi
grep -q '\[未运行\] *Docker' "${TMP}/out.txt" || fail "要报 Docker 未运行"
grep -q 'colima start' "${TMP}/out.txt" || fail "要提示启动 colima"
if run_check "--profile cgo" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get DOCKER_MEM=1073741824; then fail "Docker 内存不足应失败"; fi
grep -q '可用内存 1 GB' "${TMP}/out.txt" || fail "要说出 Docker 可用内存"
run_check "--profile cgo" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get || fail "Docker 正常时 cgo 场景应通过：$(cat "${TMP}/out.txt")"

# ---------------------------------------------------------------- web 场景：Node 版本范围
make_tool pnpm "10.28.2"
cat >"${TMP}/tools/node" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == "-p" ]] && { echo "${NODE_FAKE_VERSION:-24.22.0}"; exit 0; }
echo "v${NODE_FAKE_VERSION:-24.22.0}"
EOF
chmod +x "${TMP}/tools/node"
for ok in 24.22.0 22.13.0 22.20.1 20.19.0 20.20.0; do
  run_check "--profile web" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get NODE_FAKE_VERSION="${ok}" || fail "Node ${ok} 应通过：$(cat "${TMP}/out.txt")"
done
for bad in 22.11.0 22.12.9 20.18.0 18.20.4 21.7.0; do
  if run_check "--profile web" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get NODE_FAKE_VERSION="${bad}"; then fail "Node ${bad} 应判为不符"; fi
  grep -q "\[不符\] *Node.js" "${TMP}/out.txt" || fail "Node ${bad} 要报不符"
done

# ---------------------------------------------------------------- verify 场景：make / rg / jq / rsync / Rust
if run_check "--profile verify" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=dnf; then fail "缺 make、rg 等应失败"; fi
for tool in "make" "ripgrep" "jq" "rsync" "Rust ${RUST_WANT}"; do grep -q "\[缺少\] *${tool}" "${TMP}/out.txt" || fail "verify 场景要报缺少 ${tool}"; done
grep -q 'sudo dnf install -y ripgrep' "${TMP}/out.txt" || fail "dnf 下 ripgrep 的建议不对"
grep -q "rustup toolchain install ${RUST_WANT} .*--target wasm32-unknown-unknown" "${TMP}/out.txt" || fail "Rust 的安装建议要给出固定版本和 wasm 目标"

# Rust：不经 rustup 时看 rustc 的版本与 wasm 目标；经 rustup 时看固定版本的工具链装没装。
make_rustc() { # rustc --version 报的版本；sysroot 固定在 ${TMP}/sysroot
  cat >"${TMP}/tools/rustc" <<EOF
#!/usr/bin/env bash
[[ "\$1" == "--version" ]] && { echo "rustc $1 (abcdef 2026-01-01)"; exit 0; }
[[ "\$1 \$2" == "--print sysroot" ]] && { echo "${TMP}/sysroot"; exit 0; }
exit 0
EOF
  chmod +x "${TMP}/tools/rustc"
}
rust_line() { grep "Rust ${RUST_WANT}" "${TMP}/out.txt" || true; }
make_tool cargo "cargo ${RUST_WANT}"
make_rustc "1.80.0"
run_check "--profile verify" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=dnf || true
rust_line | grep -q '\[不符\].*rustc 1.80.0' || fail "Rust 版本不符要报不符并说出发现的版本：$(cat "${TMP}/out.txt")"
make_rustc "${RUST_WANT}"
run_check "--profile verify" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=dnf || true
rust_line | grep -q "\[不符\].*没有 wasm32-unknown-unknown 目标" || fail "缺 wasm 目标要报不符：$(cat "${TMP}/out.txt")"
mkdir -p "${TMP}/sysroot/lib/rustlib/wasm32-unknown-unknown"
run_check "--profile verify" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=dnf || true
rust_line | grep -q "\[通过\].*rustc ${RUST_WANT}" || fail "Rust 齐全时应通过：$(cat "${TMP}/out.txt")"
# web 场景不检查 Rust。
run_check "--profile web" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=apt-get || fail "web 场景应通过：$(cat "${TMP}/out.txt")"
! grep -q 'Rust' "${TMP}/out.txt" || fail "web 场景不该检查 Rust"

cat >"${TMP}/tools/rustup" <<'EOF'
#!/usr/bin/env bash
case "$1 $2" in
  "toolchain list") echo "stable-x86_64-unknown-linux-gnu (default)"; [[ -z "${RUSTUP_FAKE_PINNED:-}" ]] || echo "${RUSTUP_FAKE_PINNED}-x86_64-unknown-linux-gnu" ;;
  "run ${RUSTUP_FAKE_PINNED:-none}") shift 2; exec "$@" ;;
  *) exit 1 ;;
esac
EOF
chmod +x "${TMP}/tools/rustup"
run_check "--profile verify" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=dnf || true
rust_line | grep -q "\[不符\].*rustup 里没有工具链 ${RUST_WANT}" || fail "rustup 没装固定版本要报不符：$(cat "${TMP}/out.txt")"
run_check "--profile verify" MOOX_BUILD_ENV_OS=linux MOOX_BUILD_ENV_PM=dnf RUSTUP_FAKE_PINNED="${RUST_WANT}" || true
rust_line | grep -q "\[通过\].*rustc ${RUST_WANT}" || fail "rustup 装了固定版本时应通过：$(cat "${TMP}/out.txt")"
rm -f "${TMP}/tools/rustup"

# ---------------------------------------------------------------- JSON 输出
if run_check "--profile verify --json" MOOX_BUILD_ENV_OS=windows MOOX_BUILD_ENV_PM=winget; then fail "JSON 模式缺项时也应返回非 0"; fi
"${PY}" - "${TMP}/out.txt" <<'PY' || fail "JSON 输出不合法或字段缺失"
import json, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
assert data["os"] == "windows" and data["package_manager"] == "winget" and data["profile"] == "verify" and data["ok"] is False
ids = {c["id"]: c for c in data["checks"]}
assert ids["git"]["status"] == "ok" and ids["go"]["status"] == "ok"
assert ids["rg"]["status"] == "missing" and "winget install" in ids["rg"]["fix"]
assert ids["rust"]["status"] == "ok"
for c in data["checks"]:
    assert set(c) == {"id", "label", "status", "found", "need", "fix"}
PY

# ---------------------------------------------------------------- 参数
if run_check "--profile bogus"; then fail "未知场景应返回非 0"; fi
[[ "$?" -ne 0 ]] || true
if run_check "--bogus"; then fail "未知参数应返回非 0"; fi

echo 'check-build-env contract passed'
