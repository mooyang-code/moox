# CloudNode 云函数领域重命名执行计划

v1（2026-10-23，计划稿，尚未实施）。本计划把 CloudNode 的模块身份、云函数实例模型与异步管理作业统一命名，并保持云函数代码包、函数实例和单次调用的概念独立。

代码基线：`origin/main@3da2faedc979fab121f01e6278acc24c56cba5cf`。计划所在分支：`codex/cloudnode-naming-review`。实施前重新核对主线变化，以下文件和接口清单以此基线为准。

当前只交付执行计划，不修改业务代码、数据库、云端函数或部署配置。现行行为见[云节点模块文档](../模块/云节点.md)，现行协议见 [cloudnode.proto](../../modules/cloudnode/proto/cloudnode.proto)，现行表结构见 [cloudnode.sql](../../modules/cloudnode/schema/cloudnode.sql)。

## 1. 决策状态与目标

### 1.1 已确认的领域命名

| 概念 | 目标实体 | 目标表 | 职责 |
| --- | --- | --- | --- |
| 云函数代码包 | `FunctionPackage` | `t_cloud_function_packages` | 代码制品，记录版本、运行时、COS 位置、校验值和可用状态 |
| 云函数实例 | `FunctionInstance` | `t_cloud_function_instances` | 已创建或正在创建的云端函数资源，记录云账号、地域、命名空间、函数名、当前代码包、运行配置及生命周期身份 |
| 函数管理作业 | `FunctionJob` | `t_cloud_function_jobs` | 一次创建、部署、删除或运行配置更新作业，可包含多个实例操作 |
| 作业明细 | `FunctionJobItem` | `t_cloud_function_job_items` | 一次作业中的逐项请求、目标实例、执行状态、结果及发布租约恢复信息 |
| 云账号 | `CloudAccount` | `t_cloud_accounts` | 云账号配置及 Admin 密钥引用 |
| 单次函数调用 | `Invocation` | 本轮不新增调用记录表 | 调用一个函数实例，并返回同步结果或异步提交结果 |

这里的“函数实例”指 MooX 管理的稳定函数资源/部署实例。云厂商为一次或多次执行分配的临时容器、进程和并发执行环境不属于本表模型。

关系约定：

- 同一个代码包可部署到多个函数实例；实例记录当前关联的 `package_id`、`package_version`。
- `instance_id` 是 MooX 在空间内的逻辑身份，`function_name` 是云厂商侧名称，两者继续分开。
- `lifecycle_id` 标识该逻辑身份对应的一次资源生命周期，`lifecycle_operation_id` 参与并发变更围栏；不得用实例 ID 或作业 ID 替代它们。
- Job 表示控制面的管理作业；Item 表示其中一项实例变更。函数调用、采集批次与采集任务实例使用各自的既有模型。

### 1.2 模块名的暂定选择

模块最终使用 `cloudfunction` 还是 `serverless` 尚未明确确认。本稿采用推荐的 **`cloudfunction`** 写出可执行路径和命令；这一选择是计划假设，不能记为用户已确认。

当前实现直接管理腾讯云 SCF 的函数、代码包、触发器和调用，因此 `cloudfunction` 更贴合现有职责。若模块要承接更广的 Serverless 资源管理，可选择 `serverless`。选用后者时仅替换下表中的模块身份及相关进程标识，§1.1 的实体和表名保持不变。

| 项目 | 本稿暂定值 | 如选择 serverless |
| --- | --- | --- |
| 模块目录/组件 ID/caller | `cloudfunction` | `serverless` |
| 服务二进制 | `moox-cloudfunction` | `moox-serverless` |
| schema CLI 二进制 | `moox-cloudfunction-cli` | `moox-serverless-cli` |
| 协议文件/生成目录 | `cloudfunction.proto` / `cloudfunctiongen` | `serverless.proto` / `serverlessgen` |
| protobuf package | `trpc.moox.cloudfunction` | `trpc.moox.serverless` |
| RPC 服务 | `CloudFunctionMgr` | `ServerlessMgr` |
| 默认 Go 协议包别名 | `cloudfunctionpb` | `serverlesspb` |
| 数据库文件 | `moox_cloudfunction.db` | `moox_serverless.db` |
| schema 文件 | `cloudfunction.sql` | `serverless.sql` |
| 环境变量前缀 | `MOOX_CLOUDFUNCTION_` | `MOOX_SERVERLESS_` |
| 控制台服务路由 | `/api/admin/cloudfunction/*` | `/api/admin/serverless/*` |
| 中文模块名称 | 云函数管理 | Serverless 管理 |

实施阶段 0 必须冻结这项选择，并把本文所有暂定路径统一为最终值；随后再执行移动目录与协议生成。

### 1.3 实施范围

完成模块目录、协议、实例模型、作业模型、SQLite schema、所有管理调用方、网关身份、前端、部署制品、监控契约与现行文档的同步改名。各调用方在同一个完整变更中切换，不保留旧 RPC、旧 JSON 字段、旧表名或旧组件名的运行时别名。

遵循仓库 `AGENTS.md` 的新项目原则，不引入历史版本兼容层。改名保持现有业务行为，包括空间隔离、发布租约、生命周期围栏、创建预留、异步恢复、SCF 限频、调用预算、Timer 回读和垃圾回收规则。

以下事项留在原有边界内：

- `packages/cloudprovider`、腾讯云 SCF/COS/CLS 适配器及厂商 API 名称。
- Storage DataNode、主机/进程身份、Collector 任务实例和采集批次。
- 新增云厂商、容器运行时、统一计算资源抽象或部署历史表。
- 重新设计函数类型、包版本策略、运行配置结构或状态机。
- 自动删除数据库、COS 对象、云端函数，或重建已有函数。

## 2. 命名映射与协议契约

### 2.1 管理领域的实体、字段和文件

| 当前 | 目标 |
| --- | --- |
| `CloudNode` | `FunctionInstance` |
| `NodeID` / `NodeId` / `node_id` | `InstanceID` / `InstanceId` / `instance_id` |
| `NodeIDs` / `NodeIds` / `node_ids` | `InstanceIDs` / `InstanceIds` / `instance_ids` |
| `NodeType` / `node_type` | `FunctionType` / `function_type` |
| `NodeBatch` / `NodeBatchSummary` | `FunctionJob` / `FunctionJobSummary` |
| `NodeBatchItem` / `NodeBatchItemResult` | `FunctionJobItem` / `FunctionJobItemResult` |
| `NodeBatchOperation` / `NodeBatchStatus` / `NodeBatchItemStatus` | `FunctionJobOperation` / `FunctionJobStatus` / `FunctionJobItemStatus` |
| `NodeCreateItem` / `NodeDeployItem` / `NodeDeleteItem` | `FunctionInstanceCreateItem` / `FunctionInstanceDeployItem` / `FunctionInstanceDeleteItem` |
| `NodeRuntimeConfigPatch` | `FunctionInstanceRuntimeConfigPatch` |
| `node.go` / `node_test.go` | `instance.go` / `instance_test.go` |
| `node_batch.go` / `node_batch_runner.go` | `job.go` / `job_runner.go` |
| `NodeBatchConfig` | `FunctionJobConfig` |
| `node_batch` 配置段 | `function_job`，保留 `batch_size`、`poll_interval` 字段及取值 |

表中的 `Node*` 替换仅限云函数管理领域。文件名按职责移动，相关测试文件同步；避免对全仓 `node`、`instance` 或 `batch` 做无范围替换。

`function_type` 的现值仍为 `scf-event` 等现有分类，`provider` 仍为 `tencent-scf`，`trigger_type` 仍为 `invoke` / `timer`。SCF 导入候选对象已有厂商侧 `function_type`，例如厂商函数类型；应在注释中说明来源，不能把两个字段的取值做未经定义的互换。

### 2.2 RPC 方法及请求/响应

| 当前方法 | 目标方法 | 返回/行为 |
| --- | --- | --- |
| `GetNodeList` | `ListFunctionInstances` | 分页返回实例，保留过滤与 `refresh_readback` |
| `UpdateNode` | `UpdateFunctionInstance` | 更新允许修改的实例元数据，保留发布围栏限制 |
| `SubmitCreateNodes` | `SubmitCreateFunctionInstances` | 返回 `SubmitFunctionJobRsp` |
| `SubmitDeployNodes` | `SubmitDeployFunctionInstances` | 返回 `SubmitFunctionJobRsp` |
| `SubmitDeleteNodes` | `SubmitDeleteFunctionInstances` | 返回 `SubmitFunctionJobRsp` |
| `SubmitUpdateNodeRuntimeConfigs` | `SubmitUpdateFunctionInstanceRuntimeConfigs` | 返回 `SubmitFunctionJobRsp` |
| `GetNodeBatchChange` | `GetFunctionJob` | 返回作业摘要与逐项结果 |
| `BatchDeleteNodes` | `BatchDeleteFunctionInstances` | 保留现行同步处理语义与受围栏保护实例的拒绝规则 |
| `InvokeFunction` | 保留 | 请求中的资源字段改为 `instance_id`，调用模式与结果结构保留 |
| 账号、代码包、地域、SCF 导入和垃圾回收方法 | 保留 | 更新其涉及实例或作业的字段、文案和类型 |

具体要求：

1. `ListFunctionInstancesReq/Rsp`、`UpdateFunctionInstanceReq/Rsp`、`GetFunctionJobReq/Rsp` 按方法名统一命名。
2. 异步创建/部署/运行配置请求分别使用对应的 `Submit...Req`；同步和异步删除可继续共用 `BatchDeleteFunctionInstancesReq`，不为命名引入重复结构。
3. 管理请求中的 `node` / `nodes` 改为 `instance` / `instances`；`deployments`、通用响应 `items`、`job_id`、`item_id` 保留。
4. `SCFFunctionCandidate`、`SCFFunctionImportResult` 中的目录 `node_id` 改为 `instance_id`；`SCFFunctionRef.function_name` 保留。
5. `CollectGarbageRsp.deleted_nodes` 改为 `deleted_instances`，`node_batches` 改为 `function_jobs`；同步 Admin 手写 JSON 解码和日志。
6. 管理协议字段编号、字段类型和枚举数值可保留以减少无关改动，但不承诺混合版本兼容；不增加 `reserved` 或旧名 alias。
7. 更新 `.proto` 后重新生成 `.pb.go` / `.trpc.go`。生成目录内的 `validation.go`、`common_alias.go` 与测试为手写内容，随目录移动并审查，不得被清理命令误删。
8. `validation.go` 的校验器、客户端显式枚举解码、Web 枚举字面值及 JSON snake_case/camelCase 两种序列化行为必须逐项核对。

### 2.3 作业操作与状态

| 当前 `c_operation` | 目标值 | 目标枚举后缀 |
| --- | --- | --- |
| `create_nodes` | `create_instances` | `CREATE_INSTANCES` |
| `deploy_nodes` | `deploy_instances` | `DEPLOY_INSTANCES` |
| `delete_nodes` | `delete_instances` | `DELETE_INSTANCES` |
| `update_runtime_configs` | 保留 | `UPDATE_RUNTIME_CONFIGS` |

目标枚举前缀为 `FUNCTION_JOB_OPERATION_`、`FUNCTION_JOB_STATUS_`、`FUNCTION_JOB_ITEM_STATUS_`。存储常量、API 枚举映射、worker 分派和查询条件同步修改，特别检查 `store/node_batch.go` 内硬编码的 `create_nodes`。

新生成的作业 ID 前缀从 `node-batch-` 改为 `function-job-`，覆盖普通作业及运行配置作业的生成点；ID 仍是不透明标识，保留已有 ID 的值，不让查询或恢复逻辑依赖新前缀。

状态值保持现状，包括存储内部的 `reconciliation_required`。它参与重启恢复与再协调，不能因公开枚举中没有同名状态而删除、重置为失败或擅自新增外部状态语义。

### 2.4 同名 ID 的边界

| 所在位置 | 本轮处理 |
| --- | --- |
| 云函数管理 RPC、store、管理客户端、库存/删除快照 | `node_id` 改为函数 `instance_id` |
| 创建函数资源的 `Metadata["node_id"]` | 改为 `Metadata["instance_id"]`；Collector 构造方与云函数模块读取方同步 |
| Collector `marketfetch.Request.node_id` | 保留：该共享执行载荷同时服务 SCF 和 local，local 会填入本地执行来源名称 |
| `MarketFetchBatchCompleted.node_id` | 保留共享执行来源语义；本轮不修改 `packages/marketfetchpb` 协议 |
| `items[].instance_id` / `targets[].instance_id` | 保留 Collector 任务实例语义 |
| Storage `node_id` / `data_node_id` | 保留 DataNode 语义 |
| Monitor `node_id` / `instance_id` | 保留主机身份与报告进程身份 |
| `MOOX_NODE_ID` 等共享报告字段 | 按报告者语义保留，不替换成函数管理 ID |

CLI 调用包含两层结构：外层 `InvokeFunctionReq.node_id` 改为 `instance_id`；`event_data` 内的采集执行载荷 `node_id` 继续遵循共享契约。二者不能用一次字符串替换同时修改。

共享执行载荷以后若统一命名，可单独设计 `executor_id` 等概念。本轮不把 local 来源伪装成云函数实例，也不通过清空该字段改变既有来源追踪行为。

## 3. 数据库定义与持久化处理

### 3.1 五张表的目标

| 当前表 | 目标表 | 列变化 |
| --- | --- | --- |
| `t_cloud_nodes` | `t_cloud_function_instances` | `c_node_id → c_instance_id`；`c_node_type → c_function_type` |
| `t_cloud_accounts` | 保留 | 无 |
| `t_cloud_function_packages` | 保留 | 无 |
| `t_cloud_node_batches` | `t_cloud_function_jobs` | 无；操作值按 §2.3 修改 |
| `t_cloud_node_batch_items` | `t_cloud_function_job_items` | `c_node_id → c_instance_id` |

账号全局唯一、包/实例按空间隔离、软删除和包引用校验保持现状。此轮不补充账号/包/实例之间的新外键，不把 `c_function_name` 与 `c_instance_id` 合并，不删除包版本、部署标识或生命周期字段。

现行 schema 已在空 SQLite 中核对：5 张业务表、10 个显式索引（5 个 UNIQUE）、3 个 mtime 触发器、1 个复合外键；`integrity_check` 为 `ok`，`foreign_key_check` 为空。SQLite 内建 `sqlite_sequence` 不计入业务表。

### 3.2 三张变更表的目标 DDL

以下是变更表的目标定义片段，用于实施时逐项核对；完整 schema 还须保留现行账号表、代码包表及其索引和触发器。字段类型、默认值和约束沿用基线。

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_cloud_function_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_instance_id TEXT NOT NULL,
    c_lifecycle_id TEXT NOT NULL DEFAULT '',
    c_lifecycle_operation_id TEXT NOT NULL DEFAULT '',
    c_provider TEXT NOT NULL DEFAULT 'tencent-scf',
    c_cloud_account_id TEXT NOT NULL DEFAULT '',
    c_package_id TEXT NOT NULL DEFAULT '',
    c_package_version TEXT NOT NULL DEFAULT '',
    c_deployment_id TEXT NOT NULL DEFAULT '',
    c_function_type TEXT NOT NULL DEFAULT 'scf-event',
    c_trigger_type TEXT NOT NULL DEFAULT '',
    c_region TEXT NOT NULL DEFAULT '',
    c_namespace TEXT NOT NULL DEFAULT '',
    c_function_name TEXT NOT NULL DEFAULT '',
    c_metadata TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_function_instances_space_instance
ON t_cloud_function_instances (c_space_id, c_instance_id);
CREATE INDEX IF NOT EXISTS idx_cloud_function_instances_deleted ON t_cloud_function_instances (c_is_deleted);

CREATE TABLE IF NOT EXISTS t_cloud_function_jobs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_job_id TEXT NOT NULL,
    c_operation TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_total_count INTEGER NOT NULL,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_function_jobs_space_job
ON t_cloud_function_jobs (c_space_id, c_job_id);

CREATE TABLE IF NOT EXISTS t_cloud_function_job_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_job_id TEXT NOT NULL,
    c_item_id TEXT NOT NULL,
    c_item_index INTEGER NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_request_json TEXT NOT NULL,
    c_publish_lease_recovery_owned INTEGER NOT NULL DEFAULT 0,
    c_result_summary TEXT NOT NULL DEFAULT '',
    c_error_message TEXT NOT NULL DEFAULT '',
    c_started_at DATETIME,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (c_space_id, c_job_id)
        REFERENCES t_cloud_function_jobs (c_space_id, c_job_id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_function_job_items_job_index
ON t_cloud_function_job_items (c_space_id, c_job_id, c_item_index);
CREATE INDEX IF NOT EXISTS idx_cloud_function_job_items_status_id ON t_cloud_function_job_items (c_status, c_id);

CREATE TRIGGER IF NOT EXISTS update_cloud_function_instances_mtime
AFTER UPDATE ON t_cloud_function_instances
BEGIN
    UPDATE t_cloud_function_instances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;
```

对应旧的 5 个相关索引、实例 mtime 触发器、作业明细复合外键都必须同步。账号/代码包的索引和触发器继续保留；完整 schema 中旧账号索引的 `DROP INDEX IF EXISTS idx_cloud_accounts_account` 属于历史清理语句，可按新项目原则移除。

### 3.3 store、JSON 与初始化

1. 更新 `models.go` 中类型、GORM column tag 和 `TableName()`。把两张 Job 表也加入模型映射测试。
2. 审查 `node.go` 中过滤、upsert 冲突键和生命周期条件；唯一性保持 `(c_space_id, c_instance_id)`。
3. 审查 `node_batch.go` 的原生 JOIN、统计查询，`garbage.go` 的包引用查询和作业清理子查询；GORM 类型重命名不会修改这些字符串。
4. `c_request_json` 的 protojson 写入与回读必须同时切换。改名涉及 `nodeId → instanceId` 等默认 camelCase JSON 键，以及管理 HTTP 使用的 snake_case 字段。
5. 函数目录 `c_metadata` 中的 `node_id` 改为 `instance_id`，保留 `function_name`、`deployment_ready`、Timer 读回与运行配置等既有键及含义。
6. schema 文件名、`schema.go` 的 `go:embed`、schema CLI、bootstrap 和部署包中 SQL 资产路径一起修改。
7. 默认数据库文件、配置和 `MOOX_CLOUDNODE_DB_PATH` 一起改名；数据库 0600 权限、SQL 值不进日志、连接池与 WAL 配置保持现状。
8. 包 GC 必须继续根据未删除实例引用判断，不得把“实例表名查错”误判为包没有引用。作业明细仍通过外键级联清理，持有待恢复租约的作业仍受保护。

### 3.4 跨模块持久化值

| 位置 | 处理 |
| --- | --- |
| Admin `t_placements.c_component_id` | 服务身份从 `cloudnode` 切换到最终模块名；表与列不改 |
| Admin 发布租约 `c_holder_id` | `cloudnode-recovery:` 前缀切换；租约/操作表归属与围栏语义不变 |
| Monitor 服务名、报告进程 ID、指标标签 | 跟随新组件身份；Monitor schema 不因本次改名而改 |
| Collector `t_collector_fetch_plans.c_plan/c_progress` | 基线结构使用函数名称和进度映射，本轮不改表结构，不改已有云端函数名 |
| 部署状态、网关目录和 caller 密钥资产 | 由新的组件定义重新生成和安装，不沿用只含旧 caller 的身份包 |

新版本启动逻辑不自动识别或迁移旧库。已有环境的切换必须在 §8 的部署演练中单独处理；修改默认 DB 路径不能被当成数据已经转换完成。

## 4. 代码与配置影响清单

以下路径均为基线位置；实施时把旧路径映射到最终目录。

| 工作包 | 重点文件 | 要完成的内容 |
| --- | --- | --- |
| 工作区与依赖 | `go.work`、模块/协议生成包的 `go.mod`、CLI/Collector `go.mod`、根 `Makefile` | 移动两个 Go module，更新 require/replace/import 和生成入口，检查依赖图 |
| 协议 | `modules/cloudnode/proto/` | §2 的服务、方法、类型、字段、枚举与手写校验器 |
| 核心服务 | `internal/rpc/`、`internal/store/`、`internal/bootstrap/`、`internal/config/`、`cmd/` | 实例/作业模型、worker、SQL、启动注册、配置及日志 |
| 服务目录 | `packages/servicecatalog/catalog.yaml` 及测试 | 组件、二进制、RPC、methods/read_only_methods/ACL、console_name、doctor、依赖、路径及路由摘要 |
| 主机网关 | `modules/hostgateway/internal/snapshot/`、`router/`、`controlplane/` 及测试 | 校验新目录、服务全名和方法；重建并更新所有接收该目录的 host-gateway 二进制 |
| Admin | `internal/console/policy.go`、`internal/service/garbage/collector.go`、部署拓扑相关代码 | 授权路由、垃圾回收服务与 JSON 字段、新组件身份 |
| Collector | `internal/scfinvoker/`、`internal/localexec/client.go`、`internal/marketfetch/plan_reconciler.go` | 管理客户端、协议类型、查询/发布/回读、metadata 动态键；遵守 §2.4 的共享载荷边界 |
| CLI | `internal/adminclient/cloudnode.go`、`gateway.go`、`internal/command/collector*.go` | 管理服务名、库存/删除 JSON、实例 ID、作业轮询、手写枚举解析与错误输出 |
| 构建与部署 | `scripts/build/build.sh`、`internal/setup/unitpackage/package.go`、`unitdeploy/software.go`、`unitinstall/state.go` | 构建 target、两种二进制、helper/config/schema 打包、安装顺序及依赖 |
| 默认部署配置 | 根及 `modules/cli/internal/configinit/` 的 `moox.toml.example` | placements 中组件身份及生成配置路径 |
| Web API | `api/cloud-node.ts`、`cloud-account.ts`、`function-package.ts`、`api/admin/signed-client.ts` | 服务路由、类型、字段、权限与免空间规则 |
| Web 页面/工具 | `views/collector/cloud-node/`、`task-management/index.vue`、`utils/cloud-node-batch-change.ts`、工作流测试 | 实例/包分区、选择快照、删除前核对、作业轮询、文件名与 import |
| 监控 | `packages/report/module_health_checks.go`、Collector `marketfetch/metrics.go`、Monitor `bootstrap/module_health.go` / `observability/overview.go` | 模块身份、健康检查 ID、指标前缀与失败 reason 的生产/消费配对 |
| 脚本 | `test-trpc-plugin-config.sh`、`verify-event-contracts.sh`、`test-egress-dns-collector-scf-e2e.sh` 等 | 模块列表、路径、环境变量和直接 SQL |
| 发布资产 | `web-host/internal/statik/statik.go`、部署包/身份包/网关配置 | 根据新源码重新生成，不手工修改压缩前端内容 |
| 文档 | README、模块索引、总体设计、部署与运维、模块文档、VitePress 导航、仓库相关 skills | 新名称、正确 API 路径、实例与代码包定义、当前命令示例 |

基线按 cloudnode 及常见拼写匹配到 217 个内容文件，包含测试、生成文件和文档，仅表示引用面。还应单独搜索文件/目录名，避免漏掉内容中没有旧名的 `web/src/utils/cloud-node-batch-change.ts`。

## 5. 执行阶段与依赖

各阶段用于组织开发和验收，最终按完整协议版本统一交付，不把中间状态分别部署到互相调用的服务。

| 阶段 | 工作 | 依赖 | 阶段完成条件 |
| --- | --- | --- | --- |
| 0 | 冻结模块名与边界、刷新基线清单 | 无 | 命名矩阵唯一，暂定项清零，确认相关分支变化 |
| 1 | 移动模块、冻结新 proto 与 schema | 0 | 生成路径正确，目标 schema 可载入，生成包可编译 |
| 2 | 完成服务内部实例/作业重命名 | 1 | 核心模块及嵌套协议模块测试通过 |
| 3 | 更新目录、Admin、Collector、CLI | 1、2 | 管理调用与鉴权端到端一致，动态 JSON 与共享载荷边界有回归覆盖 |
| 4 | 更新前端实例/代码包/作业交互 | 1，可与 3 并行 | Web 类型检查、工作流测试和构建通过 |
| 5 | 更新部署身份、配置、监控和脚本 | 1，可与 3、4 并行 | 配置/制品/身份包/网关路由契约通过 |
| 6 | 文档、嵌入制品与残留审查 | 2～5 | 当前文档与构建产物一致，旧名仅在明确保留范围中出现 |
| 7 | 集成验证、提交、全量验收 | 6 | schema、协议、业务和部署验证完成，干净树生成检查通过 |
| 8 | 环境切换演练与发布 | 7，独立部署任务 | 数据与身份切换有记录，真实调用链验收通过 |

### 阶段 0：准备与冻结

- [ ] 最终选择 `cloudfunction` 或 `serverless`，更新 §1.2 和全部命令示例。
- [ ] 核对主线新增协议、schema、部署规则；把基线差异补充到计划，而非重复覆盖近期实现。
- [ ] 保存旧名称内容与路径清单，标明代码、生成物、当前文档、历史材料和共享 `node_id` 保留项。
- [ ] 确认本次实施不修改已有云端函数名称、账号 ID、包 ID、实例逻辑 ID 的值。

### 阶段 1：协议和 schema

- [ ] 移动模块及生成模块，更新 `go.work`、`go.mod` 与 proto Makefile。
- [ ] 按 §2 修改 API 与 JSON 字段，保留所需的手写生成包辅助代码。
- [ ] 按 §3 修改三张表及对应索引/触发器/FK，保留账号、包的完整定义。
- [ ] 更新 SQL embed 与 schema CLI，运行空库初始化和协议包测试。

### 阶段 2：核心服务

- [ ] 完成 store 的模型、方法、SQL 字符串和冲突键修改。
- [ ] 完成 RPC 实例管理、SCF 导入、调用、作业提交/查询/执行及垃圾回收的类型修改。
- [ ] 修改持久化请求写入/读取、metadata 键、操作值和恢复 holder 前缀。
- [ ] 保持锁粒度、生命周期围栏、创建预留和接管规则，覆盖重启后新格式作业恢复。
- [ ] 配置 `function_job`、新数据库路径、caller 校验与健康/metrics 服务名同步。

### 阶段 3：调用链和权限

- [ ] 服务目录使用新 RPC 全名；更新所有方法列表、只读方法和调用方 ACL。
- [ ] 同时更新 SecretMgr 和 CollectorPublishLease 中允许调用的旧 `cloudnode` 身份。
- [ ] Admin 控制台权限与垃圾回收改用新服务和字段；继续禁止 console 调用垃圾回收及内部租约方法。
- [ ] Collector 管理请求及创建 metadata、CLI 的手写 JSON 和作业枚举解析同步；外层资源 ID 与执行载荷 ID 分别处理。
- [ ] 复核请求空间、签名 caller、超时预算、发布租约错误分类和进行中作业状态。

### 阶段 4：前端

- [ ] 页面文案明确区分“云函数实例”“代码包”“管理作业”，维护包到实例的关联展示。
- [ ] 建议 API 文件改为 `function-instance.ts`，实例视图目录改为 `function-instance/`，作业工具按 `function-job` 命名；原 `function-package.ts` 和云账号 API 保持领域名称。
- [ ] 资源 ID、批量选择、删除快照、执行前复核、批次结果/状态枚举全部切换。
- [ ] 现有 `/collector/tasks?tab=executors` 页面入口可保留，菜单组织不随后台模块目录重建。
- [ ] 修改实际 `/api/admin/<新模块名>/*` 路由及免空间正则，清理仍写 `/api/service/cloudnode/*` 的过时命令文案。
- [ ] 工作流测试使用 Vitest；若移动 `web/tests/cloud-node-workflows.spec.ts`，同步 `web/vitest.config.ts` 的文件列表。

### 阶段 5：运行身份、部署和监控

- [ ] `gateway_client.caller`、`caller-<新模块名>.key`、组件 ID、二进制和模块路径一致。
- [ ] 健康检查 `11411`、原生 tRPC `11401` 等现有端口及 960 秒 RPC 预算保持，改名不分配新端口。
- [ ] 重生成 unit package、caller 身份包和主机网关配置，审查各主机目录版本变化后更新黄金摘要。
- [ ] 重建并更新所有接收该目录的 `host-gateway` 二进制。网关用编译时内嵌 catalog 校验快照中的全部服务，旧二进制会拒绝包含新服务名的整份目录；只更新配置或只更新 control 主机均不充分。
- [ ] 按内嵌 servicecatalog 的直接及传递依赖核对制品集合，包含管理 CLI、运行管理工具、Admin、Access、Monitor 和 gatewayclient 消费者；发布使用同一提交生成的整套制品。
- [ ] 修改新服务环境变量、默认 DB 文件、数据目录与 doctor 配置路径。
- [ ] 修改模块健康 ID、`moox_cloudnode_*` 指标前缀以及 Collector/Monitor 成对使用的 `reason=cloudnode`。
- [ ] E2E 环境变量、默认数据库路径和直接查询 SQL 同步。
- [ ] 新建函数的默认自动命名前缀可从 `moox-cloudnode` 改为最终模块名；显式指定及已存在的云端函数名保持原值。

### 阶段 6：文档和制品

- [ ] 当前模块文档改为“云函数管理”或“Serverless 管理”，更新根/模块 README、总体设计表格和 VitePress 导航。
- [ ] 现行部署与使用文档更新到新命令；历史故障记录保留原始日志和原名，补充命名对应说明即可。
- [ ] 本计划的基线旧路径链接在代码移动后改为有效新路径，或改为不含链接的历史路径文字。
- [ ] 前端构建后再生成 statik，随后构建 web-host，避免页面仍调用旧服务。
- [ ] 检查旧模块路径、服务字符串、表名、字段、枚举、配置键、脚本目标和文件名残留。

## 6. 验证矩阵

| 场景 | 必须验证的结果 |
| --- | --- |
| 代码包与实例关系 | 一个包可关联多个实例；包元数据和实例部署信息分开；被活动实例引用的包不会被错误回收 |
| 空间隔离 | 同空间实例 ID 冲突被拒，不同空间同 ID 正常；作业查询及明细关联不跨空间 |
| 创建与重试 | 重复创建、竞争 reservation、已存在云函数的归属校验保持；未知结果不能被误认成成功或直接重建 |
| 发布围栏 | Collector 受保护实例必须经过发布租约；生命周期和操作 token 不因重命名漏传 |
| 异步作业 | 创建/部署/删除/配置四类操作都可提交、查询和执行；部分失败、重试、再协调与新格式持久化后的重启恢复有效 |
| 同步调用 | `InvokeFunction` 使用实例 ID 定位资源；SCF 同步/异步模式、响应和超时预算保持 |
| Timer 与环境 | Timer 启停、cron、期望/实际状态回读、部署代码时的完整环境保留及包 ID 更新行为保持 |
| SCF 导入 | 新函数导入、已有实例识别、软删除恢复及阻止导入的原因仍正确 |
| 管理 JSON | snake_case HTTP、camelCase protojson、手写 map、CLI 输出和 Web 类型使用一致的新字段 |
| 共享执行载荷 | SCF/local 的执行请求和结果不因管理实体改名改变 `node_id` 含义；任务 `instance_id` 保持 |
| 权限 | Collector、CLI、Admin 的允许调用正常；console 的内部调用仍被拒绝；账号/地域免空间规则正确 |
| GC | dry_run 不删除；活动实例引用、进行中作业、待恢复租约与明细级联删除规则保持 |
| 部署与可观测性 | 服务启动、caller 密钥装配、路由发现、健康身份和失败 reason 都使用新组件名 |
| 全主机网关目录 | 各主机的新网关二进制接受包含新 RPC 服务的完整快照；不存在未知服务导致整份目录被拒绝的情况 |

优先更新现有行为测试。对缺少覆盖的实际边界补测试，避免仅镜像字段替换的无行为断言。

## 7. 实施后的命令清单

以下命令面向后续代码实施，不代表本次计划编写已执行。默认从仓库根运行，模块名先按 `cloudfunction` 展示；生成需要 `trpc-open`、`protoc`、`protoc-gen-go` 等工具。

### 7.1 协议、schema 与局部回归

```bash
make -C modules/cloudfunction/proto all
go test -count=1 ./modules/cloudfunction/schema ./modules/cloudfunction/cmd/cli
go test -count=1 ./modules/cloudfunction/...
go test -count=1 ./modules/cloudfunction/proto/cloudfunctiongen/...
go test -count=1 ./packages/servicecatalog/... ./packages/report/...
make check-go-module-graph
```

嵌套的协议 Go module 需单独测试，不能假设上一层 `./...` 会自动遍历它。Collector/Admin/CLI/Monitor 的整合验证使用仓库工作区测试入口；该入口包含特定模块的禁内联参数和 CLI 并发约束。

按仓库约定，逐个将所有模块 schema 载入空 SQLite，并检查外键与完整性：

```bash
python3 - <<'PY'
from pathlib import Path
import sqlite3

for path in sorted(Path('modules').glob('*/schema/*.sql')):
    db = sqlite3.connect(':memory:')
    db.execute('PRAGMA foreign_keys = ON')
    db.executescript(path.read_text(encoding='utf-8'))
    assert db.execute('PRAGMA foreign_key_check').fetchall() == [], path
    assert db.execute('PRAGMA integrity_check').fetchall() == [('ok',)], path
    db.close()
    print(path)
PY
```

另外核对目标 schema 的五张表、10 个显式索引、3 个触发器和复合 FK；确保新库不生成旧三张表，也不重新引入已经删除的 invocation 记录表。

### 7.2 工作区、部署、前端与构建

```bash
make test-go
make test-deployment-packages test-host-placement-deployment
make test-host-identity-bundles test-unit-install test-unit-runtime test-unit-bootstrap test-unit-core
make check-boundaries test-greenfield-contract test-event-contracts
make test-web
pnpm --dir web run check:detail-pages
make -C web-host statik
# 构建同一提交的完整制品，包含所有主机使用的 host-gateway 及内嵌服务目录的消费者。
./scripts/build/build.sh
make test-docs-architecture
git diff --check
```

完整 CI 还应执行既有文档构建和对应质量门禁。无需为了改名重复运行无关线上实验；本地或模拟测试未覆盖的真实 SCF/网关行为列入部署演练。

### 7.3 残留与生成一致性

```bash
rg -n -i 'cloud[_-]?node' modules packages web scripts config skills Makefile go.work
rg --files --hidden -g '!.git' -g '!node_modules' -g '!web/dist' -g '!docs/.vitepress/dist' -g '!bin' -g '!release' | rg -i 'cloud[_-]?node'
rg -n 't_cloud_nodes|t_cloud_node_batches|t_cloud_node_batch_items|c_node_id|node_id|NodeID|NodeId' modules/cloudfunction
git diff --check
```

`rg` 无命中返回 1 是预期结果；有命中时逐条分类。云函数管理代码中的旧名应清零，历史文档中的真实原名和 §2.4 的共享身份字段按明确保留清单审查。

`make proto-check` 会先执行全仓协议生成，再要求整个工作树干净。正确顺序是：完成生成与审查 → 提交源文件、文档和生成物 → 在干净工作树执行 `make proto-check`，或在未完成其他质量门禁时执行包含它的 `make verify`。若生成检查产生差异，检查生成工具/输入，修正并提交后再重跑受影响检查。

## 8. 已有环境的切换安排

本节是未来部署任务的准备要求，不在当前文档任务中执行。新安装环境直接初始化新 schema；已有环境不能通过重命名 DB 路径自动变成新结构。

1. 盘点已有实例、代码包、非终态作业、发布租约和新旧组件 placements，记录待保留数据及对应云端函数名称。
2. 在发布窗口暂停新的管理变更入口及自动写入来源，包括 Collector 对账/发布和 Admin 定时 GC；等待在途作业完成并确认操作占用释放。仍处于恢复/未知状态的作业先完成再协调，不直接删库或跳过围栏。
3. 排空后停止旧 CloudNode 及相关写入进程，按现有维护/停用机制禁止守护进程重新拉起，确认旧进程退出且数据库没有并发写入，再保存一致的数据与配置快照。数据库、Admin 部署身份、网关配置和 caller 密钥包作为同一版本组处理；停止与恢复哪些组件应记录到部署清单。
4. 在切换前另行确定现有数据是通过一次性离线转换保留，还是在明确选择的新环境中重新初始化。离线转换不加入业务进程启动逻辑，也不建立长期双版本兼容层。
5. 如果保留数据，转换必须覆盖表/列/索引/触发器、作业操作值、请求 JSON、metadata 资源键及组件身份；保持账号/包/实例 ID 的实际值、云函数名称与 COS 对象位置。先在副本上验收，再用于目标环境。
6. 在维护窗口安装同一版本的服务、管理/运行工具、前端制品、部署配置、身份包和网关目录，更新所有接收目录的 host-gateway 二进制。旧网关不能接受新目录，新网关也不应继续使用旧目录；按匹配的制品和配置组切换后启动，不以混合版本维持管理请求。验证新身份、健康检查及各主机目录接受状态。
7. 验收账号/包/实例列表、作业提交查询、受控部署/配置更新、SCF 调用、Timer 回读、GC dry_run 和监控归属；确认旧组件不会被守护进程再次拉起，再恢复自动写入和管理入口。
8. 回退时停止新管理写入并恢复同一套旧制品、配置和数据快照。若发布后已经发生云端创建/部署/删除，本地快照无法回退云端副作用，需先核对云端实际状态并恢复目录一致性。

环境切换在代码实施和演练完成后单独执行，记录实际数据处理方式、发布版本及结果。本轮只交付计划文档。

## 9. 最终交付与完成标准

- [ ] 最终模块名已经确定，文档和代码没有互相矛盾的命名。
- [ ] 五张业务表准确表达账号、代码包、实例、作业、作业明细；没有混淆包与实例。
- [ ] 管理域中的 `CloudNode/NodeBatch` 及旧协议、字段、表和组件身份已统一替换。
- [ ] Collector 任务实例、共享执行来源、Storage 节点和 Monitor 主机/进程身份没有被误改。
- [ ] 全部管理调用方、ACL、动态 JSON、持久化 reader/writer 和部署资产同步切换。
- [ ] 实例创建与发布围栏、异步恢复、Timer、调用和 GC 的原有业务约束通过回归。
- [ ] 所有模块 schema 均可载入空 SQLite，完整性和外键检查通过。
- [ ] 新协议生成物、前端嵌入物与源码一致；旧名残留已按清单审查。
- [ ] 现行文档、导航和构建示例可用；历史记录保留其基线语义。
- [ ] 按仓库要求提交本次改动并推送工作分支，记录 commit、测试结果和尚未进行的发布步骤。

本版进度：阶段 0 的代码盘点与领域模型讨论已完成；模块名待明确，阶段 1～8 均未执行。
