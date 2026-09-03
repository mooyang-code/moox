# MooX 示例

默认系统初始化文件集中在仓库根目录 [`config/setup/`](../config/setup/)：

- `metadata.yaml`：A 股、加密货币和内部监控元数据。
- `dataset-health-policy.yaml`：Monitor 的 Dataset 健康判定阈值。
- `service-deployments.yaml`：Admin 服务部署清单。
- `collector-rules.yaml`：Collector 默认采集规则。

新系统使用 `moox-cli setup init` 读取这个固定目录，不需要逐个挑选 YAML：

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

行情样例在 [`data/kline/`](./data/kline/)，可执行端到端流程在
[`e2e/`](./e2e/)。业务运行数据、交易所标的和凭据不进入默认配置。
