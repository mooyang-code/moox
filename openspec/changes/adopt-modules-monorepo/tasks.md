## 1. 建立大仓基础骨架

- [ ] 1.1 在 moox 根目录创建 `modules/`、`build/`、`skills/moox/`、`configs/`、`deployments/`、`var/`，并确保 `var/` 不提交运行时数据。
- [ ] 1.2 创建根 `go.work`，先纳入现有可迁移模块路径，执行 `go work sync` 验证 workspace 可用。
- [ ] 1.3 创建或更新根 `README.md`，说明 moox 是量化数据存储统一解决方案，并链接 `docs/monorepo-architecture.md`。
- [ ] 1.4 创建根 `.gitignore` 规则，覆盖 `var/`、构建产物、各模块 release/bin/cache 目录。

## 2. 迁移控制面模块

- [ ] 2.1 将 `server` 迁移为 `modules/control`，保留独立 `go.mod`。
- [ ] 2.2 将 `modules/control/main.go` 移动为 `modules/control/cmd/moox-server/main.go`，调整 Makefile 或启动脚本中的入口路径。
- [ ] 2.3 修正 `modules/control` 内部相对路径、配置路径、proto 生成路径和 release 路径。
- [ ] 2.4 在根 `go.work` 中加入 `./modules/control`，执行 `go test ./...` 或模块现有测试命令验证。

## 3. 迁移统一 CLI 模块

- [ ] 3.1 将 `cli` 迁移为 `modules/cli`，保留独立 `go.mod`。
- [ ] 3.2 将 CLI 入口调整为 `modules/cli/cmd/moox-cli/main.go`，二进制名统一为 `moox-cli`。
- [ ] 3.3 修正 CLI 对 control proto 和 storage proto 的本地引用，优先通过 `go.work` 消除不必要的跨仓库 replace。
- [ ] 3.4 执行 `go test ./...` 验证 `modules/cli` 可独立编译。

## 4. 迁移存储模块

- [ ] 4.1 将 `xData-mini/storage` 迁移为 `modules/storage`，保留独立 `go.mod` 和 storage 专用 Makefile。
- [ ] 4.2 将入口调整为 `modules/storage/cmd/moox-storage/main.go`，二进制名统一为 `moox-storage`。
- [ ] 4.3 将存储模块内的服务路径、proto 生成路径、配置路径和 release 路径改为新目录结构。
- [ ] 4.4 保留 storage 的 CGO-aware 测试入口，执行 `make test` 验证 Pebble、DuckDB 构建约束没有被根 workspace 破坏。
- [ ] 4.5 将 `xData-mini/cli` 中仍有价值的数据导入、扫描、元数据命令迁入 `modules/cli`，避免长期保留第二套 CLI。

## 5. 迁移采集模块并吸收 miner 能力

- [ ] 5.1 将 `data-collector` 迁移为 `modules/collector`，入口统一为 `modules/collector/cmd/moox-collector/main.go`。
- [ ] 5.2 修正 collector 配置目录，从 `configs/` 或模块内配置目录加载数据源配置。
- [ ] 5.3 执行 `go test ./...` 或 collector 现有构建命令，确认采集模块迁移后可编译。
- [ ] 5.4 将 `data-miner` 中有价值的交易所连接、调度限频、标的发现能力合并进 `modules/collector/internal/source`、`internal/discovery`、`internal/scheduler`。
- [ ] 5.5 修正被吸收能力的运行时数据目录，将业务运行数据移入根 `var/` 或 collector 模块忽略目录。
- [ ] 5.6 执行 `go test ./...` 或 collector 现有构建命令，确认 collector 吸收 miner 能力后可编译。

## 6. 建立 factor、order、account 模块骨架

- [ ] 6.1 创建 `modules/factor`，包含 `go.mod`、`cmd/moox-factor/main.go`、`internal/`、`README.md`，先承载因子计算职责边界。
- [ ] 6.2 创建 `modules/order`，包含 `go.mod`、`cmd/moox-order/main.go`、`internal/`、`README.md`，先承载订单和交易通道职责边界。
- [ ] 6.3 创建 `modules/account`，包含 `go.mod`、`cmd/moox-account/main.go`、`internal/`、`README.md`，先承载账户、凭证和权限职责边界。
- [ ] 6.4 将三个新模块加入根 `go.work`，执行 `go work sync` 和最小编译验证。

## 7. 统一 storage 命名和运行时目录

- [ ] 7.1 扫描迁移后代码中的 `internal/data`，将存储抽象、存储路由、存储引擎封装统一改为 `internal/storage`。
- [ ] 7.2 将运行时数据目录从源码路径中剥离，统一使用根 `var/` 或模块私有 ignored 目录。
- [ ] 7.3 保留 Go 测试样例目录 `testdata/`，不要把测试样例迁入 `var/`。
- [ ] 7.4 执行 `rg -n "internal/data|cmd/moox\\b"` 验证旧命名无残留。

## 8. 建立根构建和发布入口

- [ ] 8.1 创建根 `Makefile`，提供 `build`、`test`、`release`、`package-skill`、`clean` 入口。
- [ ] 8.2 创建 `build/build.sh`，按模块执行构建，storage 使用模块专用构建策略。
- [ ] 8.3 创建 `build/test.sh`，按模块执行测试，storage 使用 `make test`，普通 Go 模块使用 `go test ./...`。
- [ ] 8.4 创建 `build/release.sh`，服务端按目标平台构建，CLI 固定构建 Linux amd64、Darwin amd64、Darwin arm64、Windows amd64。
- [ ] 8.5 创建 `build/package-skill.sh`，打包 `skills/moox` 和全平台 `moox-cli` 二进制。
- [ ] 8.6 创建 `build/deploy.sh`，支持把 moox 各模块统一发布到 `43.132.204.177:~/moox`，并允许通过 `REMOTE_ROOT` 自定义远端发布根目录；脚本必须在远端解析 `~/moox`，不能把它展开成本机用户目录。
- [ ] 8.7 创建 `build/acceptance.sh`，支持上传本机 `/Users/mooyang/Downloads/APT-USDT.csv` 和 `/Users/mooyang/Downloads/AR-USDT.csv` 到远端 `~/moox/var/storage/acceptance`，并写入 xData/storage 作为验收数据。

## 9. 建立 moox Agent 技能

- [ ] 9.1 创建 `skills/moox/SKILL.md`，说明 moox 大仓模块边界、常用命令和触发场景。
- [ ] 9.2 创建 `skills/moox/references/build.md`，记录 build/test/release/package-skill 命令。
- [ ] 9.3 创建 `skills/moox/references/storage.md`，记录 storage 模块、Pebble、DuckDB、Bleve、CSV 的职责边界。
- [ ] 9.4 创建 `skills/moox/references/protocol.md`，链接 PB 协议重设计文档和核心概念文档。
- [ ] 9.5 创建 `skills/moox/references/release.md`，记录技能包和 CLI 发布流程。

## 10. 全局验证和收尾

- [ ] 10.1 执行 `go work sync`，确认 workspace 没有无效模块路径。
- [ ] 10.2 执行根 `make test`，确认所有已迁移模块测试通过或输出清晰的模块级失败原因。
- [ ] 10.3 执行根 `make package-skill`，确认技能包包含 `SKILL.md`、references 和全平台 `moox-cli`。
- [ ] 10.4 执行 `REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' make deploy`，确认远端 `~/moox/bin`、`~/moox/configs`、`~/moox/var` 路径下存在发布产物，并按模块隔离配置、日志和运行时数据。
- [ ] 10.5 执行 `REMOTE_HOST=43.132.204.177 REMOTE_ROOT='~/moox' APT_CSV=/Users/mooyang/Downloads/APT-USDT.csv AR_CSV=/Users/mooyang/Downloads/AR-USDT.csv make acceptance`，确认 APT-USDT 和 AR-USDT 两份 CSV 均上传到 `~/moox/var/storage/acceptance`、写入 xData/storage 且可查询。
- [ ] 10.6 执行 `openspec validate adopt-modules-monorepo --strict`，确认 OpenSpec change 合法。
- [ ] 10.7 更新 `docs/monorepo-architecture.md` 中的迁移状态，记录已完成模块和仍待迁移模块。
