---
name: moox
description: 在 MooX 仓库里工作、操作 moox-cli，或查询 MooX 采集数据（例如 BTC-USDT 的 crypto market queries、K-line/K线行情）时使用。同时覆盖量化存储、采集云函数、主机网关与外部接入、首次部署与按主机部署（bootstrap、deploy-host、deploy-service、rollback、pause、resume）、Linux amd64/arm64 主机采集器监控、EventBus 凭据同步（EventBus rotate、Authorization Violation、FIN-WAIT-2、certificate signature failure）、View 水位追平与 repair-view、Factor 周期诊断与 Recalc、腾讯云 Lighthouse 防火墙、CCN/云联网、CCN 费用、SCF 公网、SCF VPC、内网组网、private-network、scf-network-plan、MOOX_ACCESS_ADDRESS 和控制面维护。
---

# MooX 量化数据系统

在 MooX 仓库里工作时使用本技能，尤其是量化数据存储、协议变更、采集集成、因子数据、发布和部署。

## MooX 是什么

MooX 是个人量化数据平台的统一仓库，在一个 Go workspace 里包含存储、采集、云节点执行、因子计算、交易和控制面模块。

核心概念：

- Space：隔离的用户或策略域。
- DataSource：具体的上游数据源，例如 BINANCE 现货、OKX 现货、HKEX、NASDAQ 或自定义数据源。
- Subject：Space 里存储的业务对象，例如 APT-USDT、一只股票、一个排名项或一个文档主题。
- DataSet：绑定数据源的一类逻辑数据，例如 K 线、行情、公司资料、排名、事件或因子值。
- Field 和 Factor：Space 范围内可复用的列定义，由 DataSet 的列选用。
- View：面向查询、异步构建的宽视图，覆盖一个主 DataSet 和相关 DataSet 的选定列。
- DataNode：Dataset 在激活时绑定的 Storage 节点；它把字段级事实持久化到 Pebble，并通过 outbox 发布行变更。DuckDB/Bleve 视图和
  Parquet 归档都由这些变更异步派生。

系统结构（详见 `docs/总体设计.md`）：

- **主机与组件**：每台 MooX 主机上恰好运行一个**主机网关**（`moox-host-gateway`），它是系统里唯一的网关，只把已签名的请求转发到本机服务。组件、端口、
  tRPC 服务、ACL、健康检查和外部调用方白名单都写在代码里的组件目录（`packages/servicecatalog`）；哪个组件部署在哪台主机由 `moox.toml` 的
  `[placements]` 决定，CLI 部署主机时同步到 Admin。
- **调用方**：MooX 主机上的组件经本机网关的服务目录找到目标，跨主机走 TLS（MooX 私有 CA）；SCF、因子引擎、moox-skill 这类不在 MooX 主机上的调用方
  只能经**外部接入**（`moox-access`，端口 11004）访问；moox-cli 经 SSH 隧道访问；浏览器经控制台代理（Caddy，9527）和管理后台的控制台 API。
- **出口代理**（`moox-egress-proxy`，部署在香港）替 Collector 访问 Binance 等国内连不上的接口，并为 SCF 解析 DNS。

主机采集器部署和 EventBus 凭据的操作规则：

- 主机采集器（`host-agent`）是每台主机自动部署的主机组件，随 `moox-cli setup deploy-host` 部署，只支持 Linux `amd64`/`arm64`，不需要 sudo，细节见
  [`references/host-agent.md`](references/host-agent.md)。
- EventBus 凭据只通过 `moox-admin-cli eventbus-credentials` 生成和轮换，发布包和提交进仓库的 YAML 必须保持无凭据。
- EventBus CA 或 role token 轮换之后，必须 fan-out 到每一个客户端才算部署完成，按
  [`references/eventbus-credentials.md`](references/eventbus-credentials.md) 执行。control 上的 `export` 不会更新其他主机上的主机采集器和业务组件。

## 仓库结构

- `modules/cli`：`moox-cli`（部署、初始化、SCF 发布、数据导入、Doctor）。
- `modules/admin`：控制面（控制台 API、认证、空间、主机与部署、网关控制、调用方密钥与 PKI、密钥、SSH、Setup）。
- `modules/hostgateway`：每台主机的网关；`modules/access`：外部接入；`modules/egressproxy`：出口代理。
- `modules/eventbus`：内嵌的 NATS JetStream broker。
- `modules/storage`：元数据（primary）、字段级事实与 outbox（node）、可重建视图（view）。
- `modules/collector`：采集任务、周期批次规划、SCF Timer 运行时与对账、标的同步。
- `modules/cloudnode`：云账号、SCF 节点、代码包和函数发布。
- `modules/factor`：因子管理端和 Python 因子引擎。
- `modules/strategy`、`modules/trade`、`modules/monitor`、`modules/hostagent`、`modules/archive`。
- `packages/servicecatalog`：组件目录；`packages/gatewayclient`：调用方客户端；其余共享包见 `docs/模块/共享包.md`。
- `deploy/runtime`：主机上的运行脚本（安装、启停、暂停、健康检查）。
- `docs`：总体设计、部署与运维和 `docs/模块/` 下的模块设计文档。
- `scripts`：构建、发布、检查和契约/E2E 测试脚本。

## 常用命令

在仓库根目录：

```bash
make build
make check-boundaries
make release
make verify
```

部署一律用 `moox-cli setup`：`bootstrap`（首次）、`deploy-host`、`deploy-service`、`rollback`、`pause`、`resume`、`service`、`firewall`，细节见
[`references/release.md`](references/release.md)。发布不再打服务 ZIP 包：CLI 按 `moox.toml` 渲染每台主机的发布，用已核验的 SSH 主机指纹完成上传、安装、
回滚和健康检查；不要在命令行里拼接密码。

敏感信息扫描已接入 CNB 和 GitHub CI 的 Pull Request 流程：`.cnb.yml` 的 `pull_request` 流水线和 `.github/workflows/ci.yml` 的 `Secret scan` 任务都会扫描
本次 PR 的提交范围，命中密码、令牌或密钥时以非零状态失败。要真正阻止合并，还需要在两个平台的 `main` 分支保护规则中把 `Secret scan` 和常规验证任务设置为
必需状态检查；CNB 同时启用“需要通过状态检查”。

协议生成：

```bash
make proto
```

## MooX CLI 运维

需要确定性解析或反复拼装 `moox-cli` 参数的流程，优先用本技能自带的脚本。

获取已采集的行情数据，例如“获取 BTC-USDT 的 1m K 线”，读
[`references/data-query.md`](references/data-query.md)。它定义了自然语言到参数的映射、目录约束、打包凭据的处理方式和
`moox-cli data kline get` 的结果摘要约定。

View 恢复前先读 [`references/cli-operations.md`](references/cli-operations.md)。它记录了先 dry-run 的安全流程和 Storage 的 `repair-view`；Factor 的恢复用
durable 积压诊断和显式的 Recalc，不删除 consumer，也不用 Storage 的 `force-rebuild-view`，同时说明 durable 名称、默认值、凭据查找、备份、View 根数预算
（`[storage_retention] view_bars`）和高风险的整体索引重置。有对应的 `moox-cli` 操作时，不要手工删除 durable consumer 或 View 索引。

用户修改 `moox.toml`（例如 `[storage_retention]` 的保留期或 `[storage_view]`）时，用 `moox-cli config` 发布：先运行 `moox-cli config plan --file ./moox.toml`，把每个目标的
状态、变化的键名、要重新部署的组件、告警和 `retention_shrinks` 给用户看，用户确认后才运行 `moox-cli config publish --file ./moox.toml --yes`。缩短保留期会在下一次
Storage 清理时删除数据，只有用户明确接受时才加 `--allow-retention-shrink`。不要手工编辑主机上的配置文件。

crypto K 线或 Factor 周期停滞时，先读 [`references/view-catchup.md`](references/view-catchup.md)。分别测量 Primary、View 和 Factor 的水位。Collector 的完成事件驱动
Factor 从 Storage PrimaryStore 读取，Factor 的 durable 是 `factor_collector_period_v1`；用带集合标签的周期和 lane 指标区分事件积压、Storage 故障和 Python 饱和。

### 腾讯云防火墙

各主机的入站规则由 `moox-cli setup firewall` 按部署表统一同步（`--dry-run` 看差异，`--prune` 删除受管端口上多余的旧规则），不要手工逐条开放。只有临时需要开放某个端口、
并且用户给出了轻量应用服务器的实例详情页 URL 时，才用本技能自带的脚本解析实例 ID 并调用 `moox-cli`：

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add \
  --detail-url 'https://console.cloud.tencent.com/lighthouse/instance/detail?searchParams=rid%3D5&rid=1&id=lhins-a7yikq89' \
  --ports 11004 --cidr <来源网段> --dry-run
```

`--ports` 必须显式指定，没有默认值；内部端口要把 `--cidr` 收窄到具体地址，不要对 `0.0.0.0/0` 开放。确认 dry-run 正确后去掉 `--dry-run` 创建规则。没有 URL 中的地域信息时默认
`ap-guangzhou`。

安全的检查命令：

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py parse --detail-url '<控制台详情页 URL>'
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add --detail-url '<控制台详情页 URL>' --ports 11004 --print-command
```

脚本优先调用仓库里的 `bin/moox-cli`，没有时用 `PATH` 里的 `moox-cli`。腾讯云凭据通过 `TENCENTCLOUD_SECRET_ID` 和 `TENCENTCLOUD_SECRET_KEY` 提供；不要在最终回复或日志里回显密钥。

### 腾讯云 SCF 与外部接入的网络

SCF 只经外部接入访问 MooX，路径按地域选择。发布 SCF 之前先运行 `moox-cli setup scf-network-plan --file ./moox.toml`：与函数同地域的主机上部署了外部接入时，函数绑定该主机的
VPC，经私网访问；跨地域走 `access@storage` 的公网地址。不要创建 CCN。需要访问公开行情源时保持 `public_net_status=ENABLE`。完整的决策表、canary 顺序和验收证据见
[`references/private-network.md`](references/private-network.md)。不要把控制台代理或 `[hosts.control] address` 改成私网地址。

运行数据可以删除，并从 `examples/` 和服务流程重建。不要重新引入独立的验收 CSV 脚本。

### 腾讯云 CLS 日志查询

主机上的组件日志不写 CLS，只在 `<部署根目录>/logs/<组件>/stdout.log`。CLS 里是 SCF 采集函数的日志。用户要求查询、验证或排查 CLS 日志时，用自带的
`skills/moox/scripts/cls_search.py`。查询流程读 [`references/cls-query.md`](references/cls-query.md)，签名 API 的约定读 [`references/cls-api.md`](references/cls-api.md)。
脚本支持 CQL、相对时间范围、raw/JSON 输出、字段选择和分页，默认使用 `cls.internal.tencentcloudapi.com`，凭据只从命令行参数、`CLS_*` 环境变量或未跟踪的 `.env`
文件读取。脚本提示缺少 SDK 时，在当前 Python 环境里安装 `tencentcloud-sdk-python-cls`；不要把腾讯云凭据写进仓库。

检查 MooX 的 CLS 时，先用窄时间范围执行 `--query '*'` 并检查返回的结构化记录。在对 `service_name` 使用 CQL 条件之前，先确认记录里有这个字段；固定的 Topic 可能还没有
`service_name` 索引，这时 API 会返回明确的 `field ... is not indexed` 错误，需要在客户端过滤。报告 Topic ID、时间范围、结果数、RequestId、service name 取值以及任何
索引或投递错误，但不要打印 CLS 凭据。

## 首次部署

初始化一套全新的 MooX 时，严格按 [`references/custom-setup.md`](references/custom-setup.md) 执行。用户在部署前创建仓库根目录的 `moox.toml`。Agent 只可以检查它是否存在，
不能在 `moox-cli setup` 之外读取、解析、打印、复制或 `source` 它。

顺序是：`validate`、（按需）`trust-host`、`firewall`、`bootstrap`、`apply`、`status`，在 `status` 返回 `completed` 之后再 `setup init --config-dir ./config/setup`。
`bootstrap` 离线初始化管理后台（建表、MooX 私有 CA、全部主机和部署、调用方密钥），启动 control，再依次部署其他主机；部署表在 `moox.toml` 的 `[placements]` 里，
不需要再询问用户把 Storage 放在哪台机器，也不要悄悄替用户选择。`setup init` 创建或核对默认的 Admin 空间和 Storage 元数据，激活 Dataset 并验证结果。不要在 Agent 上下文里
解析生成的过滤后的 seed 文件。腾讯云主机就绪后，按 [`references/private-network.md`](references/private-network.md) 检查 SCF 的地域路由。

控制台代理（Caddy）的安装、证书模式选择、HTTPS 验收和健康检查续期由 `moox-cli setup bootstrap` 和部署命令负责，不要让用户单独运行 Caddy 脚本。证书模式和 internal 根证书的处理见
[`references/caddy-https.md`](references/caddy-https.md)；internal 模式下，`setup apply` 会检查并安装本机浏览器信任，中断后可以单独执行
`./bin/moox-cli setup trust-browser --file ./moox.toml`。

组件目录（`packages/servicecatalog`）是服务和端口的唯一来源；Admin 里的 `t_hosts`、`t_placements` 记录部署，`t_host_gateway_status` 记录主机网关的心跳。控制台的「服务部署」页展示它们；
`/#/ops/storage/nodes` 仍是独立的 PrimaryStore 拓扑，不会被悄悄同步。SCF 的外部接入地址、实例 ID 和 `scf-collector` 凭据由 Collector 经 CloudNode 写进函数环境，不在打包时嵌入。

## 开发约定

- 优先使用 `modules/storage/proto/*.proto`、`modules/admin/proto/*.proto`、`modules/collector/proto/*.proto` 和 `modules/cloudnode/proto/*.proto` 下的新协议；共享的响应、鉴权和分页类型在 `packages/commonpb`。
- 不要为已删除的调用路径新增兼容用的 proto 包；把调用方改成当前模块的 proto。
- 不要在公开 API 里重新引入 `object_id`。使用 Space、DataSource、Subject、DataSet、View、Field 和 Factor。
- 用 `subject_id` 表示规范化的标的身份。与数据源相关的符号由各采集适配器从规范 `subject_id` 派生，不要在元数据里持久化数据源符号映射。
- 使用 `start_time`、`end_time` 和 `snapshot_time`；避免 `_ms` 之类的后缀。
- `series_tag` 是一个可选的标量用户标签，属于时序身份的一部分，例如 `venue:binance`。不要重新引入 map 形式的维度。
- Pebble 支撑的 PrimaryStore 是在线有序事实存储，DuckDB 用于分析查询和带版本的宽视图存储，Parquet 是冷归档，Bleve 用于文本检索。
- 新增或修改服务、端口、ACL 都在组件目录里做；不要在各模块里写目标地址或端口，调用方只配置 `gateway_client` 的身份和密钥文件。

更多说明见 `references/`。

### Storage 的 CGO 编译

`moox-storage-*` 依赖 CGO，**不要在 macOS 上 `GOOS=linux CGO_ENABLED=1` 交叉编译**。在 `moox.toml` 的 `[compile_host]` 配置一台 Linux 编译主机（不是 MooX 主机）后，
`moox-cli setup deploy-host` 和 `moox-cli setup build-linux` 会自动在那里构建；SSH 目标和凭据都只在 `moox.toml` 里，不要写进仓库或命令行。
