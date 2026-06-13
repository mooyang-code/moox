# moox 大仓架构与第一阶段迁移方案

本文记录 moox 作为量化数据存储统一解决方案的大仓结构设计。本文只描述代码组织、模块边界、构建发布和迁移顺序，不涉及业务代码实现。

## 目标

moox 将作为唯一大仓，逐步吸收 `data-collector`、`account-center`、`factor-calculator`、`order-center` 和 `xData-mini`，并将 `data-miner` 中有价值的交易所连接、调度、限频、标的发现能力合并进 collector。

第一阶段目标不是立刻统一所有代码和依赖，而是先建立稳定的大仓骨架：

- 使用 `modules/` 承载各业务模块。
- 接受 `go.work + 多 go.mod` 的 workspace 模式。
- 统一命令入口命名，所有二进制使用 `moox-*` 前缀。
- 将原 `internal/data` 类命名收窄为 `internal/storage`。
- 新增 `skills/` 存放 moox 相关 Agent 技能。
- 新增 `scripts/` 存放编译、发布、技能打包脚本。

## 第一阶段技术决策

### 接受 go.work + 多 go.mod

第一阶段采用 `go.work` 管理多个 Go module，而不是直接合并成单个 `go.mod`。

原因：

- 当前 `moox/cli`、`moox/server`、`xData-mini/storage`、`xData-mini/cli`、`data-collector`、`data-miner` 已经是独立 Go module；其中 `data-miner` 不再作为一级模块迁移，而是作为 collector 的内部能力来源。
- 各模块依赖差异很大，尤其 `xData-mini/storage` 包含 Pebble、DuckDB 等 CGO 依赖。
- 使用多 `go.mod` 可以降低迁移风险，让每个模块保持可独立编译和测试。
- `go.work` 可以在本地开发时消除大量临时 `replace`，同时保留模块边界。

后续当公共协议、公共工具和依赖版本稳定后，再评估是否收敛为更少的 Go module。

### 使用 modules 目录

第一阶段统一使用 `modules/`，不在仓库根目录平铺业务模块。

推荐结构：

```text
moox/
├── go.work
├── docs/
├── openspec/
├── skills/
│   └── moox/
├── scripts/
│   └── node_exporter/
├── modules/
│   ├── control/
│   ├── cli/
│   ├── storage/
│   ├── collector/
│   ├── factor/
│   ├── order/
│   └── account/
├── web/
├── configs/
├── schema/
├── deployments/
└── var/
```

`modules/` 的好处是边界清楚：根目录放跨模块资产，业务代码统一进入 modules。

### cmd 命名

`cmd` 下不使用泛化的 `moox` 目录，统一使用明确的二进制名。

推荐命名：

```text
modules/cli/cmd/moox-cli/main.go
modules/control/cmd/moox-server/main.go
modules/storage/cmd/moox-storage/main.go
modules/collector/cmd/moox-collector/main.go
modules/factor/cmd/moox-factor/main.go
modules/order/cmd/moox-order/main.go
modules/account/cmd/moox-account/main.go
```

这样 `cmd` 目录、二进制名称和发布产物保持一致。

### internal/storage 替代 internal/data

代码中的 `data` 容易被理解为数据文件、样例数据或运行时数据。涉及存储抽象、存储路由、存储引擎封装时，统一使用 `storage`。

推荐约定：

| 目录 | 含义 |
| --- | --- |
| `internal/storage` | 存储抽象、存储路由、存储引擎封装 |
| `internal/dataset` | DataSet 领域模型和业务规则 |
| `var/` | 本地运行时数据，例如 db、index、cache、临时文件 |
| `testdata/` | Go 测试样例数据 |

### skills 目录

根目录新增 `skills/`，专门放 moox 相关 Agent 技能。

推荐结构：

```text
skills/
└── moox/
    ├── SKILL.md
    └── references/
        ├── build.md
        ├── storage.md
        ├── protocol.md
        └── release.md
```

`SKILL.md` 负责说明 moox 的常用操作、模块边界和调用入口。`references/` 存放更细的构建、协议、存储、发布说明。

### scripts 目录

根目录新增 `scripts/`，存放跨模块编译、发布和打包脚本。根 `Makefile` 只做入口转发，不承载复杂逻辑。

推荐结构：

```text
scripts/
├── build.sh
├── test.sh
├── release.sh
├── package-skill.sh
├── node_exporter/
└── make/
    ├── modules.mk
    ├── release.mk
    └── skills.mk
```

推荐入口：

```text
make build
make test
make release
make package-skill
```

CLI 产物固定全平台构建，服务端产物按目标平台构建。技能包可以包含 `moox-cli` 多平台二进制和 `skills/moox` 内容。

## 模块职责

| 模块 | 来源 | 职责 |
| --- | --- | --- |
| `modules/control` | `moox/server` | 控制面、管理台后端、Workspace、节点、任务和元数据编排 |
| `modules/cli` | `moox/cli` | 统一命令行入口，面向用户、脚本和 Agent |
| `modules/storage` | `xData-mini/storage` | 数据事实面、在线写入、在线查询、存储引擎适配 |
| `modules/collector` | `data-collector` + `data-miner` 有价值能力 | 在线数据采集、数据源接入、交易所连接、标的发现、调度限频、采集任务执行 |
| `modules/factor` | `factor-calculator` | 因子定义、因子实例计算、因子结果写回 |
| `modules/order` | `order-center` | 订单、成交、交易通道和账户交易操作 |
| `modules/account` | `account-center` | 账户、凭证、权限和用户资产配置 |

## 第一阶段迁移顺序

推荐按风险从低到高迁移：

1. 建立根目录骨架：`go.work`、`modules/`、`scripts/`、`skills/`、`configs/`、`deployments/`、`var/`。
2. 迁移 `moox/server` 到 `modules/control`，入口改为 `cmd/moox-server`。
3. 迁移 `moox/cli` 到 `modules/cli`，入口改为 `cmd/moox-cli`。
4. 迁移 `xData-mini/storage` 到 `modules/storage`，入口改为 `cmd/moox-storage`。
5. 迁移 `xData-mini/cli` 中仍有价值的命令到 `modules/cli`，避免保留两个 CLI。
6. 迁移 `data-collector` 到 `modules/collector`，并把 `data-miner` 中有价值的交易所连接、调度限频、标的发现能力吸收进 collector 内部包。
7. 为 `factor`、`order`、`account` 建立模块骨架，再按代码成熟度逐步迁入。
8. 建立根 `Makefile` 与 `scripts/` 脚本，统一测试、构建和发布入口。
9. 建立 `skills/moox`，打包为可分发的 moox Agent 技能。

## 非目标

第一阶段暂不做以下事情：

- 不强行合并成单个 `go.mod`。
- 不重写协议语义，只同步路径和 import。
- 不改变存储引擎职责。
- 不合并控制面、数据面和采集面的进程职责。
- 不在迁移过程中做大规模业务重构。

## 验收标准

第一阶段完成后应满足：

- 根目录存在 `go.work`，覆盖所有已迁移 Go module。
- 所有新模块位于 `modules/` 下。
- CLI 入口使用 `cmd/moox-cli`，二进制名为 `moox-cli`。
- 运行时数据目录统一使用 `var/`，代码存储抽象统一使用 `internal/storage`。
- 根目录存在 `skills/moox` 和 `scripts/`。
- 根 `Makefile` 能转发常用构建、测试、发布命令。
- 每个迁移模块仍能独立运行自己的 `go test ./...` 或约定测试入口。
- `make release` 能生成发布产物。
- `REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' make deploy` 能把各模块统一发布到远端 `~/moox`：
  - 二进制：`~/moox/bin`
  - 模块配置：`~/moox/configs/<module>`
  - 运行时数据：`~/moox/var/<module>`
  - 模块日志：`~/moox/var/log/<module>`
- `make acceptance` 能将本机下载目录中的 `/Users/mooyang/Downloads/APT-USDT.csv` 和 `/Users/mooyang/Downloads/AR-USDT.csv` 上传到远端 `~/moox/var/storage/acceptance`。
- 验收脚本能把 APT-USDT 和 AR-USDT 的 K 线数据写入 xData/storage，并通过查询接口读回，且两者写入行数和查询行数都大于 0。
