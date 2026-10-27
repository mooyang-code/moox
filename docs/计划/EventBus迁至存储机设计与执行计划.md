# EventBus 迁至存储机设计与执行计划

- **日期**：2026-10-27
- **状态**：代码已完成（分支 `feat/eventbus-on-storage`，单元测试与 Docker 里的 Linux bootstrap 门禁通过，见第 5 节）；**生产迁移尚未执行**，第 6 节是按线上真实运维方式重写后的步骤，等用户确认后执行
- **前置文档**：[策略读取变慢排查与修复方案](F1策略迁移/策略读取变慢排查与修复方案.md)。那份文档定位了存储机 ↔ 控制机公网链路的突发丢包；本文是其中提到的结构性修复。
- **约束**：项目尚未上线，可以停机迁移；不考虑兼容、平滑切换和成本；规范和代码都可以改，以设计合理为第一准则。

## 1. 目标与依据

### 1.1 为什么要搬

EventBus 九成以上的流量来自存储机上的 Storage 组件。下表是 2026-10-27 07:55 边界前后 18 秒，在控制机 4222 端口抓到的流量：

| EventBus 的客户端 | 发往 EventBus | 从 EventBus 收 |
|---|---|---|
| 存储机（storage-node 发布，storage-view 消费） | 1007 KB | 993 KB |
| 控制机本机的组件 | 约 35 KB | 约 65 KB |
| 海外机、因子机 | 约 11 KB | 约 10 KB |

storage-node 把整行写入数据发到 EventBus，再由 storage-view 取回来更新 View 索引。这本来是 Storage 内部的一条数据管道，两端都在存储机上；但 EventBus 在控制机，于是每条数据都要经南京 ↔ 广州的公网往返一次。

F1 上线后，每个 5 分钟边界后的 2～10 秒内，三个 FH 任务的结果事件（每个约 190 KB）叠加到这条链路上，造成突发丢包。

### 1.2 搬过去之后

**目标拓扑**：EventBus 部署在存储机，与 storage-primary、storage-node、storage-view 同机。其余组件的位置不变。

各事件跨两机链路的次数：

| 事件 | 现在 | 搬后 |
|---|---|---|
| 行变更、因子周期完成、同步点（storage-node → storage-view） | 往返 2 次，量大 | 0 |
| View 就绪（storage-view → strategy） | 1 次 | 1 次 |
| 采集周期完成（storage-node → collector） | 1 次 | 1 次 |
| 采集周期完成（storage-node → 因子机） | 存储机 → 控制机 → 因子机 | 存储机 → 因子机 |
| 目标权重（strategy → 海外机上的 Trade） | 控制机 → 海外机 | 控制机 → 存储机 → 海外机 |
| 控制机各服务的指标（各服务 → monitor） | 0 | 往返 2 次，量很小 |

**收益**：每个边界，存储机 ↔ 控制机链路两个方向各少约 1 MB，约占这条链路边界流量的六成。

**收益的边界**：

- 存储机发往控制机仍有策略读取 View 的应答，每个边界约 690 KB，而且集中在几秒内，仍可能丢包。
- 所以前置文档 4.0 的"查公网带宽"照样要做，验收也要单独看"当期与上一根行"的 p90（第 7 节）。
- 把 strategy 也放到与 View 同一台主机，列为后续评估项（第 8 节），不放进本计划。

**代价**：

- 目标权重多一跳。消息很小，延迟增加约一个 RTT（十到几十毫秒）。
- 控制机服务的指标要经存储机绕一圈。量很小（每秒几 KB），延迟不敏感。
- 存储机宕机时，全系统的事件流都会停。不过存储机一停，数据链路本来也全停了；对单用户系统来说，这不构成新的单点。

## 2. 设计决策

### D1 EventBus 独立成单元，由 `[deployments]` 决定放在哪台主机

- 新增软件包 profile `eventbus`，只含 `eventbus` 一个组件。control profile 去掉 eventbus。
- 用 `setup deploy-unit --host <主机> --profile eventbus` 安装，生命周期与 access、trade 等业务单元相同：
  - 每次操作只涉及一台主机；
  - 有自己的维护锁和恢复日志。
- 服务目录里 EventBus 本来就是 `scope: any`，Admin 也是按部署记录找到 EventBus 的位置（`sysdeploy.ResolveEventBusEndpoint`）。改的只是部署工具里"EventBus 一定在控制机"的假设。
- **不并入 storage 单元的原因**：两者的升级和重启节奏不同。Storage 要 CGO 构建、要初始化数据库；EventBus 是纯 Go 组件、没有数据库。合在一起的话，每次部署 Storage 都会重启 EventBus。

### D2 核心阶段只建控制面；HostAgent 的就绪不再依赖 EventBus

- `bootstrap --stage core` 只在控制机安装 Admin 和主机单元（host-gateway、host-agent），EventBus 凭据仍在核心阶段离线签发，写进 Admin 库。
- **背景**：host-agent 只有连上 EventBus 才算就绪（`hostagent/internal/app/health.go:35`），而部署链路的每一环都要求组件就绪：
  - 进程启动：`unitruntime/runtime.go` 的 `awaitStartup` 超时未就绪就停掉进程；
  - 激活：`unitinstall/activate.go`、`unitinstall/deploy.go`；
  - 恢复与引导完成：`unitbootstrap/recover.go` 的 `verifyComplete`；
  - 日志：`unitinstall/journal.go`；
  - `deploy-host` 的 `host-ready`。
  
  所以在 EventBus 部署之前，存储机和控制机上的 host-agent 都启动不了。
- **改法**：改 HostAgent 的就绪语义，而不是在五处门禁里给它开例外。
  - `/readyz` 改为"自身采集循环正常"，不再包含 EventBus 连接；
  - 采集错误和发布错误要分开。现在的 `last_error` 两者混用，而且只在发布成功后才清空（`app.go:86-120`）；只去掉连接条件的话，EventBus 不在时 HostAgent 仍然就绪不了。新增独立的采集状态（最近一次采集成功的时间和采集错误）供 `/readyz` 使用；
  - 发布错误和连接状态只放在状态接口（`eventbus_connected` 等字段）和指标里；
  - 发布中断由 Monitor 的"host-agent 停止上报"判定覆盖。
  - 一个组件的就绪不应依赖可能宕机的远端服务，否则启动流程会反复杀掉它。
- **其他组件不受影响**：
  - Admin 和 host-gateway 的就绪本来就不依赖 EventBus。现在的核心阶段就是先等它们就绪，再启动 eventbus（`bootstrap.go:395-410`）。
  - 控制机上其余组件（strategy 等）在完整控制阶段才部署，那时 EventBus 已经在存储机运行。
- **全新安装的顺序**：
  1. 核心阶段（控制机）；
  2. `deploy-host storage`；
  3. `deploy-unit eventbus`，存储机；
  4. `deploy-unit storage`；
  5. 其他主机和单元；
  6. `bootstrap --stage control`。
- **完整控制阶段的启动顺序**：admin → host-gateway → host-agent → 其余组件。删掉"控制单元必须含 EventBus"的校验（`request.go:163`）。

### D3 服务端材料只发给部署了 EventBus 的主机

- 安全模型的变化：现在的规则是"broker 私钥不随主机部署扩散"，`unitinstall.Deploy` 只接受只含客户端材料的单元。新规则：
  - broker 的证书和私钥、`users.yaml`、`eventbus-internal-admin` 凭据，只导出给 profile 为 `eventbus`、且 `[deployments]` 里该主机确实部署了 eventbus 的单元；
  - 其他单元仍然只拿所需的客户端角色和公共 CA。
- **导出**：单元导出（`unitbootstrap/export.go`）按 profile 分流：
  - `eventbus` 调用 `eventbus-credentials export`，得到完整的服务端材料；
  - 其余调用 `export-clients`。
- **传输**：现有链路只认客户端清单，服务端材料过不去，需要新开一条独立的服务端材料链路。
  - 现状：导出结果类型 `hostbundle.ClientMetadata`（`export.go:31`）只描述客户端；`unitbundle/eventbus.go` 的 `readEventBusClients` 只放行 CA 加选定的角色文件；`unitdeploy/host.go:339-433` 按客户端清单下载、校验、上传。
  - 新增服务端材料清单类型，逐文件记录大小与摘要，和 `FetchServerMaterial`、`LoadServerMaterialAt` 这一组校验与传输函数；
  - 只有 `eventbus` profile 走这条链路，其余 profile 仍严格按客户端清单处理；
  - 服务端清单和它的摘要要进入导出检查点、传输校验和部署请求摘要（`unitinstall/deploy.go:35-77` 的 `DeploymentHash`、`DeploymentRequestHash`，调用处在 `unitdeploy/host.go:398-405`）。这样同一操作重试时，换不进另一套服务端材料。
- **安装**：`unitinstall.Deploy` 的 profile 白名单加入 `eventbus`。只有这个 profile 可以携带 `server.pem`、`server-key.pem` 和 `users.yaml`，其他 profile 带了就拒绝。`projectEventBus` 已有的服务端证书校验（证书必须能按端点主机名验证）保留。

### D4 TLS 身份随 EventBus 地址轮换

- **现状**：TLS 身份与地址绑定，记在 CA 记录的 `nats_url` 里，`ensure` 和 `export` 都会校验。地址一变，两者都会拒绝，提示"需要显式轮换"；但现有的 `rotate` 只能换角色令牌，换不了证书。CA 私钥签完就丢弃，没有保存，也就没法用原 CA 重签。
- **新增 `moox-admin-cli eventbus-credentials rotate-tls --confirm`**：
  - 按部署记录得出的新地址重新生成 CA 和服务端证书。服务端证书的地址包含 EventBus 主机地址、127.0.0.1 和 ::1；
  - 角色令牌不变；
  - 输出的提示写明：所有客户端必须重新部署，才能拿到新 CA。
- **对外入口是 `skills/moox/scripts/eventbus-credentials.sh rotate-tls --confirm ...`**（即 Admin CLI），与 `ensure`、`export`、`rotate` 同一个入口，不另做 `moox-cli setup` 子命令：
  线上凭据本来就只通过这个脚本维护（见 `skills/moox/references/eventbus-credentials.md`），生产也没有 `moox-cli setup` 的状态目录。
- 控制机上要先用 `native-release.sh install ... --no-restart` 装上新版 `moox-admin-cli`，旧版没有 `rotate-tls`。
  轮换之后，`ensure`、`export`、`export-clients` 都按新地址校验通过。
- **CA 私钥继续不保存**：轮换意味着全量重新部署，这对单用户、可以停机的系统可以接受，也不必为了保存 CA 私钥扩大密钥面。

### D5 客户端地址规则只有一处实现

- **规则**：客户端与 EventBus 在同一台主机，用 `tls://127.0.0.1:<端口>`；否则用 `tls://<EventBus 主机地址>:<端口>`。
- **实现**：在 `setup/config` 里加一个函数，例如 `Manifest.EventBusURLFor(hostID)`。所有渲染 EventBus 地址的地方都改为调用它（清单见 4.1）。
- **客户端连接地址与 broker 对外宣告地址分开**：
  - 现在 broker 的 `client_advertise` 取自同一个 `EventBusURL`（`unitinstall/eventbus.go:243`，broker 在 `eventbus/internal/broker/server.go:79` 采用）。按上面的规则，存储机本机的地址会变成 127.0.0.1，broker 就会把回环地址宣告给远端客户端。
  - `PrepareOptions` 增加 `EventBusAdvertise`，取值始终为 EventBus 主机的公网地址，只用于渲染 broker 配置；`EventBusURL` 只用于渲染客户端。
- **顺带纠正现状**：控制机上的本机组件现在用公网 IP 连本机的 EventBus（例如 strategy 配置里是 `tls://106.53.107.122:4222`），流量要绕一圈 NAT，与文档"本机服务连接 127.0.0.1"不符。
- **Admin 导出侧的清理**：`localEventBusRoles` 删除，导出只写公共地址。
  - 这张表假设 strategy、monitor 等角色与 EventBus 同机，甚至把 factor、trade 也算成"本机"。
  - 安装时 `projectEventBus` 会用渲染出的地址覆盖导出的地址，这张表本来就不起作用。

### D6 JetStream 数据：排空后启用全新的 store，不迁移

- **为什么不迁移**：控制机上的 store 有 2.3 GB，走那条小带宽链路要一个多小时。
- **为什么全新 store 是安全的**：
  - 已核对代码，各消费者都没有持久化 JetStream 的流序号。`StreamSeq` 只在 storage-view 分发队列的内存里用，Trade 只用它打日志。
  - 消费者都是各自启动时创建的（`DeliverAll` 或 `DeliverNew`），EventBus 启动时自己声明所有流。
- **前提**：迁移前先停掉所有写入入口，再把系统完全排空：所有 outbox 都为空，业务流上所有持久消费者的待投递数和待确认数都为 0（6.2 节第 7 步的核对表）。观测流 `MOOX_OBSERVABILITY` 不在此列，指标丢了也没关系。之后发出的事件，从全新的流序号 1 开始。

### D7 守护进程与告警

- **守护进程**（`unitwatch`）按主机发现组件，`headComponents` 只对本机实际安装的组件生效。
  - 控制机不再有 eventbus，启动依赖自然变成 admin → host-gateway → host-agent；
  - 存储机变成 host-gateway → eventbus → host-agent → Storage。
  - 只需改注释和 `peer.go` 里"EventBus 在 control 上"的文案。
- **新故障形态**：存储机或两机之间的网络断开时，控制机上所有 EventBus 客户端都会"未就绪"，Monitor 的观测输入也会中断。
- **守护进程**：
  - 对未就绪组件只告警、不重启，持续 5 分钟才报，同一种告警有最小间隔；
  - 没有部署 EventBus 的主机，在守护进程里增加一次对 EventBus 端点的 TLS 握手检查，方式与现有的 control 网关交叉检查相同；
  - 端点不可达时发一条"EventBus 不可达"，同时抑制本轮其他组件的未就绪告警；
  - EventBus 所在的主机上，本机 broker 未就绪同样作为抑制条件：只报 broker 一条，不再逐个报本机其他客户端未就绪（逐组件告警的逻辑在 `unitwatch/alert.go:161-165`）；
  - 守护进程按主机独立运行，告警键里含主机 ID（`unitwatch/alert.go:87`），所以结果是"每台受影响的主机一条"。单用户系统不再做跨主机的集中去重。
- **Monitor 会扩散告警，必须改**：观测消费者断开时，Monitor 只记一次 ingest 不可用（`metrics_runtime.go:106`）。之后几条判定照常运行，会逐个报出来：
  - 各上报方随时间被判为 stale（`metrics/catalog.go:173`）；
  - 业务新鲜度检查逐项失败（`business_freshness.go:199`）；
  - 主机上报超时，报"host-agent 停止上报"（`host_presence.go:113`）；
  - 独立定时器每 30 秒调用 `SilenceScanner.Scan`（`schedule_timers.go:59-69`），把超时的主机直接写成 `unreachable`（`hostmetrics/silence.go:40-46`）。
  
  改为：
  - ingest 不可用期间，暂停所有依赖观测输入的超时判定，包括 `SilenceScanner` 这个扫描入口，不只是在通知出口拦截；
  - 恢复时为各上报方和主机设定新的计时起点，避免旧的 `last_seen_at` 一恢复就触发告警；
  - Monitor 自己发一条"观测输入中断"告警；
  - 不依赖观测输入的检查照常判定，包括网关心跳、部署探活和 K 线新鲜度里直接读 Storage 的部分。
- **Doctor**：不需要改。`doctor` 按组件 ID 从部署清单里找 eventbus 的健康检查，本来就不假设它在哪台主机。

### D8 防火墙

- EventBus 端口的公网放行（0.0.0.0/0，凭 TLS 和 NATS 鉴权）移到 EventBus 所在的主机。`setup firewall`，以及 `deploy-host` 对出站主机的放行确认，都改为按 EventBus 主机检查。云接口已经会按主机实际所在的地域重试（`ensureFirewallRuleAtDiscoveredRegion`），不用改。
- **删除旧规则没有做**：现有的云接口只能新增或确认规则，没有删除能力；补删除要调腾讯云的删除接口（轻量应用服务器防火墙和 CVM 安全组两种），在没有测试桩的情况下写出来也验证不了。
  控制机上旧的 4222 规则在迁移验收之后到腾讯云控制台手工删除（按描述"MooX EventBus TLS"找到那一条）。CVM 的规则在安全组上，安全组可能被多台实例共用，删之前先看它关联了哪些实例。

## 3. 不采用的方案

| 方案 | 不采用的原因 |
|---|---|
| EventBus 并入 storage 单元 | 升级节奏不同，见 D1 |
| 核心阶段同时安装控制机和 EventBus 主机 | 一次操作跨两台主机，要两份恢复日志和两把维护锁，恢复逻辑复杂很多。D2 让 HostAgent 的就绪不依赖远端服务，更简单 |
| 在启动、激活、恢复、引导完成、host-ready 五处门禁里给 host-agent 开"只要在运行"的例外 | 例外散落在五处，后来者容易漏掉；改 HostAgent 的就绪语义只改一处（D2） |
| 迁移 JetStream 数据（整体拷贝，或先清空流再拷贝） | 排空之后，store 里没有任何未消费的数据，迁移只有代价、没有收益。整体拷贝 2.3 GB 还要一个多小时 |
| 行变更事件只发通知，storage-view 从本机 storage-node 取行 | 省下的流量差不多，但要改 Storage 内部的写入和消费模型；因子结果的写入目前也不记行变更日志。搬 EventBus 不改数据模型，改动集中在部署工具 |
| 控制机和存储机各放一个 EventBus | 两条事件流要么分开治理，要么相互镜像，复杂度远超收益 |

## 4. 代码改动（已完成，分支 `feat/eventbus-on-storage`，基于 `feat/f1-strategy` 的 7acf8896）

### 4.1 部署 CLI（`modules/cli/internal`）

| 位置 | 改动 |
|---|---|
| `setup/config/topology.go` | `EventBusClientURL`、`EventBusAdvertiseURL`、`Manifest.EventBusURLFor(hostID)`、`EventBusHost()`（D5）；所有渲染 EventBus 地址的地方都经过它 |
| `setup/unitpackage/package.go` | 新增 `eventbus` profile，control profile 去掉 eventbus；`setup release build` 也构建 eventbus 包（`unitdeploy/release.go`） |
| `setup/unitdeploy/core.go`、`unitbootstrap/{request,bootstrap,recover}.go` | 核心阶段的控制单元只有 Admin；拓扑必须把 eventbus 恰好登记在一台主机上（`eventBusHost`）；启动顺序 admin → host-gateway → host-agent → 其余；控制机上的客户端按"是否与 EventBus 同机"选地址 |
| `setup/unitbootstrap/export.go`、`unitbundle/eventbus.go`、`hostbundle/types.go` | 服务端材料链路（D3）：`ExportRequest.Server`、`ClientMetadata.Server`、`ServerFiles`；清单逐文件校验大小与摘要，没声明 Server 时多带或少带服务端文件都被拒绝 |
| `setup/unitinstall/{deploy,eventbus,prepare}.go` | profile 白名单加入 `eventbus`；服务端材料只允许 `eventbus` 单元携带（`DeploymentHash` 里校验，摘要因此绑定服务端清单）；新增 `EventBusAdvertise`：broker 的 `client_advertise` 始终是对外地址，拒绝回环地址，证书必须同时覆盖对外地址与本单元客户端实际连接的地址 |
| `setup/unitdeploy/{host,unit,external}.go` | 业务单元、Storage 的 `MOOX_STORAGE_EVENTBUS_URL`、因子引擎的 `--eventbus-url` 都改用 `EventBusURLFor`；`DeployUnit` 接受 `eventbus` profile，导出时带 `Server` |
| `setup/unitwatch/{watch,peer,alert,eventbus_peer}.go` | D7：EventBus 端点 TLS 握手检查、`peer-eventbus.json`、"EventBus 不可达/已恢复"告警；EventBus 不可用时抑制依赖它的组件的"持续未就绪"告警（Admin、主机网关、EventBus 自己除外）；control 交叉检查与它共用一套"连续失败约 3 分钟才告警"的状态机 |
| `command/{setup,setup_firewall}.go` | D8：EventBus 端口规则只放在 EventBus 所在主机，出站主机接入的规则也分到两台主机上 |
| `configinit/moox.toml.example`、根目录 `moox.toml.example` | `[deployments]` 里 eventbus 从 control 移到 storage |
| `command/setup_deploy_unit.go` | 命令说明里没有逐个列 profile，无需改动 |

### 4.2 Admin（`modules/admin/cmd/cli`）

- 新增 `eventbus-credentials rotate-tls --confirm`（D4）：按部署记录里的新地址重新生成 CA 与服务端证书，两条记录在一个事务里更新，角色令牌不变。
- `export-clients --server`：额外导出 `server.pem`、`server-key.pem`、`users.yaml`，清单标记 `server: true`（D3）；`--server` 只能用于 `export-clients`。
- 删除 `localEventBusRoles`（导出一律写对外地址，各单元安装时再改写）；删除标注"待删除"的 `archive-eventbus` 角色（令牌、用户项、导出文件）。线上 DB 里已有的 `eventbus_archive` 令牌记录无害，不再被使用。

### 4.3 其他模块

- **HostAgent**：`internal/app` 新增独立的采集状态；`/readyz` 只看采集（至少成功采集过一次，且最近一次采集没有出错），不再包含 EventBus 连接和发布错误（D2）。
- **Monitor**：观测消费者没连上、或刚重新连上的 5 分钟宽限内，冻结所有依赖观测输入的超时判定（`SilenceScanner` 的主机失联扫描，以及业务新鲜度里的上报方、数据集、业务检查、模块健康）；只留一项"观测输入"检查说明原因；网关、K 线新鲜度、行情探针照常判定（D7）。
- **Strategy**：暂停的 `Quiesce` 等正在求值的那一期结束，并且 outbox 里的目标都发布到 EventBus。
- **`packages/sysctl`**：策略的"在途操作没有结束"改为阻断，不再往 Trade 和更上游继续暂停（其他模块仍然只是警告）。
- **Trade**：暂停期间真实目标不再被 `system_paused` 终止，改为延迟 5 秒重投，放开后照常接收，已过有效期的按过期终止。这与原来文档写的"暂停时目标被拒绝并终止"不同，已更新 `docs/模块/交易.md`。

### 4.4 文档与技能

`docs/总体设计.md`、`部署与运维.md`、`实现状态.md`、`模块/{事件总线,存储,主机代理,监控,命令行工具,因子,交易,策略}.md`，`skills/moox` 里的 `SKILL.md`、`references/{eventbus-credentials,custom-setup,host-registry}.md` 和 `scripts/eventbus-credentials.sh` 都按新位置改过。

### 4.5 没有做的

- **SCF 包里可选携带 EventBus CA 的清理**（`collectorpackager/scf.go`、`command/collector.go`）：涉及 40 多处测试和云函数环境校验，与迁移无关，推迟。
- **防火墙规则的删除**（`setup firewall --prune`）：见 D8。
- **`setup eventbus-rotate-tls`**：改走 skill 脚本，见 D4。
- **`moox-cli doctor`**：不需要改，见 D7。
- **`unitdeploy/core_linux_test.go`（完整原生核心门禁）已按新布局改过，但没有跑**：它要真实的 Access、出口代理、Trade 和 CGO 构建的 Storage 制品，本机没有备齐。

## 5. 测试

**已运行并通过**：

- 单元测试：`modules/{cli,admin,hostagent,strategy,trade,monitor}`、`packages/{sysctl,lifecycle,servicecatalog}` 全部 `go test ./...`；`GOOS=linux go vet ./modules/cli/...`。
- 新增的用例：
  - `EventBusURLFor` 的同机和异机两种情况；
  - broker 对外宣告对外地址、拒绝回环和证书不覆盖的地址；
  - `rotate-tls` 的完整流程：EventBus 换主机后 `ensure`、`export`、`export-clients` 被拒绝，轮换之后通过，令牌不变、CA 换新；
  - `export-clients --server` 与服务端清单的校验；
  - HostAgent 在 EventBus 不可达或发布失败时仍然就绪；
  - 守护的 EventBus 端点检查、告警状态机与抑制；
  - Monitor 在观测输入中断时冻结、恢复宽限期内仍然冻结、宽限结束后恢复判定；
  - 策略等 outbox 排空、sysctl 在策略没发完时停在策略这一步、Trade 暂停时延迟重投。
- **Linux 门禁 `unitbootstrap`（9 个场景全部通过）**：在 Docker 里（arm64）用真实的 Admin、HostGateway、HostAgent、Console Proxy 制品跑 `TestBootstrapLinuxActualAdminGatewayAndServicesRecoverTogether` 等场景。
  拓扑把 eventbus 登记在控制机上，但控制单元里没有它、也没有任何 broker 在运行：**HostAgent 照样就绪**，这是 D2 在真实环境里的验证。

**没有运行**：`test-unit-core-linux`（见 4.5）、`test-unit-install-linux`、`test-unit-runtime-linux`。`eventbus` 单元的真实安装（真实 broker、TLS、跨主机连接）在第 6 节的迁移里首次发生，所以迁移要先在存储机上单独验证它，再停机。

## 6. 生产迁移（按线上真实的运维方式；已于 2026-10-27 执行，记录见 6.6）

### 6.0 与第一版计划的差别

第一版第 6 节用 `deploy-unit`、`bootstrap --stage control` 做重新部署。核实之后发现线上做不了：

- 线上是原生部署，发布走 `skills/moox/scripts/native-release.sh`（SSH 公钥，逐组件替换二进制并核对）。本机没有 `moox-cli setup` 的状态目录和 core 收据，`deploy-unit`、`bootstrap` 跑不起来；技能文档也明确写了"不要用 `moox-cli` 做发布"（它会用 `moox.toml` 里的 SSH 密码）。
- 线上 EventBus 的 `users.yaml` 是手工维护的，凭据文档要求"以现有文件为准，只追加新角色的块"。
- 存储机安全组没有放行 4222：从控制机连 4222 超时，11003、20211 是通的。
- 新版代码里"eventbus 单元"的目录布局已经确定（`/data/moox/eventbus/releases/<发布>` + `current`），迁移时在存储机上按同样的布局手工搭一份；以后用 `setup deploy-unit --profile eventbus` 重装时，会得到同样的布局。

所以迁移改成：用 `native-release.sh` 换新二进制，手工搭 EventBus 单元，脚本化扇出 CA 与地址，全程只用 SSH 公钥，不读取 `moox.toml` 里的账号密码；只有放开存储机 4222 一步要用 `moox.toml` 里的腾讯云凭据（技能文档规定的做法）。

### 6.1 线上现状（2026-10-27 只读核实）

| 主机 | 单元 | 含旧 EventBus 地址 `106.53.107.122:4222` 的文件 | `ca.pem` 副本 |
|---|---|---|---|
| 控制机 | `prod`、`host` | 28 个：`runtime-*.json` 8 个，各组件 `secrets/eventbus/*.yaml`，`host-agent/config/eventbus.yaml`，`collector`、`monitor`、`strategy` 的 `config/app.yaml` | 11 个 |
| 存储机 | `storage`、`access`、`host` | 17 个：`runtime-*.json`，storage 三个组件与 access、host-gateway、host-agent 的 `secrets/eventbus/*.yaml` | 8 个 |
| 海外机 | `access`、`collector-stream`、`egress-proxy`、`trade`、`host` | 15 个：同上，另有 `trade/config/app.yaml` | 10 个 |
| 因子机 | 外部程序，`~/moox/factor-engine/` | 引擎配置里的 `factor-eventbus` 角色文件与 `--eventbus-url`（迁移时核实具体文件） | 1 个 |

（统计命令：在各单元 `current/` 下 `grep -rIl 106.53.107.122:4222`，排除 `data`、`logs`；`find -L ... -name ca.pem -path '*eventbus*'`。）

### 6.2 要换的二进制（一律 `native-release.sh`，一次一个组件、一台主机）

| 二进制 | 主机 | 为什么 |
|---|---|---|
| `moox-host-agent` | 控制机、存储机、海外机 | 就绪不依赖 EventBus。不先换的话，迁移期间旧版 HostAgent 重启后连不上 EventBus 会一直起不来 |
| `moox-runtime`（`host-gateway` 目标产出） | 各主机 host 单元 | 守护的 EventBus 端点检查；就地替换文件，cron 条目不用动 |
| `moox-strategy` | 控制机 | 暂停等 outbox 发完 |
| `moox-trade`（及 cli） | 海外机 | 暂停期间目标延迟重投 |
| `moox-monitor`（及 cli） | 控制机 | 观测输入中断期间冻结判定 |
| `moox-admin-cli` | 控制机（`--no-restart`） | `rotate-tls`、`export-clients --server` |
| `moox-eventbus`、`moox-admin` 服务端、Storage 全部 | 不换 | 这次没有改动；存储机上的 EventBus 用控制机当前发布里的同一份二进制 |

服务目录（`catalog.yaml`）没有改动，不需要整体发布。这些二进制先发布、先观察一个周期，再开始迁移。

### 6.3 准备（不停机）

0. **屏蔽告警推送**：`moox-cli monitor mute --for 3h --reason EventBus迁移 --file ./moox.toml`（`references/initial-import.md` 里的做法；`mute-status` 查看）。第 3 步之后 Admin 的部署登记已经把 eventbus 记在存储机上，而它要到切换时才真正在那里运行，期间 Monitor 的部署探活会报"eventbus 未就绪"；切换完成后解除。
1. **改 `moox.toml`**：把 `[deployments]` 里的 eventbus 从 control 移到 storage。文件是加密的，参照 F1 迁移用过的补丁工具（在本机进程内解密、校验、备份、原子替换，不输出内容）。
2. **放开存储机 4222**：用新构建的 `moox-cli setup firewall --file moox.toml`（只放行，不删除）。
   验证：从控制机、海外机、因子机各连一次 `存储机:4222`，没有 EventBus 在监听时应当是"连接被拒绝"，而不是超时。
3. **同步部署登记**：`setup sync-deployments --host control`，再 `--host storage`（顺序不能反，单实例组件不能同时在两台主机上启用）。
   验证：Admin 的 `ResolveEventBusEndpoint` 解析到存储机。
4. **轮换 TLS**：备份 Admin 库（SQLite 在线备份，不要直接复制带 WAL 的文件）；用新版 `moox-admin-cli`：`eventbus-credentials.sh rotate-tls --confirm ...`，再 `export` 到 0700 临时目录。
   然后**比对线上 `users.yaml` 与新导出的那份**：逐个角色比较令牌和权限块。令牌不会变；线上手工加过的权限不能丢。最终沿用线上现有的 `users.yaml`，只把需要追加的块补上。
   这一步之后，旧 EventBus 仍然用它磁盘上的旧证书继续工作，不受影响。
5. **在存储机上搭好 EventBus 单元（不启动）**：`/data/moox/eventbus/releases/eventbus-<时间>/`，布局与 `eventbus` profile 一致：
   - `bin/moox-eventbus`：从控制机当前发布里拷来（同一份二进制，核对 sha256）；
   - `eventbus/config/app.yaml`：以控制机现在的那份为底，把 `broker.client_advertise` 改成 `146.56.196.204:4222`，`server_name` 改成 `eventbus-storage`，证书、`users.yaml`、CA 的路径指向新发布目录；
   - `eventbus/secrets/eventbus/`：新 CA、新服务端证书和私钥（来自第 4 步的导出）、线上的 `users.yaml`、`internal-admin.yaml`（地址 `tls://127.0.0.1:4222`）、`metrics-publisher.yaml`；
   - `eventbus/data/eventbus/jetstream/`：空目录（全新的 store，D6）；
   - `secrets/runtime-eventbus.json`、`runtime.json`、`start.sh` 等七个生命周期脚本：以控制机当前发布里的为模板，改主机 ID 为 `storage`、路径指向新发布目录；
   - `host-gateway` 链接指向 `/data/moox/host/current/host-gateway`；
   - `prepared.json`：复制存储机 host 单元的 `prepared.json` 里的身份部分，改 profile 为 `eventbus`、组件为 `["eventbus"]`，再执行 `moox-runtime reseal --unit-root /data/moox/eventbus --yes` 重新生成文件清单。
   先**不建 `current` 链接**，守护进程发现不了它，不会提前拉起。
6. **单独验证这个单元**：用 `moox-runtime start --plan <新发布>/runtime.json` 手工起来，核对 `/readyz`、`ListStreams`（四个流都在、都是空的）、从控制机和海外机用对外地址做 TLS 握手、用某个角色的新凭据做一次真实连接；然后停掉。此时新 CA 还没有扇出，所以握手时要显式指定新 CA。
7. **写好扇出脚本并干跑**：对每台主机，在各单元 `current/` 下找出含旧地址的文件和 `secrets/eventbus/ca.pem`，输出"将要改哪些文件"的清单，只读。核对数量与 6.1 一致。

### 6.4 切换（停机窗口，预计 30～45 分钟）

1. **关守护**：各主机用户 crontab 里的 `# moox-watchdog` 行先备份再注释掉（四台逐台操作；验证：`crontab -l` 里该行已注释）。不用 `pause.sh`，它会留下持久的暂停标记。
2. **系统暂停**：`moox-cli system pause`（要带四个模块的健康端点，用 SSH 隧道或在各机上执行）。顺序是策略 → Trade → 因子引擎 → 采集。
   - 策略要等 outbox 发完才算停下，发不完会停在策略这一步报错，查清原因后重新执行，不要跳过。
3. **停其余写入方**：
   - 控制机的 collector 进程（它的 subject-sync 定时任务在暂停期间仍会写 Storage）；
   - 海外机的 collector-stream；
   - 两台主机的 access（兜底，挡住所有经 Access 进来的写入）；
   - 因子机的引擎和归档 Worker；
   - 已登记的私有写入任务（先在各主机核对 crontab 和 systemd 用户服务）。
4. **排空核对**：所有条件满足、并且跨过至少一个 1m 边界和一个 5m 边界之后仍然满足，才继续：
   - storage-node：`moox_storage_outbox_pending_entries` 为 0，`/readyz` 里 `outbox_pending_entries` 也为 0；
   - storage-view：`moox_storage_view_ready_queue_pending` 为 0；
   - strategy：待发布数为 0；
   - 持久消费者：先从 View 清单和各模块列出应有的集合，再用 `ListConsumers` 翻完分页，`MOOX_STORAGE`、`MOOX_COLLECTOR`、`MOOX_TRADE` 三个流上每个消费者的待投递数和待确认数都是 0（`MOOX_OBSERVABILITY` 不在范围内）。
     Trade 暂停期间到达的目标会延迟重投，所以 `MOOX_TRADE` 上的待投递数可能不是 0；这时等它们过期，不要强行继续。
5. **停机**：除 Admin 和各主机的 host-gateway 外，所有组件都停掉，包括 storage 三个组件、所有 host-agent、控制机上的 eventbus。
6. **建 `current` 并启动存储机上的 EventBus**：`ln -sfn` 建 `/data/moox/eventbus/current`，`moox-runtime start --plan`；核对就绪与四个流。
7. **扇出**：运行第 7 步的脚本，把各主机上的 `ca.pem` 换成新 CA，把 `106.53.107.122:4222` 换成该主机应使用的地址（存储机上的客户端用 `127.0.0.1:4222`，其余用 `146.56.196.204:4222`）；文件权限保持 0600；每个单元改完执行 `moox-runtime reseal`。
   验证：再 `grep` 一遍，旧地址一个都不剩；每个 `ca.pem` 的 sha256 与新 CA 一致。
8. **按依赖顺序启动**：
   1. 存储机：host-agent、storage-primary、storage-node、storage-view、access；
   2. 控制机：host-agent、monitor、strategy、factor-mgr、collector、cloudfunction、archive-mgr 等其余组件；
   3. 海外机：host-agent、access、egress-proxy、trade、collector-stream；
   4. 因子机：引擎、归档 Worker。
   每一步核对就绪，日志里不能出现 `Authorization Violation`、`certificate`。
9. **恢复**：`moox-cli system resume`，先追赶后放行。
10. **开守护并解除屏蔽**：恢复各主机 crontab 里的 `# moox-watchdog` 行；`moox-cli monitor mute` 对应的解除。

没有回退设计（项目未上线）。要换回控制机，就把 `[deployments]` 改回去，用同样的流程再做一遍。

### 6.5 收尾

- 观察至少 12 个周期，第 7 节全部通过之后：到腾讯云控制台删除控制机上旧的 4222 规则（见 D8）；
- 删除控制机旧发布目录里的 eventbus 数据目录（约 2.3 GB）；
- 把 `skills/moox/references/native-release.md` 的单元表里 `eventbus` 从控制机 `prod` 单元移到存储机的 `eventbus` 单元，并记下这次手工搭单元的做法；
- 更新记忆里的 `prod-deploy-procedure`、`eventbus-store-limit-exact` 和迁移进度。

### 6.6 执行记录与偏差（2026-10-27）

时间线（UTC）：02:47～02:50 换完二进制（四台主机的 host-agent、`moox-runtime`，控制机的 strategy、monitor、admin-cli，海外机的 trade）；02:50～02:53 改 `moox.toml`、同步部署登记、轮换 TLS；
03:17 在存储机搭好单元并单独验证；03:21 关守护并系统暂停；03:27 新 EventBus 启动；03:28～03:29 按顺序拉起；03:30 开始恢复，03:35 采集放行；
04:02 因子引擎与 Trade 放行；04:10 策略放行。写入方（采集）停了约 12 分钟（03:23～03:35），策略 49 分钟没有出目标（03:21～04:10）。

与计划不同或计划没写到的地方：

- **因子机也有 `host` 单元（host-agent、host-gateway）**，也要换 `moox-host-agent`、`moox-runtime`，也要改地址与 CA；它不在 `native-release.sh` 的主机别名里，用环境变量 `MOOX_RELEASE_HOST_FACTOR` 临时指定。
- **控制机 `prod` 单元必须摘掉 `eventbus` 组件**：一是看守进程恢复后会把停掉的旧 EventBus 拉起来；二是新版 `moox-runtime reseal` 按 `control` profile 校验，计划里多出 `eventbus` 就报 `preparation receipt requires a valid unit selection`（10 个组件对 9 个）。做法：停旧 EventBus → 改 `runtime.json`、`prepared.json`、`package-manifest.json` 去掉该组件 → 把 `eventbus/` 目录整个移出发布目录（清单边界检查不认识它）→ `reseal --yes`。
- 防火墙：`setup firewall` 报 18 条规则都已存在、没有新建；实测从控制机、海外机、因子机连存储机 4222 都是"连接被拒绝"（已放行、无人监听），启动后 TLS 握手验证通过。
- `users.yaml`：线上与新导出的逐角色比对，令牌与权限块全部一致；线上多一个 `archive-eventbus` 角色（代码里已不用），沿用线上文件。
- 排空：停机时只剩一个消费者（`storage_view_factor_3ab1…`）有 1 条在途消息，暂停前就在，View 的待发布数为 0；新 store 丢掉这一条，不影响恢复。
- **系统恢复在栅栏处等到超时**：暂停跨过新的 K 线周期后，策略要等因子引擎放行、算出最新周期才能追平，因子引擎在等栅栏。30 分钟超时后用 `resume --release-partial --yes` 放行因子引擎与 Trade，策略约 8 分钟后追平，再 `resume` 放行策略。下次暂停窗口要预留这一步，或把恢复的超时缩短到 5 分钟。
- 新地址下 `moox-cli system pause/resume` 要走 SSH 隧道到各模块健康端口（控制机 11412/11431、海外机 11210、因子机 11417），鉴权密钥取自控制机 `runtime-monitor.json`，只放内存。
- 暴露了一个原来就有的问题：Collector 重启后每分钟报 `collector fetch plan reconciliation failed space=crypto: no available code package named "moox-collector-crypto-plan"`，Monitor 出一条 `market_fetch:scf_timer` 严重告警。云函数 GC 在 10-25 03:47 清掉了这个代码包，之前的进程没有重新对账所以没暴露。它与 EventBus 位置无关。根因是对账器每轮先解析代码包，解析不到整轮失败，而 crypto 的计划已全是推送数据集、云端没有函数，包本来用不上。已修复（`bea87310`：包只在创建或升级云函数时才要求，云端已有计划函数时仍然拦住），04:31 UTC 发布 collector，告警于 04:32 恢复。

未做：故障演练（临时阻断 4222，会影响生产）；SCF 里旧 EventBus CA 的清理；控制机安全组里旧的 4222 规则（到腾讯云控制台删）；控制机上旧 EventBus 数据（`/data/moox/prod/eventbus-move-*/old-eventbus-component`，2.4 GB）与 Admin 库备份，观察满 12 个周期后删。

## 7. 验收

**流量**：

- 边界前后各 20 秒抓包：存储机 ↔ 控制机之间 4222 端口的字节数，应该从每个边界约 2 MB 降到只剩 ViewDataReady、目标权重和指标这类小消息（几十 KB 级）。

**延迟**：

- storage-view 的"因子结果就绪 … 投递滞后"：p50 小于 50 ms，p90 小于 200 ms。基线是 chain 的 0.72 / 2.67 秒。
- strategy 的"当期与上一根行"：p90 回到 0.5 秒以内。如果达不到，剩下的就是 690 KB 读应答的问题，按前置文档 4.0～4.2 继续处理。

**功能**：

- Monitor 收到全部主机的主机快照和全部服务的指标，没有持续的"已停止上报"；
- Trade、因子引擎、Collector 的合成消费者、Strategy 都在正常消费；
- `moox-cli doctor` 全部通过；
- 守护日志里没有持续的未就绪告警。

**故障演练**：在两机之间临时阻断 4222（例如在存储机安全组临时拒绝控制机的来源地址），确认以下几点：

- 控制机只收到一条"EventBus 不可达"（守护进程）和一条"观测输入中断"（Monitor）；
- 没有逐个组件的未就绪告警，也没有"已停止上报"告警；
- 恢复后各组件自动重连，Monitor 重新计时，之后不补报误告警。

### 7.1 验收结果（2026-10-27 04:10～04:21 UTC，策略放行后的前两个 5 分钟边界）

| 项 | 结果 | 对照 |
|---|---|---|
| 存储机 ↔ 其他主机 4222 流量 | 04:19:55 起 28 秒（跨 5 分钟边界）共 152 KB：控制机 113 KB、海外机 28 KB、因子机 12 KB | 迁移前每个边界约 2 MB |
| strategy"当期与上一根行" | 04:15、04:20 两个周期各 5 个实例共 10 个样本，209～299 ms，p90 约 0.29 秒 | 迁移前 p50 1.0 秒、p90 2.2 秒；目标 p90 小于 0.5 秒，达到 |
| storage-view"投递滞后" | 21 个事件：p50 317 ms、p90 509 ms；chain 5m 178 ms | 迁移前 chain 0.72 / 2.67 秒；目标 p50 小于 50 ms、p90 小于 200 ms，**没有达到**，剩下的部分不在 EventBus 往返上，需要另查（事件产生到 View 处理之间的 outbox 与提交路径） |
| Monitor 告警 | 只剩 `market_fetch:scf_timer` 一项（原有问题，见 6.6）；控制机磁盘使用率回落到 55%，告警已恢复 | |
| 消费者 | `ListConsumers` 四个流：22 个 View 消费者与策略、Trade、因子消费者都已重建，待投递与待确认为 0 | |

故障演练没有做。

### 7.2 耗时与带宽分解（2026-10-27，04:10～05:12 UTC，迁移后稳态）

**单期耗时**（5 分钟周期，从边界 B 起算，中位数；引擎数据取自因子机日志 36 期、策略数据取自 60 个样本）：

| 实例 | 引擎 load | 引擎 compute | 引擎 write | K线至就绪 | 就绪等待 | 读取行 | 求值 | 端到端总计 |
|---|---|---|---|---|---|---|---|---|
| ma_var_5m | 0.38s | 0.003s | 0.30s | 1.5s | 0.15s | 0.24s | 0.002s | 2.1s |
| chain | 0.31s | 0.79s | 0.54s | 2.3s | 0.34s | 0.23s | 0.002s | 3.0s |
| FH002 | 0.31s | 1.36s | 0.76s | 3.4s | 0.33s | 0.30s | 0.19s | 4.3s |
| FH001 | 0.26s | 3.32s | 0.65s | 5.1s | 0.29s | 0.31s | 0.04s | 5.9s |
| FH003 | 0.31s | 3.89s | 0.79s | 6.0s | 0.38s | 0.27s | 0.48s | 7.2s |

- 五个任务在 B+0.5～0.8 秒同时开始（采集周期完成事件到引擎）；View 就绪事件的"投递滞后"p50 317 ms：它是 View 开始处理的时刻减去因子引擎填的完成时间（`ComputedAt`，因子机时钟；两台主机的 chrony 偏差都小于 1 ms，不是时钟误差），View 自身处理不到 50 ms。这段时间包含引擎上报到 Access 的跨主机往返、数据节点 outbox 轮询（`RelayOptions` 默认 100 ms，代码里写死）、EventBus 投递与 View 消费者拉取，各段没有单独计时。`storage.yaml` 里的 `outbox.flush_interval_ms` 与 `view.batch_wait_ms` 只被解析、没有任何代码使用（死配置），改它们不会有效果。
- 关键路径：FH001、FH003 的引擎计算占端到端的一半以上（长窗口 EMA 每期重算 5 万根）；其余是 View 滞后（约 0.3 秒）、策略就绪等待（约 0.3 秒）、读行（约 0.3 秒）。

**压缩**：系统内已经全面压缩，tRPC（含跨主机网关）与事件都是 zstd 加字典（`packages/wirecompress`，只有 `default-v1` 一个字典、级别 SpeedDefault）。线上计数：事件压缩比 6.4～12.4 倍，tRPC 4.5～8 倍；策略读行响应 4.5 倍，因子引擎写入 6.6～8.2 倍。海外机 Access 无计数是因为它按入站帧的压缩类型原样转发。

**跨主机流量**（压缩后的线上字节）：

| 流 | 5 分钟边界 | 每分钟（仅 1m 任务） | 每小时 |
|---|---|---|---|
| 因子引擎 → 存储机 Access（写结果） | 约 860 KB（其中 5m 任务约 630 KB，峰值 232 KB/秒） | 约 230 KB | 约 21 MB（1m 任务占 13.8 MB） |
| 存储机 → 控制机（策略读行） | 约 520 KB（5 个实例各约 105 KB，峰值约 200 KB/秒） | | 约 6 MB |
| EventBus 4222（控制机、海外机、因子机） | 约 150 KB | | 约 4 MB |

没有任何策略使用 `ftask_spot_1m`、`ftask_swap_1m` 两个 1m 因子任务（五个策略实例都绑 5m 任务）。

**1m 因子任务已暂停**（2026-10-27 06:15 UTC，`moox-cli lifecycle pause --task ftask_spot_1m|ftask_swap_1m`，手动暂停，恢复用 `lifecycle resume`）：因子引擎每分钟写入从压缩后约 230 KB 降到约 20 KB。副作用：Monitor 的因子计算与结果视图新鲜度检查不看任务暂停状态，对这两个任务出 4 条告警（"任务运行结果已过期"、"结果视图等待中"），需要 Monitor 跳过手动暂停的因子任务。策略的"当期与上一根行"只读当期：五个策略都不引用 `bars[-1]`，所以不读上一根，缓存上一根行没有收益。

## 8. 后续评估（不在本计划内）

- **strategy 与 View 同机**：搬完以后，存储机 → 控制机最大的一块流量是策略读取 View 的应答。strategy 搬到存储机后，读取变成本机访问；代价是 strategy 与 factor-mgr、Trade 之间的调用要跨机。等本计划验收后，用数据再决定。
- **前置文档 4.0 的公网带宽核查**：不论本计划做不做，都要做。

## 9. 审查记录

### 9.1 Codex 第一轮（2026-10-27，xhigh，文档审查）

审查以 `feat/f1-strategy` 工作树为代码基线。

**必须修改：10 条，全部核实成立，均已修改。**

| # | 意见 | 处置 |
|---|---|---|
| 1 | 只放宽部署门禁不够：`unitruntime` 启动时会停掉超时未就绪的进程，激活、恢复、日志也都要求就绪 | 改为调整 HostAgent 的就绪语义，`/readyz` 不再含 EventBus 连接；五处门禁都不改（D2、4.3） |
| 2 | 服务端材料过不了现有只认客户端清单的导出—传输链路 | D3 新增独立的服务端材料清单与传输链路，4.1 补上所涉文件 |
| 3 | 导出用的是控制机当前发布里的旧 Admin CLI，没有 `rotate-tls`；同一 release 的未完成记录不能在 CA 变化后重试 | `eventbus-rotate-tls` 改为从新版本制品取 Admin CLI 执行；每一步都带新的 release（D4、6.1） |
| 4 | 暂停顺序可能把在途的 Trade 目标终止：Trade 暂停后，新到的目标被 `system_paused` 终止 | 已核实策略暂停不等 outbox 排空。在代码里修：策略暂停要等 outbox 发完（4.3）；排空核对加上"Trade 没有终止过目标"；预演加入在途目标用例 |
| 5 | 停写范围不对：Collector 暂停会停用 SCF 定时器，但 subject-sync 仍会写入；`ListConsumers` 有分页，而且只能看到已存在的消费者 | 重写第 6 步的写入方清单；第 7 步先列出应有的消费者集合，再翻完所有分页核对 |
| 6 | `sync-deployments` 必须带 `--host`；单实例组件不能同时在两台主机启用 | 改为先同步 control、后同步 storage，并验证只有一个启用部署（第 9 步） |
| 7 | 在 EventBus 启动前做不了握手验证；现有云接口没有删除规则的能力 | 握手验证移到第 12 步；`packages/cloudprovider` 补删除能力，`setup firewall` 改为对账（D8） |
| 8 | Admin 库在 WAL 模式下直接复制不一致；回退时覆盖 Admin 库会丢掉迁移后的变化；旧版本按"先 host 后 control"部署会失败 | 曾改为在线备份与分情况回退。后按用户决定（项目未上线，不为回退做额外工作）删去备份与回退设计，6.4 只保留一段 |
| 9 | 本机地址改成回环后，broker 的 `client_advertise` 也会变成 127.0.0.1 | 客户端地址与 broker 对外宣告地址分开，新增 `EventBusAdvertise`（D5） |
| 10 | Monitor 在观测输入中断后会逐个报 stale、新鲜度失败、主机上报超时 | D7 明确改 Monitor：ingest 不可用期间暂停这些判定，只发一条"观测输入中断"；守护进程的告警写成"每台受影响的主机一条" |

**建议修改：3 条，已采纳。**

- 补齐遗漏的文件：`setup_deploy_unit.go`、`configinit/moox.toml.example`、`docs/模块/因子.md`、`skills/moox` 下的 `host-registry.md` 和 `view-catchup.md`；编码收尾时再全仓检索一次。
- 预演加上"迁移后新建数据集和 View"的验证（第 5 节）。
- 命令写成可执行的形式：`pause.sh`、`resume.sh` 在生产机上存在，Codex 认为"仓库里没有"，这一点不准确；但它们只暂停单元里的进程，不是系统暂停。系统暂停改用 `moox-cli system pause/resume`（第 6 节开头）。容量改为停机前现场核验（第 5 步）。

**可选：1 条，已采纳。** 告警数量目标写清为每台受影响的主机一条，见 D7 和第 7 节。

### 9.2 Codex 第二轮（2026-10-27，high，复审第一轮处置）

复审确认第一轮 #5、#6、#8、#9 的处置与代码相符。

**必须修改：9 条，全部核实成立，均已修改。**

| # | 意见 | 处置 |
|---|---|---|
| 1 | 策略 outbox 排空只说明已发布，不说明 Trade 已接收；Trade 暂停后仍会终止新到的目标 | Trade 暂停的第一步改为等目标积压清零，然后才关闭接收（4.3） |
| 2 | 编排器遇到"在途操作没有结束"只记警告，继续暂停上游（`sysctl.go:164-177`，已核实） | 对 Strategy 和 Trade 改为按失败处理，停在那里（4.3、第 6 步） |
| 3 | 停机期间守护进程每分钟会把普通停掉的组件重新拉起（已核实） | 第 8 步先在主机策略里关闭守护，第 14 步再打开；写明不能用 `pause.sh` 代替 |
| 4 | 回退沿用 `<REL>` 会被操作记录拒绝 | 回退设计已按用户决定删去（见 9.1 第 8 条）；6.4 写明换回控制机时用新的 release ID |
| 5 | 第 11 步执行对账式的 `setup firewall`，会提前删掉控制机的规则 | 新增与删除拆开：`setup firewall` 只放行，`--prune` 只删除，第 15 步才删（D8） |
| 6 | 构建发布时没带 `--storage-binary-dir`，发布里不会有 storage 包 | 第 1 步先构建 Storage 的 Linux 制品，再带参数构建发布，并核对制品清单 |
| 7 | Monitor 的 `SilenceScanner` 是独立的扫描入口，会直接把超时主机写成 `unreachable` | 抑制放到扫描入口，恢复时重设计时起点（D7、4.3） |
| 8 | HostAgent 的 `last_error` 混着采集错误和发布错误，只去掉连接条件仍然就绪不了 | 新增独立的采集状态供 `/readyz` 使用（D2、4.3） |
| 9 | 服务端清单没有纳入部署请求摘要 | 服务端清单进入导出检查点、传输校验和 `DeploymentHash` / `DeploymentRequestHash`（D3、4.1） |

**建议修改：2 条，已采纳。**

- EventBus 主机上本机 broker 未就绪时，抑制本机其他客户端的未就绪告警（D7）。
- 防火墙删除只针对 MooX 建的那条规则；安全组同时关联 EventBus 主机时不删（D8）。

### 9.3 实现阶段的取舍与发现（2026-10-27）

- **线上没有 `moox-cli setup` 的状态目录，且技能文档禁止用 `moox-cli` 发布**：第 6 节整体重写，见 6.0。
- **HostAgent 的就绪语义**：Docker 里的 Linux bootstrap 门禁验证了"没有 broker 时 HostAgent 照样就绪"；该门禁里原来依赖控制单元含 eventbus 的断言（断言 broker 拒绝无角色的 TLS 客户端、EventBus CA 在控制单元里的路径）已按新布局改写。
- **Trade 暂停的语义**：第二轮审查建议"Trade 暂停前先等目标积压清零"，但生命周期框架没有"暂停前"的钩子；改为让暂停期间到达的真实目标延迟重投、不终止，同样不会静默丢目标，改动更小。副作用：迁移排空核对时 `MOOX_TRADE` 上可能还有待投递的目标，要等它们过期。
- **没有写防火墙删除、SCF CA 清理**：见 4.5。

