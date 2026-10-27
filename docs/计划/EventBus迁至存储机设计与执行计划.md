# EventBus 迁至存储机设计与执行计划

- **日期**：2026-10-27
- **状态**：待评审，尚未编码
- **前置文档**：[策略读取变慢排查与修复方案](策略读取变慢排查与修复方案.md)。那份文档定位了存储机 ↔ 控制机公网链路的突发丢包；本文是其中提到的结构性修复。
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
- **对外入口是 `moox-cli setup eventbus-rotate-tls --release <REL>`**，不让操作者直接 SSH 去跑 admin CLI。
  - 现有导出调用的是控制机当前发布里的 `moox-admin-cli`（`export.go:169`）。迁移时控制单元最后才更新，当前发布里的旧 CLI 还没有 `rotate-tls`（`eventbus_credentials.go:98`）。
  - 所以这条命令从新版本制品里取出 `moox-admin-cli`，上传到控制机的状态目录，再经 `moox-runtime` 在维护锁内对 Admin 库执行。这和导出调用离线 CLI 是同一种方式，不会踩到"自己持锁再启动组件，锁被子进程继承"的坑。
  - 轮换之后，`ensure`、`export`、`export-clients` 都按新地址校验通过。同一个 `--release` 下未完成的旧操作记录绑定的是旧材料，不能拿来重试；迁移的每一步都用新的 release（6.1）。
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
- **Doctor**：`doctor` 里把 eventbus 当作关键组件的检查，改为按它的部署主机去定位。

### D8 防火墙

- EventBus 端口的公网放行（0.0.0.0/0，凭 TLS 和 NATS 鉴权）移到 EventBus 所在的主机。`setup firewall`，以及 `deploy-host` 对出站主机的放行确认，都改为按 EventBus 主机检查。云接口已经会按主机实际所在的地域重试（`ensureFirewallRuleAtDiscoveredRegion`），不用改。
- 现有的云接口只能新增或确认规则，没有删除能力。`packages/cloudprovider` 要补上删除（轻量应用服务器的防火墙和 CVM 安全组两种都要）。
- 新增和删除拆成两个操作：
  - `setup firewall` 只负责放行，行为不变；
  - 新增 `setup firewall --prune`，删除不再需要的规则：EventBus 端口只保留在 EventBus 主机上，其余主机上的这条规则删掉。
  - 这样迁移时可以先放行新主机、验收后再清理旧主机。
- **删除只针对 MooX 自己建的那条规则**：按协议、端口、来源和描述（"MooX EventBus TLS"）精确匹配。CVM 的规则在安全组上，而安全组可能被多台实例共用（`cvm_firewall.go:136-159`）。所以删除前先核对这个安全组关联了哪些实例；如果 EventBus 主机也在其中，就不删，并给出提示。

## 3. 不采用的方案

| 方案 | 不采用的原因 |
|---|---|
| EventBus 并入 storage 单元 | 升级节奏不同，见 D1 |
| 核心阶段同时安装控制机和 EventBus 主机 | 一次操作跨两台主机，要两份恢复日志和两把维护锁，恢复逻辑复杂很多。D2 让 HostAgent 的就绪不依赖远端服务，更简单 |
| 在启动、激活、恢复、引导完成、host-ready 五处门禁里给 host-agent 开"只要在运行"的例外 | 例外散落在五处，后来者容易漏掉；改 HostAgent 的就绪语义只改一处（D2） |
| 迁移 JetStream 数据（整体拷贝，或先清空流再拷贝） | 排空之后，store 里没有任何未消费的数据，迁移只有代价、没有收益。整体拷贝 2.3 GB 还要一个多小时 |
| 行变更事件只发通知，storage-view 从本机 storage-node 取行 | 省下的流量差不多，但要改 Storage 内部的写入和消费模型；因子结果的写入目前也不记行变更日志。搬 EventBus 不改数据模型，改动集中在部署工具 |
| 控制机和存储机各放一个 EventBus | 两条事件流要么分开治理，要么相互镜像，复杂度远超收益 |

## 4. 代码改动清单

**基线**：生产版本来自 `feat/f1-strategy`，这个分支的 CLI 和 main 有约 30 个文件的差异。先把它合入 main，再在新的 main 上开分支改。

### 4.1 部署 CLI（`modules/cli/internal`）

| 位置 | 改动 |
|---|---|
| `setup/config/topology.go`、`config.go` | 新增 `EventBusURLFor(hostID)`（D5）；EventBus 的对外地址仍由 `[deployments]` 推出 |
| `setup/unitpackage/package.go` | 新增 `eventbus` profile；control profile 去掉 eventbus |
| `setup/unitdeploy/core.go` | 核心请求的控制单元只含 admin；更新注释 |
| `setup/unitbootstrap/request.go:163` | 改为只要求 admin |
| `setup/unitbootstrap/bootstrap.go` | 核心阶段不再启动 eventbus；控制阶段的启动顺序改为 admin → host-gateway → host-agent → 其余；`eventBusURL` 改用 D5 的函数 |
| `setup/unitbootstrap/recover.go` | `startStages` 与上一行一致；暂停例外名单去掉 eventbus。就绪判定不改，D2 改的是 HostAgent 自己的就绪语义 |
| `setup/unitbootstrap/export.go`、`setup/unitbundle/eventbus.go`、`setup/unitdeploy/host.go:339-433`、`hostbundle` 的清单类型 | 按 profile 分流导出；新增服务端材料的清单、校验与传输链路（D3） |
| `setup/unitbootstrap/bootstrap.go:142`（"不能缩减控制单元"） | 逻辑不改。它只比对 `[deployments]` 里仍属于控制机的组件，把 eventbus 从部署里移走后，这条校验自然放行。补一条测试：已安装的控制单元含 eventbus，部署记录把它移走后，完整控制阶段能装出不含 eventbus 的新发布 |
| `command/` 新增 `setup eventbus-rotate-tls` | 从新版本制品取出 `moox-admin-cli` 上传到控制机，经 `moox-runtime` 持维护锁执行 `rotate-tls`（D4） |
| `setup/unitbootstrap/offline.go` | 核心阶段仍执行 `eventbus-credentials ensure`；地址按部署记录取，不再假设是控制机 |
| `setup/unitinstall/deploy.go` | 第 84 行的 profile 白名单加入 `eventbus`，服务端材料只允许出现在这个 profile 里；`DeploymentHash`、`DeploymentRequestHash` 纳入服务端清单（D3） |
| `setup/unitinstall/eventbus.go`、`prepare.go` | 保留服务端证书校验；客户端地址取 `EventBusURL`，broker 的 `client_advertise` 改取新增的 `EventBusAdvertise`（D5） |
| `setup/unitdeploy/host.go:370`、`unit.go:114`、`external.go:155` | EventBus 地址改用 D5 的函数。这三处分别是业务单元、Storage 的 `MOOX_STORAGE_EVENTBUS_URL`、因子引擎的 `--eventbus-url` |
| `setup/unitwatch/watch.go`、`peer.go`、`alert.go` | 改注释和文案；新增 EventBus 端点检查与告警抑制（D7） |
| `command/setup_firewall.go`、`command/setup.go` | 防火墙规则按 EventBus 主机下发；新增 `--prune`，精确删除其他主机上的 EventBus 规则（D8） |
| `command/setup_deploy_unit.go` | 命令说明和 `--profile` 帮助加入 `eventbus` |
| `configinit/moox.toml.example` | 第二份默认配置模板，与仓库根目录的 `moox.toml.example` 同步修改 |
| `doctor/diagnose.go`、`doctor/bootstrap.go` | eventbus 按部署主机定位（D7） |
| `collectorpackager/scf.go`、`command/collector.go` | 删除"可选携带 EventBus CA"及相关校验。SCF 函数已经不连 EventBus，这是死代码 |
| `setup/unitdeploy/README.md`、`setup/unitinstall/README.md` | 按 D1～D3 重写核心阶段、单元部署和材料扩散规则 |

### 4.2 Admin（`modules/admin`）

| 位置 | 改动 |
|---|---|
| `cmd/cli/eventbus_credentials.go` | 新增 `rotate-tls`（D4）；删除 `localEventBusRoles`（D5）；删除标注为"待删除"的 `archive-eventbus` 角色及其令牌与用户项 |
| `internal/service/sysdeploy` | 不改，只补测试：EventBus 不在 Admin 主机时，走登记的对外地址 |

另有 `packages/cloudprovider`：补上删除防火墙规则的能力，轻量应用服务器的防火墙和 CVM 安全组都要（D8）。

### 4.3 其他模块

- **HostAgent**：`internal/app/app.go` 新增独立的采集状态；`internal/app/health.go` 的 `/readyz` 只看采集状态，不再包含 EventBus 连接和发布错误。发布状态仍放在状态接口和指标里（D2）。
- **Monitor**：观测 ingest 不可用期间暂停依赖观测输入的超时判定，恢复后重新计时，并只发一条"观测输入中断"（D7）。涉及 `bootstrap/metrics_runtime.go`、`metrics/catalog.go`、`bootstrap/business_freshness.go`、`bootstrap/host_presence.go`、`bootstrap/schedule_timers.go`、`hostmetrics/silence.go`。默认配置里的 `nats://127.0.0.1:4222` 只是开发默认值，生产由渲染覆盖，可以不动。
- **系统暂停与在途目标**（Strategy、Trade、`packages/sysctl`）：
  - **问题**：系统暂停按"策略 → Trade"的顺序进行，目标权重可能在三个地方被丢：
    - 策略暂停不等 outbox 发完（`strategy/internal/bootstrap/lifecycle.go`）；
    - outbox 一经 EventBus 确认就记为已发送（`strategy/internal/outbox/relay.go:69-85`），Trade 是异步消费的；
    - Trade 暂停后，新收到的目标会因 `system_paused` 被直接终止（TERM，`trade/internal/eventconsumer/target.go:112-114`）。
    
    此外，编排器遇到"已暂停，但在途操作没有结束"只记一条警告，就继续暂停下一个模块（`packages/sysctl/sysctl.go:164-177`）。
  - **改法**：
    - 策略暂停在 outbox 排空之后才算停下；
    - Trade 暂停的第一步是等目标持久消费者的积压清零（策略已经停了，不会再有新目标），之后才关闭接收；
    - 编排器对 Strategy 和 Trade 的"在途操作没有结束"一律按失败处理，停在那里，不再继续暂停上游。
  - 这是暂停与恢复本身的缺陷，不只影响迁移，所以在代码里修，不在操作步骤里绕过去。
- **Storage、Collector、Trade、因子引擎**：代码不改，地址都由部署渲染。

### 4.4 配置与文档

- **配置**：
  - `moox.toml.example`：`[deployments]` 中 eventbus 从 control 移到 storage，注释说明 EventBus 可以部署在任意一台直达主机上；
  - 生产 `moox.toml` 同步修改。
- **`docs/总体设计.md`**：部署表、拓扑图，以及"跨模块异步协作统一走 EventBus"一节里 EventBus 的位置。
- **`docs/部署与运维.md`**：
  - profile 表、核心阶段和全新安装顺序、防火墙、端口表；
  - "control 的 EventBus 端口需要对公网开放"改为 EventBus 主机。
- **`docs/模块/事件总线.md`**：改写"本机服务连接 127.0.0.1，因子机使用公网地址"一句，改成 D5 的规则；补上 `rotate-tls`；删除 `archive-eventbus` 一行。
- **`docs/模块/存储.md`**：第 3 节"Storage 与 EventBus 跨公网往返一次要近 1 秒"那句，改成同机说明。
- **`docs/模块/主机代理.md`、`监控.md`、`命令行工具.md`、`因子.md`**：凡写了"EventBus 在 control"或"引擎 → 控制机 4222"的地方都要改；`主机代理.md` 还要写明新的就绪语义。
- **`skills/moox`**：
  - `SKILL.md`、`references/eventbus-credentials.md`、`native-release.md`、`host-agent.md`、`custom-setup.md`、`private-network.md`、`host-registry.md`、`view-catchup.md` 中关于 EventBus 位置、凭据和防火墙的描述；
  - `scripts/eventbus-credentials.sh` 及其契约测试。
- **编码收尾**：全仓再检索一次 `ControlHost()`、`control.Address`、`4222`、`eventbus`，确认没有遗漏。
- **记忆**：`prod-deploy-procedure` 里"EventBus 删流"一段的路径改到存储机；`eventbus-store-limit-exact` 补一句 store 已是全新的。

## 5. 测试门禁

**单元测试**：

- `go test ./modules/cli/... ./modules/admin/...`
- 新增用例：
  - `EventBusURLFor` 的同机和异机两种情况；
  - `eventbus` profile 只能在部署了 eventbus 的主机上安装，其他 profile 携带服务端材料时被拒绝；
  - `rotate-tls` 之后 `ensure`、`export`、`export-clients` 都能通过，旧 CA 不再被信任；
  - EventBus 还没部署时，HostAgent 照样就绪，`core-ready` 与 `host-ready` 都能拿到；
  - 本机客户端的地址是 127.0.0.1，broker 的 `client_advertise` 仍是公网地址；远端客户端能连上，断开后能重连；
  - 服务端材料只能经 `eventbus` profile 传输；
  - 防火墙对账会删除非 EventBus 主机上的 EventBus 规则；
  - 守护进程的 EventBus 端点检查与告警抑制；
  - Monitor 在 ingest 不可用期间不产生 stale、新鲜度和主机上报告警，恢复后重新计时；
  - 策略暂停在 outbox 排空之后才完成，Trade 暂停在目标积压清零之后才关闭接收，暂停前已发布的目标都能被 Trade 正常接收；
  - 编排器遇到 Strategy 或 Trade 的在途操作没有结束时，停止暂停，不再继续往上游暂停；
  - HostAgent 在 EventBus 不可达时照样就绪，发布错误不影响 `/readyz`；
  - 同一操作重试时，换入另一套服务端材料会被拒绝；
  - `setup firewall --prune` 只删 MooX 那条规则，安全组同时关联 EventBus 主机时不删；
  - Monitor 恢复观测后，不因旧的 `last_seen_at` 立即报 `unreachable`。

**集成门禁**：

- `make test-unit-core`、`make test-unit-core-linux`、`make test-unit-install-linux`。Linux 场景的三主机拓扑要改成"EventBus 在 storage"，并且真实启动 eventbus 单元。
- `./scripts/check/verify-event-contracts.sh`
- `skills/moox/scripts/test-eventbus-credentials-contract.sh`

**本机预演**（迁移前必须完成）：

在 Linux 门禁的隔离网络里按第 6 节完整走一遍：先停写、排空，再用全新 store 启动，然后验证：

- storage-view 不触发任何重建：日志里没有 rebuild 或 reset，View 的水位接着原来的位置推进；
- 各持久消费者都自动创建出来；
- strategy、collector、因子引擎、Trade、Monitor 都能收到新事件；
- **在途目标**：制造"暂停前已经发布、暂停期间才投递到"的目标，确认 Trade 正常接收，没有被 `system_paused` 终止；
- **迁移后新建的数据集和 View**：确认数据从 Storage 正常补齐。
  - 按数据集创建的动态消费者用的是 `DeliverAll`，靠重放流里已有的行（`inventory_reconciler.go:325`）。全新 store 里没有迁移前的事件。
  - 这和现在流保留期（72 小时）以外的历史是同一种情况，理应走从 DataNode 重建的路径，但要实测确认。如果实测需要旧流里的事件，D6 就要重新评估。

## 6. 生产迁移步骤

全程停机。每一步都写明验证方法。

文中的 `moox-cli system pause/resume` 是系统级的暂停与恢复：先追赶，追平后放行。主机上发布目录里的 `pause.sh`、`resume.sh` 只暂停单元里的进程，不是这里要用的。

### 6.1 准备

1. **版本**：`feat/f1-strategy` 合入 main，按第 4 节编码完成，并通过第 5 节的门禁和预演。
   - 先用 `setup build-linux --module storage` 构建 Storage 的 Linux 制品；
   - 再用 `setup release build --storage-binary-dir <目录>` 构建发布制品，记下版本号 `<REL>`。不带这个参数的话，发布里不会有 storage 包（`unitdeploy/release.go:113-126`）；
   - 核对制品清单里有 host、control、eventbus、storage、access、trade、egress-proxy、collector-stream、factor-engine、archive-worker 全部包。
   - 之后的每一次 `deploy-host`、`deploy-unit`、`bootstrap --stage control`、`eventbus-rotate-tls` 都带 `--release <REL>`。操作记录按"主机 + profile + release"固定首次的拓扑和 CA，用新的 release 可以避开旧记录。
2. **基线**：记录 chain、FH、5d158293 的"当期与上一根行"p50/p90，以及 storage-view 的投递滞后 p50/p90（统计口径同前置文档第 2 节）。
3. **收编手工改动**：用新版本渲染出各主机的配置，与主机上现有的配置副本逐个比对，列出全部手工改动。按"合理性第一"，把它们收进仓库模板或 `moox.toml`，不再靠手改维持。已知的有：
   - 因子机 `engine.yaml` 的 `compute.workers: 32`；
   - 长回看所需的 `window.lookback` 与 `compute.max_memory`；
   - 记忆 `swap5m-ema50000-status` 记录的 moox.toml 未同步项。
4. **登记写入方**：除仓库里已知的写入方，再到各主机逐个确认有没有私有的写入任务，例如 cron 里的导入、补数脚本。有的话一一登记，在第 6 步停掉。
5. **现场核验容量**：
   - Storage 重新部署时，会把 current 发布目录里的数据（目前 4.0 GB）复制进候选目录。这是同盘复制，估计几分钟，计入停机窗口；
   - 停机前现场确认存储机磁盘余量至少是 Storage 数据加上 EventBus 流上限（`MOOX_STORAGE` 为 8 GiB）之和的两倍（目前剩 79 GB）。

### 6.2 停写、排空、停机

6. **系统暂停，并停掉所有写入方**：
   - `moox-cli system pause`，带上四个模块的健康端点参数或对应的环境变量。按"策略 → Trade → 因子引擎 → 采集"的顺序逐个暂停。
     - 按 4.3 修复后：策略在 outbox 排空后才算停下；Trade 等目标积压清零后才关闭接收；任何一步没完成，编排器都会停下来报错。出现这种情况时，查清原因后重新执行，不要跳过；
     - Collector 暂停时会等到云端定时器确认停用，SCF 定时采集随之停止。
   - 再停掉暂停管不到的写入方：
     - 控制机的 collector 进程：它的 subject-sync 定时任务只在进程退出时才停，暂停期间仍会写 Storage；
     - 海外机的 collector-stream；
     - 两台主机的 access，作为兜底，挡住所有经 Access 进来的写入；
     - 因子机的 factor-engine、archive-worker；
     - 第 4 步登记的私有写入任务。
7. **排空核对**。生产者全部停下、storage-view 等消费者仍在运行时做最终核对；以下全部满足，并且在至少一个 1m 边界和一个 5m 边界之后仍然满足，才继续：
   - storage-node 的 outbox 为空：`moox_storage_outbox_pending_entries` 为 0，storage-node 的 `/readyz` 里 `outbox_pending_entries` 也为 0；
   - storage-view 的就绪队列为空：`moox_storage_view_ready_queue_pending` 为 0；
   - strategy 的 outbox 为空：strategy 健康快照里待发布数为 0；
   - **持久消费者**：
     - 先从部署和 View 清单列出应有的集合：storage-view 每个有 View 的数据集各一个，加上 strategy、Trade、factor，以及 collector 的 4 个合成消费者；
     - 再用 EventBusMgr 的 `ListConsumers` 翻完所有分页，核对 `MOOX_STORAGE`、`MOOX_COLLECTOR`、`MOOX_TRADE` 上的消费者与应有集合一致，每个的待投递数和待确认数都是 0；
   - **Trade 没有终止过目标**：检查 Trade 的上报和日志，暂停窗口内没有 `system_paused` 或其他终止记录；
   - `MOOX_OBSERVABILITY` 不在排空范围内。各服务一直在上报指标，而指标丢了也没关系。
8. **停机**：
   - **先关守护**：在 Admin 的主机策略里关闭守护（`watchdog.disabled`）。各主机的守护进程每分钟运行一次，会把普通停掉的组件重新拉起（`unitwatch/watch.go:242-285`）。
     - 验证：各主机 `run/watchdog.log` 的最新一行带 `watchdog_disabled`；
     - 不要用单元的 `pause.sh` 代替：它会留下持久的暂停标记，之后部署返回完成时，组件仍处于暂停状态（`unitinstall/deploy.go:214-227`）。
   - **再停组件**：除 Admin 和各主机的 host-gateway 外，所有组件都停掉，包括 storage 三个组件、所有 host-agent 和控制机的 eventbus。
   - Admin 和 host-gateway 必须一直运行：后面的部署登记同步、凭据轮换与导出、单元部署都要经过它们；
   - Admin 作为指标发布方，会反复重连已经停掉的 EventBus，这无害；
### 6.3 切换

9. **改部署登记**：修改 `moox.toml` 的 `[deployments]`，把 eventbus 从 control 移到 storage。
    - 先执行 `setup sync-deployments --host control`，去掉控制机的 eventbus 登记；
    - 再执行 `setup sync-deployments --host storage`，加上存储机的登记。
    - 顺序不能反过来：Admin 的拓扑校验会拒绝同一个单实例组件在两台主机上同时启用。
    - 验证：Admin 的 `ResolveEventBusEndpoint` 解析到存储机，而且恰好只有一个启用的 EventBus 部署。
10. **轮换 TLS**：`setup eventbus-rotate-tls --release <REL>`（用新版本的 Admin CLI，见 D4）。
    - 验证：`ensure`、`export`、`export-clients` 都通过；导出的服务端证书地址包含 146.56.196.204。
11. **防火墙**：`setup firewall`（不带 `--prune`），放开存储机的 4222，控制机上的旧规则这一步不动。云接口按主机实际所在的地域（ap-nanjing）下发。
    - 验证：查询存储机的防火墙或安全组，确认规则已经存在。此时 EventBus 还没启动，做不了握手验证。
12. **部署 EventBus**：`setup deploy-unit --host storage --profile eventbus --release <REL>`。
    - 验证：readyz 为 ready；`ListStreams` 显示所有声明的流都已建出，且都是空的；从控制机、因子机、海外机都能完成到存储机 4222 的 TLS 握手。
13. **按依赖重新部署各单元**，把新的 CA 和地址渲染下去，每次都带 `--release <REL>`：
    1. 各主机的 host 单元：`deploy-host`，依次为 storage、control、overseas、factor；
    2. `deploy-unit storage`；
    3. storage 和 overseas 上的 access；
    4. overseas 上的 trade、egress-proxy、collector-stream；
    5. factor 上的 factor-engine、archive-worker；
    6. `bootstrap --stage control`，完整控制单元。新发布不再含 eventbus。
    - 每一步都确认返回 `<profile>-ready`。HostAgent 的就绪已不依赖 EventBus（D2），但此时 EventBus 已在运行，各主机快照应该都能发出。
14. **恢复**：
    - 在 Admin 的主机策略里重新打开守护，确认各主机 `run/watchdog.log` 恢复正常检查；
    - `moox-cli system resume`，先追赶，追平后放行。
    - 验证：第 7 步列出的持久消费者都已重新建出，并且在消费；第一个边界的 ViewDataReady 正常送达 strategy；storage-view 日志里没有重建。
15. **收尾**：观察至少 12 个周期，第 7 节全部通过之后：
    - 执行 `setup firewall --prune`，删除控制机上的 4222 规则，然后查询控制机的防火墙或安全组确认已删除；
    - 删除控制机旧发布目录里的 eventbus 数据目录。

### 6.4 回退

项目尚未上线，不单独设计回退，也不为回退做备份。新版本支持 EventBus 部署在任意主机，如果要换回控制机，就在 `[deployments]` 里改回去，用一个新的 release ID 把本节流程再走一遍。

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

