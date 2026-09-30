# Collector 周期标的池实施记录

## 执行范围

- 目标：完整执行 `2026-09-30-collector-period-universe-remediation-plan.md`，随后新起 Agent 审查、编译发布正式环境并做端到端验证。
- 业务基线：`593a51ca`；计划文档更新 `20ae4697` 已合入实施分支（merge `510c14ac`），保留并分别提交已有实现改动。
- 隔离工作区：`.worktrees/collector-period-universe-remediation`；分支：`codex/collector-period-universe-remediation`。
- 主 checkout `feature/mooyang` 在启动时干净；不修改无关工作树。
- 线上清理或重建在列出确切影响范围并取得授权前不得执行。

## 任务状态

| 任务 | 状态 | 证据 |
| --- | --- | --- |
| Task 1 命名与快照对象 | 完成并提交 `34da1414` | 规格/质量审查均 PASS，主 Agent 七包测试、三个入口 build 复跑通过；本实施分支清单已勾选 |
| Task 2 Storage 回执/截止/查询 | 实施中 | 新 worker 从精确 Store RED 测试开始 |
| Task 3 生产代理/Gateway/鉴权 | Gateway 部分已实现，整体未完成 | YAML/defaults 四方法 Collector-only 路由及实际 seed 派生契约已由失败转通过；adapter/授权仍待 Task 2 协议后实施 |
| Task 4 权威状态与清理 | 未开始 | 依赖 Storage 查询协议 |
| Task 5 持久逐目标失败回执 | 未开始 | 依赖 Task 2、4 |
| Task 6 Timer 持久计划/Claim | 未开始 | 不得省略 owning Run、CAS、身份及超时恢复 |
| Task 7 SCF Timer worker 与环境 | 未开始 | 不得保留普通 Upsert 或零写入 success 回退 |
| Task 8 真实进程 E2E | 未开始 | 必须经过生产 resolver/adapter/Gateway |
| Task 9 全量门禁、文档、正式发布 | 门禁前置项已修复并提交 `391e4b2a` | 分项 codeCR PASS；architecture-doc 与 Gateway 完整部署契约主 Agent 复跑通过；未完成全量门禁，尚未编译发布正式环境 |

## 已执行验证

2026-09-30，基线工作区：

```text
go test -count=1 ./modules/admin/internal/service/sysdeploy ./modules/gateway/internal/router
PASS: sysdeploy 3.124s；gateway/router 4.582s。
```

以上仅证明原有两个包的测试基线，不证明新增 RPC、Timer 或正式环境端到端达标。

2026-09-30，Task 1 主 Agent 当前工作区复跑：

```text
go test -count=1 ./modules/collector/internal/domain ./modules/collector/internal/store ./modules/collector/internal/marketfetch ./modules/collector/internal/rpc ./modules/storage/internal/service/datanode/pebble
PASS: domain 0.884s；store 2.988s；marketfetch 4.482s；rpc 3.148s；Pebble 8.653s。
go build ./modules/collector/cmd/subject ./modules/collector/cmd/server ./modules/storage/cmd/server
PASS: exit 0。
git diff --check
PASS: exit 0。
```

Task 1 的固定 fixture 保持 hash `3652f82a6c767f05a7816ea0cc42c817772239f4c9af147026b1fda0b93fe954`：Binance BTC、Binance ETH、OKX BTC 共 3 series，Universe 为 2 Subject，index 为 0/1/2。清理错误语义刻意留待 Task 4，不把改名误记为行为已修复。

Task 9 文档门禁的失败与修复：先补真实 `go.work` 的 `tools/moox-mcp` 条目和 55-module 数量断言，随后门禁仍因 Strategy 文档缺精确事件名称失败；只补实际发布的 `LogicalAccountTargetWeightRequested` 名称后，`make test-docs-architecture` 为 PASS。没有修改 Strategy/Trade 行为，也没有删除或弱化架构检查。

Storage Proto 重新生成：`make -C modules/storage/proto all` exit 0，仅 `storagegen/data_node.pb.go` 有实际生成差异。额外包当前复跑为 PASS：marketstorage 1.402s、DataNode 2.493s、PrimaryStore 3.474s、Admin sysdeploy 4.164s、Gateway router 3.930s。

## Gateway 部署门禁根因与回归

独立 explorer 实跑 `make test-gateway-deploy` exit 2，定位 `patch_configs` 和生成的 `start_admin` 在合法空 `trade-gateway.json` 时无输出的 Python producer 后，无条件 `read` 遇 EOF，被 `set -e` 终止；不是实际凭据、健康或回滚保护导致的失败。

补充直接执行生成 Admin placement block 的测试以及 Gateway overlay 测试，覆盖合法空对象、合法远端配置、半缺失字段、远端 HTTP、非法 node 和非法 JSON。修改前明确 FAIL：`empty optional Trade placement prevented Admin startup`。沿用已有 `import_trade_owner_route` 的捕获 producer 退出码、非空才 `read` 模式修复两处，非法配置仍拒绝。没有填假 Trade placement 绕过缺陷，也没有禁用生产安全校验。

修复后 `bash -n scripts/deploy/deploy-moox.sh scripts/test/contract/test-deploy-moox-gateway.sh` exit 0；`make test-gateway-deploy` 完整 PASS（build、package、lifecycle、secret、CA、Caddy），其 session 已退出 0 且 fixture cleanup 完成。此修复属于正式发布门禁前置项，不代表 period adapter/权限或正式环境已验证。

## 本次接续验证与提交

此前目标轮次完成计划文档核验与提交 `20ae4697`，属于实际进展；本次继续完整编码和正式发布目标，没有缩减完成标准。

主 Agent 在当前实施工作区复跑七包测试全部 PASS：domain 0.980s、store 2.888s、marketfetch 6.109s、rpc 5.369s、Pebble 8.173s、DataNode 4.096s、Primary 5.732s；三个入口 `go build` exit 0。旧 TaskPeriodSeries/GetRoster 等源码符号搜索无匹配，固定集合、并发首建和任务专属结果身份测试仍在。

Admin sysdeploy 2.349s、Gateway router 2.105s；`make test-gateway-deploy` 完整 PASS，`make test-docs-architecture` PASS，`git diff --check` PASS，所有对应命令均已终态。

提交分别为 `34da1414`（Task 1）、`396683ae`（Task 3 默认路由部分）、`391e4b2a`（Task 9 门禁前置部分）；计划更新通过 `510c14ac` 合并，只解决两处文档措辞冲突。主 checkout 尚未合入业务实现，不进行部分发布。

用户已授权正式发布和线上验证；精确范围的数据重建/清理仍待另行确认。最终交付必须包括编码后新 Agent 审查、全部本地门禁、正式版本实际生效及真实 1m/1h 端到端证据，不能以本记录中的局部成功替代。
