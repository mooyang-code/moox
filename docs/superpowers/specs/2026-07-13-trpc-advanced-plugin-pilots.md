# tRPC 高级插件试点准入

以下能力按最小范围启用；扩大范围前仍需独立变更、回滚步骤和对应测试证据。

| 插件 | 首个范围 | 必要证据 | 禁止范围 |
|---|---|---|---|
| slime | Factor 到 Storage 的 `ReadTimeSeriesRows` | 注入网络错误证明最多两次，且写调用未挂 filter | trade 写入、任务创建、timer、事件发布、ack、所有写操作 |
| transinfo-blocker | Factor 到 Storage 客户端 | trace/space 白名单，JWT、auth、HMAC 不透传 | 未盘点前的全局启用或黑名单 |
| masking | Admin Secret、Trade API Key 响应 | 官方 filter 与受控明文接口兼容测试 | 请求字段、写入字段、`RevealSecret` 内部响应 |
| filterextensions | 一个成本高的读方法 | filter 顺序测试 | 大范围服务改造 |
| OpenTelemetry | Admin 与 Storage server/client | collector 兼容、1% 采样、body 抑制、访问控制与成本归属 | 未验证前扩大到其他模块、与 Jaeger 双栈 |
| degrade/hystrix | 暂不试点 | 压测、SLO、批准的 fallback | 作为 WAF 或边界防护替代 |

官方 `oteltrpc v1.0.2` 固定使用 OpenTelemetry 1.16，与 Storage 依赖的 1.35 API 不兼容，因此第一阶段使用 MooX 的同名兼容适配器。适配器只记录 RPC 名称、状态和 W3C propagation，不记录 body；只有设置 `MOOX_OTEL_ENDPOINT` 才创建 exporter。上线前仍须确认 collector 传输、保留、访问控制与费用负责人，并完成一条端到端 trace；不得并行部署 Jaeger。
