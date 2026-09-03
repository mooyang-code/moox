# TDX Go 移植边界

## 已实现的协议边界

`packages/tdx` 将 TDX 访问拆成三个互不兼容的 Source：

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

## 2026-09-01 探测记录

本机探测是单线路、单会话的开发环境证据，不能替代 SCF 地域验证，也不构成频控结论：

- `jstdx.gtjas.com:7709` 成功完成普通协议三条 setup command 和证券数量请求。请求流 86 bytes，响应流 403 bytes，读取到 8 次，四个响应帧头分别为 `b1cb74001c02189300000d004f00bd00`、`b1cb74001c02189400000d004f00bd00`、`b1cb74000c0318990000db0fb300b300` 和 `b1cb74000c0c186c00004e0402000200`。完整原始抓包保存在 `/tmp/tdx-wire-jstdx-normal-20260901`，其中 `request-stream.bin` SHA-256 为 `64645939a6177dc90e8d6e3a7ceceadeb5361903cd5e3d6755c3bfb5ddabb202`，`response-stream.bin` SHA-256 为 `964d04ac5fba55e94e1004e91a21f63b077a3635c6d01e5b16c7b7342cef0d78`。该结果推进 `normal_7709` 的开发线路证据，但仍需从实际 SCF 地域重新采集并人工对账。
- 同一线路使用 `normal_7709` 请求 `SH.600000` 的日线 `category=4` 成功解码 3 根 K 线，返回日期为 `2026-08-28`、`2026-08-31`、`2026-09-01`，并含 OHLC、volume、amount。分钟 `category=7` 也能解码 3 根记录，但返回标签为 `2026-10-27 13:30` 至 `13:32`，超出本次探测日期 `2026-09-01`；这说明当前帧和字段解析路径可运行，但服务端数据的时间语义尚未通过人工对账，不能作为 canonical K 线或生产启用证据。
- `113.105.73.88:7709`、`119.147.171.206:7709`、`218.108.50.178:7709` 和 `hq.cjis.cn:7709` 均建立 TCP 连接但在读取首个响应帧时超时；不能据此判定节点永久不可用。
- `jstdx.gtjas.com:7727`、`shtdx.gtjas.com:7727` 的 classic/MAC 尝试都收到 443 bytes 非完整 TDX body，解析以 `unexpected EOF` 结束；当前没有可用的 `7727` 完整响应头、压缩体、解压体和登录成功证据。

## 2026-09-03 分钟 K 线复验与修复

此前 Go 实现将 `600000.SH` 的分钟时间解析成 `2026-10-29`，根因不是请求中的 `category=7` 或 `start=0` 参数，而是压缩日期字段的解码错误：`zipday` 的低 11 位才是 `MMDD`，原实现却直接对完整 16 位值执行 `%100`。例如原始字节 `87 b3` 表示 `2026-09-03`，旧逻辑得到日 `59`，被 `time.Date` 规范化为 `2026-10-29`。

`packages/tdx/commands.go` 已改为先取 `zipday%2048` 再解析月、日，并新增回归测试。修复后从 `shtdx.gtjas.com:7709` 实测 `600000.SH`、`000001.SZ` 的 1 分钟请求均返回 20 根有效记录，时间落在 `2026-09-03`；与 easy_tdx 同线路请求的分钟时间及最新 OHLCV 样本一致。easy_tdx 的协议实现也明确使用 `month=(zipday%2048)//100`、`day=(zipday%2048)%100`，并将 category 7 定义为 1 分钟线。[easy_tdx datetime codec](https://github.com/handsomejustin/easy_tdx/blob/main/src/easy_tdx/codec/datetime_.py)

当前 `normal_7709` 已切换为 `dataset_stockcn_equity_kline` 的 active fallback 来源。正式运行链为 `sina -> tencent -> tdx -> eastmoney`：每个 Provider 请求失败最多重试 3 次，再进入下一个 Provider；TDX 失败后最后使用东方财富。TDX 不参与主分片分配，但会随每个 stockcn SCF 包注册，因此源绑定的 Timer 也能跨 Provider fallback。

仍有两个独立的后续门禁：日线标准化需要修正为公共 UTC bucket 语义，且日线接口返回的当天记录必须等收盘后再纳入；北交所还需要使用真实有效证券代码完成实测。这些门禁不影响当前 1m normal TDX fallback 的启用。

同日稍后重新执行三种 `wire-spike` 变体时，`jstdx.gtjas.com:7709` 的 normal 和 MAC 请求收到 `HTTP/1.1 403 Forbidden`，classic 请求收到 `HTTP/1.1 502 Bad Gateway`，三次均未收到 TDX 帧。原始响应分别保存在 `/tmp/tdx-wire-jstdx-normal-20260901-rerun`、`/tmp/tdx-wire-jstdx-classic-20260901-rerun` 和 `/tmp/tdx-wire-jstdx-mac-20260901-rerun`；这只是当次网络观测，不能据此判定节点永久不可用。Go transport 已将这类非 TDX HTTP 响应分类为 `ErrProtocol`，避免把代理/WAF 返回误报为 TDX body 的 `unexpected EOF`，但该诊断改进不改变 Wire/Field Acceptance 未通过的结论。

因此 `normal_7709` 已有开发环境的完整 setup/count、日线和分钟 K 线解码证据；日线标准化、当日未收盘过滤、北交所有效样本以及目标 SCF 地域验证仍未完成，`ex_classic_7727` 和 `ex_mac_7727` 也不能推进启用门禁。

## 线路探测

`tdx.RouteProber` 只接受 TCP 候选，并根据 SourceID 选择对应的握手/登录探针。线路评分使用实际目标 `host:port` 的协议响应，不移植 tdx-go 需要特权 ICMP 的实现。`routeprobe` 快照按 SCF 地域、Provider、Source 和 Transport 隔离。

SCF 不做主动限频、全局配额或冷却。随机公网出口是部署事实，不是规避上游限制的功能承诺。

## Wire Spike 命令

`packages/tdx/cmd/wire-spike` 用于对一条指定线路执行一次完整会话并保存原始字节流，不会遍历节点或重复重试：

```bash
go run ./cmd/wire-spike \
  -host 113.105.73.88 -port 7709 -variant normal \
  -timeout 8s -out /tmp/tdx-wire-spike-normal
```

`-variant` 支持 `normal`、`ex_classic` 和 `ex_mac`。输出目录包含 `request-stream.bin`、`response-stream.bin` 和 `report.json`；报告只列出每个响应帧的 16 字节头部摘要，原始内容仅写入本地文件。命令是 Wire Spike 的采集工具，不代表线路已经通过协议验收；在正式启用扩展 Source 前仍必须完成目标 SCF 地域的完整抓包、解压和人工对账。
