# TDX Go 移植边界

## 已实现的协议边界

`internal/tdx` 将 TDX 访问拆成三个互不兼容的 Source：

| SourceID | 端口 | 连接行为 | Go 状态 |
| --- | ---: | --- | --- |
| `normal_7709` | 7709 | 建连后执行普通三条 setup command，再请求证券行情/证券目录 | 已实现请求构造、帧头、zlib、价格/成交量解码和 Adapter |
| `ex_classic_7727` | 7727 | 不执行普通 7709 setup，使用 extended classic command | 已实现最小市场探针和基础 bars parser，待真实线路对账 |
| `ex_mac_7727` | 7727 | 使用 MAC 特殊帧，先执行协议登录，再请求 MAC bars | 已实现登录和请求帧 parser，待真实线路对账 |

## 证据状态

当前仓库中的测试是从 easy_tdx 参考布局构造的确定性 wire 样本，能够验证长度、字段偏移、压缩解压和 differential price 恢复；它们不是从生产线路抓取的完整会话。完整 Wire Spike 在启用扩展 Source 前必须记录：

1. 原始请求字节和连接顺序。
2. 完整 16 字节响应头、压缩 Body 和解压 Body。
3. `count`、周期编号、市场编号、分页上限和时间标签。
4. 与 easy_tdx/QUANTAXIS 或客户端行情的人工逐字段对账。

未完成以上证据的扩展 Source 只能保持 `catalog_only`，不能被 Market manifest 选为唯一生产来源。尤其不能把 classic 7727 和 mac 7727 当成同一协议，也不能把未确认的扩展字段写入 canonical amount/volume。

## 当前接入

当前 Collector 的 `config/markets/stockcn/route.yaml` 按频率分路由：`normal_7709` 在 1m 是 fallback，
在 5m 是主源（通达信权重 4、东方财富权重 2，新浪、腾讯为备源），在 1d 是主源（东方财富为备源）；运行时候选链由 `internal/marketwiring/pipeline.go` 的 `newRoutedKlinePipeline` 按请求频率组装；
时序数据写入采集任务独占的 `dataset_<task_id>`。

日内 K 线的时间标签是收盘时刻（2026-11-04 实测 600519 5m：首根 09:35、末根 11:30 / 15:00，与东方财富逐根对齐；
成交量单位为股，首根含 09:25 开盘集合竞价）；午休期间上午最后一根被标成 13:00，Collector 归一化时归回 11:25 那根。
实测样本在 `modules/collector/internal/sources/stockcn/tdx/testdata`。

## 现状与未完成项

`normal_7709`（已在生产使用）：

- 日期解码：压缩日期 `zipday` 只有低 11 位是 `MMDD`，`commands.go` 先取 `zipday%2048` 再解析月、日，有回归测试。
- 日线及以上的键：`modules/collector/internal/sources/stockcn/tdx/provider.go` 的 `dateKey` 把日线记为交易日期的 UTC 零点（周线取所在周周一、月线取当月 1 日），与采集的统一校验一致。2026-11-11 用 600519、000001、300750 的同 10 个交易日与东方财富对过，开高低收逐根相同（见 `route.yaml` 的 1d 注释）。
- 未收盘的根：`normalizeBars` 不返回结束时刻晚于取数时刻的根，交易时段内正在形成的那根、开盘前价格为 0 的当天占位根都会被跳过。
- 北交所：`marketForExchange` 把 `XBSE` 映射到 `MarketBJ`；仓库里没有北交所的实测样本。
- 南京云函数出口的并发与延迟实测结论写在 `route.yaml` 的 5m 注释里。

`ex_classic_7727`、`ex_mac_7727`：只有请求构造和解析代码，没有拿到过完整的真实响应，没有通过上面“证据状态”的要求，Collector 没有接入。

通达信数据源声明的限速默认是每秒 5 个请求、突发 2、单并发（`provider.go`）。云函数的公网出口是随机的，这是部署事实，不是规避上游限制的功能承诺。

## Wire Spike 命令

`modules/collector/internal/tdx/cmd/wire-spike` 用于对一条指定线路执行一次完整会话并保存原始字节流，不会遍历节点或重复重试：

```bash
# 从仓库根目录运行
go run ./modules/collector/internal/tdx/cmd/wire-spike \
  -host 113.105.73.88 -port 7709 -variant normal \
  -timeout 8s -out /tmp/tdx-wire-spike-normal
```

`-variant` 支持 `normal`、`ex_classic` 和 `ex_mac`。输出目录包含 `request-stream.bin`、`response-stream.bin` 和 `report.json`；报告只列出每个响应帧的 16 字节头部摘要，原始内容仅写入本地文件。命令是 Wire Spike 的采集工具，不代表线路已经通过协议验收；在正式启用扩展 Source 前仍必须完成目标 SCF 地域的完整抓包、解压和人工对账。
