# MooX 示例

默认系统初始化文件集中在仓库根目录 [`config/setup/`](../config/setup/)：

- `metadata.yaml`：A 股、港股、美股和加密货币四个业务空间与 Storage 元数据。
- `dataset-health-policy.yaml`：Monitor 的 Dataset 健康判定阈值。
- `collection-tasks.yaml`：Collector 默认采集任务。
- `factor-tasks.yaml`：默认因子定义与因子任务，由 `moox-cli setup start` 导入。

新系统使用 `moox-cli setup init` 读取这个固定目录，不需要逐个挑选 YAML：

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host storage
```

默认展示策略在 [`strategies/dual_ma_swap_1m.yaml`](./strategies/dual_ma_swap_1m.yaml)：基于币安合约 1m 因子任务的 5/10 双均线（全部合约里快线在慢线之上的都持有、之下的剔除），
不挂账户的观察实例，由 `moox-cli setup start --space-id crypto` 自动创建，说明见 [`skills/moox/references/fresh-deploy.md`](../skills/moox/references/fresh-deploy.md) 的“默认展示策略”。
A 股、港股空间的默认展示策略是同目录的 `dual_ma_cn_5m.yaml`、`dual_ma_hk_5m.yaml`。

行情样例在 [`data/kline/`](./data/kline/)，可执行端到端流程在
[`e2e/`](./e2e/)。业务运行数据、交易所标的和凭据不进入默认配置。
