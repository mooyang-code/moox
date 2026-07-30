# MooX 初始化与端到端示例

`examples/` 只保留默认初始化、部署必需元数据和可执行 E2E。业务运行数据通过模块
API 或采集流程生成，不在这里维护交易所、标的或历史行情快照。

## 元数据文件

- `metadata-quant-initial.seed.yaml`：量化业务元数据唯一初始化事实源，包含 A 股、
  港股、美股和加密货币市场。
- `platform-local.seed.yaml`：兼容既有本地 bootstrap 命令的空 seed；DataNode 注册由部署流程负责。
- `metadata-monitor-metrics.seed.yaml`：MooX 服务指标逻辑元数据。
- `metadata-monitor-host.seed.yaml`：主机资源逻辑元数据。
- `setup/default/service-deployments.yaml`：Admin `t_service_deployments` 的初始化清单，区分独立进程
  (`deployment_mode: process`) 与同进程 RPC 端点 (`deployment_mode: endpoint`)。

默认量化 seed 使用市场作为 Space。`crypto` 是加密货币市场，`binance` 和 `okx`
是该 Space 下的 DataSource。相同 Schema、频率和生命周期的行情使用共享
`spot_kline_1h`、`perpetual_kline_1h` Dataset，以行上的 `venue:binance` 和
`venue:okx` scalar `series_tag` 区分；Dataset 不登记 tag 名称。

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

## 服务部署导入

Admin 启动时会自动补齐同一套默认服务部署。需要在启动前导入或在重建数据库时显式
初始化时，可以使用 Admin CLI：

```bash
moox-admin-cli service-deployments import \
  --db-path ./data/admin.db \
  --file examples/setup/default/service-deployments.yaml \
  --node-id gateway-node-1 \
  --public-host 203.0.113.10 \
  --eventbus-nats-url tls://127.0.0.1:4222
```

该命令以 `node.id + service.name` 为幂等键，重复执行会更新清单中的地址、端口、网关
路由和健康检查配置。`--public-host` 会统一设置节点地址以及公开入口，不需要手工修改
seed 中的 `127.0.0.1`。配置中的独立进程包括 Storage 的
`storage-primary`、`storage-view`，以及 Collector、CloudNode、
Factor、Strategy、Monitor、EventBus、Archive、HostAgent、Trade 等服务；Admin、
Storage、Trade 内部 RPC 则以 `endpoint` 端点登记，不会被误认为独立进程。

## E2E

删库重建和端到端验证流程见 [e2e/README.md](./e2e/README.md)。E2E 使用默认量化
seed，并通过 Metadata API 动态登记测试 Subject 和 DatasetSubject。

## Python 因子

可导入的时序与截面因子示例见 [factors/README.md](./factors/README.md)。因子按
`timeseries/` 和 `sections/` 分类，未包含策略配置或账户相关文件。

## K 线测试数据

A 股与加密货币的真实行情抽样见 [data/kline/README.md](./data/kline/README.md)。
这些 CSV 已使用当前 `series_tag` 和共享 crypto Dataset 契约。旧样本和旧运行数据
不迁移，切换时清理后重新导入。
