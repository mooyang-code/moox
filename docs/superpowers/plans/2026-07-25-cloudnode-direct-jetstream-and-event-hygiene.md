# CloudNode 直连 JetStream 与事件系统清理执行计划

> **执行要求：** 实施时使用 `superpowers:executing-plans`，按任务顺序推进；每个任务先补测试，再改实现，并在任务结束后提交一个可独立评审的 commit。

**目标：** 让 Collector SCF 直接从 JetStream 作业执行队列取任务，CloudNode 只负责提交、状态与终态上报；同时删除未上线项目中已经失去意义的兼容、attempt、cancel、租约和伪配置。

**总体架构：**

- CloudNode 在首次提交某条作业路由时创建 JetStream durable pull consumer，然后发布任务。
- Collector SCF 只绑定已经存在的 durable，不创建、不修改 Consumer。
- SCF 执行后仍通过带 HMAC 签名的 Service Gateway RPC 上报终态；上报成功后才 ACK/TERM。
- `t_cloud_accounts` 保留账户业务模型，但所有明文 SecretID/SecretKey 统一归 `t_secrets` 管理。
- EventBus 的进程监听地址属于本地启动配置；客户端连接地址在系统初始化时写入既有 `eventbus` 服务部署记录。
- SCF 代码包保持无密钥；EventBus 用户名、token 和 CA 在发布函数时注入环境变量。

**技术栈：** Go、tRPC-Go、NATS JetStream、SQLite/GORM、Tencent SCF/COS、Vue 3。

---

## 1. 实施原则

这是未上线的新项目，本计划以最终合理结构为准：

1. 不做历史数据迁移、兼容读取、双写或灰度开关。
2. 切换时允许通过 `--reset-data` 清空全部现有数据和 JetStream 状态。
3. 采集任务是幂等的，接受少量重复执行，不引入 exactly-once、Saga、DLQ 或额外协调状态。
4. 删除没有实际读者的概念，不为“以后可能需要”保留字段、RPC、枚举或配置键。
5. 不增加自动探测、自动回退、启动预算、截止时间余量、续租和重放控制等可用性机械。
6. Phase 1 的 CloudNode/SCF 改造同一次发布完成；Phase 2 的事件卫生改造独立提交、独立评审。

---

## 2. 最终术语与所有权

### 2.1 作业执行队列

业务名称统一为 **Job Execution Queue / 作业执行队列**。

- 不再使用 `durable`、`topic`、`route consumer` 或“任务分配者”作为业务名称。
- JetStream 实现层仍使用官方术语 `Consumer`、`Durable`，因为它们是明确的传输概念。
- 作业执行队列身份为：

```text
space_id + code_package_id + job_type
```

- CloudNode 是队列生命周期 owner：首次向该身份提交任务时创建/校准队列。
- Collector SCF 是队列使用者：只绑定并拉取，不创建、不修改。
- 队列本身存储在 JetStream 中，不落 CloudNode 数据库，不建立第二份目录表。

**原因：** 这个资源真正表达的是“某一空间、代码包和作业类型的待执行工作”，而不是发布订阅 Topic，也不是常驻的调度服务。“作业执行队列”既贴合业务，也不掩盖其排队语义。

### 2.2 云账户与密钥

`t_cloud_accounts` 保留，负责非敏感的云账户业务信息：

```text
account_id
account_name
provider
app_id
cos_region
cos_bucket
credential_secret_id
extra_config
```

`t_secrets` 是唯一的明文密钥权威：

- `category = cloud`
- `provider = tencent`
- `key_id = Tencent SecretID`
- 加密的 `secret_value = Tencent SecretKey`
- `status = active`

CloudNode、CLI 和前端都不得再通过 CloudNode RPC 获取明文 SecretID/SecretKey。

**原因：** 云账户是需要被节点、代码包、区域和 COS 配置引用的业务对象，不能删；但密钥加密、轮换、启停和审计已经由 SecretMgr 负责，不应在 CloudNode SQLite 再保存一份明文。

### 2.3 EventBus 地址

必须区分两个概念：

| 概念 | 示例 | 所有者 |
|---|---|---|
| Broker bind/listen 地址 | `127.0.0.1:4222`、`0.0.0.0:4222` | EventBus 进程启动配置 |
| 客户端 advertised/connect URL | `tls://203.0.113.10:4222` | `t_service_deployments` 中既有 `eventbus` 行的 `extra_config.nats_url` |
| 同机客户端 URL | `tls://127.0.0.1:4222` | 凭据导出器根据 advertised URL 的端口推导 |

不新增 `eventbus-nats` 服务行，也不把 NATS 地址伪装成第二个 HTTP/tRPC 服务。既有 `eventbus` 行的 `host:127.0.0.1, port:11420` 继续表示 EventBus 控制面，NATS 数据面 URL 放在同一行的 `extra_config`：

```yaml
extra_config:
  health_url: http://127.0.0.1:11419/readyz
  health_kind: readiness
  monitor_enabled: true
  nats_url: tls://203.0.113.10:4222
```

**原因：** 服务部署表当前是一机一实例的静态初始化目录，不是实时注册中心。把客户端 URL 写进去可以形成单一初始化真源，又不必改变服务粒度、协议校验或引入心跳发现。

部署机不能假设能通过自己的公网 IP 回连自身。凭据导出时，同机运行的 EventBus、CloudNode、Factor、Strategy、Trade、Monitor 和指标上报角色写入 loopback TLS URL；HostAgent、Storage、Archive 和 SCF worker 等机外角色写入 advertised URL。两类 URL 共享同一端口、CA 和服务端证书，不增加用户配置项。

启用 TLS 时，NATS 服务端和所有 MooX 客户端统一使用 TLS handshake-first。这样公网端口从第一个字节就是标准 TLS 握手，不依赖中间网络放行 NATS 的明文 `INFO` 前导；该行为由代码根据 TLS 自动启用，不暴露额外开关。

用户只在仓库根目录的 0600 `custom.toml` 填写 `eventbus.public_address`、`eventbus.port` 和 `eventbus.tls_enabled`。`setup deploy-control` 根据这些值配置 bind、advertised URL、证书和腾讯云 Lighthouse 防火墙；防火墙规则用“查询后缺失才创建”保证重复部署不堆积重复规则。不做公网探测、自动回退或替换地址。

### 2.4 SCF EventBus 凭据

当前 EventBus 认证不是 NATS JWT `.creds`，而是：

```yaml
urls:
  - tls://203.0.113.10:4222
username: cloudnode-worker
token: ...
ca_file: ca.pem
```

代码包中不得包含这份 YAML、token 或 CA。发布工具读取部署机上的 `~/.config/moox/eventbus/cloudnode-worker.yaml`，在创建函数时注入：

```text
MOOX_EVENTBUS_NATS_URL
MOOX_EVENTBUS_NATS_USERNAME
MOOX_EVENTBUS_NATS_PASSWORD
MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64
MOOX_CODE_PACKAGE_ID
```

**原因：** 同一个代码包会被下载、复用和部署多次，把 token 烘进 zip 会扩大泄露面，并把凭据轮换错误地变成代码包版本升级。函数创建/发布才是凭据与具体运行实例绑定的正确阶段。

---

## 3. 运行时契约

### 3.1 JobItem 状态

保留的状态只有：

```text
pending -> success
pending -> failed
pending -> enqueue_failed
enqueue_failed -> pending    # 同一 job_item_id 重新提交并重新发布
```

规则：

- 删除 `running`、`canceled` 和全部 attempt 状态。
- `ReportJobItemStatus` 不再携带 `attempt_no`。
- 首个终态写入获胜；后续 `success`/`failed` 上报返回成功但不覆盖。
- 对已不存在或已过期的 JobItem 上报也返回成功，让 worker 可以结束当前投递。
- `duration_ms` 与 `execution_node` 只在首个终态写入。
- 只有首次终态写历史；重复/缺失记录不重复写。历史写入失败只记录日志，不反向改变 JobItem 终态。
- Active KV 继续使用 48 小时 TTL；不增加 sweeper。

**原因：** JetStream 的投递次数已经是重试权威。服务端 attempt、running lease 和恢复时间只是在复制传输层状态。首终态优先会接受极少数“失败先到、成功后到”的假失败，但这是明确接受的简化。

### 3.2 重试、上报与 ACK

`MaxDeliver` 只从实际绑定的 JetStream Consumer 读取，worker 不再配置第二份最大尝试次数。

| 执行结果 | 当前投递 | worker 行为 |
|---|---|---|
| 成功 | 任意 | 上报 `success`；上报成功后 ACK |
| 永久失败 | 任意 | 上报 `failed`；上报成功后 TERM |
| 可重试失败 | `delivery_count < max_deliver` | 不上报；NAK，延迟 1 秒 |
| 可重试失败 | `delivery_count >= max_deliver` | 上报 `failed`；上报成功后 TERM |
| 任何终态上报失败/被控制面拒绝 | 任意 | NAK，延迟 1 秒 |
| 无法解码或路由身份不匹配 | 任意 | TERM，并记录结构化错误 |

不调用 `InProgress`，不做 ACK 续租。CloudNode Consumer 的 `ack_wait` 固定为 60 秒；Collector 单条作业上下文保持 45 秒，给终态上报留出剩余函数时间。

### 3.3 多作业类型绑定

Collector SCF 明确声明：

```go
var SupportedJobTypes = []string{
    "collect.kline",
    "collect.symbol",
}
```

启动时逐个绑定对应作业执行队列：

- 某一队列返回 `jetstream.ErrConsumerNotFound`：说明该作业类型从未被提交，只跳过这一项。
- 连接、TLS、认证、权限、配置冲突等其他错误：立即失败。
- 至少绑定一个队列：进入消费。
- 所有队列都不存在：返回清晰的 `no active job execution queue` 错误并快速退出。
- 已绑定队列一轮全部为空：聚合 Consumer 返回 `jetstream.ErrClosed`，Runner 正常退出。

必须覆盖“只有 K 线队列存在、Symbol 队列不存在，K 线仍能消费”的测试。

**原因：** 队列由提交路径按需创建，未提交过的作业类型本来就不应阻塞其他类型。只忽略明确的“不存在”，可以保持简单，同时避免把权限或网络错误误判为空队列。

### 3.4 单次 SCF 消费参数

```text
BatchSize      = 1
FetchMaxWait   = 500ms
NAK delay      = 1s
AckWait        = 60s
Job context    = 45s
SCF timeout    = 60s
```

多队列使用 Collector 内部的 `roundRobinConsumer`，不修改通用 `jetstream.Runner`。仅 `nats.ErrTimeout` 且没有 delivery 才表示该队列本轮为空；如果同时返回 deliveries 和 error，二者都必须向 Runner 传播。

不实现最小启动预算、deadline margin、IdleTimeout 或公网可达性自动回退。

---

## 4. 明确不做

- 不保留 HTTP `PollJobItems` 作为 fallback。
- 不保留 cancel RPC、心跳控制指令或人工中止能力。
- 不保留 attempt 表、attempt API、attempt 日志字段和默认尝试次数。
- 不保存 SCF 环境变量到 `t_cloud_nodes.c_metadata`。
- 不把 EventBus 凭据写入 Collector zip 或 CloudNode 数据库。
- 不让发布 CLI 直接读取 Admin SQLite 或 `t_secrets`。
- 不将 `MOOX_EVENTBUS_HOST` 暴露为第二个运维旋钮。
- 不做 EventBus 动态注册、心跳、健康探测、自动选址、自动回退或负载均衡。
- 不做 SCF 环境变量更新失败的回滚。
- 不保留已成功发布的 Strategy outbox 行作为审计日志。
- 不增加通用 Consumer 抽象来统一 Archive、Monitor、Trade 的不同生命周期。
- 不给 Event 公共 API 增加没有运行时读者的投递类型字段。

---

## 5. 实施顺序

```text
Phase 0：删除旧契约与建立基础能力
  Task 0-4
      ↓
Phase 1：CloudNode/Collector/SCF 一次性切换
  Task 5-9
      ↓
Phase 2：独立的事件系统卫生改造
  Task 10-13
      ↓
Task 14：全量验收、破坏性切换与文档收尾
```

Task 0 必须先完成：后续重画状态机时不再为 `canceled` 保留临时分支。

Phase 1 的 Task 5-9 必须同一次发布，不允许出现“服务端已按新队列投递、SCF 仍走 Poll”或反向的中间版本。

---

# Phase 0：删除旧契约与建立基础能力

## Task 0：删除 cancel 全链路

**原因：** 当前没有可靠的远程中断执行机制；cancel 只是把状态和心跳指令做复杂，并不能保证停止已经运行的 SCF。

**Files:**

- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Modify: `modules/cloudnode/internal/jobstate/types.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store.go`
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/jobhistory/schema.go`
- Modify: `modules/cloudnode/internal/jobhistory/store.go`
- Modify: `modules/collector/internal/reporter/heartbeat.go`
- Test: 上述目录现有测试

**Steps:**

- [ ] 先写/改测试，固定心跳响应只含成功状态，不再携带控制指令。
- [ ] 从 proto 删除 `CancelJobItem` RPC、请求/响应、`ControlDirective*`、`ReportHeartbeatRsp.directives`，以及三个 CloudNode `*_CANCELED` 枚举值。
- [ ] 运行 `make -C modules/cloudnode/proto` 重新生成 `cloudnodegen`；删除的字段号不再占位，因为项目无需兼容。
- [ ] 删除 `StatusCanceled`、`CancelReason`、`MarkCanceled`、`ClearCancelDirective`、`ListCancelDirectives` 和所有分支。
- [ ] 删除 Collector 手写 heartbeat directive DTO、分发循环与 handler。
- [ ] 从历史 DDL 和写入代码删除 `c_cancel_reason`。
- [ ] 更新当前 CloudNode/Collector 文档：批量停任务只能停上游调度并等待正在运行的任务结束。
- [ ] 在 `modules/cloudnode` 与 `modules/collector` 分别运行 `go test ./... -race`。

**完成条件：** 生产代码、proto、生成代码中 `CancelJobItem|ControlDirective|StatusCanceled|CancelReason|c_cancel_reason` 零命中。

---

## Task 1：保留云账户，统一通过 SecretMgr 解析腾讯云密钥

**原因：** 云账户仍是 SCF、COS 和节点配置的业务对象；密钥则必须只有一个加密权威，不能同时存在于 `t_cloud_accounts` 和 `t_secrets`。

**Files:**

- Modify: `modules/cloudnode/schema/cloudnode.sql`
- Modify: `modules/cloudnode/internal/store/models.go`
- Modify: `modules/cloudnode/internal/store/account.go`
- Modify: `modules/cloudnode/internal/store/account_test.go`
- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Modify: `modules/cloudnode/internal/rpc/account.go`
- Modify: `modules/cloudnode/internal/rpc/account_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/rpc/package.go`
- Modify: `modules/cloudnode/internal/rpc/invocation.go`
- Create: `modules/cloudnode/internal/cloudcredential/resolver.go`
- Create: `modules/cloudnode/internal/cloudcredential/resolver_test.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/go.mod`
- Modify: `modules/cloudnode/go.sum`
- Modify: `modules/admin/proto/secret_service.proto`
- Modify: `modules/admin/internal/service/secret/rpc/service.go`
- Modify: 对应 Admin SecretMgr tests
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/internal/adminclient/cloudnode_test.go`
- Modify: `modules/cli/internal/adminclient/service_auth.go`
- Create: `modules/cli/internal/adminclient/secret.go`
- Create: `modules/cli/internal/adminclient/secret_test.go`
- Modify: `modules/cli/internal/clsprepare/prepare.go`
- Modify: `modules/cli/internal/clsprepare/prepare_test.go`
- Modify: `modules/cli/internal/command/collector.go`
- Modify: `modules/cli/internal/command/collector_test.go`
- Modify: `modules/cli/internal/command/tencent_ops_firewall_open.go`
- Modify: 对应 CLI tests
- Modify: `examples/service-deployments.seed.yaml`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: 对应 Admin route/default tests
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-deploy-moox-gateway.sh`
- Modify: `web/src/api/cloud-account.ts`
- Modify: `web/src/views/collector/cloud-account/cloud-account-manage.vue`
- Reuse: `web/src/api/admin/secret.ts`

**Target contract:**

```go
type CloudAccount struct {
    AccountID          string
    AccountName        string
    Provider           string
    CredentialSecretID string
    AppID               string
    COSRegion           string
    COSBucket           string
    ExtraConfig         string
}

type TencentCredential struct {
    SecretID  string
    SecretKey string
}
```

**Steps:**

- [ ] 先加 schema/model/proto 测试，断言 `t_cloud_accounts` 只有 `c_credential_secret_id`，不含 `c_secret_id/c_secret_key`。
- [ ] 把 `CloudAccountInput` 和 `CloudAccountSummary` 改为 `credential_secret_id`；删除 `CloudAccountSecret`、`GetCOSAccountInfo*` 与 RPC。
- [ ] 重新生成 `cloudnodegen`。
- [ ] 让 `RevealedSecret` 返回 `category/provider/status`；SecretMgr 继续在 RPC 层拒绝 inactive 记录，重新生成 `admingen`。
- [ ] 新建 `cloudcredential.Resolver`：用 `credential_secret_id` 调 `/api/service/secret/RevealSecret`，再次校验 `status=active`、`category=cloud`、`provider=tencent`，映射 `key_id -> SecretID`、`secret_value -> SecretKey`。
- [ ] CloudNode module 显式依赖 Admin 生成 proto 与 `packages/gatewayauth`；使用 `CredentialsFromEnv` 和服务签名 tRPC client，启动缺少 gateway target、node ID、caller 或 service key 时直接报配置错误，不读 Admin SQLite。
- [ ] `scfClientFactory` 改为接收已解析的 `TencentCredential`；SCF、COS、预签名上传、对象校验和同步调用每次按账户引用解析，不缓存、不落库、不写日志。
- [ ] CloudAccount provider 最终只接受 `tencent`，删除 `tencent-scf` 等兼容别名。
- [ ] CLI 的 `clsprepare`、Collector CLS 解析/发布与防火墙命令先列 CloudAccount，再根据 `credential_secret_id` 调 SecretMgr；删除 `GetCOSAccountInfo` client。
- [ ] Collector publish 从所选 CloudAccount secret 注入 `MOOX_CLS_SECRET_ID/MOOX_CLS_SECRET_KEY`，`--env` 不得覆盖；不再要求部署机预设 `TENCENTCLOUD_SECRET_*`。
- [ ] Secret 服务路由保留 `ListSecrets/RevealSecret`，`gateway_callers` 明确允许 `admin-gateway`、`cloudnode`、`moox-cli`；Reveal 仍只能走 `/api/service`。
- [ ] 部署脚本为 `cloudnode`、`moox-cli` 生成独立 Gateway caller 凭据并写入 `gateway-credentials.json`；启动 CloudNode 时使用 `gateway_service_env_for cloudnode`。生成 0600 的 `gateway-moox-cli.env`，包含 CLI 所需 key ID、caller、secret、target node 与 CA 路径，不复用 `admin-gateway` 身份。
- [ ] `adminclient.ServiceAuthConfig` 增加 Caller 并参与签名；Collector/运维 CLI 默认从 `MOOX_GATEWAY_CALLER` 读取 `moox-cli`。
- [ ] 前端云账户表单从 SecretMgr 列出 active 的 Tencent cloud secret，使用下拉框保存 `credential_secret_id`；删除 SecretID/SecretKey 输入框与展示。
- [ ] 先在 SecretMgr 创建云密钥，再创建引用它的 CloudAccount；删除 Secret 时不做级联兼容，存在失效引用时调用明确失败。
- [ ] 运行 `make -C modules/cloudnode/proto`、`make -C modules/admin/proto`、`go work sync`。
- [ ] 在 `modules/admin`、`modules/cloudnode`、`modules/cli` 运行 `go test ./... -race`，在 `web` 运行现有 typecheck/test。

**完成条件：**

- 全仓生产 schema/model/proto/API 中不存在 CloudNode 明文 SecretID/SecretKey 字段。
- CloudNode 与 CLI 只通过 SecretMgr 的 service-auth 路径获得明文。
- 前端云账户仍可创建、编辑、删除，并能选择云密钥。

---

## Task 2：把 EventBus 客户端 URL 写入初始化服务目录

**原因：** bind 地址只对本机进程有意义；SCF 需要的是可连接 URL。初始化目录可作为唯一静态真源，而无需把系统升级成动态服务发现。

**Files:**

- Modify: `examples/service-deployments.seed.yaml`
- Modify: `modules/admin/cmd/cli/service_deployments.go`
- Modify: `modules/admin/cmd/cli/service_deployments_test.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `packages/jetstream/credentials.go`
- Modify: `packages/jetstream/credentials_test.go`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-deploy-moox-eventbus.sh`

**Steps:**

- [ ] 先加 `service-deployments import` 测试：`--eventbus-nats-url` 只修改指定 node 的 `eventbus.extra_config.nats_url`，保留 health 字段；缺失 eventbus 行、非 `tls` URL、无 host 或无 port 均报错。
- [ ] seed 的本机默认值写为 `tls://127.0.0.1:4222`。
- [ ] `service-deployments import` 增加必填的 `--eventbus-nats-url`，在 import 前以结构化 map 合并 `extra_config`，不用字符串拼接 JSON。
- [ ] `eventbus-credentials ensure/export` 删除 `--public-ip`，增加 `--node-id`；从 Admin DB 读取该 node 的 active `eventbus` 行并解析 `extra_config.nats_url`。
- [ ] 签证书时使用 URL host：IP 写入 `IPAddresses`，域名写入 `DNSNames`；loopback 仍保留 `localhost/127.0.0.1/::1`。
- [ ] TLS bundle/secret 的 `extra_config` 记录 `nats_url` 或 advertised host，删除旧 `public_ip` 字段，不做兼容读取。
- [ ] 已有 TLS bundle 的签名 host 与服务目录不一致时明确失败，要求执行 `--reset-data` 重建；不新增自动重签或证书迁移路径。
- [ ] 凭据导出器从 advertised URL 推导同端口的 `tls://127.0.0.1:<port>`：同机角色使用 loopback URL，机外角色使用 advertised URL；不得要求用户填写第二个地址。
- [ ] `packages/jetstream.CredentialFile` 增加 `URLs []string`；`ApplyCredentialFile` 把 YAML URL 写入 `Config.URLs`，并继续把 `token` 归一为 `Password`。
- [ ] `deploy-moox.sh` 根据 `MOOX_EVENTBUS_PUBLIC_IP` 计算一次 `nats_url`：非空为 `tls://<public-ip>:4222`，否则为 loopback。
- [ ] 脚本在导出凭据之前调用 `service-deployments import --eventbus-nats-url ... --node-id ...`，随后 `ensure/export --node-id ...`。
- [ ] 脚本内部根据 URL host 推导 bind host：loopback 用 `127.0.0.1`，其他 host 用 `0.0.0.0`；覆盖并传递内部 `MOOX_EVENTBUS_HOST`，不要求运维填写。
- [ ] 加脚本契约测试，断言本地和远端路径都传递计算后的 URL、node ID 与 bind host。
- [ ] EventBus TLS 服务端、内部控制连接和 `packages/jetstream` 客户端统一启用 handshake-first；增加真实 TLS 握手测试，不能只断言配置字段。
- [ ] `setup deploy-control --file ./custom.toml` 在服务部署成功后幂等确保 Lighthouse TCP 防火墙允许 EventBus 端口；规则创建失败则部署命令失败，不做探测或回退。
- [ ] 运行：

```bash
bash -n scripts/deploy-moox.sh
./scripts/test-deploy-moox-eventbus.sh
(cd modules/admin && go test ./cmd/cli/... ./internal/service/sysdeploy/... -race)
(cd packages/jetstream && go test ./... -race)
```

**完成条件：** 凭据导出不再读取 `MOOX_EVENTBUS_PUBLIC_IP`；advertised URL 只来自 `eventbus.extra_config.nats_url`，loopback URL 只由其端口确定性推导。

---

## Task 3：增加 ensure/bind Consumer 与 CA PEM 环境变量

**原因：** CloudNode 需要创建/校准 Consumer 但不应订阅，SCF 需要订阅但不能创建/修改；同时 serverless 环境不能依赖部署机上的 CA 文件路径。

**Files:**

- Modify: `packages/jetstream/consumer.go`
- Modify: `packages/jetstream/consumer_test.go`
- Modify: `packages/jetstream/config.go`
- Modify: `packages/jetstream/config_test.go`
- Modify: `packages/jetstream/client.go`
- Create: `packages/jetstream/client_test.go`
- Modify: `packages/events/consumer.go`
- Modify: `packages/events/consumer_test.go`

**Target API:**

```go
func (c *Client) EnsureConsumer(
    ctx context.Context,
    cfg ConsumerConfig,
) error

type BindConsumerConfig struct {
    Stream        string
    Durable       string
    FilterSubject string
    FetchMaxWait  time.Duration
}

func (c *Client) BindConsumer(
    ctx context.Context,
    cfg BindConsumerConfig,
) (consumer *Consumer, maxDeliver int, err error)
```

**Steps:**

- [ ] 先加 `EnsureConsumer` 测试：缺失时创建、配置漂移时只校准 owner 字段、完成后不调用 `PullSubscribe`。
- [ ] 先加测试：Consumer 不存在返回 `ErrConsumerNotFound`；Filter/AckPolicy 冲突返回 `ErrConsumerConfigConflict`；整个调用不执行 `AddConsumer`、`UpdateConsumer` 或 Stream 枚举。
- [ ] 从现有 `NewConsumer` 提取 `EnsureConsumer`；`NewConsumer` 保持“ensure 后绑定”的兼容行为，其他模块不受影响。
- [ ] `BindConsumer` 只做 `ConsumerInfo -> 校验 FilterSubject/AckExplicit -> PullSubscribe(nats.Bind(...))`。
- [ ] 只返回实际 Consumer 的 `MaxDeliver`，不返回无运行时读者的 AckWait/MaxAckPending。
- [ ] `events.EnsureSubjectConsumer` 与 `events.BindSubjectConsumer` 都使用 Registry 渲染同一个 subject，分别调用 ensure 与 bind API。
- [ ] `jetstream.Config` 增加 `TLSCAPEMBase64`，读取 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64`。
- [ ] `TLSCAFile` 与 `TLSCAPEMBase64` 互斥；两种输入都解析到同一个 `x509.CertPool` 和 `nats.Secure` 配置。
- [ ] 覆盖非法 Base64、非法 PEM、冲突来源和纯环境变量 TLS 连接测试。
- [ ] 锚定 Runner 现有行为：Consumer 返回 `ErrClosed` 时 `Runner.Run` 正常结束。
- [ ] 在 `packages/jetstream`、`packages/events` 运行 `go test ./... -race`。

---

## Task 4：建立共享的作业执行队列身份

**原因：** CloudNode 创建队列与 SCF 绑定队列必须使用同一个命名算法；复制算法会让代码包升级后两侧永久错开。

**Files:**

- Create: `packages/cloudjobqueue/go.mod`
- Create: `packages/cloudjobqueue/identity.go`
- Create: `packages/cloudjobqueue/identity_test.go`
- Delete: `modules/cloudnode/internal/jobqueue/naming.go`
- Modify: `modules/cloudnode/internal/jobqueue/naming_test.go`（迁移后删除或改为调用方测试）
- Modify: `modules/cloudnode/go.mod`
- Modify: `packages/cloudruntime/go.mod`
- Modify: `packages/cloudruntime/runtime.go`
- Modify: `packages/cloudruntime/runtime_test.go`
- Modify: `go.work`

**Target API:**

```go
type Identity struct {
    SpaceID       string
    CodePackageID string
    JobType       string
}

func (i Identity) ConsumerName() (string, error)
func (i Identity) SubjectID() (string, error) // code_package_id + "/" + job_type
```

**Steps:**

- [ ] 创建独立最小 module，不依赖任何业务模块。
- [ ] 用长度前缀和 SHA-256 生成 `cn_exec_<24 hex>`；`SubjectID` 统一生成 `code_package_id + "/" + job_type`，为二者写固定输入 golden test。
- [ ] 校验三个身份字段非空，拒绝前后空白；调用者不能悄悄归一不同身份。
- [ ] CloudNode 和 CloudRuntime 都直接 import `packages/cloudjobqueue`；不保留旧 `jobqueue.ConsumerName` 转发层。
- [ ] `cloudruntime.Config` 增加 `CodePackageID`；Task 6 的直连入口必须校验非空。旧 Poll 入口在 Task 7 删除前不读取该字段。
- [ ] 更新 `go.work` 以及单 module 构建所需 `require/replace`，然后运行 `go work sync`。
- [ ] 在 `packages/cloudjobqueue`、`packages/cloudruntime`、`modules/cloudnode` 运行测试。

---

# Phase 1：CloudNode/Collector/SCF 一次性切换

## Task 5：由 CloudNode 提交路径按需创建作业执行队列

**原因：** 作业执行队列只在真实路由第一次被使用时存在，避免启动时扫描所有空间、包和作业类型，也避免 worker 获得管理权限。

**Files:**

- Modify: `modules/cloudnode/internal/jobqueue/queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue_test.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/rpc/job_item_test.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/config/app.yaml`

**本任务新增的方法：**

```go
EnsureJobExecutionQueue(ctx context.Context, identity cloudjobqueue.Identity) error
```

**Steps:**

- [ ] 先加提交测试，断言每个 item 先做无副作用校验，每个 request 内相同 `space/package/job_type` 只 Ensure 一次，且副作用顺序严格为 `Ensure -> CreatePending -> Publish`。
- [ ] 加失败测试：Ensure 失败不创建 JobItem；Publish 失败把新记录标为 `enqueue_failed`；重复提交 `enqueue_failed` 可重新发布。
- [ ] `EnsureJobExecutionQueue` 使用共享 identity，并通过 `events.EnsureSubjectConsumer` 创建/校准；服务端不得产生 pull subscription：

```text
Stream          MOOX_CLOUDNODE_EXEC
FilterSubject   共享 Registry 渲染的该作业路由 subject
AckPolicy       explicit
AckWait         60s
MaxDeliver      现有 CloudNode 静态配置值
MaxAckPending   现有 CloudNode 静态配置值
DeliverPolicy   all
```

- [ ] CloudNode 启动校验该作业队列的 `MaxDeliver > 0`，保证 worker 能判定末次投递。
- [ ] 只保留 request-local map 去重，不加全局缓存、后台 reconciliation 或启动时预创建。
- [ ] 为保持本任务可编译，旧 `PublishResult/Fetch/Ack/Nak/Term/InProgress` 和 QueueMeta 暂存到 Task 7；本任务不再增加任何调用方。
- [ ] 在 `modules/cloudnode` 运行 `go test ./... -race`。

---

## Task 6：Collector 直接绑定并消费作业执行队列

**原因：** 数据面不再经过 CloudNode HTTP Poll，减少一层协议和服务端 inflight 状态；终态控制仍保留在 CloudNode。

**Files:**

- Create: `modules/collector/internal/taskrunner/jobstream.go`
- Create: `modules/collector/internal/taskrunner/jobstream_test.go`
- Modify: `modules/collector/go.mod`
- Modify: `modules/collector/go.sum`
- Modify: `packages/cloudruntime/runtime.go`
- Modify: `packages/cloudruntime/runtime_test.go`
- Modify: `packages/cloudruntime/go.mod`
- Modify: `packages/cloudruntime/go.sum`
- Modify: `packages/cloudruntime/README.md`
- Modify: `modules/collector/README.md`

**Steps:**

- [ ] 新增 `RunJobItems` 直连实现；本任务先不切换 SCF 入口，旧 `poller.go` 只临时保留到 Task 7 原子删除。
- [ ] 从环境读取 `MOOX_SPACE_ID`、`MOOX_CODE_PACKAGE_ID` 与 JetStream Config；注册 Kline/Symbol handlers。
- [ ] 对两个 SupportedJobTypes 计算共享 queue identity 和 subject，调用 bind-only API。
- [ ] 每条 delivery 用 `events.DecodeDelivery` 解码，并校验 subject、envelope `space_id`、payload `code_package_id/job_type` 与当前 queue identity 一致；解码或身份不匹配按 §3.2 TERM。
- [ ] 实现 Collector 私有 `roundRobinConsumer`：按技术 Consumer 名保存每个 bound consumer 的 `MaxDeliver`，处理 delivery 时用 `delivery.Consumer` 精确取值；BatchSize 固定为 1；每轮从下一索引开始。
- [ ] 添加单元测试：
  - 只有 Kline 队列存在、Symbol 返回 `ErrConsumerNotFound`，Kline 正常消费。
  - 两个队列都有消息时轮转且不饥饿。
  - 所有队列不存在时返回 `no active job execution queue`。
  - 所有已绑定队列 timeout 时返回 `ErrClosed`。
  - deliveries 与非 timeout error 同时返回时不丢 deliveries，也不吞 error。
  - auth/permission/config conflict 不得当成缺失队列跳过。
- [ ] `cloudruntime` 先新增单条 JobItem 执行函数，按 §3.2 返回 `jetstream.HandlerResult`；report 必须返回 error，不能只记日志后假装成功。旧 Poll loop 在本任务暂存，Task 7 一次删除。
- [ ] Runner 配置 `BatchSize=1`、`InProgressInterval=0`；重试 NAK 延迟固定 1 秒。
- [ ] 在 `packages/cloudruntime`、`modules/collector` 运行 `go test ./... -race`。

---

## Task 7：原子删除 Poll、attempt/running，让心跳只表达存活

**原因：** 直连入口就绪后，Poll、attempt、running lease 和服务端 ack token 必须一起删除；拆开会留下无法正确上报或无法确认消息的中间状态。

**Files:**

- Delete: `modules/collector/internal/taskrunner/poller.go`
- Delete: `modules/collector/internal/taskrunner/poller_test.go`
- Modify: `modules/collector/cmd/scf/main.go`
- Modify: `modules/collector/internal/serverless/handler.go`
- Modify: `modules/cloudnode/proto/cloudnode.proto`
- Modify: `modules/cloudnode/internal/jobstate/types.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store_test.go`
- Modify: `modules/cloudnode/internal/jobhistory/schema.go`
- Modify: `modules/cloudnode/internal/jobhistory/store.go`
- Modify: `modules/cloudnode/internal/jobhistory/store_test.go`
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/rpc/job_item_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/rpc/server_test.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/rpc/node_test.go`
- Modify: `modules/cloudnode/internal/jobqueue/queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/schema/cloudnode.sql`
- Modify: `modules/cloudnode/schema/schema_test.go`
- Modify: `modules/cloudnode/cmd/cli/init_schema_test.go`
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `packages/cloudruntime/runtime.go`
- Modify: `packages/cloudruntime/runtime_test.go`
- Modify: `examples/e2e/README.md`

**最终队列接口：**

```go
type ExecutionQueue interface {
    EnsureJobExecutionQueue(ctx context.Context, identity cloudjobqueue.Identity) error
    Publish(ctx context.Context, item *cloudnodepb.JobItem) error
    Close() error
}

type JobStateStore interface {
    MarkReported(ctx context.Context, event ReportEvent) (
        state *State,
        firstTerminal bool,
        err error,
    )
}
```

**Steps:**

- [ ] 先加状态测试：`pending -> success/failed`；重复终态成功返回；首终态获胜；不存在的 JobItem 上报成功；首次终态固定 `duration_ms` 与 `execution_node`。
- [ ] 加明确的假失败测试：先上报 `failed` 再上报 `success`，最终仍是 `failed`。
- [ ] 从 proto 一次删除：
  - `PollJobItems`、`PolledJobItem` 请求/响应。
  - `JobItemAttempt`、`ListJobItemAttempts`。
  - 所有 `attempt_no`、running/start/recover 字段与相关枚举。
- [ ] 重新生成 `cloudnodegen`，并同步 CLI/Collector 调用方。
- [ ] 删除 CloudNode RPC Poll、协议版本常量、节点查询和所有服务端消费动作。
- [ ] 删除 `TryMarkRunning`、`RunningRequest`、`RunningState`、`Attempt*`、`Attempts`、`RunningNode`、`RecoverAt`、`QueueMeta`、`MarkPublished`、`HistorySynced`、`StartedAt` 和相关错误。
- [ ] `State` 只保留提交身份、`pending/enqueue_failed/success/failed`、结果、错误、`DurationMS`、`ExecutionNode`、创建/完成时间。
- [ ] `MarkReported` 实现 first-terminal-wins；重复或不存在记录均返回幂等成功，并让调用方只在首次写入时落终态历史。
- [ ] `ReportJobItemStatus` 只更新终态和终态历史，不调用 JetStream ACK/TERM；投递动作由 worker 在 RPC 成功后执行。
- [ ] `Publish` 返回值收敛为 `error`；删除 QueueMeta/stream sequence 的状态写入。
- [ ] 从服务端 `ExecutionQueue` 删除 `Fetch/Ack/Nak/Term/InProgress` 与所有消费锁、round robin、inflight map。
- [ ] 历史库直接删除 attempt 表及 `c_attempt_no/c_running_node/c_start_time`，新增 `c_duration_ms/c_execution_node`。
- [ ] 删除 `cloudnode.sql` 中为旧 attempt/job item 表准备的 `DROP TABLE` 和旧库兼容测试；保留“不得创建 attempt 表”的断言。
- [ ] 删除 `recover_after_millis`、`default_max_attempts` 及全部配置、默认值、校验和夹具。
- [ ] `ack_wait_millis` 固定为 `60000`，删除它与恢复时间的联动校验。
- [ ] 心跳只校验节点身份并更新 last seen/能力信息，响应不再包含任务控制。
- [ ] 删除 CloudNode 服务端 `fetch_max_wait_ms`、`DefaultMaxBatch`、Poll limit、protocol version 及所有 fake/fixture。
- [ ] 删除 `cloudruntime` Poll DTO、HTTP Poll、`Config.Limit/RuntimeVersion/ProtocolVersion` 与 `JobItem.AttemptNo`；最终 Config 校验 `CodePackageID` 非空，日志只保留局部 `delivery_count`。
- [ ] 删除 Collector `poller.go`，把 SCF 和 serverless 入口切换到 Task 6 的 `RunJobItems`。
- [ ] 删除 CLI/Admin client 中的 Poll DTO 或调用。
- [ ] 把当前 E2E 文档中的 Poll HTTP 示例改为“SCF 直连 Job Execution Queue”。
- [ ] 运行 proto 生成和 CloudNode/CloudRuntime/CLI/Collector tests。

**完成条件：** CloudNode/CloudRuntime/Collector 生产代码、proto 和生成代码中 `PollJobItems|PolledJobItem|PollAndExecuteJobItems|AttemptNo|attempt_no|TryMarkRunning|RunningNode|RecoverAt|QueueMeta|inflight` 零命中。

---

## Task 8：增加 cloudnode-worker 最小权限角色

**原因：** worker 只需要查看、拉取和确认已经存在的作业执行队列；创建/删除 Consumer 和发布业务消息都属于服务端权限。

**Files:**

- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`
- Modify: `scripts/verify-event-contracts.sh`
- Modify: `docs/运维/MooX-EventBus运维.md`

**Required ACL:**

```text
publish allow:
  $JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>
  $JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>
  $JS.ACK.MOOX_CLOUDNODE_EXEC.>

subscribe allow:
  _INBOX.>
```

**Steps:**

- [ ] 在 `eventBusRoles`、`eventBusKeys`、`usersYAML`、`roleFiles` 四处加入 `cloudnode-worker`，导出 `cloudnode-worker.yaml`。
- [ ] 不授予 `$JS.API.CONSUMER.CREATE*`、DELETE、Stream 枚举、KV、业务 subject publish 或其他 Stream 权限。
- [ ] 加生成文本测试，断言 allowlist 精确匹配并明确排除管理权限。
- [ ] 扩展 `scripts/verify-event-contracts.sh`，校验 CloudNode server 与 worker 权限边界。
- [ ] 真实 NATS 测试：worker 可 bind/fetch/ack，`NewConsumer`/CreateConsumer/业务 publish 均被拒。
- [ ] 在 `modules/admin` 运行 `go test ./cmd/cli/... -race`，再运行 `./scripts/verify-event-contracts.sh`。

---

## Task 9：发布时注入 EventBus 凭据，并保留前端批量部署能力

**原因：** 首次创建函数需要完整凭据；稳态换版本必须同时更新代码和 `MOOX_CODE_PACKAGE_ID`。删除前端部署会丢掉现有产品能力，只更新代码则会让 worker 与新作业执行队列永久错开。

**Files:**

- Modify: `modules/cli/internal/command/collector.go`
- Modify: `modules/cli/internal/command/collector_test.go`
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/internal/adminclient/cloudnode_test.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/rpc/node_test.go`
- Modify: `modules/cloudnode/internal/rpc/server.go`
- Modify: `modules/cloudnode/internal/providers/tencentscf/client.go`
- Modify: `modules/cloudnode/internal/providers/tencentscf/client_test.go`
- Modify: `modules/cloudnode/internal/rpc/package.go`
- Modify: `scripts/build-collector-scf-package.sh`
- Modify: 对应 package tests
- Verify/Modify: `web/src/api/cloud-node.ts`
- Verify/Modify: `web/src/views/collector/cloud-node/cloud-node.vue`

**Publish contract:**

```text
collector function publish
  1. 读取 cloudnode-worker.yaml（0600）
  2. 读取相对 ca_file 并编码 PEM Base64
  3. 上传无密钥 zip，得到 PackageID
  4. CreateFunction 注入托管的五个环境变量
  5. 函数已存在则失败，提示使用 deploy；凭据轮换则先删除函数再 publish
```

**Deploy contract:**

```text
collector function deploy / 前端 BatchDeployNodes
  1. GetFunction，取得当前完整 Environment
  2. UpdateFunctionCode
  3. 复制 Environment，只替换 MOOX_CODE_PACKAGE_ID
  4. UpdateFunctionConfiguration
  5. 成功后更新本地 node/package deployment 记录
```

**Steps:**

- [ ] 给 `collector function publish` 增加凭据文件参数，默认 `~/.config/moox/eventbus/cloudnode-worker.yaml`；只接受官方 loader 解析的 username/token/URLs/CA，并要求恰好一个 `tls` URL。
- [ ] 拒绝 loopback `MOOX_EVENTBUS_NATS_URL` 发布到 SCF；本机 EventBus 部署本身仍合法。
- [ ] 生成并锁定五个托管键；`--env` 如果包含其中任一键直接报错，不允许覆盖。
- [ ] 明确拒绝 `MOOX_EVENTBUS_NATS_TLS_CA_FILE`，只注入 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64`。
- [ ] `MOOX_EVENTBUS_NATS_PASSWORD` 取 loader 已归一后的 `token/password`。
- [ ] `MOOX_CODE_PACKAGE_ID` 必须等于本次 UploadPackage 返回值。
- [ ] Collector publish 固定 SCF `Timeout=60s`；单条作业 context 继续由 Collector 固定为 45 秒，不增加动态 deadline margin。
- [ ] CloudNode 不把 `NodeCreateItem.Environment` 写进 `t_cloud_nodes.c_metadata`；测试同时断言 CreateFunction 仍收到环境变量。
- [ ] `ensureSCFFunction` 遇到已存在函数直接返回明确错误，不再偷偷只更新代码。
- [ ] 扩展 Tencent provider：`FunctionInfo` 返回完整 Environment，新增 `UpdateFunctionConfiguration` wrapper。
- [ ] `BatchDeployNodes` 按上面的五步执行；任一步失败立即返回，不回滚，不提前更新本地记录。
- [ ] 保留 proto、Web API、批量部署按钮和单节点部署入口；前端无需接触任何 EventBus 凭据。
- [ ] 加包安全测试：构造 sentinel token/SecretID/SecretKey，生成 zip 后逐个读取解压后的 entry 内容，确认均不含 sentinel，文件列表也不含 `cloudnode-worker.yaml`；不能只搜压缩后的 archive bytes。
- [ ] 发布运行位置只保留一种：本机构建 zip并传到部署机，部署机运行 `moox-cli collector function publish --zip <path>`；不把远端 worker token/CA 拷回开发机。
- [ ] 运行 CloudNode、CLI、Collector 和 Web tests。

**完成条件：**

- 初次 publish 创建的函数包含新 EventBus URL/用户名/password/CA/PackageID。
- deploy 保留原凭据，只更新代码和 PackageID。
- 前端批量与单节点部署仍可用。
- 可下载的函数包不含任何 EventBus 或腾讯云明文密钥。

---

# Phase 2：事件系统卫生

## Task 10：简化 Strategy Outbox，按自增 ID 发布后删行

**原因：** 当前单进程 relay 不需要 claim/lease/token；按秒级 `c_ctime` 排序无法保证同一事务内多条命令的顺序。

**Files:**

- Modify: `modules/strategy/schema/strategy.sql`
- Modify: `modules/strategy/internal/store/outbox.go`
- Modify: `modules/strategy/internal/store/outbox_test.go`
- Modify: `modules/strategy/internal/bus/outbox.go`
- Modify: `modules/strategy/internal/bus/outbox_test.go`
- Modify: `modules/strategy/internal/bus/runtime_test.go`

**Target schema:**

```sql
CREATE TABLE t_strategy_outbox (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_message_id TEXT NOT NULL UNIQUE,
    ...
);
```

**Steps:**

- [ ] 先加排序测试：多行 `c_ctime` 完全相同时，发布顺序仍严格等于插入顺序。
- [ ] 加不变量测试：publish 或删行失败后，不发布后续行，只处理连续成功前缀。
- [ ] 删除 `c_published/c_claimed_until/c_claim_token`，不写迁移。
- [ ] `ListPendingOutbox` 直接按 `c_id ASC` 取全表；`c_message_id` 保留 UNIQUE 以维持幂等插入。
- [ ] 删除 `ClaimOutbox/ReleaseOutbox/tokenIndex/lease`；把 `MarkOutboxPublished` 明确改名为 `DeleteOutbox`。
- [ ] relay 单线程、fail-fast；每条 publish 成功后立即删行，任一步失败立即 return。
- [ ] 不为 Strategy 增加新 observability 抽象或复制 Storage 指标。
- [ ] 在 `modules/strategy` 和 `modules/trade` 运行 focused tests，运行 `./scripts/test-strategy-trade-event-e2e.sh`。

---

## Task 11：把 Consumer 身份和传输参数从 YAML 收回代码

**原因：** Consumer 名改变等于换消费进度，不是普通运维旋钮；`max_ack_pending/ack_wait_ms` 已由代码行为决定，假配置只会制造第二真源。

**Files:**

- Modify: `modules/archive/config/app.yaml`
- Modify: `modules/archive/internal/config/config.go`
- Modify: `modules/archive/internal/config/config_test.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/internal/config/config.go`
- Modify: 对应 Monitor tests
- Modify: `modules/trade/config/app.yaml`
- Modify: `modules/trade/internal/config/app.go`
- Modify: 对应 Trade config tests
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify: `scripts/verify-event-contracts.sh`

**Steps:**

- [ ] 先加测试，断言 Archive、Monitor、Trade、Storage View 的 Consumer 名来自模块常量。
- [ ] 把现有 Consumer 名和相关 ack 参数改成模块内常量，不建立跨 module 共享生产包。
- [ ] 删除上述 YAML 中 `consumer/max_ack_pending/ack_wait_ms` 等已无读者的键。
- [ ] 更新 loader structs、默认值、校验和测试夹具。
- [ ] 让 `scripts/verify-event-contracts.sh` 静态校验代码常量与 EventBus ACL 字符串一致。
- [ ] 在四个模块分别运行 config tests，再运行 `./scripts/verify-event-contracts.sh`。

**完成条件：** 当前生效配置 YAML 中不存在已被代码接管的 Consumer 身份和 ack 参数。

---

## Task 12：修复 Storage View 部分字段写入并删除 Replace

**原因：** Storage 事件是增量字段，不是完整行。现有 upsert 会把事件未携带的列写成零值；`Replace` 与 `LiveWrite` 在底层又没有差异，是无效区分。

**Files:**

- Modify: `modules/storage/internal/service/view/event_apply.go`
- Create: `modules/storage/internal/service/view/event_apply_test.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/service_test.go`
- Modify: `modules/storage/internal/service/viewindex/model.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index_test.go`

**Steps:**

- [ ] 先加必然失败的单 Dataset 测试：行原有 A/B，事件只更新 A，应用后 B 保持原值。
- [ ] 加多 Dataset 测试：一个 Dataset 的增量事件不得清空另一个 Dataset 的列。
- [ ] 判定单位必须是每个 `RowWrite`：按目标列集合把 writes 分成 complete/incomplete，只把 incomplete 子集交给现有 `recoverMissingRows`，再按 row key 与 complete 子集合并，统一 `LiveWrite`。
- [ ] 删除 `viewindex.Replace`、`WriteMode.String()` 分支、DuckDB 注释和所有测试入参。
- [ ] 删除 RPC `"REPLACE"` 分支；只接受 `LIVE_WRITE/BACKFILL`，未知模式明确拒绝。
- [ ] 加闸门测试，确认恢复后的完整行走 live delivery 并发限制，Backfill 仍走 backfill 限制。
- [ ] 运行 `modules/storage` 全量测试和：

```bash
./scripts/e2e/storage-datanode-management.sh
./scripts/e2e/storage-field-upsert.sh
```

**完成条件：** `modules/storage/internal/service/view{,index}` 含测试在内 `Replace|"REPLACE"` 零命中。

---

## Task 13：修 Trade 重复唤醒，并用静态表锁定 Stream retention

**原因：** Trade 有真实的重复唤醒；各模块 Consumer 生命周期并不同构，不值得为一个局部 bug 新增通用抽象。Stream retention 则用配置测试即可，不需要污染 Event 公共模型。

**Files:**

- Modify: `modules/trade/internal/bootstrap/kernel_workers.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers_test.go`
- Modify: `modules/eventbus/internal/config/config_test.go`

**Steps:**

- [ ] 加测试断言 `SetWakeup(wakeup.Wake)` 后一次状态变化只触发一次 wake。
- [ ] 删除同时调用 `wakeup.Wake()` 与 `s.Wake()` 的重复路径，只保留单一回调。
- [ ] 在 EventBus config test 写死四个 Stream 的期望：

| Stream | Retention |
|---|---|
| `MOOX_STORAGE` | `limits` |
| `MOOX_METRICS` | `limits` |
| `MOOX_CLOUDNODE_EXEC` | `work_queue` |
| `MOOX_TRADE` | `work_queue` |

- [ ] 测试同时断言 Stream 数量为 4；增删 Stream 必须显式更新期望表。
- [ ] 不修改 `packages/events` 公共 API，不增加 `DeliveryKind`。
- [ ] 在 `modules/trade`、`modules/eventbus` 运行 `go test ./... -race`，再运行 `./scripts/verify-event-contracts.sh`。

---

# Phase 3：验收与切换

## Task 14：全量验证、破坏性重置和文档收尾

### 14.1 自动化验证

- [ ] 重新生成 proto：

```bash
make -C modules/cloudnode/proto
make -C modules/admin/proto
go work sync
```

- [ ] 在以下 module 逐个运行 `go test ./... -race`：

```text
packages/cloudjobqueue
packages/jetstream
packages/events
packages/cloudruntime
modules/admin
modules/cloudnode
modules/collector
modules/cli
modules/eventbus
modules/strategy
modules/trade
modules/storage
modules/monitor
modules/archive
```

- [ ] 运行仓库验证：

```bash
./scripts/test-go-workspace.sh
./scripts/verify-event-contracts.sh
./scripts/test-deploy-moox-eventbus.sh
./scripts/test-strategy-trade-event-e2e.sh
./scripts/e2e/storage-datanode-management.sh
./scripts/e2e/storage-field-upsert.sh
pnpm --dir web test
pnpm --dir web build:prod
make verify-pr
git diff --check
```

### 14.2 真实 EventBus 验证

- [ ] 运行 `moox-cli setup e2e-eventbus --file ./custom.toml`：由 `cloudnode-eventbus` 创建队列并发布，`cloudnode-worker` 绑定/FETCH/ACK，`eventbus-internal-admin` 只负责清理探针 Consumer。命令只输出四个布尔证明，不输出凭据。
- [ ] 如需用 NATS CLI 单独诊断，使用 `cloudnode-worker.yaml` 的 `username/token/ca_file`；不得使用 JWT `--creds`：

```bash
nats --server 'tls://<public-ip>:4222' \
  --user cloudnode-worker \
  --password '<token>' \
  --tlsca ./ca.pem \
  server ping
```

- [ ] 从部署机之外验证证书 SAN、网络和认证；本机 `/readyz` 成功不能替代这一项。
- [ ] 验证 worker 可 INFO/FETCH/ACK，但 CREATE/DELETE Consumer、Stream 枚举、KV 和业务 publish 均被拒。
- [ ] 只创建 Kline 作业执行队列，保持 Symbol 队列不存在；启动 worker 后 Kline 成功执行。
- [ ] 两种队列都不存在时，worker 清晰报 `no active job execution queue` 并快速退出。
- [ ] 两个队列都存在时验证 round robin，无消息时一轮后正常退出。

### 14.3 CloudNode/SCF 端到端验证

- [ ] 提交一条采集任务，确认调用链：

```text
EnsureJobExecutionQueue
-> CreatePending
-> Publish
-> SCF bind/fetch
-> handler
-> ReportJobItemStatus
-> ACK/TERM
```

- [ ] 成功任务从 `pending` 直接到 `success`，历史记录含 `duration_ms/execution_node`。
- [ ] 可重试非末次失败不写终态并重投；末次失败写 `failed` 后 TERM。
- [ ] 模拟上报网络失败，确认投递 NAK；重复上报终态返回成功。
- [ ] 模拟 `failed` 先到、`success` 后到，确认接受最终假失败。
- [ ] 新节点 metadata 中不存在环境变量、token、password、SecretID/SecretKey。
- [ ] 通过前端选择一个 active Tencent cloud secret 创建 CloudAccount；SCF/COS 操作成功，禁用该 secret 后同一操作明确失败，CloudNode DB 始终只有 `credential_secret_id`。
- [ ] SCF 环境变量的 URL 等于 `eventbus.extra_config.nats_url`，CA/token 等于本次导出值。
- [ ] 下载函数代码包，确认不含 worker YAML、token、CA 和腾讯云密钥。
- [ ] 在前端分别执行批量部署和单节点部署，确认代码与 `MOOX_CODE_PACKAGE_ID` 同步更新，原 EventBus 凭据保持不变。

### 14.4 首次破坏性切换

顺序不可调整：

1. 停止上游调度，不再提交新 JobItem。
2. 在腾讯云删除旧 Collector SCF。当前凭据会在 reset 后轮换，而 publish 对已存在函数明确失败。
3. 使用初始化文件中的 EventBus 公网地址执行控制面重置：

```bash
./bin/moox-cli setup deploy-control --file ./custom.toml --reset-data
```

4. 确认部署脚本完成以下内部动作：
   - 计算 `tls://<public-ip>:4222`。
   - 把它写入 `eventbus.extra_config.nats_url`。
   - 推导 broker bind 为 `0.0.0.0`。
   - 幂等确保 Lighthouse 防火墙开放该 TCP 端口。
   - 重签 EventBus 私有 CA 和所有 role token。
   - 导出 `cloudnode-worker.yaml` 及其他角色凭据。
5. 重建基础资源：
   - Admin 空间与用户。
   - DataNode 注册。
   - Metadata 导入与 Dataset 激活。
   - 在 SecretMgr 新建 Tencent cloud secret。
   - 在 CloudNode 新建引用 `credential_secret_id` 的 CloudAccount。
   - 给部署目录外的 HostAgent 重新分发 `hostagent-publisher.yaml` 与新 `ca.pem`。
6. 本机构建无密钥 Collector zip，传到部署机；在部署机加载 `gateway-moox-cli.env` 后执行 `collector function publish --zip <path>`，记录新 PackageID。
7. 创建/更新采集规则，使其指向新 PackageID，并重算任务实例。
8. 先做机外 EventBus TLS 测试，再提交一条 Kline 和一条 Symbol 作业做端到端 smoke。
9. 恢复上游调度。

`--reset-data` 会清空 Storage、Strategy、Trade、Monitor、Archive、CloudNode、Admin 与 JetStream 数据，并导致全部 EventBus token/CA 重新生成。这里不提供迁移或回滚路径。

### 14.5 稳态代码包升级

1. 上传新代码包，取得新 PackageID。
2. 先通过前端批量部署/单节点部署或 CLI `collector function deploy` 更新 SCF 代码和 `MOOX_CODE_PACKAGE_ID`。
3. 再把采集规则切到新 PackageID并重算任务实例。

不能先改规则：否则 deploy 失败时，规则已经持续向无人消费的新队列发布。先 deploy 仍会留下一个很短的窗口，旧规则可能把幂等任务写入旧队列而新 worker 已不再消费；本系统明确接受这批任务丢弃，由后续周期采集补齐。这样选择的原因不是“零丢失”，而是 deploy 失败时规则仍停留在已知旧版本，恢复动作更简单。第 2 步 worker 若发现新队列尚不存在会快速退出；规则提交后 CloudNode 会按需创建新队列。

凭据轮换不走 deploy：删除旧函数后用 publish 重建。

### 14.6 零残留检查

- [ ] CloudNode/CloudRuntime 限定目录零残留：

```bash
rg -n 'CancelJobItem|ControlDirective|StatusCanceled|CancelReason|AttemptNo|attempt_no|TryMarkRunning|RunningNode|RecoverAt|QueueMeta|MarkPublished|HistorySynced|PollJobItems|PolledJobItem|inflight' \
  modules/cloudnode/internal \
  modules/cloudnode/proto \
  packages/cloudruntime
```

- [ ] Collector 零残留：

```bash
rg -n 'PollAndExecuteJobItems|poller|Directives' modules/collector
```

- [ ] 云账户不存明文：

```bash
rg -n 'c_secret_id|c_secret_key|CloudAccountSecret|GetCOSAccountInfo' \
  modules/cloudnode \
  modules/cli \
  web/src/api/cloud-account.ts \
  web/src/views/collector/cloud-account
```

- [ ] Strategy 只保留新 outbox 模型：

```bash
rg -n 'c_published|c_claimed_until|c_claim_token|ClaimOutbox|ReleaseOutbox|MarkOutboxPublished' modules/strategy
```

- [ ] Storage View 不再有无效 Replace：

```bash
rg -n 'Replace|\"REPLACE\"' \
  modules/storage/internal/service/view \
  modules/storage/internal/service/viewindex
```

这些命令预期均无输出。不要把检查扩大到其他模块的合法 `cancel/replace` 业务，也不要修改历史计划和历史评审报告来制造“全仓零命中”。

### 14.7 当前文档更新

- [ ] 更新 `modules/cloudnode/README.md`
- [ ] 更新 `modules/collector/README.md`
- [ ] 更新 `packages/cloudruntime/README.md`
- [ ] 更新 `docs/云节点管理.md`
- [ ] 更新 `docs/云节点执行平台架构.md`
- [ ] 更新 `docs/运维/MooX-EventBus运维.md`
- [ ] 更新 `examples/e2e/README.md`

文档只描述最终模型：

- Job Execution Queue 的 owner、身份和缺失语义。
- CloudAccount 引用 SecretMgr，CloudNode 不保存或揭示明文密钥。
- EventBus bind 与 advertised URL 的区别。
- SCF 代码包无密钥，凭据在 publish 时注入。
- 首次 publish、稳态 deploy、凭据轮换三条操作路径。
- JobItem 无 attempt、running、cancel、Poll fallback。

### 14.8 最终评审门槛

- [ ] 对完整 diff 做一次独立 code review，重点检查权限边界、密钥泄露、队列身份一致性、report-before-ack、缺失队列语义和前端部署回归。
- [ ] 修复全部 Critical/Important 问题并重跑受影响测试。
- [ ] 确认每个 Phase 有独立 commit，Phase 1 为一个可原子部署的 commit 集。
- [ ] 确认工作区无意外生成物，`git diff --check` 通过。

---

## 6. 最终验收标准

以下条件全部满足才算完成：

1. Collector SCF 不再调用 `PollJobItems`，只绑定 CloudNode 已创建的作业执行队列。
2. 未激活的某一作业类型不会阻塞其他类型；所有类型均未激活时错误清晰。
3. JobItem 没有 attempt、running、cancel 或 ack token 状态，重试权威唯一来自 JetStream。
4. 成功/失败终态上报完成后才 ACK/TERM；上报失败会重投。
5. `t_cloud_accounts` 仍可支撑 SCF/COS 业务，但不含明文密钥，只引用 SecretMgr。
6. EventBus 客户端 URL 只来自初始化后的 `eventbus.extra_config.nats_url`，broker bind 地址由部署脚本推导。
7. Collector zip 不含任何运行凭据；SCF 创建时获得 EventBus 环境变量。
8. 前端批量部署和单节点部署保留，并同步更新代码与 PackageID。
9. Strategy outbox 按自增 ID 串行发布、成功即删；Storage View 部分字段不会清空其他列。
10. 所有自动化、真实 broker、端到端、破坏性切换和零残留检查通过。
