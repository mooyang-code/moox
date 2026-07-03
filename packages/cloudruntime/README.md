# packages/cloudruntime

`packages/cloudruntime` 是 MooX 根级共享库，承载 CloudNode work_item runtime 的通用逻辑。

它不属于 `modules/cloudnode`、`modules/collector`、`modules/factor` 中任意一个业务模块，避免业务模块之间直接 import 对方实现。

## 职责

- 使用 `/api/service/cloudnode/PollWorkItems` 获取 CloudNode work_item lease。
- 调用业务模块传入的 workload handler。
- 使用 `/api/service/cloudnode/ReportWorkItemStatus` 回报执行结果。
- 生成 `/api/service/*` 所需 HMAC 服务鉴权 header。

## 边界

- 本包不直接依赖 collector/factor/trade 等业务模块。
- 本包不持久化状态，不拥有数据库表。
- 业务模块只负责把 work_item payload 转成自己的执行模型。

