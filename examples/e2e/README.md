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
| 服务部署信息 | `moox-admin` | SysDeploy 启动补齐默认部署记录，再通过管理台 `/ops/services?tab=instances` 调整 |
| Storage DataNode 注册和业务元数据 | `moox-storage` | 部署注册 DataNode；`examples/*.seed.yaml` 通过 `moox-cli metadata import` 导入直接绑定的 disabled Dataset |
| 云账户、云节点、函数包 | `moox-cloudnode` | 管理台或 `/api/admin/cloudnode/*` API 创建 |
| 采集规则、任务实例 | `moox-collector` | 管理台或 `/api/admin/collectmgr/*` API 创建规则，再由 collector 生成；采集执行日志由 SCF/CLS 承载 |
| SCF 异步 JobItem | `moox-cloudnode` | 由 collector/factor/trade 等业务服务通过 `/api/service/cloudnode/*` 提交；Collector 不同步调用 SCF |
| K 线、标的、视图数据 | `moox-storage` | collector/SCF 通过 storage RPC 写入，view/archive 通过 rebuild 或事件更新 |

这些数据如果被删除，不需要旧库迁移，也不应该通过手工 SQL 恢复；按上表重新走模块入口即可。

## 重建顺序

1. 启动 `moox-admin`，生成新的 `data/admin.db`。
2. 启动 `moox-cloudnode`，生成新的 cloudnode 数据库。
3. 启动 `moox-collector`，生成新的 collector 数据库。
4. 启动 `storage-primary`、`storage-node` 和 `storage-view`。
5. 在管理台创建演示用 Space，例如 `crypto`。
6. 用部署 CLI 注册 DataNode，确认 `service_target` 可达；用 `moox-cli metadata import` 导入直接绑定的 disabled Dataset。
7. 用 Doctor 执行只读激活检查，并显式激活健康 Dataset；激活成功后绑定锁定。
8. 在管理台或通过 cloudnode API 创建云账户、两阶段上传 collector SCF 代码包（`InitPackageUpload` → COS 直传 → `CompletePackageUpload`）、部署云节点。
9. 在采集规则页面创建规则，由 `moox-collector` 根据 Dataset subjects 生成 task instances，并提交给 `moox-cloudnode` 的 JobItem 队列。
10. SCF runtime 直接从 JetStream Job Execution Queue 获取 JobItem，执行采集并写入已激活 Dataset。
11. 如果 View 需要历史重建，执行 ViewBuilder 的 `op=maintain` 维护流程；Archive 由独立 `modules/archive` 服务负责，不通过 Storage View rebuild。

## Metadata seed 导入

在仓库根目录执行：

```bash
cd modules/cli

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/platform-local.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists

GOWORK=off go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-quant-initial.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --spaces crypto \
  --if-not-exists
```

Storage metadata seed 使用 Schema v5。DataNode 不是逻辑 seed 的隐含路由；部署流程先执行：

```bash
moox-storage-cli register-node \
  --node-id storage-node-0 \
  --service-target ip://127.0.0.1:20107 \
  --metadata-target ip://127.0.0.1:20100
moox-storage-cli activate-datasets --metadata-target ip://127.0.0.1:20100
```

`activate-datasets` 只激活已经通过只读检查的 Dataset。绑定一旦锁定，不能再解绑或迁移；
新项目不提供历史 Schema 或节点拓扑迁移。

默认 seed 不静态枚举测试币种。E2E 的 setup 阶段通过 Metadata
`RegisterDataSubject` API 登记 `BTC-USDT`，同时创建 Binance 外部代码映射和
`binance_spot_kline_1h` DatasetSubject 绑定。

## 创建采集规则

Collector schema 不内置运行态采集规则。删库后需要通过管理台或 collectmgr API 创建规则，再触发任务实例重算。

管理台会通过 `/api/admin/collectmgr/CreateTaskRule` 发送符合 proto 的请求体，核心结构如下：

```json
{
  "rule": {
    "space_id": "crypto",
    "rule_id": "binance_spot_kline_1h",
    "biz_type": "data_collector",
    "data_type": "kline",
    "data_source": "binance",
    "collect_params": "{\"source\":{\"kind\":\"dataset_subjects\",\"dataset_id\":\"binance_spot_kline_1h\"},\"collector\":{\"exchange\":\"binance\",\"market\":\"spot\",\"data_type\":\"kline\",\"intervals\":[\"1h\"]},\"target\":{\"dataset_id\":\"binance_spot_kline_1h\"},\"schedule\":{\"interval\":\"1h\"}}",
    "enabled": "true",
    "creator": "system"
  }
}
```

创建规则后，调用 `/api/admin/collectmgr/ScheduleTasks`，由 `moox-collector` 从 storage metadata 读取 `binance_spot_kline_1h` 数据集的 subjects，生成 task instances，并通过 `moox-cloudnode` 只提交下一次 CloudNode JobItem。`execute_at` 缺失或已到期时立即执行。

## 最小可演示闭环

删除所有运行时数据后，一个最小的端到端演示环境应按下面的边界重建：

1. `moox-admin` 启动后，可以登录管理台并看到 `moox_cloudnode`、`moox_collector`、storage-primary/metadata/view 等服务部署记录。
2. `moox-storage` 的 `storage-primary`、`storage-node` 和 `storage-view` 进程启动后，先注册 DataNode，再导入 `platform-local.seed.yaml` 和 `metadata-quant-initial.seed.yaml` 的 `crypto` Space，并完成 Doctor 检查与显式激活。
3. `moox-cloudnode` 启动后，通过云账户页面重新创建 Tencent Cloud 账号；密钥不进入 examples。
4. 使用 collector 打包/发布流程上传 `moox-collector` SCF 包，并通过 cloudnode 批量创建/部署云节点。
5. `moox-collector` 启动后，E2E 注册 `BTC-USDT`，再创建 Binance 现货 1H K 线规则；规则根据 `binance_spot_kline_1h` 数据集里的 Subject 生成 task instances。
6. SCF runtime 直接消费 Job Execution Queue，执行后通过 Node Service Gateway 的 Storage PrimaryStore RPC 写入 K 线，并通过 CloudNode service route 上报 JobItem 终态。
7. 通过数据浏览或视图浏览页面确认 `binance_spot_1h_view` 能看到最新现货 K 线。

如果第 7 步没有数据，不要回写 SQLite。按链路依次检查：服务部署地址、SCF 心跳、collector 任务实例、CloudNode JobItem、storage 写入、view rebuild/事件更新。

## 一键端到端验证

本目录提供可重复执行的最小闭环脚本：

```bash
examples/e2e/run.sh --target localhost --dir /tmp/moox-e2e
```

`run.sh` 是本地或指定主机上的运行时诊断入口。它会显式启动
`run-scf-resident.sh`，适合验证 JetStream consumer、延期投递和 Gateway 配置，
但它不能证明任务由腾讯云 SCF 节点执行。

真实腾讯 SCF 验收使用独立入口，并要求先完成 SCF 包发布、至少两个真实节点部署和
心跳上线：

```bash
# 在 106 的 /home/ubuntu/moox/prod 中执行
read -rsp 'E2E admin password: ' MOOX_E2E_ADMIN_PASSWORD && echo
export MOOX_E2E_ADMIN_PASSWORD
printf '%s\n' "${MOOX_E2E_ADMIN_PASSWORD}" | \
  ./bin/moox-admin-cli user ensure \
    --db-path ./data/admin.db \
    --username "${MOOX_E2E_ADMIN_USERNAME:-mooxe2eadmin}" \
    --password-stdin

examples/e2e/run-real-scf.sh \
  --gateway http://127.0.0.1:11000 \
  --web http://127.0.0.1:9527 \
  --host 106.53.107.122 \
  --e2e-node control \
  --timeout-seconds 240
```

`run-real-scf.sh` 依次执行 `verify.mjs` 的 `setup`、`schedule`、`assert`，
退出时再执行 `cleanup`；它不启动本地 wrapper 或 `moox-collector-scf` 进程。真实环境的 `setup`
传入 `--skip-cloud-node-setup`，因此不会写入假的 `e2e-local` CloudNode；它直接复用
已发布并上线的云函数节点。脚本成功或失败都会保留 state 和完整日志，路径在输出中显示。
默认使用独立规则 `moox_real_scf_e2e_kline_1h`，不会覆盖正式规则
`binance_spot_kline_1h`。state 包含 `scheduled_job_ids`、`immediate_job_item_id`、
`batch_job_item_ids`、`expected_batch_size=10` 和 `failure_job_item_id`。assert 阶段会额外
提交 20 条不含 `execute_at` 的采集任务，等待全部终态成功；日志同时输出逐个
`job_item_id` 的 CLS 查询提示。
setup 会先要求至少两个状态在线、心跳在两分钟内的 `tencent-scf/scf-event`
节点，要求它们都支持 `collect.binance.kline` 且来自不同 `package_id`；节点、代码包版本、
支持的 workload 和心跳会写入 state
作为验收证据。所有任务终态还会校验 `execution_node` 必须属于这些真实节点。schedule
持续检查任务在 `execute_at` 到来前保持 pending；是否发生过提前消费后的
`deferred/RETRY` 则以随后 CLS 查询为准。

`assert` 还会提交一个小型受控失败 JobItem。该任务使用稳定的非法 Binance symbol，
不包含凭据或大体积参数，并等待 CloudNode 记录最终失败，同时校验
`COLLECT_FAILED`、retryable error kind、简短的 HTTP 400 错误以及真实
`execution_node`。生产 `max_deliver=4`，使用它在 CLS 中确认四轮
`received/started/instance_reported/done`、三次 `delivery_action(RETRY)`，
以及最终 `cloudnode_reported` 和 `delivery_action(TERM)`；它不写入生产 DSL，
也不依赖 JetStream testkit。schedule 捕获本次 JobItem 后会立即禁用 E2E 规则，
避免生产 Timer 改写 TaskInstance 绑定；无论测试成功还是中途失败，runner 仍会执行 cleanup，
禁用独立 E2E 规则，避免生产环境继续每 30 秒生成采集任务。

### 本地诊断 E2E

脚本默认会调用 `scripts/deploy-moox.sh --reset-data`，然后导入：

```text
examples/platform-local.seed.yaml
examples/metadata-quant-initial.seed.yaml --spaces crypto
```

随后通过管理台同一套 HTTP 网关完成注册/登录、修正 public service deployments、创建
`crypto` Space、登记测试 Subject 和本地逻辑 SCF 节点并创建 Binance 现货 1H K 线规则。
脚本先启动常驻 `moox-collector-scf`，再提交带未来 `execute_at` 的任务；到期前必须观测到
同一 `job_item_id` 的 `deferred + delivery_action(RETRY)`，到期后才允许执行。脚本退出时会
清理该进程和分阶段状态文件。最后断言：

这里的 `-resident` 是为本地/远端 E2E 提供显式节点身份和网关地址的诊断模式，用于验证
与生产一致的常驻 taskrunner；它不替代 Tencent SCF 发布、keepalive 或云端运行验收。

- `9527` 管理台静态页面可访问；
- `11000` admin gateway health 可访问；
- 管理台 JWT 请求能访问 space、sysdeploy、cloudnode、collector、storage metadata；
- SysDeploy 的地址派生、更新、删除后重建和再次删除契约通过临时服务记录验证；
- collector 生成 task instances、绑定 `cloud_job_item_id`，并提交带未来 `execute_at` 的 JobItem；
- 常驻诊断 SCF 已运行时，JobItem 在到期前保持 pending，并实际记录 deferred/RETRY；
- 到期后 scheduled JobItem 全部成功，task instance 的 `cloud_job_item_id` 仍对应本次执行；
- 另行提交一个缺失 `execute_at` 的诊断 JobItem，确认其立即进入同一执行链路；该 JobItem
  使用独立 `task_id`，不声称更新 scheduled TaskInstance；
- K 线严格写入规则指定的 `crypto/binance_spot_kline_1h`，并可经 DataNode Snapshot 和 View 查询；
- 输出 scheduled/immediate `job_item_id`、预期生命周期事件和对应 CLS 查询条件。

SCF 步骤从部署目录的 `secrets/gateway-service.env` 读取节点和服务身份，将公开 CA 证书以 `MOOX_GATEWAY_CA_PEM_B64` 传入运行时，并通过独立 Gateway `127.0.0.1:11002` 访问 `/api/service/*`。任一身份或 CA 配置缺失时会在调用前失败，不会打印密钥。

远端发布并验证示例：

```bash
examples/e2e/run.sh \
  --target root@106.53.107.122 \
  --dir ~/moox/prod \
  --host 106.53.107.122
```

如果远端使用 `~/.ssh/config` 别名，`--host` 需要填写浏览器和网关可访问的公网主机名或
IP。需要复用已启动服务时可传 `--skip-deploy`；需要保留已有运行数据时可传
`--preserve-data`。无论是否保留数据，本次运行都必须改变目标 Dataset 的完整行快照；
水位线已最新而产生零写入时会明确失败，不能用历史行冒充本次采集证据。

## 边界说明

- `examples/*.seed.yaml` 只表达 Storage 逻辑元数据和 Dataset 的直接 `data_node_id` 绑定，不直接写 admin/cloudnode/collector/trade 表；DataNode 注册属于部署流程。
- 云账户和真实云厂商密钥不进入 examples；先在 SecretMgr 创建 Tencent cloud secret，再创建引用其 `credential_secret_id` 的 CloudAccount。
- 采集任务实例不是 seed 数据，应由 collector 规则和 dataset subjects 重新生成。
- CloudNode 批量创建/部署节点返回 `batch_id`，这是控制面 `batch_change`，不是 collector `task_instance`，也不是 SCF runtime `JobItem`。
- SCF 异步执行协议统一使用 `SubmitJobItems`、JetStream Job Execution Queue、
  `ReportJobItemStatus` 和 `job_item_id` 字段。
- 每个 SCF 进程使用一个 NATS 连接和一个常驻 taskrunner 绑定多个受支持 durable；
  心跳 timer 与 taskrunner 相互独立。动态增加实例后，相同 durable 自动加入竞争消费，
  执行完全由 JetStream delivery 驱动。
- 允许少量重复执行，不承诺任务级去重。JobItem 获取、延期、执行、状态上报、完成和
  delivery action 可按 `job_item_id` 在 CLS 检索。
- Binance TLS 证书校验关闭是本 E2E 接受的运行配置。
