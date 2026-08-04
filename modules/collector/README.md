# moox-collector

MooX 的行情采集控制面和短时 SCF 运行时。Collector 负责规则、完整 Symbol Dataset、稳定
Timer 分片、公共 DNS Environment 和数据新鲜度；SCF 只执行一批行情请求，不常驻等待任务。

实时 K 线的正式架构是 `node_type=scf-event, trigger_type=timer`：Collector 每分钟协调
每个函数独立的任务环境，最多 30 个标的；Tencent Timer 到点触发 SCF，函数并发请求 Binance
并聚合写 Storage。SCF 不请求 Collector/Admin，也不发布实时 Completion。Symbol 快照、缺口
补采、出口探针和人工 E2E 才使用有界 `InvokeFunction`。常驻心跳、EventBus 消费和逐节点实时
Invoke 会增加函数运行时长或控制面排队，不能重新引入。维护背景见
[SCF 短时行情采集架构](../../docs/architecture/scf-short-lived-market-fetch.md)。

配置驱动的标准发布会在每个启用地域自动补 1 个 `trigger_type=invoke` 辅助函数；它不计入
`custom.toml` 的 Timer `function_count`，只用于 Symbol 快照、缺口补采、探针和人工 E2E。

## 职责

| 组件 | 职责 |
| --- | --- |
| `moox-collector` | Rule、TaskInstance、Timer 分片、DNS 协调和有限 Invoke 补采 |
| `moox-collector-scf` | 从 Timer Environment 读取任务，抓取、批量写入 Storage、逐标的 CLS |
| `modules/cloudnode` | 云账户、代码包、函数节点、Timer Trigger 与受管 Environment |
| `modules/storage` | 行情数据和最新时间水位真值 |
| `modules/monitor` | Dataset freshness、部署状态和中文异常诊断 |

实时主链：

```text
TaskRule + Symbol Dataset -> Collector Reconciler
         -> CloudNode Environment Patch -> Tencent Timer Trigger
         -> SCF 64MB/15s -> Binance -> Storage Primary batch upsert
         -> Dataset/K线 freshness + CLS
```

SCF 不启动 JetStream 任务消费者、驻留循环或后台 reporter。未被调用的富余函数关闭 Timer
以避免空跑费用；Monitor 通过 Collector 协调状态、Timer 状态、Storage freshness 和 CLS 判断采集是否正常。

## 规则

Symbol Rule 将手动配置的 Binance 标的写入 RECORD Dataset。K 线 Rule 从关联 Symbol Dataset
读取 active subjects，写入 TimeSeries Dataset。实时 K 线批次每项只请求最近 3 根并过滤未收盘
数据；长缺口由独立 CatchupBatch 分页恢复。

每个启用规则按当前 Timer 函数确定性分片；单个函数最多 30 个标的，完整 Environment 还必须不超过 4KB。函数固定为 64MB、15 秒，Storage 请求预算为 5 秒，函数内 HTTP 并发由 Environment 配置；短暂失败由下一次 Timer 及独立缺口补采覆盖。

规则、运行态字段与接口说明见 [采集任务管理](../../docs/采集任务管理.md)。

## 构建

```bash
make build
make build-linux
make build-scf
make package-scf

# 仓库根目录
./scripts/build.sh collector
./scripts/build.sh collector-scf
./scripts/build-collector-scf-package.sh
```

SCF 包通过腾讯云 API 查询 `moox/moox-application` 资源并写入真实 Topic ID；没有资源或索引时
构建失败，不能使用写死 ID。

## 运行与配置

- CollectMgr HTTP：`:11402`。
- 管理台 Rule API：`/api/admin/collectmgr/{Method}`。
- Collector 调 CloudNode：`GetNodeList(trigger_type=timer)`、受管 Runtime Config Batch；
  `InvokeFunction` 只供 Symbol 快照、缺口补采、探针和人工 E2E。
- EventBus：`moox.market.fetch.batch.completed.v1.*` 仅供有界 Invoke Completion，实时 Timer 不依赖它。
- Collector 数据库默认：`./data/moox_collector.db`。

关键环境变量：

- `MOOX_COLLECTOR_DB_PATH`：Collector SQLite 路径。
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL`：通过 SysDeploy 发现依赖。
- `MOOX_COLLECTOR_STORAGE_METADATA_TARGET` / `MOOX_COLLECTOR_STORAGE_ACCESS_TARGET`：Storage Gateway 覆盖。
- `MOOX_GATEWAY_NODE_ID` / `MOOX_GATEWAY_SERVICE_KEY_ID` / `MOOX_GATEWAY_SERVICE_SECRET_KEY`：服务网关签名。
- `MOOX_FETCH_MAX_INFLIGHT_REQUESTS`：SCF 内 HTTP 并发，范围 1 到 64。
- `MOOX_FETCH_REQUEST_TIMEOUT_MS`：单次行情 HTTP 超时，默认 1000ms。

短时函数由 CloudNode metadata 和环境变量共同回读；`custom.toml` 只提供初始化发布种子，不是
第二份运行时真值。

## 验证

```bash
cd modules/collector
go test -race -count=1 ./internal/marketfetch ./internal/store ./internal/bootstrap ./internal/rpc ./test
```

真实验收先运行 `moox-cli collector function probe-egress`，随后回读 Timer Function Environment
和 Trigger，用分片 Symbol 验证真实 Storage 数据连续三个周期。实时定位字段是函数名、Timer
event 时间、Assignment/DNS hash、CLS 逐标的结果和 Storage watermark；有界补采另行保留
`batch_id`、CloudNode `request_id`、Completion 和 RetryItem。旧 `cloud_job_item_id`、JobItem
终态和常驻 SCF runner 不是实时 Timer 路径的验收条件。
