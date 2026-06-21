# MooX 示例元数据

本目录放置可直接通过 `moox-cli metadata import` 导入的示例元数据文件。文件按交易空间拆分，并保持在 `examples/` 根目录下，避免过深目录。

这些文件导入的是 moox-storage 的元数据。管理台顶部空间选择器来自 Control 服务；如果页面里尚未出现同名空间，请先在管理台空间设置中创建同名空间，或后续接入 Control 侧空间导入。

## 文件

- `metadata-cn-stock.seed.yaml`：A股交易空间示例，包含东方财富、AKShare、日线行情、股票资料、财务指标、字段、因子、视图和主存路由。
- `metadata-crypto.seed.yaml`：加密货币交易空间示例，复用 storage 默认示例并补充因子定义。

## 导入

在仓库根目录执行：

```bash
cd modules/cli

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
