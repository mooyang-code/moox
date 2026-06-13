## Why

moox 要成为个人量化数据存储的唯一大仓，需要先统一代码组织、模块边界、构建发布和 Agent 技能分发方式。当前多个仓库和多个 Go module 已经存在，第一阶段应降低迁移风险，用 `go.work + 多 go.mod` 建立可持续演进的 monorepo 基座。

## What Changes

- **BREAKING**: 代码组织从多个独立仓库迁移到 `moox/modules/` 下的模块化大仓结构。
- 新增根 `go.work`，第一阶段保留多 `go.mod`，每个模块保持独立编译和测试能力。
- 新增 `modules/control`、`modules/cli`、`modules/storage`、`modules/collector`、`modules/factor`、`modules/order`、`modules/account` 的目标结构。
- 将 CLI 入口命名从泛化的 `cmd/moox` 收窄为 `cmd/moox-cli`，所有二进制统一使用 `moox-*` 前缀。
- 将代码中的存储抽象目录命名统一为 `internal/storage`，避免 `internal/data` 与业务数据、运行时数据混淆。
- 新增根 `skills/moox`，用于沉淀 moox 专用 Agent 技能和 references。
- 新增根 `build/`，集中管理编译、测试、发布和技能打包脚本。
- 新增大仓架构文档 `docs/monorepo-architecture.md`。

## Capabilities

### New Capabilities

- `monorepo-workspace`: 定义 moox 第一阶段采用 `modules/ + go.work + 多 go.mod` 的 workspace 大仓能力。
- `module-layout`: 定义控制面、CLI、存储、采集、因子、订单、账户等模块的目录和命名约束。
- `build-and-skill-packaging`: 定义根 `build/` 构建发布入口和 `skills/moox` Agent 技能打包能力。

### Modified Capabilities

- None.

## Impact

- 影响仓库目录结构、Go module 路径、命令入口、构建脚本、发布产物和 Agent 技能分发方式。
- 第一阶段不改变业务协议语义，不强行合并 Go module，不改变存储引擎职责。
- 后续实现会涉及 `moox`、`xData-mini`、`data-collector`、`factor-calculator`、`order-center`、`account-center` 的代码迁移和 import 路径调整，并会把 `data-miner` 中有价值的能力并入 `modules/collector`。
