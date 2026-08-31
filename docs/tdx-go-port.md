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

未完成以上证据的 Source 只能保持 `catalog_only`，不能被 Market manifest 选为唯一生产来源。尤其不能把 classic 7727 和 mac 7727 当成同一协议，也不能把未确认的扩展字段写入 canonical amount/volume。

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
