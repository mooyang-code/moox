# MooX 数据链路验收

当前实时采集有两条路径：SCF 按环境中的计划响应 Timer；`collector-stream` 接收 WS 收盘推送或执行本地定时拉取。执行端直接提交 Storage，Collector 负责计划对账、检查与补采。

## 本地验证

从仓库根目录运行：

```bash
go test -count=1 ./modules/collector/internal/marketfetch ./modules/collector/internal/fetchplan ./modules/collector/internal/klinecheck ./modules/collector/internal/stream/...
go test -race -count=1 ./modules/collector/test -run '^TestKlineResample.*E2E$'
```

第一组验证采集计划、取数、补采与推送组件；第二组是当前实际存在的重采样 E2E（45m 历史作业、A 股小时线），使用 Collector SQLite 与内存 Primary 替身。它不启动真实 SCF、主机网关、EventBus 或 Storage View，因此不能代替云端验收。

Storage 的端到端验证（View 跟随变更日志）在 `modules/storage/internal/service/e2e`；需要 DuckDB/CGO 的测试环境见[构建手册](../../docs/构建手册.md)。

## 真实 SCF 验收

先核对 `moox.toml` 与已部署计划，等待 Collector 自动创建、更新函数。Timer 函数当前超时 60 秒，计划默认内存 128MB（可配置），部署参数由对账器下发；函数应就绪且 Timer 云侧读回与期望一致。

```bash
./bin/moox-cli collector fetch-plan show --file ./moox.toml --space-id stockcn
./bin/moox-cli collector function probe-egress --file ./moox.toml --space-id stockcn
./bin/moox-cli collector function list --file ./moox.toml --space-id stockcn --drift
```

示例用 stockcn 空间：A 股、港股、美股的 K 线任务默认由 SCF 采集（各市场 `market.yaml` 的 `ingest: scf`）；crypto 默认走 `collector-stream` 的 WS 推送（`ingest: stream`），没有 Timer 采集函数，按下文 WS 路径的项核对。

CLI 使用清单中的操作员 SSH 网关身份。`probe-egress` 经 Collector 对已生效计划里的采集函数发起同步诊断调用；公网 IP 反射不可用时不以缺少 IP 判定行情源失败。

先用少量标的的独立采集任务验收，每期保留：

1. `fetch-plan show` 的计划状态、数据集 hash、函数分配和 Timer 读回。
2. SCF CLS 的 `market_fetch_batch`、失败项、写入耗时与请求 ID。
3. Storage Primary 的实际行、周期提交回执，以及 `DatasetPeriodCompleted` 的 complete/degraded 标记；整期零提交不会产生周期标记。
4. View 中同一标的、频率、`series_tag` 的最新已收盘 `data_time`。
5. `collector kline check` 和自动补采作业的缺口、分块状态；Monitor 的数据集健康。

连续观察至少 30 分钟，确认数据持续写入、View 追上、超时与缺口均能解释，再逐步扩大标的规模。WS / 本地执行路径还应核对 stream 心跳、已应用计划和周期到达统计，不能用 Timer 探针替代。

不要为了验收删除 Collector、Monitor 数据库或现有云函数；修复与重置按[部署与运维](../../docs/部署与运维.md)中对应的维护流程执行。
