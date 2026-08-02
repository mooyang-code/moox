# SCF 短时行情采集

## 决策

`crypto_market` 的行情请求使用短时 SCF 执行，而不是常驻 Worker、心跳保活或消费 EventBus。目标是利用多地域函数出口请求行情 API，同时把计费运行时长限制在实际拉取、写入 Storage 和一次 CLS 上报的窗口内。

常驻方案会为等待任务、空闲轮询和保活持续计费；心跳只能证明容器仍在运行，不能证明一次采集批次完成。因此 Collector 本地定时器负责扫描已启用的 SymbolTask 和依赖 Symbol Dataset 的行情任务，分成批次并异步调用已部署函数；函数内部用受限并发拉取、聚合结果后一次写 Storage。

## 边界

- SCF 仅接受 `market_fetch` 与 `egress_probe`，固定 64 MB、15 秒超时。
- 每个数据空间独立入口、配置目录、包和函数名前缀。当前实现为 `crypto_market`。
- SCF 不启动 tRPC 服务，不订阅 EventBus，不维持心跳，不发送 Sentinel 健康检查。
- 429、网络超时和 5xx 由 Collector 重试策略和批次重试状态处理；Storage 写入与 EventBus 批次结果仍是完成事实。
- CLS 通过 `packages/clsreporter` 直接调用 SDK：环境变量提供配置，每次调用仅聚合并提交一次；CLS 失败只记标准日志，不改变采集结果。

## 可观测性

Monitor 观察 Collector 上报的批次成功、新鲜度、Dataset 水位和业务检查。每条 SCF CLS 记录至少包含空间、函数、地域、批次、请求、标的、耗时、成功状态、错误分类和写入行数；不再消费 SCF 心跳或 Sentinel 外部检查。

## 部署

配置写入 `custom.toml` 的 `scf_fetcher.spaces`：`space_id` 与 `entrypoint` 均为 `crypto_market`，配置目录为 `scf/crypto_market`。当前推荐在新加坡和东京各部署 5 个函数。发布前调用 `egress_probe` 记录出口信息；发布后由 Collector 触发真实批次并核验 CLS、批次结果和 Storage 数据。
