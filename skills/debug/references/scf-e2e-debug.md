# SCF 端到端调试

MooX collector 的云函数没有按 Timer 运行，或者没有把 K 线数据写进 Storage 时，用这份参考。

## 实时采集是怎么工作的

1. Collector（每个空间一个对账器）计算采集计划，在 Storage 登记序列快照，并把计划写进每个函数的 SCF 环境（`MOOX_*` 变量）。函数的 Timer 触发器保持启用。
2. 每次 Timer 触发时，函数根据计划和事件时间得出要处理的（数据集，标的），并发请求各数据源，然后以 `scf-collector` 外部身份经入口主机网关的外部入口（`11004`）写入：`CommitTimeSeriesBatch`（补采的行用 `UpsertFields`）。入口地址和入口主机 ID（`MOOX_ENTRY_ADDRESS`、`MOOX_ENTRY_HOST`）由部署清单推导。
3. 默认的计划 / 补采路径不调用 Collector，也不需要 EventBus；显式配置的指标 Reporter 可以向 EventBus 发布。数据源请求可以在单次调用的时间预算内重试或切换端点。最终失败记到 CLS（`market_fetch_item_failure`、`market_fetch_batch`）；剩下的缺口由 Collector 通过补采修复，不会重新下发旧的补全批次。
4. 缺口用 `moox-cli collector kline check` 查找，用 `kline backfill` 或 Collector 的自动补采修复。
5. Storage 在一个周期收到第一笔有效提交时才创建该周期，预期的序列全部到齐就立刻标记完成，到截止时间还没到齐则按 degraded 结束；完成时发布 `DatasetPeriodCompleted`（`source=ledger`）。一笔提交都没有的周期没有账本记录，也没有完成事件。Crypto 默认走 `stream`（经 collector-stream 的 WS 推送）；只有实际分配给 SCF 的数据集才用这套 Timer 流程。

## 需要的信息

动手前先收集：

- 函数名、命名空间、地域和 Timer cron（`moox-cli collector function list`）。
- 云账号 ID、COS 桶 / 地域、代码包 ID 和版本。
- 空间、采集任务 ID、结果 Dataset / View、频率，以及出问题的周期时间。
- CLS topic ID，以及出问题的那次 Timer 触发前后的一小段时间范围（CLS 时间戳单位是毫秒）。

不要把 SecretKey、服务鉴权密钥、SSH 密码或签名请求头贴进笔记或最终回答。

## 快速定位

1. 没有代码包：本地构建或 COS 上传失败。
2. 没有函数或 Timer 被停用：对账器连不上腾讯云 SCF（Monitor 的 market-fetch 检查会给出失败原因，例如 `cloud` 或 `quota`）；下次读回时它会修复函数。
3. Timer 触发了但什么都没采：函数环境里的计划没有这个周期的条目（用 `moox-cli collector fetch-plan` 核对），或者事件时间已经超过迟到截止时间。
4. 采集成功但 Storage 里没有行：经外部入口的写入失败（身份 / 方法白名单、`scf-collector` 密钥过期；看入口主机网关上的 `moox_gateway_external_denials_total`）。
5. 行已写入但 View 是空的：查看 View 的跟随进度（`moox-cli data view state`）和 `storage-view` 日志；View 跟随 Primary 的变更日志，没有 JetStream consumer。历史补采先看 `[storage_view.max_age]` 和 `moox_storage_view_expired_rows_dropped_total{space,freq}`：过期的行在写 View 时被有意丢弃，因子结果也一样，而处理本身仍算成功。Primary 的保留期是独立的（crypto 1m 默认：Primary 35 天，View 7 天）。
6. 动代码之前保留 `git status --short`。

## 构建与打包

```bash
moox-cli collector function upload --space-id crypto --cloud-account-id <account-id> --package-name moox-collector-crypto-plan --version <version> --collector-root modules/collector
```

代码包里不带凭据；运行时的地址和密钥来自 Collector 写入的函数环境。用同一个代码包名上传新版本，Collector 就会把函数滚动升级到新版本（代码包在对账 / 补采准备时解析，成功的结果至少缓存 30 秒；常规对账每分钟一次）。

## 查看函数

```bash
bin/moox-cli collector fetch-plan --file ./moox.toml --space-id crypto
bin/moox-cli collector function list --file ./moox.toml --space-id crypto --drift
```

`collector function list` 是只读的：Collector 从腾讯云读回计划里的每个函数，比对是否存在、定时器（cron、是否启用、qualifier、message）、环境里的分配哈希（`MOOX_MARKET_FETCH_ASSIGNMENT_HASH`）和代码包。列表不比对入口地址和调用方身份（`MOOX_ENTRY_ADDRESS`、`MOOX_ENTRY_HOST`、`MOOX_CALLER_KEY_ID`），这几项只由对账器在读回修复时比对并改回。`--drift` 只保留不一致的；符合本空间命名格式但不在任何计划里的函数列在 `unplanned` 下，它们的定时器已停，确认后用 `collector function delete` 删除。旧云账号下创建的函数 Collector 看不到，用 `setup inspect-scf` 查找。

## 控制面检查

```bash
rg -n "fetch plan|backfill|Collector-Cloud|Collector-TencentSCF" /data/moox/logs/collector/trpc.log
```

逐项核对：

- `collector function list` 显示函数在预期的地域和命名空间，代码包最新，Timer 状态与计划预期一致；`operation` 非空表示 Collector 内还有云操作（创建、更新、升级、删除）在执行，下一轮会接上它的结果。
- 对腾讯云的写操作只在 Collector 内部进行（`internal/cloud`，每个函数同一时间只有一个操作）；没有单独的 CloudFunction 进程、任务表或发布租约可查。
- 已生效的计划（`fetch-plan`）列出了预期的数据集，并且没有 `last_error`。

## CLS 日志排查

有 `cls-query` skill 时用它。先查一次 Timer 触发，再放宽范围。

| 现象 | 问题所在 |
| --- | --- |
| 预期的触发时刻没有调用 | Timer 被停用、cron 不对，或函数没部署 |
| 大部分标的的数据源请求报错 | 出口 IP 被封或数据源故障；运行 `moox-cli collector function probe-egress` |
| 提交被拒绝 | 序列快照不匹配（环境里的计划过期），或外部身份 / 方法白名单的问题 |

## K 线数据核对

1. 行用的是交易所的 K 线时间，不是本地时间；只写入已收盘的 K 线。
2. `moox-cli collector kline check --space-id <space> --dataset-id <dataset> --from <RFC3339> --to <RFC3339>` 列出缺失的 K 线。
3. 任务的结果 View（`view_<task>_kline_<freq>`）里，该标的出现在预期的 `data_time`。

## 常见根因

- 在腾讯云控制台手工改动后，Timer 被停用或 cron 不一致。
- 任务或标的变更后函数环境没有更新（计划过期）。
- 函数所在地域没有外部入口主机（带私网地址的 `external_entry = true`）。
- 数据源限流，或该地域的出口 IP 被封。
