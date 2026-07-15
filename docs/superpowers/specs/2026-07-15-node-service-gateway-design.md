# MooX 节点机器网关服务设计

## 状态

本设计已确认采用“只拆机器服务网关”方案。新模块位于 `modules/gateway`，每台服务器部署一个实例，只转发到本机服务。浏览器控制面 `/api/admin/*`、登录会话、SSH Raw Handler 和管理 RPC 继续由 Admin 承载。

本文定义模块边界、服务实例模型、路由生成与同步协议、安全约束、管理界面、Monitor 迁移和分阶段发布方案。本文不实施代码。

## 背景

当前 Admin 同时提供两类网关：

- `127.0.0.1:11000` 提供 `/api/admin/*`，服务于浏览器，使用 JWT、会话请求签名和一次性 nonce。
- `127.0.0.1:11002` 提供 `/api/service/*`，服务于后台进程和云运行时，使用 service HMAC。

两类网关共享 Admin 进程，但职责并不相同。控制面网关依赖登录会话、Admin 加密密钥、Badger nonce、SSH ticket 和本地 Raw Handler。机器网关主要负责验证机器请求、解析服务地址和转发 tRPC HTTP 请求。

当前机器网关的路由来自 `t_service_deployments`。该表以 `service_name` 为唯一键，只能表达一个服务的一条当前部署记录，无法表达“同一个服务在多个节点各有一个本机实例”。Monitor 双机协同时因此只能直接访问 Monitor 端口或建立反向 SSH 隧道。

## 目标

- 在 `modules/gateway` 新增独立的 `moox-gateway` 服务。
- 每台服务器部署一个网关实例，对外只提供机器接口 `/api/service/*`。
- 每个网关只允许转发到本机 loopback 或 Unix Socket 服务。
- Admin 维护节点和服务实例的期望状态，自动生成节点路由，不要求管理员手写路由 URL。
- 网关主动从 Admin 拉取带版本和签名的完整路由快照。
- Admin 暂时不可用时，网关继续使用最后一次有效路由。
- 使用稳定的节点身份、独立 Ed25519 密钥、目标节点 audience 和本地 nonce 存储防止跨节点重放。
- 把 Monitor 节点快照改为正式 RPC，通过节点网关互相访问。
- 收敛公网端口。业务服务继续绑定 loopback，仅节点 HTTPS 网关面向受控网络。

## 非目标

- 不拆分 `/api/admin/*` 控制面网关。
- 不把 Auth、Space、SSH、Secret 或 SysDeploy 业务实现迁入 Gateway。
- 不让 Gateway 直接读 Admin SQLite 或 Badger 数据文件。
- 不允许节点网关转发到任意远程 IP，也不实现通用 HTTP 正向代理。
- 不在第一阶段实现跨节点负载均衡、服务网格、自动流量迁移或全局服务发现。
- 不让浏览器或前端代码调用 `/api/service/*`。
- 不通过同步整个 SQLite 数据库维护路由。

## 总体架构

MooX 保留一个中央控制面，每台服务器运行一个节点数据面：

```text
                         Admin control plane
                    +---------------------------+
                    | Node and service inventory|
                    | Route compiler and signer |
                    | Gateway status management |
                    +-------------+-------------+
                                  ^
                     signed route | pull and status report
                                  |
          +-----------------------+-----------------------+
          |                                               |
 +--------+---------+                            +--------+---------+
 | Guangzhou node   |                            | Hong Kong node   |
 | Caddy HTTPS      |                            | Caddy HTTPS      |
 | moox-gateway     |                            | moox-gateway     |
 | -> loopback only |                            | -> loopback only |
 | -> monitor       |                            | -> monitor       |
 +------------------+                            +------------------+
```

Admin 是期望状态和路由签名的权威来源。节点网关是本节点路由的执行者，不接受其他节点的路由配置，也不把远端地址当作上游。

## 模块边界

新增模块结构：

```text
modules/gateway/
  README.md
  cmd/server/main.go
  cmd/cli/main.go
  config/app.yaml
  config/trpc_go.yaml
  internal/bootstrap/
  internal/config/
  internal/router/
  internal/proxy/
  internal/auth/
  internal/routes/
  internal/controlplane/
  internal/health/
  internal/store/
```

各目录职责：

- `bootstrap`：加载最小配置、打开本地状态库、加载最后有效路由、启动刷新器并注册 HTTP 服务。
- `config`：节点 ID、Admin 地址、监听地址、CA、凭据文件和刷新周期。
- `router`：只匹配 `/api/service/{service}/{method}`，拒绝其他路径和诊断路径。
- `proxy`：将 JSON body 透传到本机 tRPC HTTP 服务，保持 `trpc-ret`、`trpc-func-ret`、trace 和 gzip 契约。
- `auth`：验证节点机器签名、目标 audience、时钟窗口和 nonce。
- `routes`：校验、编译和原子切换路由快照。
- `controlplane`：向 Admin 拉取路由并上报状态。
- `health`：提供独立且受 health HMAC 保护的 `/healthz`、`/readyz` 和 `/metrics`。
- `store`：持久化最后有效路由、已应用版本和 nonce，不保存业务数据。

二进制：

- `moox-gateway`：节点网关服务。
- `moox-gateway-cli`：初始化本地状态、校验路由快照、查看当前版本和执行离线诊断。

Gateway 不导入 `modules/admin/internal/*`。可复用协议放到根级公共包：

- `packages/gatewayroute`：路由快照 DTO、规范化、摘要和 Ed25519 验签。
- `packages/nodegatewayauth`：节点机器请求的签名和验证。

现有 Admin gateway 中通用的转发响应、header 透传和错误转换逻辑迁入可复用包，Admin 控制面继续调用该包，避免形成两套不一致的代理实现。

## 节点身份

每个网关实例使用稳定 `node_id`。节点 ID 不使用 IP、主机名或进程 PID，因为这些值可能变化。首批节点采用：

- `gateway-gz-122`
- `gateway-hk-177`

节点记录新增到 Admin 数据库：

### `t_gateway_nodes`

| 字段 | 含义 |
| --- | --- |
| `c_node_id` | 稳定节点 ID，唯一 |
| `c_name` | 管理台显示名称 |
| `c_public_address` | 节点 HTTPS 网关地址 |
| `c_status` | enabled 或 disabled |
| `c_expected_version` | Admin 当前签发版本 |
| `c_applied_version` | 节点最近确认版本 |
| `c_last_seen_at` | 最近状态上报时间 |
| `c_last_error` | 最近一次同步或校验错误摘要 |
| `c_labels` | 地域、环境等 JSON 标签 |
| `c_ctime/c_mtime` | 创建和更新时间 |

`node_id` 可以关联 SSH 主机或 Host Agent，但不能复用它们的主键。关联关系是可选元数据，不影响网关身份。

### `t_gateway_identities`

| 字段 | 含义 |
| --- | --- |
| `c_key_id` | Ed25519 公钥标识，唯一 |
| `c_owner_type` | gateway、service 或 operator |
| `c_owner_id` | 节点 ID 或服务实例 ID |
| `c_public_key` | Base64 Ed25519 公钥 |
| `c_status` | active、rotating 或 revoked |
| `c_permissions` | 允许的 `service_id/method` JSON 列表 |
| `c_not_before/c_not_after` | 密钥有效窗口 |
| `c_ctime/c_mtime` | 创建和更新时间 |

私钥只在对应节点或调用服务上生成和保存，Admin 只登记公钥。密钥轮换先登记新 key 并发布双 key 快照，待所有节点确认新 revision 后撤销旧 key。

### `t_gateway_route_revisions`

| 字段 | 含义 |
| --- | --- |
| `c_node_id` | 目标节点 |
| `c_revision` | 节点内单调递增版本 |
| `c_digest` | 规范化快照 SHA-256 |
| `c_snapshot` | 已签名快照 JSON |
| `c_status` | published、superseded 或 rolled_back |
| `c_operator_id` | 发布操作者 |
| `c_source_revision` | 回滚时引用的历史版本 |
| `c_ctime` | 发布时间 |

唯一约束为 `(c_node_id, c_revision)`。回滚复制历史内容并生成新 revision，不重新启用旧版本号。

## 服务部署模型

继续以 `t_service_deployments` 作为服务部署事实表，但把“一项服务一条记录”升级为“一项服务在一个节点一条记录”。

新增字段：

| 字段 | 含义 |
| --- | --- |
| `c_node_id` | 所属 Gateway 节点 |
| `c_gateway_service_id` | URL 中的稳定服务 ID，例如 `monitor` |
| `c_gateway_enabled` | 是否允许机器网关暴露 |

约束调整：

- 主唯一约束改为 `(c_node_id, c_service_name)`。
- 启用网关的记录要求 `(c_node_id, c_gateway_service_id)` 唯一。
- `c_host` 必须是 `127.0.0.1`、`::1` 或受支持的 Unix Socket 标识。
- `c_gateway_path` 必须是无前导斜杠的 tRPC 服务全名。
- `c_status=active` 且 `c_gateway_enabled=true` 的记录才进入路由快照。

迁移时，现有记录统一回填到中央节点 `gateway-gz-122`。Admin 当前 `/api/admin/*` resolver 在过渡期显式选择中央节点，避免同名多实例导致不确定解析。

管理员维护服务实例，不直接维护路由。路由由以下字段确定：

```text
node_id + gateway_service_id
    -> protocol + host + port + gateway_path
```

## 路由快照

Admin 为每个节点生成完整快照，而不是发送增量指令：

```json
{
  "schema_version": 1,
  "node_id": "gateway-gz-122",
  "revision": 18,
  "issued_at": "2026-07-15T10:00:00Z",
  "routes": [
    {
      "service_id": "monitor",
      "protocol": "trpc-http",
      "address": "127.0.0.1:11410",
      "service_path": "trpc.moox.monitor.MonitorMgr",
      "auth_policy": "node_ed25519",
      "timeout_ms": 5000,
      "max_body_bytes": 4194304
    }
  ],
  "callers": [
    {
      "key_id": "monitor-gz-122",
      "public_key": "base64-ed25519-public-key",
      "allow": ["monitor/GetPeerSnapshot"]
    }
  ],
  "digest": "sha256:...",
  "signature": "base64-ed25519-signature"
}
```

路由快照规则：

- `revision` 在同一节点内严格递增。
- Admin 使用独立 Ed25519 路由签名私钥签名规范化内容。
- Gateway 只持有签名公钥，不持有 Admin 私钥。
- 完整快照不设置短期失效时间。Admin 短暂故障时节点继续服务。
- 删除路由通过下一版完整快照表达，不使用容易丢失的删除事件。
- Gateway 先完整校验新快照，再原子替换内存路由和磁盘缓存。
- 任一条路由不合法时拒绝整个版本，保留上一有效版本。
- 相同 revision 内容不一致时视为安全错误并拒绝。

本地路由缓存写入 `data/gateway/routes.json`，采用临时文件、`fsync` 和原子重命名。首次启动时，如果 Admin 不可达且不存在有效缓存，Gateway 必须失败关闭；后续启动允许使用已验签的最后有效缓存。

Admin 路由签名私钥由 `MOOX_GATEWAY_ROUTE_SIGNING_KEY_FILE` 指向独立 `0600` 文件。该私钥不复用 JWT、service HMAC、health HMAC、TLS CA 或 Admin 数据加密密钥。部署包只包含签名公钥。

## 控制面协议

在 Admin SysDeploy 管理面增加以下能力：

### 机器接口

- `GetGatewayRouteSnapshot(node_id, current_revision)`：返回新快照或 unchanged。
- `ReportGatewayStatus(node_id, applied_revision, route_count, health, last_error)`：上报应用结果。

机器接口通过中央 `/api/service/sysdeploy/*` 调用。每个节点使用独立 Ed25519 身份密钥，Admin 根据 key ID 绑定的 node ID 校验请求，调用方不能读取其他节点的路由。

### 管理接口

- `ListGatewayNodes`
- `GetGatewayNode`
- `EnableGatewayNode`
- `DisableGatewayNode`
- `ListNodeServiceDeployments`
- `UpdateNodeServiceDeployment`
- `PublishGatewayRoutes`
- `ListGatewayRouteRevisions`
- `RollbackGatewayRouteRevision`

普通服务记录变更只修改草稿期望状态。管理员执行“发布路由”后，Admin 校验全部路由、生成新 revision 并签名。紧急禁用单条服务也走一次完整发布，保证版本可审计和可回滚。

Gateway 每 15 秒主动拉取一次。节点只需要向外访问中央 Admin；Admin 不需要连接节点内部端口。状态上报与拉取共用一次周期，失败采用带抖动的指数退避，最大间隔 60 秒。

### 节点注册

节点注册由部署程序完成，不开放匿名自注册：

1. 部署程序在目标节点生成 Ed25519 身份私钥，权限设为 `0600`。
2. 部署程序通过已登录且签名的 Admin 控制面提交 node ID、显示名称、公开地址和公钥。
3. Admin 创建或核对 `t_gateway_nodes` 与 `t_gateway_identities`，返回路由签名公钥和节点证书材料。
4. 部署程序安装 Gateway 配置、身份私钥、CA 和路由签名公钥。
5. Admin 发布该节点的初始 revision。
6. Gateway 首次启动后使用节点身份拉取初始快照并上报 applied revision。

重复部署必须复用同一 node ID 和私钥。私钥丢失时执行显式的身份轮换流程，不能静默生成新身份覆盖旧记录。

## 请求鉴权

节点机器网关不复用浏览器 JWT、Admin 登录会话或集群共享 HMAC。定义独立的 `moox-node-auth-v1`：

- key ID 标识调用服务或节点。
- 调用方使用自己的 Ed25519 私钥签名，接收方只保存公钥。
- 签名覆盖 HTTP 方法、完整路径、body 摘要、时间戳、nonce、`X-Space-Id` 和目标 `X-Moox-Gateway-Node`。
- `X-Moox-Gateway-Node` 必须与当前 Gateway 的 `node_id` 相同。
- 默认有效期 60 秒，时钟偏差 30 秒。
- nonce 在本节点持久化并原子消费。
- 公钥按 key ID 形成 keyring，支持新旧 key 并行轮换。

把目标节点加入签名可阻止攻击者把发往广州节点的合法请求重放到香港节点。每个节点维护本地 nonce 即可，不需要共享 Badger。

Gateway 的入站 keyring 和权限随签名路由快照一起发布，快照只包含 key ID、公钥和权限，不包含任何私钥或共享 secret。第一阶段仅为 Monitor peer 和中央运维调用方登记最小权限身份。

每项调用身份绑定允许访问的 `service_id/method` 列表。Monitor peer 身份只能调用 `monitor/GetPeerSnapshot`，不能调用 Collector、Trade 或 SysDeploy。

为保证中央节点平滑迁移，Gateway 在过渡期支持两种逐路由鉴权策略：

- `legacy_service_hmac`：仅允许中央节点上现有 Collector、Factor、CloudRuntime 和 CLI 路由使用，凭据与 nonce 保持当前 `moox-auth-v2` 契约。
- `node_ed25519`：所有新增节点间调用使用，Monitor peer 首先迁移到该策略。

香港等新增节点不得启用 `legacy_service_hmac`。后续把中央调用方逐项迁移到 `node_ed25519`，再删除 Gateway 中的旧 HMAC 兼容逻辑。这样切换中央 Caddy upstream 时不会要求所有现有调用方同时升级，也不会把同一 HMAC secret 扩散到多个节点。

## 本机转发约束

Gateway 加载路由时强制执行：

- TCP 上游只允许 loopback 地址。
- Unix Socket 必须位于 Gateway 配置允许的目录。
- 禁止域名、通配地址、链路本地地址和其他服务器 IP。
- 路由只允许 `trpc-http` 协议。
- 请求路径只允许两段 `{service}/{method}`，拒绝路径穿越和额外斜杠。
- body 默认上限 4 MiB；超限返回 HTTP 413。
- 上游连接、响应头和总请求均有明确超时。
- 不转发客户端提供的 `X-User-Id`、`X-User-Role` 或内部鉴权结果头。
- 只透传明确允许的 trace、space 和内容协商头。

第一阶段不支持任意 Raw HTTP 上游。需要公开的能力必须定义为正式 tRPC HTTP RPC。

## Caddy 与端口

每台节点服务器部署 MooX 管理的 Caddy 和 `moox-gateway`：

```text
Public or controlled network
  -> Caddy HTTPS :443
  -> moox-gateway 127.0.0.1:11003
  -> local service loopback port
```

节点入口优先使用标准 HTTPS `443`。已有环境可在迁移期继续使用 `11001`，验收后再切换。安全组必须允许受信节点或 EdgeOne 回源访问统一 HTTPS 端口；网关只能收敛端口，不能绕过安全组。

Caddy 只代理 `/api/service/*`，对 `/api/admin/*`、`/healthz`、`/readyz` 和 `/metrics` 返回 404。Gateway 自身诊断端口绑定 loopback，并使用独立 health HMAC。

节点证书由统一 MooX 私有 CA 签发，每台节点只持有自己的 leaf 私钥。所有节点和中央 Admin 只分发 CA 公钥，不复制 CA 私钥或其他节点私钥。证书 SAN 使用节点域名；IP 只作为受控过渡 SAN。

## Monitor 迁移

Monitor 增加正式机器 RPC：

```text
trpc.moox.monitor.MonitorMgr/GetPeerSnapshot
```

响应保留当前 peer snapshot 的语义：

- instance ID
- observed time
- 最新检查状态
- 最近告警事件

节点服务实例：

```text
gateway-gz-122 / moox_monitor
  gateway_service_id: monitor
  address: 127.0.0.1:11410
  gateway_path: trpc.moox.monitor.MonitorMgr

gateway-hk-177 / moox_monitor
  gateway_service_id: monitor
  address: 127.0.0.1:11410
  gateway_path: trpc.moox.monitor.MonitorMgr
```

互访路径：

```text
Guangzhou Monitor
  -> https://gateway-hk-177.example/api/service/monitor/GetPeerSnapshot
  -> Hong Kong Gateway
  -> 127.0.0.1:11410

Hong Kong Monitor
  -> https://gateway-gz-122.example/api/service/monitor/GetPeerSnapshot
  -> Guangzhou Gateway
  -> 127.0.0.1:11410
```

迁移期间保留当前内部 snapshot HTTP handler和反向 SSH 隧道。双向 Gateway RPC 连续稳定 24 小时，并完成停机、恢复和告警验证后，删除 Monitor peer 对内部端口的公网依赖，再停用 `moox-monitor-tunnel.service`。

## 管理台设计

在“服务管理”中增加两个视图。

### 节点网关

展示：

- 节点名称、节点 ID 和公开地址。
- 在线状态和最后心跳。
- 期望版本、已应用版本和版本差异。
- 当前路由数、最近错误和证书到期时间。
- 查看路由、发布、回滚、启用和停用操作。

### 服务实例

按节点分组展示：

- 服务名称和机器网关 service ID。
- 本机端口和 tRPC service path。
- 进程健康状态。
- 是否允许机器网关访问。
- 当前路由版本和最近发布时间。

部署程序自动注册或更新服务实例。页面负责审核、启停、修正和发布，不要求管理员从空白表单手工录入所有字段。手工新增仅用于恢复未被部署程序识别的已有服务，并必须通过 loopback、端口和 service path 校验。

所有发布保存操作者、发布时间、旧版本、新版本、路由摘要和差异。回滚生成一个新的 revision，不把版本号倒退。

## 运行与故障语义

### Admin 不可用

Gateway 继续使用最后有效路由，ready 保持 true，但状态详情标记 control plane stale。超过 10 分钟未同步产生告警，不停止本机转发。

### 路由快照无效

Gateway 拒绝整个新 revision，保留旧路由，上报摘要错误。错误日志不输出密钥和完整签名。

### 本机服务不可用

路由仍存在，Gateway 返回明确的 upstream unavailable 错误并记录指标。Monitor 通过独立探测产生服务故障告警。

### Gateway 重启

先加载并验签本地最后有效路由，再开始监听。后台随后联系 Admin 检查新版本。nonce 存储必须跨普通重启保留到 TTL 到期。

### 节点被禁用

Admin 不再签发新路由，并将节点标记 disabled。Gateway 下一次成功拉取后进入拒绝新业务请求状态，但健康端点继续可用，便于运维恢复。

### 路由删除或回滚

Admin 生成新的完整快照。Gateway 原子切换，不出现一部分新路由和一部分旧路由并存的状态。

## 可观测性

Gateway 暴露以下受保护指标：

- 请求数、状态码和耗时，按 service/method 聚合。
- 上游连接失败、超时和 tRPC 错误码。
- 鉴权失败、过期请求、audience 不匹配和 nonce 重放。
- 当前 revision、路由数、路由年龄和控制面同步年龄。
- 路由校验失败和原子切换结果。
- 本地状态库健康和 nonce 清理结果。

日志统一标记 `service_name=moox-gateway`、`node_id`、`revision`、`service_id`、`method` 和 trace ID。日志不得记录 request secret、Auth 完整值、route signature 或业务 body。

健康语义：

- liveness：进程事件循环和本地状态库可用。
- readiness：已加载至少一个有效快照，路由表可读，监听已启动。
- control plane stale：作为 health details 和告警指标，不直接把 readiness 置为 false。

## 配置

每台服务器只保存最小启动配置：

```yaml
node:
  id: gateway-gz-122

server:
  service_addr: 127.0.0.1:11003
  health_addr: 127.0.0.1:11013

control_plane:
  base_url: https://admin.example.com:11001
  refresh_interval: 15s
  max_backoff: 60s
  ca_file: ./certs/control-plane-ca.crt
  identity_key_file: ./secrets/gateway-node-ed25519.key
  route_verify_key_file: ./certs/route-signing.pub

store:
  path: ./data/gateway/gateway.db

proxy:
  max_body_bytes: 4194304
  allowed_unix_socket_dirs: []
```

路由、服务地址和调用方公钥不写入长期人工维护的 YAML。节点身份私钥权限固定为 `0600`；CA、路由签名公钥和调用方公钥可以是 `0644`。

## 构建与部署

构建系统增加：

- `scripts/build.sh gateway`
- release archive 中的 `gateway/bin`、`gateway/config` 和公共证书目录
- `start.sh gateway`、`stop.sh gateway` 和 `healthcheck.sh gateway`
- `scripts/deploy-moox.sh --with-gateway`

发布顺序：

1. Admin schema 和管理 API 支持节点服务实例，但旧 Admin service gateway 继续运行。
2. 在中央节点旁路启动 `moox-gateway`，不接入 Caddy。
3. 对同一组请求执行契约测试和影子验证。
4. 把中央 Caddy 的 `/api/service/*` upstream 从 Admin `11002` 切换到 Gateway `11003`。
5. 在香港节点部署 Gateway 和 Caddy 节点入口。
6. 发布 Monitor `GetPeerSnapshot` RPC，切换双向 peer URL。
7. 完成 24 小时稳定性和故障演练。
8. 停用反向 SSH 隧道。
9. 从 Admin 移除 `trpc.moox.gateway.service`，保留 `trpc.moox.gateway.control`；本机调用方从旧 `11002` 迁移到 Gateway `11003`。

任何阶段失败都回滚 Caddy upstream 或 Monitor peer URL，不回滚已经兼容的数据库字段。

## 测试

### 单元测试

- 路由快照规范化、摘要、签名和验签。
- revision 单调性、相同版本内容冲突和完整快照删除。
- loopback、Unix Socket、path、body 大小和超时校验。
- node audience、时钟窗口、nonce 重放和 key 轮换。
- 上游 header 白名单、tRPC 错误转换、gzip 和 trace 透传。
- Admin 路由编译器只输出指定节点 active 且 gateway-enabled 的服务。

### 集成测试

- Admin 生成快照，Gateway 拉取、验签、持久化并原子应用。
- Admin 停止后 Gateway 使用最后有效路由继续转发。
- 无缓存且 Admin 不可用时 Gateway 失败关闭。
- 非法路由导致整版拒绝，旧路由继续可用。
- 两个 Gateway 使用相同 nonce 时各自只接受目标 audience 正确的请求。
- 节点禁用、路由删除、发布和回滚均产生新 revision。
- Gateway 不能访问非 loopback 上游。

### 部署契约测试

- 只有 Caddy 拥有公网 HTTPS 端口。
- Gateway service 和 health 监听保持 loopback。
- Caddy 只代理 `/api/service/*`。
- 配置、凭据、CA 和本地数据库权限正确。
- 重复部署不重置 node ID、路由缓存、nonce 数据或证书身份。
- 发布失败恢复旧二进制、配置和 Caddy upstream。

## 验收标准

1. 广州和香港各运行一个独立 `moox-gateway`，重启后自动恢复。
2. Admin 页面同时显示两个节点、已应用 revision、路由数和最后同步时间。
3. 每个 Gateway 的全部上游均为本机 loopback。
4. 修改服务实例后，只有目标节点获得新 revision。
5. Admin 停止 10 分钟期间，两个 Gateway 继续转发已有路由。
6. 非法签名、错误 audience、过期请求和 nonce 重放均被拒绝。
7. Monitor 双向快照、故障触发和恢复告警通过节点 HTTPS 网关完成。
8. 安全组不再开放 Monitor `11409/11410`，只保留统一 HTTPS 入口。
9. 连续运行 24 小时无路由漂移、同步错误和告警丢失。
10. 完成验证后停用反向 SSH 隧道，任一节点故障仍能被对端感知。

## 分阶段交付

### 第一阶段：契约与数据模型

- 建立 `modules/gateway` 骨架和公共路由/鉴权包。
- 扩展 Gateway 节点和服务实例 schema。
- 实现 Admin 路由编译、签名、拉取和状态 API。
- 保持现网流量仍由 Admin service gateway 承载。

### 第二阶段：中央节点替换

- 在广州部署 Gateway。
- 完成 Admin service gateway 与新 Gateway 的响应契约对比。
- 切换中央 Caddy upstream。
- 验证 Collector、Factor、CloudRuntime 和 CLI 兼容性。

### 第三阶段：双节点与 Monitor

- 在香港部署 Gateway 和节点 HTTPS 入口。
- 发布 Monitor peer RPC 和节点凭据。
- 切换双向 Monitor peer 路径并执行故障演练。
- 稳定后移除 SSH 隧道。

### 第四阶段：清理

- 从 Admin 删除机器网关监听和 service HMAC nonce 职责。
- 清理旧配置、部署脚本分支和过渡兼容逻辑。
- 更新架构、鉴权、服务管理和运维文档。

## 已确认决策

- 新模块固定放在 `modules/gateway`。
- 只拆机器服务网关，不拆浏览器控制面网关。
- 每台服务器一个 Gateway，只转发本机服务。
- Admin 管理服务实例，路由自动生成。
- Gateway 主动拉取签名完整快照并保存最后有效版本。
- 服务部署程序自动注册，管理页面负责审核、启停、修正、发布和回滚。
- Monitor 使用正式 RPC 通过节点 Gateway 互访。
- 节点 HTTPS 入口优先收敛到标准端口 443。
