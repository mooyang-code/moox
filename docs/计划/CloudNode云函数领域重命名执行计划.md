# CloudNode 云函数领域重命名执行计划

v3（2026-10-23，实施中）。本计划把 CloudNode 的模块身份、云函数实例模型与异步管理作业统一命名，并保持云函数代码包、函数实例和单次调用的概念独立。

v2 相对 v1 的变化：消除 `function_type` 撞名；删除无调用方的 `UpdateNode`、同步 `BatchDeleteNodes` 及遗留字段；`biz_type` 统一为 `workload_type`；写出全部请求/响应类型名；前端目录改为 `cloud-function/` 下分实例与代码包；schema 索引和触发器按基准命名；定下默认函数名前缀与模块文档文件名；补全残留扫描范围、断链检查、提交拆分和已有环境的离线转换草稿。

代码基线：`origin/main@3da2faedc979fab121f01e6278acc24c56cba5cf`。计划所在分支：`codex/cloudnode-naming-review`。实施前重新核对主线变化，以下文件和接口清单以此基线为准。

用户已授权按计划编码、独立 Agent 审查、编译并发布正式环境；正式环境是未上线的新项目，允许重置数据，不实现旧协议兼容和数据迁移。现行行为见[云节点模块文档](../模块/云函数管理.md)，现行协议见 [cloudnode.proto](../../modules/cloudfunction/proto/cloudfunction.proto)，现行表结构见 [cloudnode.sql](../../modules/cloudfunction/schema/cloudfunction.sql)。

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
- 代码包与实例共用的业务用途（现值 `market_fetcher` 等）统一叫 `workload_type`。基线中代码包表列已是 `c_workload_type`，接口和实例 metadata 却叫 `biz_type`，本轮一并统一（§2.1）。

### 1.2 已确认的模块名

用户已确认模块名为 **`cloudfunction`**。当前实现直接管理腾讯云 SCF 的函数、代码包、触发器和调用，这个名称与现有职责一致。本文路径、协议与命令统一采用该名称。

| 项目 | 确认值 |
| --- | --- |
| 模块目录/组件 ID/caller | `cloudfunction` |
| 服务二进制 | `moox-cloudfunction` |
| schema CLI 二进制 | `moox-cloudfunction-cli` |
| 协议文件/生成目录 | `cloudfunction.proto` / `cloudfunctiongen` |
| protobuf package | `trpc.moox.cloudfunction` |
| RPC 服务 | `CloudFunctionMgr` |
| 默认 Go 协议包别名 | `cloudfunctionpb` |
| 数据库文件 | `moox_cloudfunction.db` |
| schema 文件 | `cloudfunction.sql` |
| 环境变量前缀 | `MOOX_CLOUDFUNCTION_` |
| 控制台服务路由 | `/api/admin/cloudfunction/*` |
| 指标前缀 | `moox_cloudfunction_` |
| 新建函数默认名前缀 | `moox-cloudfunction`（仅在请求未指定 `function_name` 时使用） |
| 前端目录 | `web/src/views/collector/cloud-function/` |
| 模块文档 | `docs/模块/云函数管理.md` |
| 中文模块名称 | 云函数管理 |

默认函数名前缀可以直接改：Collector 创建采集函数时总是显式传入 `function_name`（`moox-fetcher-<space>…`），SCF 导入也只按 `moox-fetcher-` 前缀识别（`scf_sync.go:423`），没有逻辑依赖 `moox-cloudnode` 前缀。已存在的云端函数名不变。

### 1.3 实施范围

完成模块目录、协议、实例模型、作业模型、SQLite schema、所有管理调用方、网关身份、前端、部署制品、监控契约与现行文档的同步改名。各调用方在同一个完整变更中切换，不保留旧 RPC、旧 JSON 字段、旧表名或旧组件名的运行时别名。

遵循仓库 `AGENTS.md` 的新项目原则，不引入历史版本兼容层，并清理改名范围内已无调用方的接口、字段和函数（§2.5）。改名保持现有业务行为，包括空间隔离、发布租约、生命周期围栏、创建预留、异步恢复、SCF 限频、调用预算、Timer 回读和垃圾回收规则。

以下事项留在原有边界内：

- `packages/cloudprovider`、腾讯云 SCF/COS/CLS 适配器及厂商 API 名称（其中提到 CloudNode 的注释随改名更新）。
- Storage DataNode、主机/进程身份、Collector 任务实例和采集批次。
- 新增云厂商、容器运行时、统一计算资源抽象或部署历史表。
- 重新设计函数类型、包版本策略、运行配置结构或状态机。
- 自动删除 COS 对象或云端函数；数据库重置遵循 §8 的本次发布授权。
- `packages/wirecompress/dicts/default-v1.zdict` 是带版本号的压缩字典，内含旧服务名样本，本轮不重新训练。

## 2. 命名映射与协议契约

### 2.1 管理领域的实体、字段和文件

| 当前 | 目标 |
| --- | --- |
| `CloudNode` | `FunctionInstance` |
| `NodeID` / `NodeId` / `node_id` | `InstanceID` / `InstanceId` / `instance_id` |
| `NodeIDs` / `NodeIds` / `node_ids` | `InstanceIDs` / `InstanceIds` / `instance_ids` |
| `NodeType` / `node_type` | `FunctionType` / `function_type` |
| `BizType` / `biz_type`（实例、列表过滤、SCF 候选、代码包接口、实例 metadata 键） | `WorkloadType` / `workload_type` |
| `SCFFunctionCandidate.function_type`（厂商函数类型） | `scf_type` |
| `CloudRegion.max_nodes` | `max_instances` |
| `NodeBatch` / `NodeBatchSummary` | `FunctionJob` / `FunctionJobSummary` |
| `NodeBatchItem` / `NodeBatchItemResult` | `FunctionJobItem` / `FunctionJobItemResult` |
| `NodeBatchOperation` / `NodeBatchStatus` / `NodeBatchItemStatus` | `FunctionJobOperation` / `FunctionJobStatus` / `FunctionJobItemStatus` |
| `NodeCreateItem` / `NodeDeployItem` / `NodeDeleteItem` | `FunctionInstanceCreateItem` / `FunctionInstanceDeployItem` / `FunctionInstanceDeleteItem` |
| `NodeRuntimeConfigPatch` | `FunctionInstanceRuntimeConfigPatch` |
| `NodeBatchConfig` / `node_batch` 配置段 | `FunctionJobConfig` / `function_job`，保留 `batch_size`、`poll_interval` 字段及取值 |
| `lockSCFNode` / `lockSCFNodes` | `lockFunctionInstance` / `lockFunctionInstances` |
| `isFencedCollectorNode` 等含 `Node` 的内部函数、变量与错误文案 | 对应改为 `Instance` / “实例” |
| `CatalogRepository` 的 `GetNode`、`UpsertNode`、`ListNodes` 等方法 | `GetInstance`、`UpsertInstance`、`ListInstances` 等 |
| Collector `scfinvoker.Node` 及其 `SubmitDeleteNodes`、`GetNodeBatchChange` 等客户端方法 | `scfinvoker.FunctionInstance` 及与新 RPC 同名的方法 |
| CLI `adminclient/cloudnode.go` 及其中 `SubmitNodeBatchResponse` 等类型 | `adminclient/cloudfunction.go`，类型按本表改名 |

文件移动（`modules/cloudnode/` 整体移到 `modules/cloudfunction/` 后）：

| 当前 | 目标 |
| --- | --- |
| `internal/rpc/node.go` / `node_test.go` / `node_item_test.go` | `instance.go` / `instance_test.go` / `instance_item_test.go` |
| `internal/rpc/node_batch.go` / `node_batch_runner.go` 及测试 | `job.go` / `job_runner.go` 及测试 |
| `internal/store/node.go` / `node_batch.go` 及测试 | `instance.go` / `job.go` 及测试 |
| `internal/store/invocation.go` | 删除（§2.5） |
| `internal/store/catalog.go`、`internal/rpc/function_lock.go`、`publish_fence.go` | 文件名保留，内容按上表改名 |
| `proto/cloudnode.proto`、`proto/cloudnodegen/` | `proto/cloudfunction.proto`、`proto/cloudfunctiongen/` |
| `schema/cloudnode.sql` | `schema/cloudfunction.sql` |

表中的 `Node*` 替换仅限云函数管理领域。避免对全仓 `node`、`instance` 或 `batch` 做无范围替换。

`function_type` 的现值仍为 `scf-event` 等现有分类，`provider` 仍为 `tencent-scf`，`trigger_type` 仍为 `invoke` / `timer`。SCF 导入候选里原来的 `function_type` 是腾讯云函数的 `Type`（如 `Event`、`HTTP`），改名为 `scf_type` 后，同一协议和同一前端类型中不再出现两个取值不同的同名字段；导入弹窗的“类型”列同步改读 `scf_type`。

`workload_type` 的取值保持现值（`market_fetcher` 等），实例按 metadata 过滤的 `json_extract(c_metadata, '$.biz_type')` 改为 `$.workload_type`，代码包仍按 `c_workload_type` 过滤。

### 2.2 RPC 方法及请求/响应

| 当前方法（请求 → 响应） | 目标方法（请求 → 响应） |
| --- | --- |
| `GetNodeList(GetNodeListReq → GetNodeListRsp)` | `ListFunctionInstances(ListFunctionInstancesReq → ListFunctionInstancesRsp)`，保留过滤与 `refresh_readback` |
| `UpdateNode(UpdateNodeReq → UpdateNodeRsp)` | 删除（§2.5） |
| `SubmitCreateNodes(BatchCreateNodesReq → SubmitNodeBatchRsp)` | `SubmitCreateFunctionInstances(SubmitCreateFunctionInstancesReq → SubmitFunctionJobRsp)` |
| `SubmitDeployNodes(BatchDeployNodesReq → SubmitNodeBatchRsp)` | `SubmitDeployFunctionInstances(SubmitDeployFunctionInstancesReq → SubmitFunctionJobRsp)` |
| `SubmitDeleteNodes(BatchDeleteNodesReq → SubmitNodeBatchRsp)` | `SubmitDeleteFunctionInstances(SubmitDeleteFunctionInstancesReq → SubmitFunctionJobRsp)` |
| `SubmitUpdateNodeRuntimeConfigs(BatchUpdateNodeRuntimeConfigsReq → SubmitNodeBatchRsp)` | `SubmitUpdateFunctionInstanceRuntimeConfigs(SubmitUpdateFunctionInstanceRuntimeConfigsReq → SubmitFunctionJobRsp)` |
| `GetNodeBatchChange(GetNodeBatchChangeReq → GetNodeBatchChangeRsp)` | `GetFunctionJob(GetFunctionJobReq → GetFunctionJobRsp)`，响应仍为 `job` 摘要加 `items` 逐项结果 |
| `BatchDeleteNodes(BatchDeleteNodesReq → BatchDeleteNodesRsp)` | 删除（§2.5） |
| `InvokeFunction` | 保留；请求中的 `node_id` 改为 `instance_id`，调用模式与结果结构保留 |
| 账号、代码包、地域、SCF 导入和垃圾回收方法 | 方法名保留；更新其涉及实例、作业或 `workload_type` 的字段、文案和类型 |

具体要求：

1. 管理请求中的 `node` / `nodes` 改为 `instance` / `instances`，`node_ids` 改为 `instance_ids`；`deployments`、通用响应 `items`、`job_id`、`item_id` 保留。
2. `SubmitDeleteFunctionInstancesReq` 只服务异步删除，字段为 `instance_ids` 与发布租约两项；同步删除删除后不再有共用请求。
3. `SCFFunctionCandidate`、`SCFFunctionImportResult` 中的目录 `node_id` 改为 `instance_id`；`SCFFunctionRef.function_name` 保留。
4. `CollectGarbageRsp.deleted_nodes` 改为 `deleted_instances`，`node_batches` 改为 `function_jobs`；同步 Admin 手写 JSON 解码和日志。
5. 管理协议字段编号、字段类型和枚举数值可保留以减少无关改动，但不承诺混合版本兼容；删除的字段不留 `reserved`，不加旧名 alias。
6. 更新 `.proto` 后重新生成 `.pb.go` / `.trpc.go`。生成目录内的 `validation.go`、`common_alias.go` 与测试为手写内容，随目录移动并审查，不得被清理命令误删。
7. `validation.go` 的校验器、客户端显式枚举解码、Web 枚举字面值及 JSON snake_case/camelCase 两种序列化行为必须逐项核对。
8. 服务目录的 `methods`、`read_only_methods`、各 `acl` 段同步去掉两个删除的方法。

### 2.3 作业操作与状态

| 当前 `c_operation` | 目标值 | 目标枚举后缀 |
| --- | --- | --- |
| `create_nodes` | `create_instances` | `CREATE_INSTANCES` |
| `deploy_nodes` | `deploy_instances` | `DEPLOY_INSTANCES` |
| `delete_nodes` | `delete_instances` | `DELETE_INSTANCES` |
| `update_runtime_configs` | 保留 | `UPDATE_RUNTIME_CONFIGS` |

目标枚举前缀为 `FUNCTION_JOB_OPERATION_`、`FUNCTION_JOB_STATUS_`、`FUNCTION_JOB_ITEM_STATUS_`。存储常量、API 枚举映射、worker 分派和查询条件同步修改，特别检查 `store/node_batch.go` 内硬编码的 `create_nodes`（创建预留冲突查询）。

新生成的作业 ID 前缀从 `node-batch-` 改为 `function-job-`，覆盖 `rpc/node_batch.go` 与 `rpc/runtime_config.go` 中的全部生成点；ID 仍是不透明标识，不让查询或恢复逻辑依赖前缀。

状态值保持现状，包括存储内部的 `reconciliation_required`。它参与重启恢复与再协调，不能因公开枚举中没有同名状态而删除、重置为失败或擅自新增外部状态语义。

### 2.4 同名 ID 的边界

| 所在位置 | 本轮处理 |
| --- | --- |
| 云函数管理 RPC、store、管理客户端、库存/删除快照 | `node_id` 改为函数 `instance_id` |
| 创建函数资源的 `Metadata["node_id"]` | 改为 `Metadata["instance_id"]`；Collector 构造方（`plan_reconciler.go` 两处）与云函数模块读取方同步 |
| Collector `marketfetch.Request.node_id` | 保留：该共享执行载荷同时服务 SCF 和 local，local 会填入本地执行来源名称 |
| `MarketFetchBatchCompleted.node_id` | 保留共享执行来源语义；本轮不修改 `packages/marketfetchpb` 协议 |
| `items[].instance_id` / `targets[].instance_id` | 保留 Collector 任务实例语义 |
| Storage `node_id` / `data_node_id` | 保留 DataNode 语义 |
| Monitor `node_id` / `instance_id` | 保留主机身份与报告进程身份 |
| `MOOX_NODE_ID` 等共享报告字段 | 按报告者语义保留，不替换成函数管理 ID |

CLI 调用包含两层结构：外层 `InvokeFunctionReq.node_id` 改为 `instance_id`；`event_data` 内的采集执行载荷 `node_id` 继续遵循共享契约。二者不能用一次字符串替换同时修改。

共享执行载荷以后若统一命名，可单独设计 `executor_id` 等概念。本轮不把 local 来源伪装成云函数实例，也不通过清空该字段改变既有来源追踪行为。

### 2.5 清理无调用方的接口与字段

以下内容在基线上已无业务调用方，改名时直接删除，不迁移到新名称。阶段 0 刷新基线时用 `rg` 再确认一次仍无调用方。

| 删除项 | 依据 |
| --- | --- |
| `UpdateNode` RPC、`UpdateNodeReq/Rsp` 及服务端实现 | 只出现在 proto、服务端、测试和服务目录；Web、CLI、Collector 均不调用 |
| 同步 `BatchDeleteNodes` RPC、`BatchDeleteNodesRsp`、`store.DeleteNodesWithPreflight`、`errFencedCollectorCatalogDelete` | 该方法只删目录记录、不删云端函数；Web 删除走发布租约加 `SubmitDeleteNodes`，CLI、Collector 也只用异步删除 |
| `CloudNode.ip_address`、`CloudNode.tag` 及 metadata 中对应键的读写 | 唯一写入方是 `UpdateNode`；`CloudRegion.tag`（国内/海外）不受影响，前端实例行若需显示地域标签改按地域取 |
| `store/invocation.go` 的 `FindNodeForInvocation` | 只有测试调用；`InvokeFunction` 按实例 ID 经 `GetNode` 定位 |

删除后同步更新对应测试、服务目录方法与 ACL、前端类型（`ip_address`、`tag` 字段）和文档。

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

现行 schema 已在空 SQLite 中核对：5 张业务表、10 个显式索引（5 个 UNIQUE）、3 个 mtime 触发器、1 个复合外键；`integrity_check` 为 `ok`，`foreign_key_check` 为空。SQLite 内建 `sqlite_sequence` 不计入业务表。目标 schema 的数量不变。

### 3.2 索引、触发器命名与变更表 DDL

表名本来就要改，索引和触发器名趁此按 `AGENTS.md` 的基准文件 `modules/storage/schema/metadata.sql` 对齐为 `idx_t_<表名>_<用途>`、`trg_t_<表名>_mtime`，账号表和代码包表的索引、触发器也一起改名：

| 当前 | 目标 |
| --- | --- |
| `idx_cloud_nodes_space_node`（UNIQUE） | `idx_t_cloud_function_instances_space_instance` |
| `idx_cloud_nodes_deleted` | `idx_t_cloud_function_instances_deleted` |
| `idx_cloud_accounts_account_id`（UNIQUE） | `idx_t_cloud_accounts_account_id` |
| `idx_cloud_accounts_provider` | `idx_t_cloud_accounts_provider` |
| `idx_cloud_packages_space_package`（UNIQUE） | `idx_t_cloud_function_packages_space_package` |
| `idx_cloud_packages_workload` | `idx_t_cloud_function_packages_workload` |
| `idx_cloud_packages_deleted` | `idx_t_cloud_function_packages_deleted` |
| `idx_cloud_node_batches_space_job`（UNIQUE） | `idx_t_cloud_function_jobs_space_job` |
| `idx_cloud_node_batch_items_job_index`（UNIQUE） | `idx_t_cloud_function_job_items_job_index` |
| `idx_cloud_node_batch_items_status_id` | `idx_t_cloud_function_job_items_status_id` |
| `update_cloud_nodes_mtime` | `trg_t_cloud_function_instances_mtime` |
| `update_cloud_accounts_mtime` | `trg_t_cloud_accounts_mtime` |
| `update_cloud_packages_mtime` | `trg_t_cloud_function_packages_mtime` |

触发器补写 `FOR EACH ROW`（SQLite 只有行级触发器，语义不变），条件改为基准写法 `WHERE c_id = OLD.c_id`（`c_id` 是 rowid 别名且从不更新，与 `rowid = NEW.rowid` 等价）。基准中的 `WHEN NEW.c_mtime = OLD.c_mtime` 会改变显式写 `c_mtime` 时的行为，本轮不加。完整 schema 中旧账号索引的 `DROP INDEX IF EXISTS idx_cloud_accounts_account` 属于历史清理语句，按新项目原则移除。`schema/schema_test.go` 中断言的索引名同步更新。

以下是三张变更表的目标定义，字段类型、默认值和约束沿用基线；完整 schema 还须保留账号表、代码包表（按上表改名索引与触发器）。

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

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_instances_space_instance
ON t_cloud_function_instances (c_space_id, c_instance_id);
CREATE INDEX IF NOT EXISTS idx_t_cloud_function_instances_deleted ON t_cloud_function_instances (c_is_deleted);

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

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_jobs_space_job
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_job_items_job_index
ON t_cloud_function_job_items (c_space_id, c_job_id, c_item_index);
CREATE INDEX IF NOT EXISTS idx_t_cloud_function_job_items_status_id
ON t_cloud_function_job_items (c_status, c_id);

CREATE TRIGGER IF NOT EXISTS trg_t_cloud_function_instances_mtime
AFTER UPDATE ON t_cloud_function_instances
FOR EACH ROW
BEGIN
    UPDATE t_cloud_function_instances SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
```

### 3.3 store、JSON 与初始化

1. 更新 `models.go` 中类型、GORM column tag 和 `TableName()`。把两张 Job 表也加入模型映射测试。
2. 审查实例 store 中过滤、upsert 冲突键和生命周期条件；唯一性保持 `(c_space_id, c_instance_id)`；`biz_type` 的 metadata 过滤改为 `workload_type`。
3. 审查作业 store 的原生 JOIN、统计查询，`garbage.go` 的包引用查询和作业清理子查询；GORM 类型重命名不会修改这些字符串。
4. `c_request_json` 的 protojson 写入与回读必须同时切换。改名涉及 `nodeId → instanceId` 等默认 camelCase JSON 键，以及管理 HTTP 使用的 snake_case 字段。
5. 函数目录 `c_metadata` 中的 `node_id` 改为 `instance_id`、`biz_type` 改为 `workload_type`，删除 `ip_address`、`tag` 的读写；保留 `function_name`、`deployment_ready`、Timer 读回与运行配置等既有键及含义。
6. schema 文件名、`schema.go` 的 `go:embed`、`schema_test.go`、schema CLI、bootstrap 和部署包中 SQL 资产路径一起修改。
7. 默认数据库文件、配置和 `MOOX_CLOUDNODE_DB_PATH`、`MOOX_CLOUDNODE_PPROF_ADDR`、`MOOX_CLOUDNODE_HEALTH_ADDR` 一起改名；数据库 0600 权限、SQL 值不进日志、连接池与 WAL 配置保持现状。
8. 包 GC 必须继续根据未删除实例引用判断，不得把“实例表名查错”误判为包没有引用。作业明细仍通过外键级联清理，持有待恢复租约的作业仍受保护。

### 3.4 跨模块持久化值

| 位置 | 处理 |
| --- | --- |
| Admin `t_placements.c_component_id` | 服务身份从 `cloudnode` 切换到 `cloudfunction`；表与列不改 |
| Admin 发布租约 `c_holder_id` | `cloudnode-recovery:` 前缀改为 `cloudfunction-recovery:`；租约/操作表归属与围栏语义不变 |
| Monitor 服务名、报告进程 ID、指标标签 | 跟随新组件身份；Monitor schema 不因本次改名而改 |
| Collector `t_collector_fetch_plans.c_plan/c_progress` | 基线结构使用函数名称和进度映射，本轮不改表结构，不改已有云端函数名 |
| 部署状态、网关目录和 caller 密钥资产 | 由新的组件定义重新生成和安装，不沿用只含旧 caller 的身份包 |

新版本启动逻辑不自动识别或迁移旧库。正式环境按 §8 停机后使用新 schema 重建相关运行数据，不实现旧库转换。

## 4. 代码与配置影响清单

以下路径均为基线位置；实施时把旧路径映射到最终目录。

| 工作包 | 重点文件 | 要完成的内容 |
| --- | --- | --- |
| 工作区与依赖 | `go.work`、模块/协议生成包的 `go.mod`、CLI/Collector `go.mod`、根 `Makefile` | 移动两个 Go module，更新 require/replace/import 和生成入口，检查依赖图 |
| 协议 | `modules/cloudnode/proto/` | §2 的服务、方法、类型、字段、枚举与手写校验器；§2.5 的删除项 |
| 核心服务 | `internal/rpc/`、`internal/store/`、`internal/bootstrap/`、`internal/config/`、`cmd/`、`schema/` | 实例/作业模型、worker、SQL、启动注册、配置及日志 |
| 限频指标 | `internal/providers/tencentscf/ratelimit.go` | `moox_cloudnode_tencent_api_*` 改为 `moox_cloudfunction_tencent_api_*` |
| 服务目录 | `packages/servicecatalog/catalog.yaml` 及测试 | 组件、二进制、RPC、methods/read_only_methods/ACL、console_name、doctor、依赖、路径及路由摘要 |
| 主机网关 | `modules/hostgateway/internal/snapshot/`、`router/`、`controlplane/` 及测试 | 校验新目录、服务全名和方法；重建并更新所有接收该目录的 host-gateway 二进制 |
| Admin | `internal/console/policy.go`、`internal/service/garbage/collector.go`、`cmd/e2e-console`、部署拓扑相关代码 | 授权路由、垃圾回收服务与 JSON 字段、新组件身份 |
| Collector | `internal/scfinvoker/`、`internal/localexec/client.go`、`internal/marketfetch/plan_reconciler.go`、`plan_manager.go` | 管理客户端、协议类型、查询/发布/回读、metadata 动态键、`workload_type` 过滤；遵守 §2.4 的共享载荷边界 |
| CLI | `internal/adminclient/cloudnode.go`、`gateway.go`、`internal/command/collector*.go`、`tencent_ops_firewall_open.go` | 管理服务名、库存/删除 JSON、实例 ID、作业轮询、手写枚举解析与错误输出；命令说明中的服务名与路由 |
| 构建与部署 | `scripts/build/build.sh`、`internal/setup/unitpackage/package.go`、`unitdeploy/software.go`、`unitinstall/state.go` | 构建 target、两种二进制、helper/config/schema 打包、安装顺序及依赖 |
| 默认部署配置 | 根及 `modules/cli/internal/configinit/` 的 `moox.toml.example` | placements 中组件身份及生成配置路径 |
| Web API | `api/cloud-node.ts`、`cloud-account.ts`、`function-package.ts`、`api/admin/signed-client.ts` | 服务路由、类型、字段、权限与免空间规则 |
| Web 页面/工具 | `views/collector/cloud-node/`、`task-management/index.vue`、`utils/cloud-node-batch-change.ts`、路由配置、`tests/cloud-node-workflows.spec.ts`、`vitest.config.ts` | 实例/包分区、选择快照、删除前核对、作业轮询、文件名与 import |
| Web 检查脚本 | `web/scripts/check-detail-page-style.mjs` | 第 24、25 行硬编码的两个页面路径改为新路径，否则 `check:detail-pages` 读文件失败 |
| 监控 | `packages/report/module_health_checks.go`、Collector `marketfetch/metrics.go`、Monitor `bootstrap/module_health.go` / `observability/overview.go` | 模块身份、健康检查 ID `cloudnode-jobs`、失败 reason 的生产/消费配对 |
| 脚本 | `test-trpc-plugin-config.sh`、`verify-event-contracts.sh`、`test-egress-dns-collector-scf-e2e.sh` 等 | 模块列表、路径、环境变量（含 `MOOX_CLOUDNODE_SSH`）和直接 SQL |
| 发布资产 | `web-host/internal/statik/statik.go`、部署包/身份包/网关配置 | 根据新源码重新生成，不手工修改压缩前端内容 |
| 注释与说明 | `packages/cloudprovider/tencent/scf_environment.go`、`packages/gatewayroute/route.go`、Admin/Monitor bootstrap 注释、`examples/e2e`、`tools/zstddict/README.md` | 更新提到 CloudNode 的注释和说明 |
| 文档 | README、`modules/README.md`、总体设计、部署与运维、模块文档、VitePress 导航、仓库相关 skills | 新名称、正确 API 路径、实例与代码包定义、当前命令示例 |

引用面统计（基线，含本计划与计划索引）：`git ls-files | xargs grep -l -i 'cloud[_-]\?node'` 得到 198 个受版本管理的文件，`git ls-files | grep -i 'cloud[_-]\?node'` 得到 113 个路径名含旧名的文件；gitignore 的 `docs/.vitepress/dist` 不计入。路径名需要单独搜索，例如内容中没有旧名的 `web/src/utils/cloud-node-batch-change.ts`。

## 5. 执行阶段与依赖

各阶段用于组织开发和验收，最终按完整协议版本统一交付，不把中间状态分别部署到互相调用的服务。

| 阶段 | 工作 | 依赖 | 阶段完成条件 |
| --- | --- | --- | --- |
| 0 | 核对命名与边界、刷新基线清单、确认 §2.5 删除项 | 无 | 命名矩阵唯一，确认相关分支变化 |
| 1 | 移动模块、冻结新 proto 与 schema | 0 | 生成路径正确，目标 schema 可载入，生成包可编译 |
| 2 | 完成服务内部实例/作业重命名与删除项 | 1 | 核心模块及嵌套协议模块测试通过 |
| 3 | 更新目录、Admin、Collector、CLI | 1、2 | 管理调用与鉴权端到端一致，动态 JSON 与共享载荷边界有回归覆盖 |
| 4 | 更新前端实例/代码包/作业交互 | 1，可与 3 并行 | Web 类型检查、工作流测试和构建通过 |
| 5 | 更新部署身份、配置、监控和脚本 | 1，可与 3、4 并行 | 配置/制品/身份包/网关路由契约通过 |
| 6 | 文档、嵌入制品与残留审查 | 2～5 | 当前文档与构建产物一致，旧名仅在明确保留范围中出现 |
| 7 | 集成验证、提交、全量验收 | 6 | schema、协议、业务和部署验证完成，干净树生成检查通过 |
| 8 | 环境切换演练与发布 | 7，独立部署任务 | 数据与身份切换有记录，真实调用链验收通过 |

提交拆分：在同一个分支/PR 内，第一个提交只做目录与文件移动加 import 路径修正（`git mv`，内容改动最小），后续提交再做领域改名、删除项和调用方切换。这样 git 能识别改名，`git log --follow` 和 `blame` 保留历史；中间提交不要求单独可部署，PR 整体通过全部检查后再合并。

### 阶段 0：准备与冻结

- [x] 确认模块名为 `cloudfunction`，统一 §1.2 和全部命令示例。
- [x] 核对主线新增协议、schema、部署规则；把基线差异补充到计划，而非重复覆盖近期实现。
- [x] 保存旧名称内容与路径清单，标明代码、生成物、当前文档、历史材料和共享 `node_id` 保留项。
- [x] 用 `rg` 再次确认 §2.5 的删除项仍无调用方。
- [x] 确认本次实施不修改已有云端函数名称、账号 ID、包 ID、实例逻辑 ID 的值。
- [x] 仓库里误提交的 Mach-O 二进制 `modules/admin/cmd/cli/cli`、`modules/storage/cmd/cli/cli`、`modules/importer/importer` 会干扰残留扫描，确认无用后在独立提交中删除并补 `.gitignore`。

### 阶段 1：协议和 schema

- [x] 移动模块及生成模块，更新 `go.work`、`go.mod` 与 proto Makefile（作为第一个提交）。
- [x] 按 §2 修改 API 与 JSON 字段，删除 §2.5 的接口与字段，保留所需的手写生成包辅助代码。
- [x] 按 §3 修改三张表、全部索引与触发器名、FK，保留账号、包的完整定义。
- [x] 更新 SQL embed、`schema_test.go` 与 schema CLI，运行空库初始化和协议包测试。

### 阶段 2：核心服务

- [x] 完成 store 的模型、方法、SQL 字符串和冲突键修改，删除 `invocation.go` 与 `DeleteNodesWithPreflight`。
- [x] 完成 RPC 实例管理、SCF 导入（含 `scf_type`、`workload_type`）、调用、作业提交/查询/执行及垃圾回收的类型修改。
- [x] 修改持久化请求写入/读取、metadata 键、操作值、作业 ID 前缀和恢复 holder 前缀。
- [x] 保持锁粒度、生命周期围栏、创建预留和接管规则，覆盖重启后新格式作业恢复。
- [x] 配置 `function_job`、新数据库路径、caller 校验、健康/metrics 服务名、限频指标名和默认函数名前缀同步。

### 阶段 3：调用链和权限

- [x] 服务目录使用新 RPC 全名；更新所有方法列表、只读方法和调用方 ACL，去掉删除的方法。
- [x] 同时更新 SecretMgr 和 CollectorPublishLease 中允许调用的旧 `cloudnode` 身份。
- [x] Admin 控制台权限与垃圾回收改用新服务和字段；继续禁止 console 调用垃圾回收及内部租约方法。
- [x] Collector 管理请求及创建 metadata、CLI 的手写 JSON 和作业枚举解析同步；外层资源 ID 与执行载荷 ID 分别处理。
- [x] 复核请求空间、签名 caller、超时预算、发布租约错误分类和进行中作业状态。

### 阶段 4：前端

- [x] 页面文案明确区分“云函数实例”“代码包”“管理作业”，维护包到实例的关联展示。
- [x] 目录与文件：
  - `views/collector/cloud-node/` → `views/collector/cloud-function/`，其中 `cloud-node.vue` → `function-instance.vue`，`function-package-manage.vue` 保持文件名；`cloud-node-model.ts`、`cloud-node-batch-poller.ts` 及测试 → `function-instance-model.ts`、`function-job-poller.ts`。
  - `api/cloud-node.ts` → `api/function-instance.ts`；`utils/cloud-node-batch-change.ts` → `utils/function-job-change.ts`；`function-package.ts` 和云账号 API 保持领域名称。
  - `tests/cloud-node-workflows.spec.ts` → `tests/cloud-function-workflows.spec.ts`，同步 `web/vitest.config.ts` 的文件列表。
  - 同步 `web/scripts/check-detail-page-style.mjs` 中的页面路径。
- [x] 资源 ID、批量选择、删除快照、执行前复核、作业结果/状态枚举、`workload_type`、`scf_type`、`max_instances` 全部切换；删除 `ip_address`、`tag` 字段。
- [x] 现有 `/collector/tasks?tab=executors` 页面入口可保留，菜单组织不随后台模块目录重建。
- [x] 修改实际 `/api/admin/cloudfunction/*` 路由及免空间正则，清理仍写 `/api/service/cloudnode/*` 的过时命令文案。

### 阶段 5：运行身份、部署和监控

- [x] `gateway_client.caller`、`caller-cloudfunction.key`、组件 ID、二进制和模块路径一致。
- [x] 健康检查 `11411`、原生 tRPC `11401` 等现有端口及 960 秒 RPC 预算保持，改名不分配新端口。
- [ ] 重生成 unit package、caller 身份包和主机网关配置，审查各主机目录版本变化后更新黄金摘要。
- [ ] 重建并更新所有接收该目录的 `host-gateway` 二进制。网关用编译时内嵌 catalog 校验快照中的全部服务，旧二进制会拒绝包含新服务名的整份目录；只更新配置或只更新 control 主机均不充分。
- [ ] 按内嵌 servicecatalog 的直接及传递依赖核对制品集合，包含管理 CLI、运行管理工具、Admin、Access、Monitor 和 gatewayclient 消费者；发布使用同一提交生成的整套制品。
- [x] 修改新服务环境变量、默认 DB 文件、数据目录与 doctor 配置路径。
- [x] 修改模块健康 ID、`moox_cloudnode_*` 指标前缀以及 Collector/Monitor 成对使用的 `reason=cloudnode`。
- [x] E2E 环境变量、默认数据库路径和直接查询 SQL 同步。
- [x] 新建函数的默认名前缀改为 `moox-cloudfunction`；显式指定及已存在的云端函数名保持原值。

### 阶段 6：文档和制品

- [x] `docs/模块/云节点.md` 改名为 `docs/模块/云函数管理.md`，内容改为“云函数管理”；更新根/模块 README、总体设计表格和 VitePress 导航。
- [x] 更新全部指向 `云节点.md` 与 `modules/cloudnode/` 的链接（基线 10 个文件链接 `云节点.md`，7 个文档引用 `modules/cloudnode/` 路径）。`docs/.vitepress/config.ts` 设置了 `ignoreDeadLinks: true`，构建不会报断链，必须用 §7.3 的命令显式检查。
- [x] 现行部署与使用文档更新到新命令；历史故障记录与历史计划保留原始日志和原名，其中的链接改到新路径，补充命名对应说明即可。
- [x] 本计划的基线旧路径链接在代码移动后改为有效新路径，或改为不含链接的历史路径文字。
- [x] 前端构建后再生成 statik，随后构建 web-host，避免页面仍调用旧服务。
- [x] 按 §7.3 检查旧模块路径、服务字符串、表名、字段、枚举、配置键、脚本目标和文件名残留。

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
| SCF 导入 | 新函数导入、已有实例识别、软删除恢复及阻止导入的原因仍正确；候选的 `scf_type` 与实例的 `function_type` 各自取值正确 |
| 业务用途过滤 | Collector 按 `workload_type=market_fetcher` 列实例和代码包的结果与改名前一致 |
| 管理 JSON | snake_case HTTP、camelCase protojson、手写 map、CLI 输出和 Web 类型使用一致的新字段 |
| 共享执行载荷 | SCF/local 的执行请求和结果不因管理实体改名改变 `node_id` 含义；任务 `instance_id` 保持 |
| 权限 | Collector、CLI、Admin 的允许调用正常；console 的内部调用仍被拒绝；账号/地域免空间规则正确；已删除的两个方法在网关返回未知方法 |
| GC | dry_run 不删除；活动实例引用、进行中作业、待恢复租约与明细级联删除规则保持 |
| 部署与可观测性 | 服务启动、caller 密钥装配、路由发现、健康身份、限频指标和失败 reason 都使用新组件名 |
| 全主机网关目录 | 各主机的新网关二进制接受包含新 RPC 服务的完整快照；不存在未知服务导致整份目录被拒绝的情况 |

优先更新现有行为测试。对缺少覆盖的实际边界补测试，避免仅镜像字段替换的无行为断言。

## 7. 实施后的命令清单

以下命令面向后续代码实施，不代表本次计划编写已执行。默认从仓库根运行；生成需要 `trpc-open`、`protoc`、`protoc-gen-go` 等工具。

### 7.1 协议、schema 与局部回归

```bash
make -C modules/cloudfunction/proto all
go test -count=1 ./modules/cloudfunction/schema ./modules/cloudfunction/cmd/cli
go test -count=1 ./modules/cloudfunction/...
go test -count=1 ./modules/cloudfunction/proto/cloudfunctiongen/...
go test -count=1 ./packages/servicecatalog/... ./packages/report/...
make check-go-module-graph
```

嵌套的协议 Go module 需单独测试，不能假设上一层 `./...` 会自动遍历它。Collector/Admin/CLI/Monitor 的整合验证使用仓库工作区测试入口（`make test-go`）；该入口包含特定模块的禁内联参数和 CLI 并发约束。

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

另外核对目标 schema 的五张表、10 个显式索引（名称按 §3.2）、3 个触发器和复合 FK；确保新库不生成旧三张表，也不重新引入已经删除的 invocation 记录表。

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

### 7.3 残留、断链与生成一致性

```bash
# 全仓内容残留（rg 默认跳过二进制，已知二进制命中见下文）
rg -n -i 'cloud[_-]?node' -g '!.git' -g '!**/node_modules/**' -g '!web/dist' -g '!docs/.vitepress/dist' .
# 文件和目录名残留
rg --files --hidden -g '!.git' -g '!**/node_modules/**' -g '!web/dist' -g '!docs/.vitepress/dist' -g '!bin' -g '!release' | rg -i 'cloud[_-]?node'
# 模块内旧领域名：逐条分类，覆盖函数名、错误文案与注释
rg -n -i 'node' modules/cloudfunction
rg -n -i 'biz_?type|ip_?address|max_?nodes' modules/cloudfunction modules/collector modules/cli web/src
# 文档断链
rg -n '云节点\.md|modules/cloudnode/' -g '*.md' -g '*.ts' .
git diff --check
```

`rg` 无命中返回 1 是预期结果；有命中时逐条分类。云函数管理代码中的旧名应清零；`rg -i node modules/cloudfunction` 的命中只允许 `nodejs` 运行时等与旧领域无关的词。历史文档中的真实原名和 §2.4 的共享身份字段按明确保留清单审查。已知二进制命中：`packages/wirecompress/dicts/default-v1.zdict`（按 §1.3 保留）；阶段 0 删除误提交的 Mach-O 文件后不应再有其他二进制命中。

`make proto-check` 会先执行全仓协议生成，再要求整个工作树干净。正确顺序是：完成生成与审查 → 提交源文件、文档和生成物 → 在干净工作树执行 `make proto-check`，或在未完成其他质量门禁时执行包含它的 `make verify`。若生成检查产生差异，检查生成工具/输入，修正并提交后再重跑受影响检查。

## 8. 正式环境发布安排（按用户最新授权）

用户于 2026-10-23 明确：项目尚未上线，不考虑兼容和平滑迁移，线上数据可以删除。本节替代 v2 的离线转换草稿；不编写历史表、旧 JSON 或旧组件身份的迁移代码。

1. 完成本计划的实现与测试，另起 Agent 审查代码，修复发现的问题后复验；提交并推送全部源码。发布制品必须能追溯至该提交。
2. 按仓库 `skills/moox` 的原生发布流程盘点生产主机、单元、进程、网关目录、caller 身份及配置。确认所有使用内嵌服务目录的制品，并同一提交构建。
3. 在维护窗口暂停 Collector 的自动发布及相关旧进程和守护拉起，确认没有旧进程继续写入。管理作业和待释放租约不跨版本恢复；清理相关作业数据前确认旧云端请求已结束，必要时等待操作占用失效。
4. 使用新 schema 初始化云函数目录和作业数据。账号、代码包等结构不变的数据可保留；需要重置的实例目录由现有 SCF 导入与 Collector 对账流程重建。不改变云端函数名称，不自动删除云端函数或 COS 对象。
5. 同步替换组件 ID、服务名、目录、环境变量、runtime 计划、部署记录与 caller 身份。新版本不保留旧服务别名；确认守护计划不再拉起旧组件。
6. 发布 CloudFunction、Collector、Admin、Monitor、CLI、Web 及服务目录依赖的制品，更新所有主机的 host-gateway、gateway CLI、runtime 与相关 Access。依据实际依赖和生产盘点落实完整清单。
7. 核对新进程及二进制 SHA、健康检查、全部主机目录 expected/applied hash、账号/包/实例查询、作业提交查询、受控调用、Timer 回读及 GC dry_run；记录数据重建结果和发布台账。
8. 如发布失败，先停止新版本写入；根据实际云端状态恢复整套匹配的代码、配置与数据，不能只回退一个服务目录消费者。

本次不以混合版本维持服务，不依赖旧作业历史，不把旧 DB 路径存在视为新库已初始化。发布结果记录在实施记录中。

## 9. 最终交付与完成标准

- [x] 最终模块名已经确定，文档和代码没有互相矛盾的命名。
- [x] 五张业务表准确表达账号、代码包、实例、作业、作业明细；没有混淆包与实例。
- [x] 管理域中的 `CloudNode/NodeBatch` 及旧协议、字段、表和组件身份已统一替换；同一协议中没有取值不同的同名字段。
- [x] §2.5 的无调用方接口、字段和函数已删除。
- [x] Collector 任务实例、共享执行来源、Storage 节点和 Monitor 主机/进程身份没有被误改。
- [x] 全部管理调用方、ACL、动态 JSON、持久化 reader/writer 和部署资产同步切换。
- [x] 实例创建与发布围栏、异步恢复、Timer、调用和 GC 的原有业务约束通过回归。
- [x] 所有模块 schema 均可载入空 SQLite，完整性和外键检查通过。
- [x] 新协议生成物、前端嵌入物与源码一致；旧名残留与文档断链已按清单审查。
- [x] 现行文档、导航和构建示例可用；历史记录保留其基线语义。
- [ ] 按仓库要求提交本次改动并推送工作分支，记录 commit、测试结果和尚未进行的发布步骤。

本版进度：已刷新主线，代码基线仍为 `3da2faed`；阶段 0～6 的代码与文档实施已完成，已完成独立 Agent 审查及问题修复。正在收尾全量验证、生成一致性检查及正式环境发布；具体结果见实施记录。

## 10. 评审记录

| 日期 | 评审 | 结论 |
| --- | --- | --- |
| 2026-10-23 | 对照基线代码审查 v1 | 提出 6 项必须修改、6 项建议补充，已全部纳入 v2（见文首的 v2 变化说明） |
| 2026-10-23 | Codex 只读审查 v2（提交 `bec9d979`，模型 gpt-6-astra） | 对照仓库代码核对现状事实、命名映射与删除项、阶段依赖、验证矩阵和 §8 转换草稿，只报会导致实施失败或丢数据的问题；结论为“可以定稿”，无修改意见 |

| 2026-10-23 | 实施后独立 Agent 代码审查 | 发现 CLI 出口探针误改共享主机/进程身份，以及 Job 唯一索引命名遗漏；均已修复，新增身份字段回归用例。复核协议、生成物、动态 JSON、恢复与 GC、Web、ACL、部署身份后，无未修复的 P1/P2 问题。 |

## 11. 实施记录

- 在 `codex/cloudnode-naming-review` 的独立 worktree 实施；目录移动提交 `4c296f0a`，误提交二进制清理提交 `12421b68`。
- 代码包保持 `FunctionPackage` 与 `t_cloud_function_packages`；实例和管理作业分别统一为 `FunctionInstance`、`FunctionJob` / `FunctionJobItem`。删除无调用方旧接口与实例 IP/tag 字段。
- 全部模块 SQL 已逐个连续两次载入空 SQLite，通过外键和完整性检查；CloudFunction 为 5 张表、10 个显式索引、3 个触发器。
- Web 单元测试 133 个文件、839 个用例通过；类型检查、生产构建、statik 生成及文档生产构建通过。部署包、主机身份、单元安装/运行/引导测试通过。
- `check:detail-pages` 存在基线失败：未修改的 `web/src/views/factor/editor/index.vue` 表格缺少 cell 边框；文件与计划基线逐字节相同。本次修改的云函数页面通过对应检查。
- 生产盘点：实际主机为 `control`、`storage`、`overseas`；不按历史文档把主机重命名。发布期间保留云账号、代码包与云端函数/COS 对象，重建本地实例与 Job 目录；所有生产操作使用 SSH 公钥。
- 尚待记录：全量 Go 测试、干净树协议检查、正式制品提交、部署与线上验收结果。

### 11.1 正式环境基线补齐

首轮制品 `6a2cfff0` 的 72 个 Go module 测试/vet、协议干净树检查及 34 个 Linux 制品构建通过。首轮线上云函数验收通过：保留 1 个账号和 1 个代码包，经 SCF 导入重建 44 个实例；Timer 实时回读、带租约的无副作用异步 Job、真实 SCF 同步出口探针、GC dry-run 与旧 RPC 拒绝均成功。

恢复其他服务时发现正式环境已部署 `feat/factor-dynamic-params` 的 `b875e7ff`，而该分支尚未进入本计划的 main 基线。因子库因此与首轮主线构建不一致。为保留已经上线的功能和数据，将 `b875e7ff` 合入当前 worktree，补齐动态调参、分类、成员别名、相关 API/权限和前端，再统一构建部署；没有重置因子库。冲突仅涉及 CLI 文档与服务目录黄金摘要，均保留两侧功能后解决。

Access 重新签发的身份包包含完整外部验证密钥集合；安装时已将整组 `secrets/access/` 投影到两个 Access 单元并重新封存，避免新验证清单引用缺失文件。
