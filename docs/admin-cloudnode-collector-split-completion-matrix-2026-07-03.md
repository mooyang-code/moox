# Admin / CloudNode / Collector 拆分完成度证据矩阵

本文把当前拆分目标拆成可核对的证据项，避免只凭“看起来差不多”判断完成。

当前结论：源码与发布脚本层面的拆分已基本具备证据；完整目标尚不能标记完成，因为还缺少一次授权后的构建、部署、删库重建和端到端运行态验证。

## 目标 1：`data/moox.db` 改为 `data/admin.db`，admin 只做转发

| 要求 | 当前证据 | 状态 |
| --- | --- | --- |
| 不再使用 `data/moox.db` | 当前源码、前端、脚本、examples、docs 中未发现活跃 `data/moox.db` / `moox.db` 引用，历史审计日志除外。 | 源码证据充分 |
| admin 默认库为 `data/admin.db` | admin 配置和部署脚本将 admin SQLite 映射到 `data/admin.db` / `../data/admin.db`。 | 源码证据充分 |
| admin 数据库边界收敛到 SQLite | admin 数据库实现只打开 SQLite；旧泛化 `DB_PATH` 环境变量改为 `MOOX_ADMIN_DB_PATH`，数据库文档不再声明 MySQL/PostgreSQL 支持。 | 源码证据增强 |
| admin database service 是基础设施而非旧 DB 管理功能 | `modules/admin/internal/service/database` 只负责打开 `data/admin.db`、应用内嵌 `schema/admin.sql`、初始化 Badger cache，并未注册对外数据库管理 RPC 或迁移入口。 | 源码证据增强 |
| admin 不再承载云节点/云函数/采集业务实现 | `modules/admin/internal/service` 当前只包含 auth、database、dnsproxy、monitor、secret、space、ssh、sysdeploy。 | 源码证据充分 |
| admin proto 不再暴露旧云节点/云函数/采集协议 | `modules/admin/proto` 当前只定义 `SpaceMgr`、`Auth`、`Dns`、`Ssh`、`Monitor`、`SecretMgr`、`SysDeploy`；未发现 `AsyncTask`、`PackageMgr`、CloudFunction、CollectMgr 或 CloudNodeMgr 旧协议。 | 源码证据增强 |
| admin 只负责 cloudnode/collector 网关转发 | `t_service_deployments` 是 admin 本地基础表；默认 `moox_cloudnode`、`moox_collector` 服务记录由 SysDeploy 启动时补齐，用于网关服务发现和转发。 | 源码证据增强 |
| 管理台云节点相关 API 走 cloudnode 网关 | 前端 `cloud-node.ts`、`cloud-account.ts`、`function-package.ts` 均通过 `callControl('cloudnode', ...)` 请求 `/api/admin/cloudnode/{Method}`；未发现旧 `/api/admin/cloudfunction` 路径。 | 源码证据增强 |
| gateway 不维护本地服务地址兼容表 | `modules/admin/internal/gateway/forward.go` 通过 `t_service_deployments` resolver 解析 address/path；`gateway.yaml` 明确不维护服务地址。 | 源码证据充分 |
| gateway 不做 endpoint 特殊转发 | `modules/admin/internal/gateway/forward.go` 只按 `{service}/{method}` 组装目标 RPC path；`collectmgr` / `cloudnode` 到独立服务部署记录的映射集中在 SysDeploy resolver。 | 源码证据增强 |
| 跨业务共享代码放根级 packages | `packages/cloudruntime` 是当前唯一跨业务共享 runtime；collector 通过根级 package 复用，不再依赖 cloudnode 模块实现。 | 源码证据充分 |
| 业务模块通过协议使用 cloudnode | 架构文档已明确 collector/factor 通过 CloudNodeMgr 或 `/api/service/cloudnode/*` 使用 cloudnode，不能直接 import `modules/cloudnode/internal/...`。 | 文档证据增强 |
| 边界脚本防止 admin 回引下游模块 | `scripts/check-module-boundaries.sh` 对 `modules/admin` 加严：admin 不允许 import 或 go.mod 依赖其他业务模块，包括对方 `proto/*`；下游访问必须经网关和 `t_service_deployments`。 | 脚本证据增强 |
| 边界脚本覆盖 go.mod 依赖 | `scripts/check-module-boundaries.sh` 已从 Go 源码 import 扩展到 `modules/*/go.mod` 和 `packages/*/go.mod`；非 Admin 业务模块只允许依赖其他模块 `proto/*` module，根级 packages 不得依赖业务模块。 | 脚本证据增强 |
| CLI 不直接 import collector 实现包 | collector SCF 打包工具已从 `modules/collector/pkg/packager` 移到 `modules/cli/internal/collectorpackager`；`modules/cli/go.mod` 不再 require/replace `modules/collector`。 | 源码证据增强 |
| CLI 云节点/代码包调用走 cloudnode 路径 | CLI `adminclient` 通过 `/api/admin/cloudnode/*` 调用代码包、云账户和批量节点接口；配置 ServiceAuth 时底层改写为 `/api/service/cloudnode/*` 并使用 HMAC。 | 源码证据增强 |
| Collector 不再暴露公共 `pkg` 实现包 | `modules/collector/pkg` 已移除；运行时配置、模型、HTTP client、Storage client 均迁入 collector 自身 `internal` 目录。 | 源码证据增强 |

仍需证明：

- 构建后的 admin 实际启动并写入部署目录 `data/admin.db`。
- `/api/admin/cloudnode/*`、`/api/admin/collectmgr/*` 在运行态经网关转发到独立进程。
- Collector 控制面已增加可选 SysDeploy dependency discovery，且 `deploy-moox.sh` 启动 collector 时会注入 `MOOX_COLLECTOR_ADMIN_GATEWAY_URL` 和后台签名默认值；仍需构建和运行态验证该路径。

## 目标 2：新的云节点、云账户服务独立部署

| 要求 | 当前证据 | 状态 |
| --- | --- | --- |
| cloudnode 独立模块 | `modules/cloudnode` 拥有独立入口、配置、schema、service 和 repository。 | 源码证据充分 |
| 云账户归 cloudnode | `modules/cloudnode/schema/cloudnode.sql` 拥有 `t_cloud_accounts`；admin secrets 不再作为 cloud account 存储。 | 源码证据充分 |
| CloudNodeMgr 协议暴露面有归属 | `GetNodeList`、`UpdateNode`、批量节点变更、云账户、代码包、`SubmitWorkItems/PollWorkItems/ReportWorkItemStatus`、`InvokeSync` 均由独立 `modules/cloudnode` 实现；其中 `GetCOSAccountInfo` 被 CLI 腾讯云防火墙辅助命令使用，`InvokeSync` 是因子等业务复用云节点同步 fan-out/fan-in 的平台能力。 | 源码证据增强 |
| 云执行队列命名收敛为 work_item | collect proto、cloudnode service/repository、`packages/cloudruntime`、collector 提交端和 schema 均从 `job/job_id` 收敛为 `work_item/work_item_id`；批量节点管理仍使用 `batch_change/batch_id`。 | 源码证据增强 |
| 通用 SCF runtime 不归 cloudnode 私有 | `packages/cloudruntime` 承载跨业务复用的 CloudNode work_item runtime；collector 只依赖该根级 package 和 collect proto，不再直接 import `modules/cloudnode/...`。 | 源码证据充分 |
| 删除 cloudnode 私有 SCF runtime 空壳 | `modules/cloudnode/scf/runtime` 已是空目录，通用 runtime 归根级 `packages/cloudruntime`，旧空目录已删除。 | 源码证据增强 |
| 共享包边界可检查 | `scripts/check-module-boundaries.sh` 禁止 `packages/*` 反向 import `modules/*`，防止共享 runtime 重新耦合业务模块。 | 脚本证据充分 |
| 独立发布 | `scripts/deploy-moox.sh` 支持 `--no-cloudnode`，复制 `moox-cloudnode` 二进制和 cloudnode config，并生成 `start_cloudnode`；CloudNode schema 已内嵌，不再随部署包复制。 | 脚本证据增强 |
| 独立数据目录 | 部署脚本将 cloudnode 数据库映射到 `../data/cloudnode/moox_cloudnode.db`。 | 脚本证据充分 |

仍需证明：

- 授权后构建 `moox-cloudnode`。
- 远端或本地部署后，cloudnode 独立进程可启动并响应 CloudNodeMgr RPC。
- 云账户 CRUD 通过 `/api/admin/cloudnode/*` 走独立 cloudnode。

## 目标 3：不迁移旧数据，云节点相关表归各模块

| 要求 | 当前证据 | 状态 |
| --- | --- | --- |
| 不保留旧数据迁移路径 | 未发现 active migration/migrate/mock/fixture/test 文件；一次性 schema override 路径已移除。 | 源码证据较强 |
| admin 表结构只保留 admin 基础能力 | `modules/admin/schema` 仅作为 admin-local schema；cloudnode/collector 表不在 admin schema 中。 | 源码证据充分 |
| admin 本地表仍有当前归属 | `t_spaces`、`t_service_deployments`、auth 审计表、SSH 表、monitor history 和 `t_secrets` 均对应 admin 本地基础服务 model/DAO 或 gateway/sysdeploy 路径，不属于旧 cloudnode/collector 业务表。 | 源码证据增强 |
| admin 默认服务部署记录可随删库重建 | `modules/admin/internal/service/sysdeploy.SeedDefaults` 在 Admin 启动创建 SysDeploy 服务时补齐缺失的默认 `t_service_deployments` 记录，不覆盖用户已修改记录。 | 源码证据增强 |
| admin schema 注释与当前边界一致 | `modules/admin/schema/admin.sql` 顶部说明已从旧“认证系统”改为 “Admin 本地基础数据库”，避免误导为 auth-only 或旧单体 schema。 | 源码证据增强 |
| schema 目录不混运行数据 seed | Admin 的 `service_deployments_seed.sql` 已删除；当前模块 schema SQL 未发现数据 `INSERT` seed，命中 `UPDATE` 均为 mtime 触发器或外键动作。 | 源码证据增强 |
| cloudnode 表归 cloudnode | `modules/cloudnode/schema/cloudnode.sql` 拥有 `t_cloud_nodes`、`t_cloud_accounts`、`t_cloud_function_packages`、`t_cloud_work_items`、`t_cloud_work_item_attempts`、`t_cloud_invocations`、`t_cloud_invocation_results`。这些表均由 cloudnode repository/service 当前路径使用。 | 源码证据增强 |
| collector 表归 collector | `modules/collector/schema/collector.sql` 是 collector 独立 schema，只保留表/索引/触发器，不再内置默认采集规则运行态 seed。 | 源码证据增强 |
| 模块 schema 初始化不再由 bootstrap 注入任意 SQL | Admin、CloudNode、Collector 的数据库 Manager 均从各自模块 `schema` 包读取内嵌 SQL；bootstrap 只传数据库配置，不再传 `schemaSQL` 文本。 | 源码证据增强 |
| 模块 override 不共用旧全局名 | Admin 使用 `MOOX_ADMIN_DB_PATH` / `MOOX_ADMIN_ENCRYPTION_KEY`，CloudNode 使用 `MOOX_CLOUDNODE_DB_PATH`，Collector 使用 `MOOX_COLLECTOR_DB_PATH`，Trade 使用 `MOOX_TRADE_DB_PATH` / `MOOX_TRADE_ENCRYPTION_KEY`；不再共用泛化 `DB_PATH` / `MOOX_ENCRYPTION_KEY` 表达模块数据库和敏感密钥路径。 | 源码证据增强 |
| task_instance 不再暴露手工 CRUD 协议 | `modules/collect/proto/collect_service.proto` 已移除未使用的 task-instance cache/detail/create/update/delete/start/stop/invalidate RPC；当前只保留列表查询和状态上报，实例由 collector planner 生成。 | 源码证据增强 |

仍需证明：

- 授权后删掉运行时数据，模块启动能各自重建 schema。
- 运行态请求不会创建旧 admin-owned 云节点/采集表。

## 目标 4：运行数据可删，并从 examples/e2e 重建

| 要求 | 当前证据 | 状态 |
| --- | --- | --- |
| examples 提供重建入口 | `examples/e2e/README.md` 描述删库后的重建顺序。 | 文档入口已补齐 |
| examples 不直接写模块表 | `examples/` 当前是 Storage metadata/platform seed 和演示数据，不包含旧 `t_cloud_*` / `t_collect*` 直接写表脚本。 | 源码证据充分 |
| collector 规则通过服务重建 | `modules/collector/schema/collector.sql` 不再 `INSERT` 默认采集规则；删库后按 `examples/e2e/README.md` 通过管理台或 collectmgr API 创建规则并生成 task instances。 | 源码证据增强 |
| collectmgr 规则 API 可重建规则 | `modules/collector/internal/service/collectmgr` 实现 `CreateTaskRule`、`UpdateTaskRule`、`DisableTaskRule`、`GetTaskRuleDetail`，前端按 proto 结构提交 `{ rule: ... }`。 | 源码证据增强 |
| 发布支持删运行数据 | `scripts/deploy-moox.sh --reset-data` 会删除部署目录的 `data`，用于从 examples 重建。 | 脚本证据充分 |
| 发布包携带 examples | `scripts/release.sh` 会将 `examples/` 复制到 release 包中，避免发布产物缺失重建 seed。 | 脚本证据充分 |
| 发布包不预置运行态数据目录 | `scripts/release.sh` 不再创建 `storage/var/storage` 空目录；运行态数据目录由部署目录或 Storage 启动脚本按配置创建。 | 脚本证据增强 |
| 部署目录携带 examples | `scripts/deploy-moox.sh` stage 会复制 `examples/`，远端或本地部署时即使清理旧 `examples` 目录，也会从 stage/archive 重新同步。 | 脚本证据充分 |
| 发布包不携带源码开发脚本 | `scripts/release.sh` 不再复制整套 `scripts/`，避免二进制包中出现依赖源码树的 build/check/release 脚本。 | 脚本证据充分 |
| 发布包不携带仓库 skills | `scripts/release.sh` 不再复制 `skills/`，避免把依赖源码上下文的维护者/调试 skill 当成运行包内容。 | 脚本证据充分 |
| 发布/部署包不携带已内嵌模块 schema | Admin、CloudNode、Collector schema 已嵌入各自二进制，`scripts/release.sh` 与 `scripts/deploy-moox.sh` 不再复制这些 schema 目录；Storage 仍保留 `metadata.sql` 供独立初始化命令使用。 | 脚本证据增强 |
| README 发布说明与包结构一致 | 根 `README.md` 已说明 release/deploy 只携带 Storage schema；Admin、CloudNode、Collector schema 由各自二进制内嵌并在启动时自动应用。 | 文档证据增强 |
| 模块 README 路径说明与部署脚本一致 | CloudNode、Collector README 已说明本地默认 SQLite 路径和 `deploy-moox.sh` 发布后 `../data/cloudnode` / `../data/collector` 路径改写规则。 | 文档证据增强 |
| 重建走服务边界 | 文档明确要求通过模块启动、`moox-cli metadata import`、admin/cloudnode API、collector 规则生成和 storage rebuild。 | 设计证据充分 |
| 不推荐手工 SQL 恢复作为重建路径 | `docs/数据库管理.md` 已移除 `sqlite3 .backup/.dump/restore` 示例，改为删运行态数据后按 examples/E2E 和服务 API 重建；备份只作为人工运维兜底，不进入仓库。 | 文档证据增强 |
| 运行态 SQLite 文件不入库 | 根 `.gitignore` 已覆盖 `/data/`、模块运行目录以及 `*.db`、`*.db-wal`、`*.db-shm`、`*.sqlite*`，避免 admin/cloudnode/collector/storage/trade 运行数据误提交。 | 源码证据增强 |
| 前端构建产物不作为源码维护 | 根 `.gitignore` 已显式忽略 `/web/dist/`；web-host 静态资源通过部署脚本默认重新构建 Vue dist 并刷新 statik，而不是手工维护旧 chunk。 | 源码证据增强 |

仍需证明：

- 实际执行一次删库重建。
- 导入 examples seed 后，Storage metadata、collector rule、task instance、SCF work_item、K 线写入和 view 查询链路都可跑通。

## 目标 5：删除无用死代码，尤其迁移、功能单测、一次性代码

| 要求 | 当前证据 | 状态 |
| --- | --- | --- |
| 删除项目自有测试文件 | 当前 active 扫描未发现 `_test.go`。 | 源码证据充分 |
| 删除孤儿测试库记录 | `modules/admin`、`modules/cloudnode`、`modules/collector` 的 `go.sum` 已移除未被 `go.mod` require、也无源码 import 的 `github.com/frankban/quicktest` checksum。 | 源码证据增强 |
| 删除迁移/fixture/mock 文件 | 当前 active 扫描未发现 migration/migrate/mock/fixture/coverage 文件。 | 源码证据充分 |
| 删除前端旧 mock 依赖锁定项 | `web/package.json` 和前端源码未使用 `mockjs` / `vite-plugin-mock`；`web/pnpm-lock.yaml` 已移除孤儿 `mockjs`、`vite-plugin-mock` 和仅由 mockjs 拉入的 `commander@14.0.0`。 | 源码证据增强 |
| 删除旧路径/旧接口残留 | 前端、CLI、脚本未发现旧 `/api/admin/cloudfunction`、旧包管理 RPC、`data/moox.db` 调用路径。 | 源码证据充分 |
| 删除旧 schema override 文档残留 | `docs/数据库管理.md` 不再展示已删除的 `Initialize(&cfg.Database, schemaSQL)` / `adminschema.AllSQL()` 用法，改为当前内嵌 admin schema 自动初始化。 | 文档证据增强 |
| 删除跨模块暴露的 CLI-only packager | `modules/collector/pkg/packager` 已移入 `modules/cli/internal/collectorpackager`，避免 collector 暴露只供 CLI 使用的打包 helper。 | 源码证据增强 |
| 删除 collector 孤儿 logger 包 | `modules/collector/pkg/logger` 未被 collector 或其他模块 import，且仍带旧 `DATA-COLLECTOR` 运行前缀；该包已删除。 | 源码证据增强 |
| 删除 collector 剩余公共 `pkg` 实现包 | `pkg/config`、`pkg/model`、`pkg/httpclient`、`pkg/storage` 均只被 collector 自身使用，已迁入 `internal/config`、`internal/model`、`internal/httpclient`、`internal/storageclient`。 | 源码证据增强 |
| 删除 collector 旧空目录 | `modules/collector/internal/cloudruntime` 已无文件，实际能力已拆为根级 `packages/cloudruntime` 和 collector 内部 `cloudnodepoller`，空目录已删除。 | 源码证据增强 |
| 删除云执行队列旧 job 语义 | CloudNode 异步执行 RPC 已改为 `SubmitWorkItems/PollWorkItems/ReportWorkItemStatus`，CloudNode 表改为 `t_cloud_work_items` / `t_cloud_work_item_attempts`，collector 任务实例字段改为 `cloud_work_item_id`。 | 源码证据增强 |
| 任务实例可关联 CloudNode work_item | CollectMgr `TaskInstance` proto 新增 `cloud_work_item_id`，collector 转换层返回该字段，管理台任务实例列表和详情展示 WorkItem ID。 | 源码证据增强 |
| 删除 cloudnode 旧 SCF runtime 空目录 | `modules/cloudnode/scf/runtime` 不再承载任何代码，已删除；SCF 通用 runtime 在 `packages/cloudruntime`，collector 业务入口在 `modules/collector/internal/scf`。 | 源码证据增强 |
| 清理 CLI 旧 collector/admin 专名 | CLI 控制面 client helper 已从 `newCollectorAdminClient` 改为 `newControlClient`，避免腾讯云运维和 cloudnode 后台调用继续挂在 collector/admin 专名上。 | 源码证据增强 |
| CloudNodeMgr 未发现可直接删除的无主 RPC | `UpdateNode` 有前端 API 入口，`GetCOSAccountInfo` 被 CLI 运维命令使用，`InvokeSync` 是已确认的平台同步调用能力；`SubmitWorkItems/PollWorkItems/ReportWorkItemStatus` 是 SCF runtime 活跃链路。 | 源码证据增强 |
| 清理旧项目运行标识 | collector active 默认运行标识、CLI 默认函数包名、日志 component、HTTP User-Agent 和数据源 app_id 已从旧 collector 运行标识改为 `moox-collector`。 | 源码证据增强 |
| 清理旧本地任务缓存语义 | SCF keepalive 后的执行入口已命名为 `pollWorkItemsAfterHeartbeat`，日志明确为 CloudNode `work_item` 拉取/执行；调试文档不再引用旧 collector local task cache。 | 源码证据增强 |
| 清理调试/旧配置提示 | admin 登录链路不再打印明文密码或完整用户对象；`gateway.yaml` 不再提示旧 `JWT_SECRET` 环境变量。 | 源码证据增强 |
| 保留仍活跃链路 | SCF keepalive 的 `ProcessProbe -> ReportHeartbeat -> PollWorkItems` 仍是运行态链路，不应删除。 | 保留有依据 |

仍需证明：

- 授权后构建所有相关模块，确认清理后无编译断点。
- 运行态验证证明被保留链路不是假活，也证明没有旧死代码被运行依赖。

## 建议的最终验证清单

这些步骤会改变运行态或耗时，需明确授权后再执行：

```bash
./scripts/build.sh admin
./scripts/build.sh cloudnode
./scripts/build.sh collector
./scripts/build.sh cli
```

如需要验证发布与删库重建：

```bash
scripts/deploy-moox.sh --target localhost --dir /tmp/moox-e2e --reset-data
```

然后按 `examples/e2e/README.md` 导入 metadata seed，创建云账户/云节点/采集规则，生成 task instances，并验证 SCF work_item 写入 storage 与 view 查询。
