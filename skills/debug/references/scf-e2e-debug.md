# SCF 端到端调试

Collector 的云函数不能发布、不按 Timer 运行，或不向 Storage 写入 K 线时使用本文。

## 实时采集的工作方式

1. Collector 为每个周期和每个函数分片规划一个 Timer 批次（`t_collector_timer_period_batches`）。
2. CloudNode 把每个函数的分片分配写进它的 SCF 环境变量，并保持它的 Timer 触发器启用。
3. 每次 Timer 触发，函数经外部接入（`MOOX_ACCESS_ADDRESS`、实例 `MOOX_ACCESS_ID`、调用方 `scf-collector`）调用 `ClaimTimerBatch` 并拿到它的批次。
4. 函数并发请求数据源，再经同一个外部接入写入 Storage：`EnsureDatasetPeriod`、`CommitTimeSeriesBatch`、`RecordDatasetPeriodFailures`。
   外部接入地址的选择：函数所在地域有外部接入、且函数已绑定它所在的 VPC 时用私网地址，否则用 `access@storage` 的公网地址（`moox-cli setup scf-network-plan`）。
5. 函数向 EventBus 发布 `MarketFetchBatchCompleted`；Collector 的完成事件消费者更新批次并安排重试。
6. 到周期截止时间，Storage DataNode 把周期标记为 `complete` 或 `degraded`，并发出 `CollectorPeriodCompleted`。

## 先收集这些输入

- 函数名、命名空间、地域、Timer cron 和 CloudNode 节点 ID。
- 云账号 ID、COS 桶/地域、代码包 ID 和版本。
- 空间、采集任务 ID、结果 Dataset/View、频率和出问题的周期时间。
- CLS Topic ID，以及出问题的那次 Timer 前后的窄时间范围。

不要把 SecretKey、服务签名密钥、SSH 密码、带签名的请求头或 EventBus token 粘贴进笔记或最终回复。

## 快速分诊

1. 没有代码包：本地构建或 COS 上传失败。
2. 没有函数或 Timer 被禁用：CloudNode 的节点批次或腾讯云 SCF API 失败。
3. Timer 触发但没有领取到批次：这个分片没有规划批次，或函数环境里的外部接入地址/凭据不对。
4. 领取成功但 Storage 没有行：数据源请求或经外部接入的写入失败。
5. 行已写入但周期一直处于等待：完成事件没有发布或没有被消费；检查 `MOOX_MARKET_FETCH` 和 Collector 的完成事件消费者。
6. 动代码之前保留 `git status --short`。

## 构建与打包

```bash
SCF_SPACE_ID=crypto scripts/build/build-collector-scf-package.sh --eventbus-ca-file /path/to/eventbus-ca.pem
unzip -l release/scf/collector-scf-crypto-<version>.zip
```

预期内容：`main`、`sources/market/*.yaml`、`certs/eventbus-ca.pem`，`stockcn` 还有 `markets/stockcn/{calendar,route}.yaml`。代码包不带任何凭据，
运行时的地址和密钥来自 CloudNode 写入的函数环境变量。

## 发布并检查函数

优先用 CLI，它读取 `moox.toml` 并驱动 CloudNode：

```bash
bin/moox-cli collector function package ...
bin/moox-cli collector function publish submit --file ./moox.toml --space-id crypto
bin/moox-cli collector function publish status ...
bin/moox-cli collector function timer-inventory \
  --file ./moox.toml \
  --space-id crypto --cloud-account-id <account-id> --namespace <namespace> --region <region>
```

`timer-inventory` 是只读的，只有每个 Timer 都有新鲜的云端回读、cron、qualifier 和 message 都符合预期时才报告 `complete=true`。

## 控制面检查

主机上的组件日志在 `<部署根目录>/logs/<组件>/stdout.log`。常用的过滤：

```bash
rg -n "ClaimTimerBatch|timer_period|market_fetch|completion" /data/moox/prod/logs/collector/stdout.log
rg -n "SubmitUpdateNodeRuntimeConfigs|EnsureTimerTrigger|UpdateFunctionConfiguration" /data/moox/prod/logs/cloudnode/stdout.log
rg -n "EnsureDatasetPeriod|CommitTimeSeriesBatch" /data/moox/storage/logs/storage-primary/stdout.log
```

检查这些边界：

- 节点记录里的函数名、地域、命名空间、代码包和 Timer 状态符合预期。
- 函数环境里有当前的分配和 binding hash。
- `t_collector_timer_period_batches` 里有这个分片和周期的行，并且被领取。
- `MOOX_MARKET_FETCH` 上 Collector 的完成事件 durable 在前进。
- 外部接入没有拒绝 `scf-collector`：看 `moox_access_rejected_total`，原因有 `unauthenticated`、`forbidden`、`replayed`、`too_large`、`unavailable`。

## CLS 日志排查

有 `cls-query` 技能时用它（或 `skills/moox/scripts/cls_search.py`）。先查一次 Timer 触发，再放宽范围。关注领取结果、逐个标的的数据源结果、Storage 提交的确认，以及完成事件的发布。

| 现象 | 边界 |
| --- | --- |
| 预期的时刻没有调用 | Timer 被禁用、cron 不对，或函数没有部署 |
| 领取返回空 | 没有规划批次、分组/binding hash 不对，或外部接入鉴权失败 |
| 大部分标的的数据源报错 | 出口 IP 被封或数据源故障；运行 `moox-cli collector function probe-egress` |
| 提交被拒绝 | Storage 周期快照不一致，或外部接入拒绝了调用（`forbidden`：`scf-collector` 的白名单在 `packages/servicecatalog/catalog.yaml`） |
| 提交成功但没有完成事件 | EventBus TLS/凭据（`market-fetch-publisher`）或 CA 不一致 |

## K 线数据验证

1. 行使用交易所的 K 线时间，不用本地时间；只写已收盘的 K 线。
2. 任务的结果 View（`view_<task>_kline_<freq>`）在预期的 `data_time` 上能看到这个标的。
3. 原始行存在但 View 为空时，检查 View 的 consumer、重建状态和 `storage-view` 的日志。

## 常见根因

- 在腾讯云控制台手工改动之后，Timer 被禁用或 cron 漂移。
- 任务或标的变更之后没有更新函数环境（binding hash 过期）。
- 函数环境里的外部接入地址或 `scf-collector` 密钥缺失或过期（重新发布函数，检查 `scf-network-plan`）。
- EventBus 的 CA 或 `market-fetch-publisher` token 轮换后没有重新发布函数。
- 数据源限流，或该地域的出口 IP 被封。
