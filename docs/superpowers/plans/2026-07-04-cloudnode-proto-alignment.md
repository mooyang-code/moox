# CloudNode 协议一次性对齐实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.
>
> **Status:** 已完成（P0/P1 已落地，E2E 需环境验证）。

**Goal:** 将 `cloudnode.proto` 与实现对齐到 `collector.proto` / `moox_common.proto` 风格，一次性完成 P0～P3 全部改造。

**Constraints:**

- 新项目，**不考虑向后兼容**；凡是不合理的字段/结构均可删除或重写。
- **保留** `package_id` / `package_version`（不合并为 `deployment_id`）。
- `InvokeSync` **保持扁平字段**，不引入 `fanout` / `trace` 嵌套结构。
- **`JobItem` 不提供 `max_attempts`**：重试次数完全由 CloudNode 服务端默认策略控制（当前默认 3 次），调用方不可覆盖。
- `function_name` / `provider` **保留为一等字段**（SCF 调用与多厂商扩展需要，不长期塞 metadata）。

**Architecture:** CloudNode 是独立 RPC 服务（`trpc.moox.cloudnode.CloudNodeMgr`），经 admin 网关暴露为 `/api/admin/cloudnode/*`（JWT）与 `/api/service/cloudnode/*`（HMAC）。控制面与执行面语义分离。响应统一首字段 `common.RetInfo`。

**Tech Stack:** Protocol Buffers（`modules/cloudnode/proto`）、Go（`modules/cloudnode`、`modules/collector`、`packages/cloudruntime`）、Vue/TypeScript（`web/src`）、CLI（`modules/cli`）。

---

## 术语与字段语义（改造后统一口径）

| 概念 | 含义 | 出现位置 |
|---|---|---|
| `space_id` | 租户隔离主键 | 节点、代码包、JobItem、invocation、心跳 |
| `package_id` | 代码包目录 ID | `CloudNode`、`NodeDeployItem` |
| `package_version` | 当前节点部署的代码包版本号 | `CloudNode` |
| `code_package_id` | JobItem 使用的代码包 ID | `JobItem` |
| `function_name` | SCF 函数名（一等字段） | `CloudNode`、DB `c_function_name` |
| `provider` | 云厂商（默认 `tencent-scf`） | `CloudNode`、DB `c_provider` |
| `supported_workloads` | 节点支持的工作负载类型列表 | 节点、心跳、Poll |
| `job_item` | 异步执行单元 | Submit / Poll / Report |
| `invocation` | 同步扇出调用 | `InvokeSync` |
| `batch_change` | 控制面批量变更 | BatchCreate / Delete / Deploy |

**关键区分：** `package_id` / `package_version` 描述「代码包制品」；`deployment_id` 描述「运行时工作负载路由」。

**当前债务（必须修复）：** `toPBNode` 将 `package_id`/`package_version` 都映射为 `DeploymentID`；`fromPBNode` 反向塌缩。见 `modules/cloudnode/internal/rpc/node.go`。

**重试策略：** 仅服务端 `c_max_attempts`（固定默认 3）；无 `backoff_ms`。

---

## space_id 双路径设计

| 路径 | space_id 来源 | 适用 RPC |
|---|---|---|
| `/api/admin/cloudnode/*`（管理面） | 网关 `X-Space-Id` → trpc `spacectx` filter → `ctx`；**不信任 body** | `GetNodeList`、`BatchCreateNodes`、`UpdateNode`、代码包 CRUD、账户 CRUD |
| `/api/service/cloudnode/*`（执行面） | 请求 body 显式 `space_id`（HMAC 签名） | `SubmitJobItems`、`PollJobItems`、`ReportJobItemStatus`、`InvokeSync`、`ReportHeartbeat` |

- `NodeCreateItem` **不加** `space_id`；`BatchCreateNodes` 从 `ctx` 注入。
- cloudnode 新增 `internal/spacecontext`（对齐 trade 模块）并在 `trpc_go.yaml` 注册 `spacectx` filter。

---

## 删除项清单

```text
CloudRetryPolicy
JobItem.max_attempts
NodeListRequest / PackageListRequest 包装层
GetNodeListReq.query / GetPackageListReq.query
PollJobItemsReq.node_pool_id
t_cloud_nodes.c_pool_id 列 + idx_cloud_nodes_pool 索引
packages/cloudruntime.Config.NodePoolID
BatchChangeResult.message
UploadPackageReq / UploadPackage RPC
UploadPackageRsp.message / status
PackageListItem.status_label / package_type_label
PackageDetail.status_label
InvokeFunctionReq.function_name / namespace / region（节点 catalog 已有）
CloudAccount 合一 message → 拆分为 Summary / Input / Secret
```

---

## 目标 Proto 结构（完整摘要）

### enum

```protobuf
enum NodeStatusCode { NODE_STATUS_UNSPECIFIED=0; NODE_STATUS_OFFLINE=1; NODE_STATUS_ONLINE=2; NODE_STATUS_TIMEOUT=3; NODE_STATUS_ABNORMAL=4; }
enum PackageStatus { PACKAGE_STATUS_UNSPECIFIED=0; PACKAGE_STATUS_PENDING=1; PACKAGE_STATUS_AVAILABLE=2; PACKAGE_STATUS_FAILED=3; PACKAGE_STATUS_DELETED=4; }
enum PackageType { PACKAGE_TYPE_UNSPECIFIED=0; PACKAGE_TYPE_COLLECTOR=1; PACKAGE_TYPE_FACTOR=2; PACKAGE_TYPE_CUSTOM=3; }
enum JobItemStatus { JOB_ITEM_STATUS_UNSPECIFIED=0; ... PENDING=1; LEASED=2; RUNNING=3; SUCCESS=4; FAILED=5; }
enum InvocationStatus { INVOCATION_STATUS_UNSPECIFIED=0; SUCCESS=1; PARTIAL_FAILED=2; FAILED=3; }
enum ScfInvokeType { SCF_INVOKE_TYPE_UNSPECIFIED=0; REQUEST_RESPONSE=1; EVENT=2; }
```

### CloudNode（含 function_name / provider / deployment_id）

```protobuf
message CloudNode {
  int32 id = 1;
  string space_id = 2;
  string node_id = 3;
  string cloud_account_id = 4;
  string package_id = 5;
  string package_version = 6;
  string deployment_id = 7;
  string running_version = 8;
  string namespace = 9;
  string node_type = 10;
  string provider = 11;
  string function_name = 12;
  string biz_type = 13;
  string region = 14;
  string tag = 15;
  string ip_address = 16;
  repeated string supported_workloads = 17;
  google.protobuf.Struct metadata = 18;
  int32 timeout_threshold = 19;
  int32 heartbeat_interval = 20;
  bool probe_enabled = 21;
  string probe_url = 22;
  NodeStatusCode status = 23;
  string last_heartbeat = 24;
  bool is_deleted = 25;
  string create_time = 26;
  string modify_time = 27;
  string cls_topic_id = 28;
}
```

### ReportHeartbeatReq

```protobuf
message ReportHeartbeatReq {
  string space_id = 1;              // 执行面 body；管理面可从 ctx 补全
  string node_id = 2;
  string node_type = 3;
  string running_version = 4;
  string source_service = 5;
  string timestamp = 6;
  google.protobuf.Struct metrics = 7;
  google.protobuf.Struct metadata = 8;
  repeated string supported_workloads = 9;
  repeated LocalDNSReportItem local_dns_records = 10;
}
```

### NodeCreateItem（无 space_id，有 deployment_id）

```protobuf
message NodeCreateItem {
  string cloud_account_id = 1;
  string node_type = 2;
  string runtime = 3;
  string handler = 4;
  map<string, string> config = 5;
  map<string, string> environment = 6;
  string region = 7;
  string namespace = 8;
  string package_id = 9;
  string deployment_id = 10;
  google.protobuf.Struct metadata = 11;
}
```

### JobItem / InvokeSync（含 InvocationStatus）

```protobuf
message JobItem {
  string space_id = 1;
  string owner_service = 2;
  string owner_ref = 3;
  string workload_type = 4;
  string deployment_id = 5;
  google.protobuf.Struct payload = 6;
  int32 priority = 7;
  int64 lease_timeout_ms = 8;
}

message InvokeSyncResult {
  string request_id = 1;
  InvocationStatus status = 2;
  string payload = 3;
  string error_message = 4;
  int64 duration_ms = 5;
}

message InvokeSyncRsp {
  common.RetInfo ret_info = 1;
  string invocation_id = 2;
  InvocationStatus status = 3;
  int32 success_count = 4;
  int32 failed_count = 5;
  int32 timeout_count = 6;
  int64 duration_ms = 7;
  repeated InvokeSyncResult results = 8;
}
```

### 账户拆分

```protobuf
message CloudAccountSummary { ...; bool is_deleted; }  // 无 secret
message CloudAccountInput { ...; string secret_id; string secret_key; }
message CloudAccountSecret { ...; }  // GetCOSAccountInfo 专用，替代 COSAccountInfo
```

### 代码包两阶段上传

```protobuf
rpc InitPackageUpload(InitPackageUploadReq) returns (InitPackageUploadRsp);
rpc CompletePackageUpload(CompletePackageUploadReq) returns (CompletePackageUploadRsp);
// 删除 UploadPackage
```

### Service RPC 列表

保留原有管理/执行 RPC；新增 `InitPackageUpload`、`CompletePackageUpload`；删除 `UploadPackage`。

---

## 数据库 Schema 变更

| 表 | 变更 |
|---|---|
| `t_cloud_nodes` | 新增 `c_package_id`、`c_package_version`；删 `c_pool_id` 及索引；`c_is_deleted` → INTEGER 0/1 |
| `t_cloud_accounts` | `c_is_deleted` → INTEGER 0/1 |
| `t_cloud_function_packages` | `c_is_deleted` → INTEGER 0/1；`c_status` 对齐 `PackageStatus` |
| `t_cloud_job_items` | 新增 `c_lease_timeout_ms` |

---

## 网关核查项（非阻塞，Task 1 后执行）

admin 网关为 **通配转发**（`/api/admin/{service}/{method}`），无 cloudnode 方法白名单。仍需核查：

- [ ] `gateway.yaml` `method_limits` 未限制新 RPC
- [ ] 无 raw handler 占用 `InitPackageUpload` / `CompletePackageUpload`
- [ ] 两阶段上传后网关不再承载大 body

---

## 文件改动地图

（同前，补充以下条目）

| 文件 | 改动 |
|---|---|
| `modules/cloudnode/internal/spacecontext/spacecontext.go` | 新增，对齐 trade |
| `modules/cloudnode/config/trpc_go.yaml` | 注册 `spacectx` filter |
| `modules/cloudnode/internal/rpc/proto_contract_test.go` | 新增契约测试 |
| `modules/cloudnode/internal/repository/node_mapping_test.go` | 三分字段表驱动单测 |
| `modules/cloudnode/internal/repository/job_item_test.go` | lease / 重试表驱动单测 |

---

## 分 Task 执行顺序

Task 1 → 2 → 3 → 4 → 5 **严格串行**。

**Task 6（Web）∥ Task 7（CLI）∥ Task 8（文档）** 在 Task 5 完成后可并行。

---

### Task 1：Proto 设计与生成

- [ ] 重写 `cloudnode.proto`
- [ ] `make clean && make`
- [ ] `go build ./modules/cloudnode/...`

**验收：** 无 `CloudRetryPolicy`、`max_attempts`、`node_pool_id`、`GetNodeListReq.query`。

---

### Task 2：Schema + Repository

- [x] 更新 `cloudnode.sql`（三分列、删 pool、lease_timeout_ms、is_deleted INTEGER）
- [x] 更新 models（三分字段、`IsDeleted bool`、删 `PoolID`）
- [x] `job_item.go`：lease 消费、固定 max_attempts=3、删 pool 过滤
- [x] `node.go`：space_id 过滤、`common.Page`、`UpdateNodeDeployment` 写 package_id+version
- [x] `UpsertHeartbeat` 增加 space_id 参数
- [x] 表驱动单测 + `go test ./internal/repository/...`

---

### Task 3：RPC 层

- [x] `spacecontext` + trpc filter
- [x] 管理面 RPC 从 ctx 取 space_id
- [x] `node.go` 三分映射 + `function_name`/`provider`/`supported_workloads`
- [x] `job_item.go` Struct params + JobItemStatus
- [x] `invocation.go` ScfInvokeResult + InvocationStatus
- [x] `account.go` Summary/Input/Secret
- [x] `proto_contract_test.go`

---

### Task 4：两阶段上传

- [x] `InitPackageUpload` + COS presign
- [x] `CompletePackageUpload`
- [x] 删除 `UploadPackage`

---

### Task 5：Collector + CloudRuntime

- [x] `taskpublisher`：Struct params，删 RetryPolicy；改走 `/api/service/cloudnode/SubmitJobItems`（HMAC）
- [x] `heartbeat`：space_id + supported_workloads
- [x] `cloudruntime`：删 NodePoolID

---

### Task 6 ∥ Task 7 ∥ Task 8（可并行）

- [x] Web API + 页面 + 两阶段上传 UI
- [x] CLI 三步骤上传
- [x] 架构/管理/代码包文档

---

### Task 9：端到端验证

```bash
go build ./...
go test ./modules/cloudnode/... ./packages/cloudruntime/... ./modules/collector/...
```

- [x] 编译与单测（本地）
- [ ] 完整 E2E（需 admin + cloudnode + collector + SCF 环境）

---

## 关键实现细节

### 三分字段映射

```text
BatchDeployNodes(node_id, package_id)
  → c_package_id = package_id
  → c_package_version = SELECT c_version FROM t_cloud_function_packages WHERE c_package_id = ?
  → c_deployment_id 不变

toPBNode:
  package_id      → c_package_id
  package_version → c_package_version
  deployment_id   → c_deployment_id
  function_name   → c_function_name
  provider        → c_provider
```

### lease / 重试

```text
Submit: c_lease_timeout_ms = req.lease_timeout_ms || 600000
Poll:   deadline = now + item.LeaseTimeoutMs
Submit: c_max_attempts = 3（常量，proto 不可覆盖）
Report failed: attempt_no < c_max_attempts → pending
```

---

## 风险与决策记录

| 项 | 决策 |
|---|---|
| `package_id` / `package_version` | 保留，修复三分语义 |
| `max_attempts` | proto 删除，服务端固定 |
| `function_name` / `provider` | proto 一等字段保留 |
| `node_pool` | 有列无独立表，删列+索引+调用方 |
| `fanout` / `trace` | 不做 |
| admin 网关 | 通配转发，核查项非白名单 |
| `is_deleted` | node/account/package 三表统一 INTEGER bool |
| Task 6/7/8 | 可并行 |

---

## 预估工作量

| Task | 预估 |
|---|---|
| Task 1-5（串行） | 4d |
| Task 6 ∥ 7 ∥ 8（并行） | 1d |
| Task 9 E2E | 0.5d |
| **合计** | **约 5～5.5 人日** |

---

## 关联文档

- `docs/云节点执行平台架构.md`
- `docs/云节点管理.md`
- `docs/代码包管理.md`
- `docs/superpowers/plans/2026-07-03-cloudnode-batch-change-naming.md`
