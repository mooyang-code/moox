# packages/cloudruntime

`packages/cloudruntime` 是 MooX 根级共享库，承载 CloudNode 云函数运行时框架的通用逻辑。

它不属于 `modules/cloudnode`、`modules/collector`、`modules/factor` 中任意一个业务模块，避免业务模块之间直接 import 对方实现。

## 职责

- 使用 `/api/service/cloudnode/PollJobItems` 获取 CloudNode JobItem。
- 按 `job_type` 查找业务模块注册的 handler。
- 调用 handler 执行业务逻辑；业务数据由 handler 写入 Storage。
- 使用 `/api/service/cloudnode/ReportJobItemStatus` 回报执行摘要和错误。
- 生成 `/api/service/*` 所需 HMAC 服务鉴权 header。

## 边界

- 本包不直接依赖 collector/factor/trade 等业务模块。
- 本包不持久化状态，不拥有数据库表。
- 业务模块负责注册 `job_type -> handler`，并把 JobItem `params` 转成自己的执行模型。
- 本包不处理租约续期、控制指令或复杂调度；运行路径保持 `poll -> execute -> report`。

## 目标使用方式

```go
runtime.Register("collect.kline", klineHandler)
runtime.Register("collect.symbol", symbolHandler)
runtime.Run(ctx)
```
