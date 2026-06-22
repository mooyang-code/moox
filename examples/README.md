# MooX 示例元数据

本目录放置可直接通过 `moox-cli metadata import` 导入的示例元数据文件。文件按交易空间拆分，并保持在 `examples/` 根目录下，避免过深目录。

这些文件导入的是 moox-storage 的元数据。管理台顶部空间选择器来自 Control 服务；如果页面里尚未出现同名空间，请先在管理台空间设置中创建同名空间，或后续接入 Control 侧空间导入。

## 文件

- `platform-local.seed.yaml`：本地开发和演示用的平台级默认存储拓扑，包含 `local` 主存节点以及 Pebble、DuckDB、Bleve、Parquet 设备。
- `metadata-cn-stock.seed.yaml`：A股交易空间示例，包含东方财富、AKShare、日线行情、股票资料、财务指标、字段、因子、视图和 Dataset 默认主存路由。
- `metadata-crypto.seed.yaml`：加密货币交易空间示例，包含 Binance 现货 K 线、交易对资料、字段、因子、视图和 Dataset 默认主存路由。
- `metadata-crypto-binance-swap-kline.seed.yaml`：Binance U 本位永续合约 1H K 线示例，匹配 `coin-binance-swap-candle-csv-1h-*` CSV 表头，包含 swap 数据集、常用标的、字段、视图和默认主存路由。

## 默认存储拓扑

`platform-local.seed.yaml` 是平台级配置，通常只需要导入一次。它不绑定具体交易空间，也不包含业务 Dataset。

`metadata-*.seed.yaml` 是空间级配置，里面的 `primary_store_routes` 会把每个 Dataset 默认路由到 `local` 主存节点。普通用户不需要手动配置主存节点和设备；只有多节点、迁移或高级运维场景才需要调整这些配置。

## 导入

在仓库根目录执行：

```bash
cd modules/cli

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/platform-local.seed.yaml \
  --metadata-url http://127.0.0.1:19101 \
  --if-not-exists

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-cn-stock.seed.yaml \
  --metadata-url http://127.0.0.1:19101 \
  --if-not-exists

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-crypto.seed.yaml \
  --metadata-url http://127.0.0.1:19101 \
  --if-not-exists
```

试跑不写入：

```bash
cd modules/cli

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-cn-stock.seed.yaml \
  --metadata-url http://127.0.0.1:19101 \
  --dry-run
```
