# MooX 初始化与端到端示例

`examples/` 只保留默认初始化、部署必需元数据和可执行 E2E。业务运行数据通过模块
API 或采集流程生成，不在这里维护交易所、标的或历史行情快照。

## 元数据文件

- `metadata-quant-initial.seed.yaml`：量化业务元数据唯一初始化事实源，包含 A 股、
  港股、美股和加密货币市场。
- `platform-local.seed.yaml`：本地开发与演示的单节点存储拓扑。
- `metadata-monitor-metrics.seed.yaml`：MooX 服务指标逻辑元数据。
- `metadata-monitor-metrics-local-route.seed.yaml`：服务指标的本地主存路由。
- `metadata-monitor-host.seed.yaml`：主机资源逻辑元数据。
- `metadata-monitor-host-local-route.seed.yaml`：主机资源的本地主存路由。

默认量化 seed 使用市场作为 Space。`crypto` 是加密货币市场，`binance` 和 `okx`
是该 Space 下的 DataSource；单频 Dataset ID 包含来源、产品和频率。

## 导入

在仓库根目录执行：

```bash
moox-cli metadata import \
  --file examples/platform-local.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists

moox-cli metadata import \
  --file examples/metadata-quant-initial.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists
```

只初始化加密货币市场：

```bash
moox-cli metadata import \
  --file examples/metadata-quant-initial.seed.yaml \
  --spaces crypto \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists
```

该 seed 只面向空系统。结构调整后清理旧运行数据并重新导入，不执行旧元数据迁移。

## E2E

删库重建和端到端验证流程见 [e2e/README.md](./e2e/README.md)。E2E 使用默认量化
seed，并通过 Metadata API 动态登记测试 Subject 和 DatasetSubject。
