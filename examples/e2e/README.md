# MooX E2E

本目录只描述当前短时行情采集链路。旧常驻 SCF 和 CloudNode JobItem 已删除，不能用于验证
当前 Collector。

## 本地闭环

Collector 的短时 E2E 使用嵌入式 JetStream、真实 Collector SQLite、假 Binance、假 Storage
Primary 和 CloudNode 调用替身。它必须验证：

```text
BatchInvocation planned
  -> CloudNode InvokeFunction(Event)
  -> SCF completion published
  -> Collector Completion Consumer ACK
  -> TaskInstance freshness / RetryItem / Storage watermark
```

运行：

```bash
cd modules/collector
go test -race -count=1 ./test -run ShortLivedMarketFetch
```

本地 E2E 的成功证据是 `batch_id`、模拟的 CloudNode `request_id`、Completion 状态、
RetryItem 状态和 Storage watermark；不读取 `cloud_job_item_id`。

## 真实 SCF 验收

发布前先完成配置校验、Collector 部署和短时 SCF 发布。函数必须为 64MB、10 秒、异步自动
重试为 0，并在 CloudNode 中显示 Active。

```bash
./bin/moox-cli collector function probe-egress \
  --control-url "$MOOX_CONTROL_URL" \
  --space-id "$MOOX_SPACE_ID" \
  --service-access-key "$MOOX_GATEWAY_SERVICE_KEY_ID" \
  --service-secret-key "$MOOX_GATEWAY_SERVICE_SECRET_KEY"
```

探针会对每个已部署的 `market_fetcher` 节点发起同步调用。Binance 轻量接口成功是通过条件；
公网 IP 只在反射服务可用时记录，用于确认地域出口分布。

随后创建或启用隔离的 Symbol 和 K 线 Rule，先使用 10 个 Symbol。每个验收周期保存：

1. Collector `BatchInvocation` 的 `batch_id`、状态、`request_id` 和错误摘要。
2. 对应 SCF CLS 日志和 `MarketFetchBatchCompleted` EventBus 事件。
3. 失败时的 `RetryItem` 状态及下一次重试时间。
4. Storage Primary 与 View 中的最新已收盘 K 线时间。
5. Monitor 的 Dataset freshness 与中文诊断。

连续运行 30 分钟后，10 个标的必须持续产生真实 Storage 数据、没有 SCF timeout，且没有
`scf:heartbeat` 告警。之后再扩大到 100 和 1000 个标的。

## 删除旧验收

本项目不迁移旧 JobItem 数据。发布前删除旧 Collector/CloudNode/Monitor SQLite、旧 JobItem
历史目录、旧 Job stream 和旧 Collector SCF 函数，然后重新初始化短时 Fetcher。旧 E2E
runner 已删除，不能在 `make verify-pr` 或发布脚本中调用。
