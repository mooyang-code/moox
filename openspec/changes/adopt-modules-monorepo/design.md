## Context

moox 当前包含 `cli`、`server`、`web-host` 等独立 Go module，同时目标系统还需要吸收 `xData-mini`、`data-collector`、`factor-calculator`、`order-center` 和 `account-center`。这些项目依赖差异较大，其中 `xData-mini/storage` 包含 Pebble、DuckDB 等 CGO 依赖，直接合成单一 `go.mod` 会显著增加第一阶段迁移风险。

第一阶段设计目标是建立大仓骨架和迁移秩序，让后续模块可以逐步迁入、独立测试、统一构建，并为 Agent 使用提供稳定技能入口。

## Goals / Non-Goals

**Goals:**

- 建立 `modules/` 作为业务模块统一承载目录。
- 建立根 `go.work`，第一阶段接受多 `go.mod`。
- 统一 Go 入口目录和二进制命名，所有产物使用 `moox-*` 前缀。
- 使用 `internal/storage` 表达存储抽象，避免 `internal/data` 概念混淆。
- 建立根 `skills/moox`，沉淀 moox 专用 Agent 技能。
- 建立根 `scripts/`，集中管理测试、构建、发布和技能打包脚本。

**Non-Goals:**

- 第一阶段不强行合并为单 `go.mod`。
- 第一阶段不重写 PB 协议语义。
- 第一阶段不改变 Pebble、DuckDB、Bleve、CSV 等存储组件职责。
- 第一阶段不合并控制面、存储面、采集面、交易面进程。
- 第一阶段不做大规模业务逻辑重构。

## Decisions

### Decision: 使用 `modules/` 而不是根目录平铺

业务代码统一放在 `modules/` 下，根目录保留跨模块资产，例如 `docs/`、`openspec/`、`skills/`、`scripts/`、`configs/`、`schema/` 和 `deployments/`。

替代方案是把 `storage/`、`collector/` 等目录直接放在根目录。该方案短期更短，但随着模块增多，根目录会混杂业务代码、构建脚本、文档和部署资产，不利于长期维护。

### Decision: 第一阶段使用 `go.work + 多 go.mod`

每个模块继续保留独立 `go.mod`，根 `go.work` 负责本地联调。这样可以减少跨模块依赖冲突，保留模块独立测试能力，也便于逐个迁移。

替代方案是立即合并为单 `go.mod`。该方案能统一依赖，但会把 CGO、不同 Go toolchain、生成代码和历史 replace 问题集中爆发，不适合第一阶段。

### Decision: 二进制入口统一使用 `cmd/moox-*`

每个模块的入口目录以最终二进制名命名，例如 `cmd/moox-cli`、`cmd/moox-server`、`cmd/moox-storage`。这比 `cmd/moox`、`cmd/server`、`cmd/cli` 混用更清晰，也方便构建脚本按名称发现产物。

### Decision: 代码存储抽象命名为 `internal/storage`

`internal/data` 容易与业务数据、样例数据和运行时数据混淆。存储抽象、存储路由、存储引擎封装统一进入 `internal/storage`；运行时数据统一进入根 `var/`，测试样例数据使用 Go 约定的 `testdata/`。

### Decision: Agent 技能与构建脚本进入根级目录

`skills/moox` 是面向 Agent 的能力入口，`scripts/` 是面向工程构建和发布的脚本入口。二者放在根目录可以服务整个大仓，而不是绑定某个模块。

## Risks / Trade-offs

- 多 `go.mod` 会保留一段时间的依赖版本差异 -> 通过 `go.work`、根 `make test` 和逐模块测试清单管理一致性。
- 模块迁移会引入 import path 改动 -> 按模块迁移，迁移一个模块后立即运行该模块测试和跨模块引用扫描。
- `xData-mini/storage` 的 CGO 依赖可能影响根构建 -> 根构建脚本必须支持模块级构建策略，storage 使用自己的 `make test` 或专用 CGO 参数。
- CLI 合并可能造成命令重复 -> 第一阶段保留有价值命令，迁移后废弃 `xData-mini/cli` 的重复入口。
- 技能包和 CLI 发布产物耦合 -> `scripts/package-skill.sh` 必须显式打包 `skills/moox` 和全平台 `moox-cli`，避免隐式依赖构建副产物。

## Migration Plan

1. 新增根骨架：`go.work`、`modules/`、`scripts/`、`skills/moox`、`configs/`、`deployments/`、`var/`。
2. 迁移 `moox/server` 到 `modules/control`，入口改为 `modules/control/cmd/moox-server`。
3. 迁移 `moox/cli` 到 `modules/cli`，入口改为 `modules/cli/cmd/moox-cli`。
4. 迁移 `xData-mini/storage` 到 `modules/storage`，入口改为 `modules/storage/cmd/moox-storage`。
5. 将 `xData-mini/cli` 中仍有价值的命令迁入 `modules/cli`，避免长期保留两个 CLI。
6. 迁移 `data-collector` 到 `modules/collector`。
7. 将 `data-miner` 中有价值的交易所连接、调度限频、标的发现能力并入 `modules/collector` 内部包。
8. 为 `modules/factor`、`modules/order`、`modules/account` 建立骨架，再按代码成熟度迁入。
9. 建立根 `Makefile` 和 `scripts/` 脚本，统一测试、构建、发布和技能打包入口。
10. 建立 `skills/moox` 并提供 build、storage、protocol、release references。

回滚策略：每一步迁移都以单模块为单位提交。若某模块迁移失败，回退该模块目录迁移和 `go.work` 中对应条目，不影响其他模块。

## Open Questions

- `factor-calculator`、`order-center`、`account-center` 当前代码成熟度不同，第一阶段先建立骨架，具体代码迁入范围在执行时按实际内容确认。
- Web 管理台是否长期保持根 `web/`，还是进入 `modules/web`，后续可在前端工程重构时单独决策。
