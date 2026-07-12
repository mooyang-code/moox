# MooX 子服务健康检查 tRPC 注册改造

## 目标

所有可长期运行的 `modules/*` 服务通过 tRPC `http_no_protocol` 注册健康接口，避免每个服务自行创建 `net/http.Server`。Monitor 通过健康 JSON 判断存活/就绪，通过 Prometheus `/metrics` 采集统计。

## 统一接口

- `/healthz`：进程存活检查。能够处理请求即返回 HTTP 200。
- `/readyz`：依赖就绪检查。数据库、EventBus、Worker 或关键调度器未完成时返回 HTTP 503。
- `/metrics`：Prometheus 文本指标。

## tRPC 配置

每个服务在 `config/trpc_go*.yaml` 中增加独立服务：

```yaml
- name: trpc.moox.<module>.Health
  port: <health-port>
  network: tcp
  protocol: http_no_protocol
  timeout: 3000
```

代码使用 `http.RegisterNoProtocolServiceMux(server.Service, mux)` 注册。健康请求因此和业务服务共享 tRPC 的生命周期、Filter、超时、日志和监控链路。

## 状态模型

公共 `packages/healthz` 提供响应结构、存活/就绪 Handler、标准路由和 tRPC 注册函数。各模块的 `internal/health.State` 保留模块状态和业务详情，服务只负责更新依赖状态与指标。

健康响应应包含 `module`、`service`、`instance_id`、`ready`、`status`、`version`、`git_commit`、`start_time`、`time` 和 `details`。

## Monitor 使用

Monitor 为每个 SysDeploy 服务读取 `health_url`：

- HTTP 探测 `/healthz`，避免依赖故障触发进程重启。
- HTTP 探测 `/readyz`，生成未就绪告警。
- 定时采集 `/metrics`，统计连接、队列积压、处理延迟、错误数和业务吞吐。

## 已迁移服务

Archive、Admin Gateway、CloudNode、Collector、EventBus、Factor、HostAgent、Monitor、Storage、Strategy、Trade 均提供 Health tRPC service 配置或网关内的 tRPC 标准 HTTP 路由。SysDeploy 默认配置为可监控服务补充 `health_url` 和 `monitor_enabled`。

## 验收重点

1. 所有 Health 服务只通过 tRPC Server 启动，不再调用独立 `ListenAndServe`。
2. 端口占用由 `server.Serve()` 返回启动错误。
3. 依赖未就绪时 `/healthz` 仍为 200，`/readyz` 为 503。
4. Monitor 可通过 `health_url` 和 `/metrics` 统一检查、统计。
