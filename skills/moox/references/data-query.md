# 查询采集的 K 线

用户要取采集到的 K 线数据时，统一使用操作员命令 `moox-cli data kline get`。这是只读的 RPC 查询，经 `moox.toml` 里的操作员 SSH 网关以 `moox-cli` 身份调用，不使用浏览器登录会话。

## 命令用法

`moox-cli data kline get` 支持两种定位数据集的方式（二选一）：

1. **直接传 `--dataset-id`**：已知结果数据集 ID 时直接读 Storage（`PrimaryStore/ReadTimeSeriesRows`）。
2. **传 `--tag-id` 与 `--interval`**：按空间、标签与周期现查 Collector 的 K 线采集任务（`CollectMgr/GetTaskList`），找到对应结果数据集后再读 Storage；如果同一标签、周期匹配到多条任务且无法唯一区分，命令会列出候选任务与数据集 ID，改传 `--dataset-id` 即可。

不确定数据集 ID 或标签 ID 时，先看任务清单：

```bash
./bin/moox-cli collector task list --space-id crypto
```

## 参数说明

- `--file`：SSH 主机配置文件，默认 `./moox.toml`。
- `--space-id`（必填）：业务空间，例如 `crypto`、`stockcn`。
- `--dataset-id` / `--tag-id`（二选一）：直接指定结果数据集 ID，或按标的标签 ID 现查采集任务结果。
- `--symbol`（必填）：交易标的，原样传入（例如 `BTC-USDT`、`600519.SH`），不要改写或猜格式。
- `--interval`：K 线周期，默认 `1m`。
- `--series-tag`：可选，精确筛选序列标签；省略时读取该标的全部序列，显式传空串表示默认序列。
- `--limit`：返回条数（`1..1000`），默认 `100`。
- `--from` / `--to`：可选，RFC3339 时间范围（`[start, end)`）。起始时间不能晚于结束时间。
- `--columns`：返回的列（逗号分隔），默认 `open,high,low,close,volume`；需要 `quote_volume`、`trade_num` 或 A 股附加字段时显式列出。
- `--timeout`：RPC 超时时间，默认 `15s`。
- `--output`：可选，输出 JSON 文件路径（权限 `0600` 原子写入）；省略时输出到 stdout。

## 示例

按标签与周期获取加密货币 `BTC-USDT` 最新 100 根 1m K 线：

```bash
./bin/moox-cli data kline get \
  --space-id crypto --tag-id binance_spot --interval 1m \
  --symbol BTC-USDT
```

按数据集 ID 获取最新 20 根 1m K 线：

```bash
./bin/moox-cli data kline get \
  --space-id crypto --dataset-id <结果数据集ID> --interval 1m \
  --symbol BTC-USDT --limit 20
```

按指定 UTC 时间范围获取 K 线并写出文件：

```bash
./bin/moox-cli data kline get \
  --space-id crypto --tag-id binance_spot --interval 1m \
  --symbol BTC-USDT \
  --from 2026-08-28T00:00:00Z --to 2026-08-28T01:00:00Z \
  --columns open,high,low,close,volume,quote_volume,trade_num \
  --output ./btc-1m.json
```

## 处理结果

成功时解析 protobuf JSON 响应，汇总空间、数据集、标的、周期、行数与时间范围。保持返回的顺序：不带时间范围的最新数据查询是降序；带了任一时间边界的查询是升序，返回匹配的最早 `limit` 行。不给起始时间默认是 Unix 纪元，不给结束时间默认是当前时间。行集为空表示查询成功但该区间没有采集到数据，不是 RPC 失败。

输出和诊断信息里不要包含网关私钥、请求签名或 `moox.secrets.toml` 内容。
