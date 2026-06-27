# Gin → tRPC 普通服务 + 统一 HTTP 转发 迁移执行计划

> 状态：方案已调整，待启动编码
> 日期：2026-06-26
> 决策方：用户

## 一、目标

将 control 服务中基于 gin 框架的 HTTP API 层，迁移到 trpc 普通RPC 服务 + 两个统一 HTTP 转发 API 的架构，彻底移除 gin 依赖，使项目风格统一于 trpc 框架。

## 二、方案调整说明（相对前一版）

放弃 trpc-go/restful 方案（Experimental、PB 转码对 WebSocket/文件流不友好），改为：

- **内部业务逻辑**：定义为 trpc **普通 RPC 服务**（proto 定义 RPC method，`protocol: trpc`），handler 签名 `func(ctx, *pb.Req) (*pb.Resp, error)`。
- **对外 HTTP**：保留两个统一转发 API（`/api/admin/{service}/{method}` JWT、`/api/service/{service}/{method}` HMAC），用 trpc **无服务协议**（`http_no_protocol`）方式实现，即 `thttp.RegisterNoProtocolService(s.Service(name))` + `thttp.HandleFunc`/gorilla mux。统一 API 内部通过**路由表**把 `(service, method)` 映射到具体 RPC 调用。
- 参考实现：`git.woa.com/video_media/content-delivery-plat/external_push_logic/main.go` 的 `sync_copyright_info`（`thttp.HandleFunc` + `thttp.RegisterNoProtocolService`）。

## 三、已确认决策

| 决策点 | 选择 |
|---|---|
| 内部服务形态 | trpc 普通 RPC 服务（`protocol: trpc`） |
| 对外 HTTP | 两个统一转发 API，trpc 无服务协议（`http_no_protocol`）实现 |
| 转发方式 | 保留 `/api/admin/{service}/{method}` 统一路径，路由表把 (service,method) 映射到 RPC 调用（前端不改 URL） |
| proto 组织 | 新增独立 `*Service` proto（如 DnsService），与现有 collector RPC service 解耦 |
| 响应格式 | 直接返回 PB message 的 JSON（前端适配，不再包 UnifiedAPIResponse） |
| 迁移范围 | 全部 8 个服务模块 |
| ssh/fileserver | WebSocket/文件流无法走 RPC，保留裸 `http.Handler`，挂无服务协议 service |
| 工具链 | `trpc-open create -p xxx.proto --rpconly --nogomod --mock=false -o ./admingen` |
| 节奏 | 先 pilot 1 个服务跑通全链路，再批量复制 |

## 四、架构设计

### 4.1 数据流

```
前端 → /api/admin/{service}/{method}   (主 gateway, http_no_protocol, gorilla/mux)
     → authorize filter (JWT)
     → handleControlRequest
     → 解析 serviceID + method
     → 查 dispatcher 路由表得到 (RPCService impl, dispatchFunc)
     → 反序列化 body JSON → pb.Req
     → 强类型调用 RPC impl: ListDNSRecords(ctx, req) → pb.Resp
     → 序列化 pb.Resp 为 JSON 返回前端
```

```
SCF/collector → /api/service/{service}/{method}  (HMAC)
     → 同上，但走 service_auth 鉴权
```

### 4.2 调度层（核心新增）

在 `internal/gateway/` 新增 dispatcher 机制：

```go
// Dispatcher 把 (serviceID, method) 映射到具体 RPC 调用。
type DispatchFunc func(ctx context.Context, body []byte) (interface{}, error)

// 各服务迁移时注册自己的 dispatch 表。
gateway.RegisterDispatcher("dnsproxy", map[string]DispatchFunc{
    "GetDNSRecordList": dispatchListDNSRecords,
    "GetDNSRecordDetail": dispatchGetDNSRecord,
})
```

`handleControlRequest` / `handleServiceRequest` 改为：
1. 查 dispatcher 表；命中则调用，返回 PB resp 的 JSON。
2. 未命中则回退到旧 `ForwardRequest`（兼容未迁移服务，渐进式）。

### 4.3 proto 组织

每个服务新增独立 `*Service` proto（纯 RPC，无 http 注解）：

```protobuf
// dns_service.proto
service DnsService {
  rpc ListDNSRecords(ListDNSRecordsReq) returns (ListDNSRecordsRsp);
  rpc GetDNSRecord(GetDNSRecordReq) returns (GetDNSRecordRsp);
}
```

`protocol: trpc`，与现有 `NodeService` 等 collector RPC service 同级，但服务名独立（`trpc.moox.dns.DnsService`）。

### 4.4 鉴权

- 控制台 JWT：现有 `authorize` filter 已挂在 `trpc.moox.gateway.stdhttp`，对 `/api/admin/` 路径生效，迁移后不变。
- 后台 HMAC：现有 `validateServiceAuth` 在 `handleServiceRequest` 内，迁移后不变。
- 内部 RPC service 不重复鉴权（鉴权在统一转发层完成）。

### 4.5 ssh / fileserver 特殊处理

- WebSocket 终端、SFTP 文件流：不走 RPC，保留裸 `http.HandlerFunc`。
- 用 `thttp.HandleFunc(path, handler)` + `thttp.RegisterNoProtocolService(s.Service(name))` 挂载，与统一 gateway 并列。
- 这部分接受风格不完全统一（裸 http.Handler）。

### 4.6 trpc_go.yaml 变化

- 新增各服务 RPC service 配置：`protocol: trpc`，监听端口。
- 主 gateway `trpc.moox.gateway.stdhttp` 保持 `protocol: http` + `RegisterNoProtocolServiceMux`（或改 `http_no_protocol`，二选一，pilot 阶段保持现状不动）。

## 五、服务迁移清单与顺序

按复杂度从低到高，pilot 在前：

| 序号 | 服务 | 复杂度 | 备注 |
|---|---|---|---|
| 1 | dnsproxy | 低 | **Pilot**。1 router、2 handler，无 WebSocket/文件流，前端无调用 |
| 2 | asynctask | 低 | |
| 3 | monitor | 中 | |
| 4 | collectmgr | 高 | task_instance/task_rule/data_type_config/task_planner 多 handler |
| 5 | cloudnode | 高 | cloud_node/cloud_account/heartbeat/package/batch/cloud_region |
| 6 | auth | 中 | auth/utils/user.go 用 gin |
| 7 | fileserver | 特殊 | 文件流，裸 handler |
| 8 | ssh | 特殊 | WebSocket + SFTP，裸 handler |

## 六、Pilot 详案（dnsproxy）

### 6.1 步骤

1. **proto 定义**
   - `modules/admin/proto/dns_service.proto`，纯 RPC `DnsService`，无 http 注解。
   - message：`IPInfo`/`DNSRecord`/`ListDNSRecordsReq/Rsp`/`GetDNSRecordReq/Rsp`。

2. **代码生成**
   - 更新 `proto/Makefile` 加入 `dns_service.proto`。
   - `trpc-open create -p ./dns_service.proto --rpconly --nogomod --mock=false -f -o ./admingen`。

3. **handler 实现**
   - 新建 `internal/service/dnsproxy/rpc/service.go`，实现 `DnsServiceService` 接口。
   - 业务逻辑复用 `dnsproxy.GetMergedDNSResult` / `dnsproxy.GetConfig` 等，不动 service 层。
   - 返回纯 PB message（不包 UnifiedAPIResponse）。

4. **dispatcher 注册**
   - 在 dnsproxy 包新增 `RegisterDispatcher()`，把 `GetDNSRecordList`/`GetDNSRecordDetail` 映射到 RPC 调用。
   - bootstrap 调用注册。

5. **主 gateway 调度层**
   - `internal/gateway/dispatcher.go`：`RegisterDispatcher` + `Dispatch` 机制。
   - `handleControlRequest`/`handleServiceRequest` 改为优先查 dispatcher，命中走 RPC，未命中回退旧 ForwardRequest。

6. **RPC service 注册**
   - `bootstrap/trpc.go`：`pb.RegisterDnsServiceService(s.Service("trpc.moox.dns.DnsService"), impl)`。
   - `trpc_go.yaml`：新增 `trpc.moox.dns.DnsService` service 配置（`protocol: trpc`，端口）。

7. **移除旧 gin 代码**
   - 删 `dnsproxy/gateway/handler.go`、`dnsproxy/gateway/register.go`、`dnsproxy/api/router.go`、`dnsproxy/api/dns_record_handler.go`。
   - 从 `bootstrap/trpc.go` 摘除 `dnsproxygateway.RegisterDNSProxyGateway()`。

8. **前端改造**
   - dnsproxy 无前端调用，跳过。但响应格式从 UnifiedAPIResponse 变为纯 PB JSON，若有外部消费方需适配（pilot 无消费方）。

9. **编译 + 单测 + 验证**
   - `go build`、`go vet`、`go test`。
   - 启动服务，curl `/api/admin/dnsproxy/GetDNSRecordList` 验证全链路。

### 6.2 Pilot 验收清单（批量复制模板）

- [ ] 纯 RPC proto 定义范例
- [ ] make 生成命令与产物位置
- [ ] PB handler 实现范式（复用 service 层）
- [ ] dispatcher 注册范式
- [ ] 主 gateway 调度层接入
- [ ] trpc_go.yaml RPC service 配置范例
- [ ] 旧 gin 代码移除确认
- [ ] 编译 + 单测通过
- [ ] 端到端 curl 验证

## 七、主要风险与缓解

| 风险 | 缓解 |
|---|---|
| dispatcher 路由表维护成本 | 每服务一个注册函数，bootstrap 统一调用；表项与 RPC method 一一对应 |
| 响应格式从 UnifiedAPIResponse 变纯 PB JSON，前端需适配 | 分服务 PR，前端同步改；pilot 选无前端依赖的 dnsproxy |
| ssh/fileserver 裸 handler 与 RPC 混合 | 文档明确特例，单独阶段处理 |
| 进程内 RPC 调用配置 | pilot 用直接接口调用（持有 impl 引用），避免进程内 RPC target 复杂度；后续按需改 client proxy |
| 8 服务工作量 | 分服务 PR，pilot 形成模板后批量复制 |

## 八、PR 拆分计划

1. PR1: 主 gateway 调度层 + dnsproxy pilot（含模板、文档）
2. PR2: asynctask
3. PR3: monitor
4. PR4: collectmgr
5. PR5: cloudnode
6. PR6: auth
7. PR7: fileserver（裸 handler 特例）
8. PR8: ssh（WebSocket/SFTP 裸 handler 特例）
9. PR9: 收尾——移除 gin 依赖、清理 `internal/gateway` 旧转发逻辑、删除 go.mod gin

## 九、不在本次范围

- 不改 collector / SCF 侧的 RPC 调用（保持现有 NodeService 等 RPC service 不变）。
- 不改 storage 模块。
- 不重构 service 业务逻辑层（只改 handler 适配层与 transport）。

## 十、下一步

启动 PR1：主 gateway 调度层 + dnsproxy pilot 编码。
