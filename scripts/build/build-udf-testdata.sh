#!/usr/bin/env bash
# 重新构建 SDK 示例与测试样本 wasm，复制到 packages/factorudf/testdata。
# 路径前缀被重映射，产物与构建目录、用户目录无关：任何机器上构建出的字节相同，CI 可以用 git diff 检查产物与源码一致。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SDK="${ROOT}/sdk/rust"
OUT="${ROOT}/packages/factorudf/testdata"
cd "${SDK}"

sysroot="$(rustc --print sysroot)"
export RUSTFLAGS="--remap-path-prefix=${SDK}=/moox-udf-sdk --remap-path-prefix=${CARGO_HOME:-${HOME}/.cargo}=/cargo --remap-path-prefix=${sysroot}=/rustc"

# 名称:crate
modules=(
	ts_mean:udf_ts_mean
	ema_trunc:udf_ema_trunc
	pair_relaxed:udf_pair_relaxed
	faulty:udf_faulty
	bad_state:udf_bad_state
	bad_past:udf_bad_past
	bad_future:udf_bad_future
	bad_cache:udf_bad_cache
)
packages=()
for entry in "${modules[@]}"; do
	packages+=(-p "${entry#*:}")
done
cargo build --release --locked --target wasm32-unknown-unknown "${packages[@]}"
mkdir -p "${OUT}"
for entry in "${modules[@]}"; do
	cp "target/wasm32-unknown-unknown/release/${entry#*:}.wasm" "${OUT}/${entry%%:*}.wasm"
done
