# 自定义算子（wasm UDF）

DSL 表达不了的**那一步算法**，用 Rust 写成自定义算子，编译成 wasm，发布后在因子表达式里调用：`udf.<名称>(<序列输入>..., <参数>...)`。
能用内置算子表达的部分写在 DSL 里，作为 UDF 的输入。源码放在用户**自己的私有 git 仓库**（不进 moox 仓库），系统只记录提交号。
全部命令在 `moox-cli udf` 下，不直接调接口。设计见 `docs/计划/因子自定义算子UDF设计.md`。

## 流程

```bash
moox-cli udf new <名称>               # 空目录生成工作区；已有工作区只添加 crate（名称 ^[a-z][a-z0-9_]{0,47}$）
# 编辑 udfs/<名称>/src/lib.rs、udf.toml（说明与示例）、tests/fixtures/*.json（对拍样本）
moox-cli udf build [<名称>]           # 源码扫描 → clippy(-D warnings) → 原生测试 → 编译 → 模块检查 → 一致性测试 → dist/<名称>-<哈希>.udfpkg
git add -A && git commit              # publish 要求工作区干净，且与包里记录的提交号一致
moox-cli udf publish dist/<名称>-<哈希>.udfpkg      # 服务端复核并重跑同一套测试、比对输出摘要；存为不可变版本（未启用）
moox-cli udf preview <名称>@<哈希> --spec <因子定义文件> --task <因子任务> [--subjects BTC,ETH --periods 20]
moox-cli udf activate <名称>@<哈希> --dry-run       # 影响清单与前提检查
```

`udf new` 生成的工作区自带 `AGENTS.md`（编写规范）、`clippy.toml`（禁用清单）、`rust-toolchain.toml`（锁定版本与 wasm 目标）。写 UDF 先读它。
没有发布 SDK 标签时用 `moox-cli udf new <名称> --sdk-path <moox 仓库>/sdk/rust` 指向本地 SDK。

## 编写规范摘要

- 用 `#[udf(name, inputs = [x], params = { n: int(2..=500), q: float(0.0..=1.0) }, lookback = "4*n", nan = "strict"|"relaxed", min_valid = N)]`
  标注一个函数 `fn f(x: &Window, p: &<驼峰名>Params) -> f64`；多个输入写 `inputs = [x, y]`，函数对应多个 `&Window`。一个 crate 一个 UDF。
- 回看公式只允许常量，或 `a*<整数参数>+b`（`4*n`、`n+1`）；参数取下限时窗口长度要 ≥ 1。
- **只依赖窗口**：不保留跨调用状态（禁用 `static` 可变状态、`Cell`、`Mutex`、原子类型、`thread_local!`）；只读 `Window` 里的数据，越界即 panic（失败）。
- **确定性**：arm64 与 amd64 上逐位相同。禁用融合乘加（`mul_add`）、`to_bits`/`from_bits`、`copysign`、`is_sign_negative`/`is_sign_positive`、时间、随机、线程、环境、
  `HashMap`/`HashSet` 的遍历（需要有序遍历用 `BTreeMap`）。没有 `unsafe`。
- **空值**：默认严格（窗口含 NaN 就输出 NaN，不调用你的函数）；`relaxed` 时各输入的 NaN 只能在窗口开头，函数收到共同有效段。输出有限值或 NaN，无法计算就返回 NaN。
- 优先写朴素循环，热路径不分配内存；需要与内置算子逐位一致的累加语义时才用 `moox_udf::exact`。
- fixtures 格式：`{"cases": [{"name", "params": [...], "inputs": [[...]], "expected": [...], "rel_tol": 0}]}`，NaN 写成 `null`，
  `inputs` 的长度 = `expected` 的长度 + 窗口长度 − 1，`rel_tol` 为 0 要求逐位一致。

## 切换启用版本（有引用方时）

切换在**因子引擎模块暂停期间**进行（新 UDF 的首次 `activate` 没有引用方，不需要暂停）：

1. `moox-cli lifecycle pause --endpoint <因子引擎健康检查地址>`，等本次暂停收敛（`GetStatus` 里引擎生命周期的 `quiesced` 为真）；
2. `moox-cli udf activate <名称>@<哈希> --dry-run`：看影响清单（因子定义 → 成员 → 因子任务 → 策略实例）、新旧 `definition_hash` 与回看、前提检查；
   有已领取的补算作业先 `CancelRecalcJob`；回看变长时按提示核对源数据保留期与引擎配置（`window.lookback`、`window.max_memory`、`memory.total_budget`、`compute.max_memory`）；
3. `moox-cli udf activate <名称>@<哈希>`；
4. `moox-cli lifecycle resume --endpoint <因子引擎健康检查地址>`：先追赶（同步目录、备齐模块、用新版本补算暂停期间的周期），全部追平才放行；
5. 引用它的因子定义 `definition_hash` 变了，关联的策略实例会进入 `factor_changed`，重新启用；需要更早的历史一致时显式补算。

回滚就是 `activate` 指回旧版本，步骤相同。`moox-cli udf show <名称>` 看各版本、引用关系与各引擎的模块状态（ready / fetching / fetch_failed / compile_failed）。
只暂停因子引擎模块，不要对 FactorMgr 做系统暂停（它会拒绝修改类接口，包括取消补算与切换）。

## 失败语义

UDF 失败（panic、超时、内存超限）只让这一次计算失败，本次运行的成员全部记失败，**不写 NaN**；引擎与其他任务不受影响。
在 `fill_nan`、`cs_rank` 里调用 UDF，失败同样整体失败，不会被吞成空值。
