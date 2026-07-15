# MooX 节点机器网关服务设计

## 状态与约束

本设计用于个人网站的未上线项目，按全新系统设计，不保留旧机器网关兼容层，也不设计灰度、双写、历史版本回滚或复杂密钥轮换。

新模块固定放在 `modules/gateway`。每台服务器部署一个 Gateway，只把请求转发给本机服务。Admin 继续承载浏览器控制面 `/api/admin/*`、登录会话、SSH Raw Handler 和服务部署管理。

## 目标

- 从 Admin 中彻底移除 `/api/service/*` 机器网关职责。
- 在 `modules/gateway` 提供独立的 `moox-gateway` 服务。
- 每台服务器部署一个 Gateway，只允许访问本机 loopback 服务。
- Admin 统一维护服务器和服务实例，自动生成每个节点的路由表。
- Gateway 定时从 Admin 拉取本节点路由，原子更新并保存本地缓存。
- 所有机器请求使用一套简单的 HMAC 鉴权协议。
- Monitor 通过对方节点的 Gateway 获取监控快照，不再使用 SSH 反向隧道。
- 公网只开放 Caddy HTTPS 入口，不暴露各业务服务端口。

## 非目标

- 不拆分 `/api/admin/*` 控制面网关。
- 不实现跨节点负载均衡、服务网格或自动故障转移。
- 不允许 Gateway 转发到其他服务器。
- 不提供通用 HTTP 正向代理。
- 不维护路由草稿、发布记录、历史 revision 或回滚版本。
- 不实现 Ed25519 身份体系、证书自动轮换或调用方权限矩阵。
- 不保留 Admin 旧 `11002` 机器网关和旧鉴权协议。

## 总体架构

```text
                           Admin
               +---------------------------+
               | Host and service registry |
               | Route generation API      |
               | Gateway status API        |
               +-------------+-------------+
                             ^
                 route pull  |  status report
                             |
          +------------------+------------------+
          |                                     |
 +--------+---------+                  +--------+---------+
 | Guangzhou server |                  | Hong Kong server |
 | Caddy :443        |                  | Caddy :443        |
 | Gateway :11002    |                  | Gateway :11002    |
 | -> local services |                  | -> local services |
 +------------------+                  +------------------+
```

Admin 是配置中心，Gateway 是路由执行器。Gateway 不读取 Admin 数据库，也不接受人工上传的路由文件。

## 模块结构

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
  internal/controlplane/
  internal/store/
  internal/health/
```

职责如下：

- `bootstrap`：加载配置和缓存，初始化路由，启动服务。
- `config`：管理 node ID、监听地址、Admin 地址、共享密钥和刷新周期。
- `router`：匹配 `/api/service/{service}/{method}`。
- `proxy`：代理本机 tRPC HTTP 请求并保持错误码、trace 和 gzip 语义。
- `auth`：校验 HMAC、时间戳、目标节点和 nonce。
- `controlplane`：从 Admin 拉取路由并上报状态。
- `store`：保存最后有效路由和 nonce。
- `health`：提供本机健康检查和 Prometheus 指标。

生成两个二进制：

- `moox-gateway`：正式网关服务。
- `moox-gateway-cli`：检查配置、打印当前路由和执行健康诊断。

Gateway 不依赖 `modules/admin/internal/*`。代理和鉴权公共协议放入：

```text
packages/gatewayproxy/
packages/gatewayauth/
```

## 节点与服务模型

### 网关节点

新增 `t_gateway_nodes`：

| 字段 | 含义 |
| --- | --- |
| `c_node_id` | 稳定节点 ID，唯一 |
| `c_host_id` | 关联 `t_ssh_host` |
| `c_name` | 页面显示名称 |
| `c_public_address` | Gateway HTTPS 地址 |
| `c_status` | enabled 或 disabled |
| `c_route_hash` | Admin 当前路由摘要 |
| `c_applied_route_hash` | Gateway 已应用摘要 |
| `c_last_seen_at` | 最近状态上报时间 |
| `c_last_error` | 最近错误摘要 |
| `c_ctime/c_mtime` | 创建和更新时间 |

首批节点 ID 可以使用：

```text
gateway-gz-122
gateway-hk-177
```

节点 ID 是配置标识，不随 IP 地址变化。节点必须关联主机工作台中的一台主机。

### 服务部署

继续使用 `t_service_deployments`，增加：

| 字段 | 含义 |
| --- | --- |
| `c_node_id` | 服务所在 Gateway 节点 |
| `c_gateway_service_id` | URL 中的服务 ID，例如 `monitor` |
| `c_gateway_enabled` | 是否允许 Gateway 暴露 |

约束：

- 唯一键改为 `(c_node_id, c_service_name)`。
- 启用 Gateway 的记录要求 `(c_node_id, c_gateway_service_id)` 唯一。
- `c_host` 只能是 `127.0.0.1` 或 `::1`。
- 只有 `active` 且 `gateway_enabled=true` 的服务进入节点路由。
- Admin 自己的 `/api/admin/*` resolver 只选择 Admin 所在节点的部署记录。

未上线项目直接修改 schema 和初始化数据，不编写旧表结构迁移程序。

## 路由维护

管理员不直接编辑路由。路由由服务部署记录自动生成：

```text
node_id + gateway_service_id
    -> protocol + loopback host + port + trpc service path
```

Admin 提供两个 Gateway 控制接口：

```text
GET  /api/gateway-control/routes?node_id=<node_id>
POST /api/gateway-control/status
```

Caddy 将 `/api/gateway-control/*` 转发给 Admin，接口使用控制面 HMAC 密钥保护，不使用浏览器 JWT。

Gateway 每 15 秒拉取一次本节点完整路由：

```json
{
  "node_id": "gateway-gz-122",
  "route_hash": "sha256:...",
  "generated_at": "2026-07-15T10:00:00Z",
  "routes": [
    {
      "service_id": "monitor",
      "address": "127.0.0.1:11410",
      "service_path": "trpc.moox.monitor.MonitorMgr",
      "timeout_ms": 5000,
      "max_body_bytes": 4194304
    }
  ]
}
```

同步规则：

- `route_hash` 未变化时不更新路由。
- 新路由全部校验通过后才原子替换当前路由。
- 任一条路由非法时拒绝整份配置，并继续使用上一份有效配置。
- 有效配置写入 `data/gateway/routes.json`。
- Admin 临时不可用时继续使用本地缓存。
- 首次启动无缓存且 Admin 不可用时启动失败。
- 服务部署保存成功后立即生效，不增加“发布路由”步骤。

这里不保存历史路由版本。配置错误时直接在服务管理页面修正，Gateway 在下一个刷新周期自动恢复。

## 请求鉴权

最终只保留一套 `moox-gateway-auth-v1` HMAC 协议。

请求头：

```text
X-Moox-Key-Id
X-Moox-Timestamp
X-Moox-Nonce
X-Moox-Target-Node
X-Moox-Signature
```

签名内容包含：

```text
HTTP method
request path
SHA-256 body digest
timestamp
nonce
target node ID
```

规则：

- Gateway 与 Admin 控制接口共享一把 control key；合法服务调用方与两台 Gateway 共享另一把 service key。
- 两类请求使用同一签名算法，但密钥分开，避免业务调用方获得修改路由状态的权限。
- `X-Moox-Target-Node` 必须等于当前 Gateway 的 node ID。
- 请求有效期 60 秒，允许 30 秒时钟偏差。
- nonce 在 Gateway 本地持久化，在有效期内只能使用一次。
- 鉴权失败统一返回 401，不向调用方暴露具体密钥信息。
- 密钥从独立 `0600` 文件读取，不写入数据库、仓库或日志。

个人项目只维护当前密钥，不实现多 key 并行轮换。需要换密钥时，修改两台服务器配置并重启相关服务。

## 本机转发约束

Gateway 加载路由时必须校验：

- 上游地址只能是 loopback。
- 协议只支持 tRPC HTTP。
- 路径只能是 `/api/service/{service}/{method}`。
- body 默认上限为 4 MiB，超限返回 413。
- 每条路由必须配置连接和请求超时。
- 不转发客户端提供的用户身份和内部鉴权结果头。
- 只透传内容类型、压缩、trace 和 space 等白名单头。

Gateway 不支持远端 IP、域名、任意 Raw HTTP 地址或通配路由。

## 端口与 Caddy

旧 Admin 机器网关被删除，因此独立 Gateway 直接使用原端口：

```text
Internet or trusted node
  -> Caddy HTTPS :443
  -> moox-gateway 127.0.0.1:11002
  -> local service loopback port
```

Caddy 路由：

- `/api/service/*` 转发到本机 Gateway `11002`。
- `/api/gateway-control/*` 仅中央站点转发到 Admin `11000`。
- Gateway 的 `/healthz`、`/readyz` 和 `/metrics` 只允许本机访问。
- Monitor、Collector、Factor 等业务端口不开放到公网。

节点 HTTPS 证书沿用当前 Caddy 证书管理方式，本方案不新增私有 CA 管理系统。

## Monitor 双机检查

Monitor 增加正式 RPC：

```text
trpc.moox.monitor.MonitorMgr/GetPeerSnapshot
```

调用关系：

```text
Guangzhou Monitor
  -> https://hk-node.example/api/service/monitor/GetPeerSnapshot
  -> Hong Kong Gateway
  -> 127.0.0.1:11410

Hong Kong Monitor
  -> https://gz-node.example/api/service/monitor/GetPeerSnapshot
  -> Guangzhou Gateway
  -> 127.0.0.1:11410
```

响应包含实例 ID、采集时间、检查结果和最近告警事件。两端 Monitor 使用 Gateway HMAC 签名请求。

新链路验证成功后直接删除内部 snapshot HTTP handler、SSH 反向隧道服务及相关配置，不保留双链路运行期。

## 管理页面

在“服务管理”页面增加两个子 Tab。

### 网关节点

展示：

- 节点名称、节点 ID、关联主机和公开地址。
- 在线状态和最后心跳时间。
- 当前路由摘要、已应用摘要和路由数量。
- 最近一次同步错误。
- 启用、停用、立即同步和查看路由操作。

### 服务实例

按节点分组展示：

- 服务名称和 Gateway service ID。
- 本机端口和 tRPC service path。
- 服务状态和最近健康检查。
- 是否通过 Gateway 暴露。
- 编辑、启停和测试调用操作。

保存服务实例后路由自动生效，不提供草稿、发布、版本历史和回滚界面。

## 故障处理

### Admin 不可用

Gateway 使用最后有效缓存继续转发。超过 10 分钟未同步时记录告警，但 readiness 保持正常。

### 路由配置错误

Gateway 拒绝新配置，保留当前路由并上报错误。管理员修正部署记录后自动恢复。

### 本机服务不可用

Gateway 返回 upstream unavailable，记录服务名、方法、耗时和错误类型。Monitor 负责产生服务故障告警。

### Gateway 重启

优先加载本地缓存并启动监听，再异步向 Admin 拉取最新路由。nonce 数据保留到过期清理。

### 节点停用

Gateway 拉取到 disabled 状态后拒绝业务请求，健康接口继续可用。

## 配置

```yaml
node:
  id: gateway-gz-122

server:
  service_addr: 127.0.0.1:11002
  health_addr: 127.0.0.1:11012

control_plane:
  base_url: https://admin.example.com
  refresh_interval: 15s
  hmac_key_file: ./secrets/gateway-control.key

auth:
  hmac_key_file: ./secrets/gateway-service.key

store:
  path: ./data/gateway

proxy:
  max_body_bytes: 4194304
```

YAML 只保存启动必需配置。服务地址和路由由 Admin 管理，不在节点上重复维护。

## 可观测性

指标保持必要最小集：

- 请求数、状态码和耗时，按 service/method 聚合。
- 上游连接失败和超时数。
- 鉴权失败和 nonce 重放数。
- 当前路由数、路由摘要和最近同步时间。
- 路由校验失败数。

日志包含 `node_id`、`service_id`、`method`、trace ID 和错误摘要，不记录密钥、签名或请求 body。

健康语义：

- liveness：进程和本地存储可用。
- readiness：已加载有效路由且 HTTP 监听正常。
- Admin 同步过期只产生告警，不影响已有路由 readiness。

## 一次性交付步骤

项目未上线，采用一次性替换：

1. 修改 Admin schema、服务部署接口和管理页面，加入 Gateway 节点及节点服务模型。
2. 实现 `modules/gateway`、控制接口、HMAC 鉴权和路由缓存。
3. 为 Monitor 增加 `GetPeerSnapshot`，更新所有机器调用方使用新鉴权协议。
4. 从 Admin 删除 `trpc.moox.gateway.service`、旧 service HMAC nonce 和旧机器网关代码。
5. 在两台服务器安装 Gateway，更新 Caddy 后统一重启 MooX 服务。
6. 验证服务调用和双机监控，随后删除 SSH 隧道和旧 snapshot handler。

不保留旧接口、旧字段、旧端口旁路或回退分支。部署失败时直接修复当前版本并重新部署。

## 测试

### 单元测试

- HMAC 签名、时间窗口、目标节点和 nonce 重放。
- 路由生成、route hash 和非法整表拒绝。
- loopback、路径、body 上限和超时校验。
- header 白名单、tRPC 错误、gzip 和 trace 透传。
- Admin 只为指定节点生成 active 且 gateway-enabled 的路由。

### 集成测试

- Gateway 从 Admin 拉取路由、持久化并原子应用。
- Admin 停止后 Gateway 使用缓存继续转发。
- 首次启动无缓存且 Admin 不可用时启动失败。
- 非法配置不会覆盖当前有效路由。
- 发往广州的签名请求不能在香港 Gateway 使用。
- Gateway 无法访问非 loopback 上游。
- Monitor 经 Gateway 双向获取快照并触发故障、恢复告警。

### 部署验证

- Admin 不再监听机器网关 `11002`。
- 两台服务器的 Gateway 均监听 `127.0.0.1:11002`。
- Caddy 只把 `/api/service/*` 转发到本机 Gateway。
- 各业务服务端口没有公网安全组入口。
- Gateway 重启后能从缓存恢复，再同步最新路由。
- SSH 反向隧道进程和配置已删除。

## 验收标准

1. 广州和香港各运行一个独立 Gateway，Admin 不再包含机器网关。
2. 服务管理页面能维护节点和节点下的服务实例。
3. 服务记录保存后 15 秒内自动同步到对应 Gateway。
4. 每条 Gateway 上游地址都是本机 loopback。
5. Admin 暂时停止时已有路由继续工作。
6. 非法签名、错误目标节点、过期请求和 nonce 重放均被拒绝。
7. 两台 Monitor 经对方 Gateway 互相获取快照并感知对端故障。
8. 公网只暴露 HTTPS 入口，不开放 Monitor 和其他业务端口。
9. 旧 Admin 机器网关、旧鉴权、SSH 隧道和旧 snapshot handler 均已删除。

## 已确认决策

- 只拆机器服务网关，浏览器控制面继续由 Admin 承载。
- 新模块位于 `modules/gateway`。
- 每台服务器一个 Gateway，只代理本机服务。
- Admin 管理节点和服务实例，路由自动生成、自动生效。
- Gateway 使用完整路由快照、本地缓存和 route hash，不维护历史版本。
- 使用简单的集群 HMAC，不建设复杂节点 PKI。
- 独立 Gateway 使用 `11002`，直接替换 Admin 原机器网关。
- Monitor 通过 Gateway 正式 RPC 互访，完成后直接删除 SSH 隧道。
- 项目按未上线新系统实施，不提供兼容层和复杂迁移策略。
