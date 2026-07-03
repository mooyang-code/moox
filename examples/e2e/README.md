# MooX E2E 数据重建入口

本目录描述删掉运行时数据后，如何从 `examples/` seed 和当前服务流程重建一个可演示的端到端环境。

这里不放功能测试代码、不放迁移脚本，也不直接写 SQLite 表。所有数据都应通过模块自己的启动流程、RPC、HTTP API 或 `moox-cli` 写入。

## 适用范围

可以删除并重建的运行时数据包括：

```text
data/admin.db
data/cloudnode/moox_cloudnode.db
data/collector/moox_collector.db
data/storage
data/trade
```

删除这些数据后，各模块负责重建自己的 schema：

```text
moox-admin      -> modules/admin/schema
moox-cloudnode  -> modules/cloudnode/schema
moox-collector  -> modules/collector/schema
moox-storage    -> modules/storage schema/config
moox-trade      -> modules/trade/schema
```

## 重建契约

删库重建时，`examples/` 只负责可共享、可公开、可重复导入的示例元数据。每个模块自己的运行态数据必须通过该模块的启动流程或服务 API 重新生成，不在 examples 中维护 SQLite seed。

| 数据类型 | 所属模块 | 重建方式 |
| --- | --- | --- |
| Space、用户、登录态、本地运维配置 | `moox-admin` | admin 启动建表，管理台/API 创建 |
| 服务部署信息 | `moox-admin` | SysDeploy 启动补齐默认部署记录，再通过管理台 `/settings/service-deployments` 调整 |
| Storage 平台拓扑和业务元数据 | `moox-storage` | `examples/*.seed.yaml` 通过 `moox-cli metadata import` 导入 |
| 云账户、云节点、函数包 | `moox-cloudnode` | 管理台或 `/api/admin/cloudnode/*` API 创建 |
| 采集规则、任务实例、执行日志 | `moox-collector` | 管理台或 `/api/admin/collectmgr/*` API 创建规则，再由 collector 生成 |
| SCF 异步 work_item、同步 invocation | `moox-cloudnode` | 由 collector/factor/trade 等业务服务通过 `/api/service/cloudnode/*` 提交 |
| K 线、标的、视图数据 | `moox-storage` | collector/SCF 通过 storage RPC 写入，view/archive 通过 rebuild 或事件更新 |

这些数据如果被删除，不需要旧库迁移，也不应该通过手工 SQL 恢复；按上表重新走模块入口即可。

## 重建顺序

1. 启动 `moox-admin`，生成新的 `data/admin.db`。
2. 启动 `moox-cloudnode`，生成新的 cloudnode 数据库。
3. 启动 `moox-collector`，生成新的 collector 数据库。
4. 启动 storage metadata/access/primary/view/archive 相关进程。
5. 在管理台创建演示用 Space，例如 `crypto`。
6. 使用 `moox-cli metadata import` 导入平台拓扑和业务元数据。
7. 在管理台或通过 cloudnode API 创建云账户、两阶段上传 collector SCF 代码包（`InitPackageUpload` → COS 直传 → `CompletePackageUpload`）、部署云节点。
8. 在采集规则页面创建规则，由 `moox-collector` 根据 dataset subjects 生成 task instances，并提交给 `moox-cloudnode` 的 work_item 队列。
9. SCF runtime 通过 `/api/service/cloudnode/PollWorkItems` 获取 work_item，执行采集并写入 storage。
10. 如果 view/archive 需要历史重建，使用 storage view rebuild，而不是依赖事件回放历史。

## Metadata seed 导入

在仓库根目录执行：

```bash
cd modules/cli

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/platform-local.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-crypto.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists
```

如果要验证 Binance 现货 1m K 线视图，应同时导入现货视图 seed：

```bash
GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-crypto-spot-kline-1m-view.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists
```

如需 A 股或 Binance 合约示例，再导入：

```bash
GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-cn-stock.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-crypto-binance-swap-kline.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists
```

## 创建采集规则

Collector schema 不内置运行态采集规则。删库后需要通过管理台或 collectmgr API 创建规则，再触发任务实例重算。

管理台会通过 `/api/admin/collectmgr/CreateTaskRule` 发送符合 proto 的请求体，核心结构如下：

```json
{
  "rule": {
    "space_id": "crypto",
    "rule_id": "binance_spot_kline_1m",
    "biz_type": "data_collector",
    "data_type": "kline",
    "data_source": "binance",
    "collect_params": "{\"source\":{\"kind\":\"dataset_subjects\",\"dataset_id\":\"binance_spot_kline\"},\"collector\":{\"exchange\":\"binance\",\"market\":\"spot\",\"data_type\":\"kline\",\"intervals\":[\"1m\"]},\"target\":{\"dataset_id\":\"binance_spot_kline\",\"workload_type\":\"collector.binance.spot.kline\",\"deployment_id\":\"collector-binance-kline-v1\"},\"schedule\":{\"interval\":\"30m\",\"timezone\":\"Asia/Shanghai\"}}",
    "assignment_type": "auto",
    "assigned_nodes": "[]",
    "node_pattern": "",
    "node_tags": "[]",
    "enabled": "true",
    "creator": "system"
  }
}
```

创建规则后，调用 `/api/admin/collectmgr/RecalculateAllTaskInstances`，由 `moox-collector` 从 storage metadata 读取 `binance_spot_kline` 数据集的 subjects，生成 task instances，并通过 `moox-cloudnode` 提交 CloudNode work_item。

## 最小可演示闭环

删除所有运行时数据后，一个最小的端到端演示环境应按下面的边界重建：

1. `moox-admin` 启动后，可以登录管理台并看到 `moox_cloudnode`、`moox_collector`、storage access/metadata/view 等服务部署记录。
2. `moox-storage` metadata/access/primary/view 进程启动后，导入 `platform-local.seed.yaml`、`metadata-crypto.seed.yaml` 和 `metadata-crypto-spot-kline-1m-view.seed.yaml`。
3. `moox-cloudnode` 启动后，通过云账户页面重新创建 Tencent Cloud 账号；密钥不进入 examples。
4. 使用 collector 打包/发布流程上传 `moox-collector` SCF 包，并通过 cloudnode 批量创建/部署云节点。
5. `moox-collector` 启动后，在采集规则页面创建 Binance 现货 K 线规则，规则根据 `binance_spot_kline` 数据集里的 subject 生成 task instances。
6. SCF runtime 通过 `/api/service/cloudnode/PollWorkItems` 获取 work_item，执行后通过 storage Access RPC 写入 K 线，并通过 `/api/service/collectmgr/ReportTaskStatus` 回写任务状态。
7. 通过数据浏览或视图浏览页面确认 `spot_kline_1m_view` 能看到最新现货 K 线。

如果第 7 步没有数据，不要回写 SQLite。按链路依次检查：服务部署地址、SCF 心跳、collector 任务实例、CloudNode work_item、storage 写入、view rebuild/事件更新。

## 边界说明

- `examples/*.seed.yaml` 只表达 Storage 元数据和存储拓扑，不直接写 admin/cloudnode/collector/trade 表。
- 云账户和真实云厂商密钥不进入 examples，需要通过管理台或 cloudnode API 重新创建。
- 采集任务实例不是 seed 数据，应由 collector 规则和 dataset subjects 重新生成。
- CloudNode 批量创建/部署节点返回 `batch_id`，这是控制面 `batch_change`，不是 collector `task_instance`，也不是 SCF runtime `work_item`。
- SCF 异步执行协议统一使用 `SubmitWorkItems`、`PollWorkItems`、`ReportWorkItemStatus` 和 `work_item_id` 字段。
