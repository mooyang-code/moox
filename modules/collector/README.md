# moox-collector

MooX 的行情采集控制面和短时 SCF 运行时。Collector 负责规则、稳定任务实例、批次、
重试和数据新鲜度；SCF 只执行一批行情请求，不常驻等待任务。

## 职责

| 组件 | 职责 |
| --- | --- |
| `moox-collector` | Rule、TaskInstance、BatchInvocation、RetryItem、Timer 与 Completion Consumer |
| `moox-collector-scf` | 接收单个 `market_fetch` 事件，抓取、批量写入 Storage、发布完成事件 |
| `modules/cloudnode` | 云账户、代码包、函数节点与腾讯云 `InvokeFunction(Event)` |
| `modules/storage` | 行情数据和最新时间水位真值 |
| `modules/monitor` | Dataset freshness、部署状态和中文异常诊断 |

短时主链：

```text
TaskRule -> Collector Scheduler -> BatchInvocation(planned)
         -> CloudNode InvokeFunction(Event) -> SCF <= 10s
         -> Storage Primary batch upsert -> EventBus Completion
         -> Collector Completion Consumer -> TaskInstance / RetryItem
```

SCF 不启动 JetStream 任务消费者、驻留循环或后台 reporter。未被调用的备用函数没有在线状态是
正常状态；Monitor 通过 Storage freshness 和 Completion 快照判断采集是否正常。

## 规则

Symbol Rule 将手动配置的 Binance 标的写入 RECORD Dataset。K 线 Rule 从关联 Symbol Dataset
读取 active subjects，写入 TimeSeries Dataset。实时 K 线批次每项只请求最近 3 根并过滤未收盘
数据；长缺口由独立 CatchupBatch 分页恢复。

每个实时周期先按当前 SCF 函数数均分标的；单个 BatchInvocation 最多 64 项。函数配置固定为 64MB、10 秒，默认 HTTP 并发为 16；临时错误由 Collector 以 5 秒、30 秒、2 分钟重试，而不是在函数内部退避。

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
- Collector 调 CloudNode：`/api/service/cloudnode/GetNodeList`、`InvokeFunction`。
- EventBus：`moox.market.fetch.batch.completed.v1.*`，Collector 和 Monitor 使用独立 durable。
- Collector 数据库默认：`./data/moox_collector.db`。

关键环境变量：

- `MOOX_COLLECTOR_DB_PATH`：Collector SQLite 路径。
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL`：通过 SysDeploy 发现依赖。
- `MOOX_COLLECTOR_STORAGE_METADATA_TARGET` / `MOOX_COLLECTOR_STORAGE_ACCESS_TARGET`：Storage Gateway 覆盖。
- `MOOX_GATEWAY_NODE_ID` / `MOOX_GATEWAY_SERVICE_KEY_ID` / `MOOX_GATEWAY_SERVICE_SECRET_KEY`：服务网关签名。
- `MOOX_FETCH_MAX_INFLIGHT_REQUESTS`：SCF 内 HTTP 并发，范围 1 到 64。
- `MOOX_FETCH_REQUEST_TIMEOUT_MS`：单次行情 HTTP 超时，默认 2000ms。

短时函数由 CloudNode metadata 和环境变量共同回读；`custom.toml` 只提供初始化发布种子，不是
第二份运行时真值。

## 验证

```bash
cd modules/collector
go test -race -count=1 ./internal/marketfetch ./internal/store ./internal/bootstrap ./internal/rpc ./test
```

真实验收先运行 `moox-cli collector function probe-egress`，随后用 10 个 Symbol 验证真实
Storage 数据。需要保留的定位字段是 `batch_id`、CloudNode `request_id`、Completion 状态、
RetryItem 状态和 Storage watermark；旧 `cloud_job_item_id`、JobItem 终态和常驻 SCF runner
不是短时路径的验收条件。
