# 量化初始化元数据市场收敛设计

## 背景

`examples/metadata-quant-initial.seed.yaml` 当前将 Binance 和 OKX 分别建模为
`crypto_binance`、`crypto_okx` 两个 Space。这个结构把数据来源误当成了业务市场，
与存储元数据的概念边界不一致：Space 是业务命名空间，DataSource 表示数据来源，
Dataset 表示来自单一 DataSource 的一类事实数据。

仓库内其他加密货币示例已经使用 `space_id: crypto`，但 Dataset 命名和初始化文件
仍存在不一致，需要以统一规则收敛。

## 目标

- 加密货币只保留一个 `crypto` Space，显示名称为“加密货币市场”。
- Binance 和 OKX 作为该 Space 下的两个 DataSource。
- 交易所、产品类型和单一频率由 Dataset ID 明确表达。
- Field、DatasetColumn、View 和 ViewColumn 全部在 `crypto` Space 内建立显式引用。
- 检查相关示例 seed，消除把交易所建模为 Space 的用法。
- 不修改 Collector 的市场能力模块边界；Collector 模块名描述采集实现，不代表
  Storage Metadata Space。

## 元数据结构

### Space 与 DataSource

```text
Space: crypto（加密货币市场）
├── DataSource: binance（币安）
└── DataSource: okx（欧易）
```

Space 维护加密货币领域共享的字段字典和业务对象。DataSource 负责标识数据的外部
来源。Binance 与 OKX 不再拥有各自的 Field 和 FieldGroup 副本。

### Dataset 命名

Dataset ID 使用以下格式：

```text
<data_source>_<instrument_type>_<data_kind>[_<frequency>]
```

单频 Dataset 必须带频率后缀。本次默认初始化文件中的四个 Dataset 为：

| DataSource | 产品类型 | Dataset ID |
| --- | --- | --- |
| Binance | 现货 | `binance_spot_kline_1h` |
| Binance | 永续合约 | `binance_perpetual_kline_1h` |
| OKX | 现货 | `okx_spot_kline_1h` |
| OKX | 永续合约 | `okx_perpetual_kline_1h` |

若一个 Dataset 本身支持多个频率，则不添加单一频率后缀。例如同时支持 `1m`、
`1h`、`1d` 的 Dataset 可使用 `binance_spot_kline`，防止名称错误暗示单频能力。

### Field 与列契约

`identity`、`quote`、`derivatives` 三组字段在 `crypto` Space 内只定义一次。
四个 Dataset 分别通过 DatasetColumn 引用共享 Field：

- 现货 K 线包含 `open`、`high`、`low`、`close`、`volume`、`quote_volume` 和
  `raw_payload`。
- 永续 K 线在现货列契约基础上增加 `funding_rate`。
- DatasetColumn 仍显式归属具体 Dataset，不通过隐式继承复用。

### View

每个 Dataset 建立一个同来源、同产品类型、同频率的查询 View。View ID 使用
`<dataset_id>_view`，主 Dataset 和投影列均显式引用完整 Dataset ID，避免同一
Space 内产生名称碰撞。

## 变更范围

主要修改 `examples/metadata-quant-initial.seed.yaml`：

1. 合并两个交易所 Space。
2. 合并重复的 FieldGroup 和 Field。
3. 重命名四个 Dataset 及其 DatasetColumn。
4. 重命名四个 View 及其 ViewColumn。
5. 更新注释和显示名称，使建模意图可直接从 seed 中读出。

同时扫描 `examples` 下其他加密货币 seed：

- 将交易所作为 Space 的示例改为 `crypto`。
- 保留已经符合统一 Space 模型的文件。
- 只在 Dataset 为单频且本次文件独立定义该 Dataset 时补充频率后缀；不把多频
  Dataset 错误改成单频名称。

不修改运行时代码、数据库迁移和既有环境数据。项目无需兼容旧初始化数据，新的
环境直接使用更新后的 seed 创建元数据。

## 校验

- 使用 CLI seed 解析和构建导入调用，验证所有引用均可解析。
- 增加针对默认量化 seed 的结构测试，断言只存在一个 `crypto` Space、两个
  DataSource、四个带 `1h` 后缀的 Dataset，且不存在 `crypto_binance` 或
  `crypto_okx`。
- 运行 CLI 元数据相关单元测试。
- 扫描示例 seed，确认不再把 Binance 或 OKX 定义为独立 Space。

