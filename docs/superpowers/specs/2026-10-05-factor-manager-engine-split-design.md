# 因子模块拆分设计：管理端（moox-factor-mgr）与计算引擎（moox-factor-engine）

- 日期：2026-10-05
- 状态：已实施（2026-10-05，代码已提交；实施中的偏差见第 15 节）
- 性质：对 [`2026-10-04-factor-dataset-period-pipeline-design.md`](./2026-10-04-factor-dataset-period-pipeline-design.md)（下称「流水线设计」）与 [`2026-10-04-factor-definition-set-membership-design.md`](./2026-10-04-factor-definition-set-membership-design.md)（下称「成员设计」）的**部署形态修订**。流水线语义（周期触发、窗口装配、Python 计算、结果写回、补算分块）不变，只改变「谁在哪个进程里做」。
- 取代：[`2026-10-04-factor-definition-membership-and-multipage-frontend.md`](../plans/2026-10-04-factor-definition-membership-and-multipage-frontend.md) 的阶段 D（单进程 factor 的部署与验收）由本设计配套计划的阶段 F 取代。

## 修订记录

| 版本 | 变更 |
| --- | --- |
| v1 | 初稿 |
| v2 | ① 管理端改名 `moox-factor-mgr`；② FactorEngine 独立端口 11405（tRPC-Go 不支持同端口挂两个服务）；③ 删除 `t_factor_catalog_revision`，改为目录哈希比对；④ 引擎用「分钟级 + 偏移」的定时器拉取目录，并落盘本地；⑤ 管理端不可达时引擎继续用最后快照计算、只告警；⑥ 新启用成员在首次同步前的周期不补，前端启用时提示；⑦ 控制机服务入口为 `:11001`；⑧ 引擎 health 端口改为 11417（11415 已被占用）；⑨ 引擎访问管理端不走本机 HTTP 代理；⑩ 补算任务领取接口命名为 `PullRecalcJob` |

## 1. 背景与问题

当前 `moox-factor` 是一个单体进程，同时承担：

| 职责 | 依赖 |
| --- | --- |
| FactorMgr RPC（因子集、定义、成员、补算任务、状态） | SQLite、Storage Metadata（建结果数据集 / 加列） |
| 结果数据集对账（启动时 + 每 5 分钟） | Storage Metadata |
| EventBus 消费 `CollectorPeriodCompleted`，按因子集分 lane 串行计算 | EventBus、SQLite（查启用的因子集与成员） |
| 实时 / 补算流水线：读窗口 → Python 计算 → 写结果 → 写周期标记 | Storage PrimaryStore、Python 进程池 |
| 补算 worker：轮询 SQLite 中的补算任务，分块执行并回写进度 | SQLite、流水线 |
| 运行状态（消费者、lane、Python 忙闲、最近运行摘要） | 进程内存 |

新的部署方案要求：**管理面留在控制机（与 Admin 同机），计算引擎跑在操作员本机（当前是一台 macOS 开发机）**。本机在 NAT 后面，只能主动向外连接；控制机与 Storage 机都不能反向连到本机。

单体进程无法满足这个形态：

1. 流水线直接读本地 SQLite 拿因子集与成员（`trigger.StoreSetLocator` → `store.EnabledSetByDataset`），补算 worker 直接读写 SQLite 的 `t_factor_recalc_jobs`；
2. 管理操作与计算之间用 `catalog.Locks`（进程内 + `flock` 文件锁）互斥，前提是两者共享同一块磁盘；
3. `GetStatus` / `last_run` 读的是同进程内存里的消费者与运行记录；
4. 引擎访问 Storage 走的是本机 Gateway 的原生 tRPC 端口（`127.0.0.1:11003`），而远端 Gateway 该端口只监听 loopback。

## 2. 目标与非目标

目标：

- 拆成两个进程（同一 Go 模块 `modules/factor`、两个二进制，命名对称）：
  - **`moox-factor-mgr`（管理端）**：FactorMgr RPC、SQLite、结果数据集对账、补算任务登记、引擎状态汇总；部署在控制机，与 Admin 同机；**不消费 EventBus、不跑流水线**；
  - **`moox-factor-engine`（计算引擎）**：EventBus 消费、实时与补算流水线、Python 进程池；**无 SQLite**，因子集 / 成员 / 补算任务从管理端拉取，目录快照落盘本地；部署在本机。
- 引擎**只做出站连接**：向控制机的服务 HTTPS 入口拉取目录与任务、上报心跳与进度；向控制机 EventBus（TLS 公网）订阅；向 Storage 机的 storage-access 公网端口读写数据（与 SCF 采集写入同一条路径）。
- storage-access 放行引擎需要的 Storage 方法，并按调用方区分凭据与方法白名单。
- 前端、strategy、`moox` CLI 对 FactorMgr 的调用方式不变；`GetStatus` 增加引擎在线状态与目录同步状态。

非目标：

- 不支持多个引擎实例并行分摊计算（V1 只允许一个活跃引擎，见 D33）；
- 不改流水线计算语义、结果数据集结构、周期标记协议；
- 不补「成员启用到引擎首次同步之间」的周期（D44）；
- 不兼容旧库：管理端 SQLite 删库重建（项目未上线，不备份、不迁移）。

## 3. 拓扑

```text
                       控制机（Admin 同机）                         Storage 机
                ┌──────────────────────────────────┐       ┌──────────────────────────┐
  浏览器 / CLI ─▶│ Caddy :11001 ─▶ Gateway :11002    │       │ storage-access :11004    │
  strategy     │   │ FactorMgr    → mgr :11403        │       │   ├─ collector（SCF）     │
                │   │ FactorEngine → mgr :11405        │       │   └─ factor-engine（新）  │
                │   ▼                                │       │        │                 │
                │ moox-factor-mgr（SQLite）           │──────▶│ Gateway :11003 ─▶ Storage │
                │   └─ Storage Metadata（本机 Gateway）│       └──────────▲───────────────┘
                │ EventBus :4222（TLS 公网）          │                  │
                └───────▲──────────────▲─────────────┘                  │
                        │HTTPS+HMAC    │TLS+凭据                          │tRPC+HMAC
                ┌───────┴──────────────┴──────────────────────────────────┴───┐
                │ 本机：moox-factor-engine（Python venv，无 SQLite，只出站）         │
                └─────────────────────────────────────────────────────────────┘
```

三条出站链路（2026-10-05 本机实测往返延迟）：

| 链路 | 目标 | 协议与认证 | 用途 | 实测 RTT |
| --- | --- | --- | --- | --- |
| 管理面 | `https://<控制机>:11001`（服务 HTTPS 入口，部署脚本默认 `--service-https-port`） | HTTP + Gateway HMAC（调用方 `factor-engine`）+ Caddy 根证书 | 拉目录、心跳、领取 / 上报补算任务 | 约 9 ms（TLS 握手约 20 ms） |
| 事件 | `tls://<控制机>:4222` | NATS TLS + 角色凭据文件 | 订阅 `CollectorPeriodCompleted` | 约 11 ms |
| 数据 | `ip://<Storage 机>:11004` | tRPC + storage-access 入站 HMAC（调用方 `factor-engine`） | 读源数据窗口、写结果、写 / 查周期标记、读列与标的 | 约 35 ms |

## 4. 已确认决策（编号接成员设计 D16–D28）

- **D29 两个二进制、同一模块、对称命名**：`modules/factor/cmd/mgr` 产出 `moox-factor-mgr`（原 `cmd/server` / `moox-factor` 改名），`cmd/engine` 产出 `moox-factor-engine`；运维 CLI 改名 `moox-factor-mgr-cli`。共享 `domain`、`pipeline`、`pyexec`、`storageio`、`periodclock`、`trigger`、`artifacts`；`store`、`catalog`、`rpc`、`enginehub` 只被管理端链接；引擎不得 import 这些包（加包边界检查）。部署记录 `moox_factor` → `moox_factor_mgr`；网关服务 ID `factormgr` 与 proto 服务名 `FactorMgr` 不变。
- **D30 管理端是唯一事实来源**：因子集、定义、成员、补算任务只存在管理端 SQLite。引擎本地只有「目录快照」的缓存（内存 + `data/engine/catalog.json`）与按 `source_hash` 内容寻址的源码文件，不产生任何需要回传的业务状态之外的数据。
- **D31 引擎拉取、管理端不推送；FactorEngine 独立端口**：引擎在 NAT 后，所有交互都由引擎发起。新增内部 tRPC 服务 `trpc.moox.factor.FactorEngine`，在管理端进程内监听 **`127.0.0.1:11405`**（tRPC-Go 不允许同一端口挂两个服务）。Gateway 种子在 `moox_factor_mgr` 部署记录上加一条 `gateway_routes`（`service_path=trpc.moox.factor.FactorEngine`、`port=11405`），只允许调用方 `factor-engine`，不对浏览器、strategy、CLI 开放。
- **D32 目录哈希 + 定时拉取**（取代 v1 的版本号表）：
  - 管理端收到 `SyncEngineCatalog` 时现场查询 SQLite 组装快照，并计算 `catalog_hash = sha256(规范化 JSON)`。快照内容包括：启用的因子集（引擎用到的全部字段）+ 每个因子集的 `result_ready` + 启用成员的定义字段（含 `source_hash`）与成员状态。不需要额外的表或触发器。
  - 引擎请求带上本地 `catalog_hash`：一致返回 `not_modified`；不一致返回完整快照，源码一并返回（当前 11 个因子的源码合计几十 KB）。
  - 引擎用**独立定时器**驱动同步：按 UTC 对齐，触发时刻为 `floor(now / interval) * interval + offset`，默认 `interval=1m`、`offset=45s`（`0 ≤ offset < interval`），即每分钟第 45 秒拉一次，使变更在下一个周期的事件到达前生效；进程启动时立即同步一次。
  - 引擎收到新快照后依次：① 按 `source_hash` 物化缺失的源码文件；② 原子替换内存快照；③ 写 `data/engine/catalog.json`（先写临时文件再 rename）；④ 因子集列表有变化时刷新消费者过滤。
  - 心跳、补算领取各自独立计时，不与目录同步合并。
- **D33 单活跃引擎租约**：引擎每 `heartbeat_interval`（默认 10 秒）调用 `EngineHeartbeat(engine_id, boot_id, status)`。管理端持有一个引擎租约（`lease_ttl` 默认 45 秒）：租约被另一个 `engine_id` 持有且未过期时返回 `ErrConflict`，引擎停止消费者、`/readyz` 503。**只有收到明确的冲突响应才停**；管理端不可达不视为丢失租约（见 D34）。这样可以防止「本机引擎 + 遗留引擎」同时消费同一个 JetStream durable。
- **D34 管理端不可达时继续计算、只告警**（取代 v1 的「快照过期即重试」）：
  - 目录同步或心跳失败时，引擎继续按最后一次成功同步的快照处理实时事件，记录告警日志并在 `/readyz` 详情中标注 `catalog_sync_failing_since`，但 `ready` 仍为 `true`。
  - 引擎启动时管理端不可达：有本地 `catalog.json` 就加载后直接开始计算；没有则保持未就绪并持续重试。此时 D33 的租约无法校验，接受「管理端宕机期间又误启动第二个引擎」这一极小风险。
  - 用旧快照计算是安全的：新启用的成员不在旧快照里，不会被误算；已停用的成员只是多算若干周期；已删除的因子集写入失败，按 `ErrInfra` 重试，直到恢复同步后 ACK 跳过。
  - 补算任务的领取与进度上报依赖管理端，不可达期间暂停。
- **D35 结果数据集就绪由管理端判定**：对账只在管理端做。快照中每个因子集带 `result_ready`；为 `false`（对账失败或尚未完成）时，引擎对该因子集的事件 `RETRY`，补算任务不发放。这一条取代单体进程中「启动对账成功后才启动消费者」的约束。
- **D36 跨进程文件锁取消，改为快照语义**：
  - 定义源码按 `source_hash` 内容寻址，编辑定义不会改写引擎正在用的文件；
  - 启用成员时，管理端先补齐结果列再翻转状态，且只在 `result_ready` 后进入快照，所以引擎写入的列一定已存在；
  - 停用成员或因子集后，引擎在下一次同步前仍按旧快照计算——可接受；
  - 删除因子集要求先停用（现有规则）；删除与在途写入的竞态表现为写入失败，按 `ErrInfra` 重试，下一次同步后 ACK 跳过；
  - 引擎进程内保留按 `set_id` 的 lane 与进程内 `Locks`，保证同一因子集的实时周期与补算分块串行；`catalog.Locks` 的 `flock` 分支删除。
- **D37 补算任务改为领取 + 租约**：`t_factor_recalc_jobs` 增加 `c_engine_id`、`c_lease_token`、`c_lease_expires_at`。引擎调用 `PullRecalcJob` 原子领取最早的 `accepted` 任务（或租约已过期的 `running` 任务），拿到租约令牌后按现有分块逻辑执行，每块完成调用 `ReportRecalcProgress(job_id, lease_token, progress_time, status, error)`。该调用同时续租，并返回任务当前状态；若已被 `CancelRecalcJob` 取消，引擎在块边界停止。令牌不匹配返回 `ErrConflict`，引擎放弃该任务。
- **D38 补算的成员筛选在管理端完成**：`PullRecalcJob` 响应携带执行所需的全部输入：因子集、**任务所选且仍启用**的成员定义（含源码）、显式标的列表（为空表示由引擎按源数据集读取 `ListDatasetSubjects`）。所选因子已全部停用时，管理端直接把任务置为 `failed`（「not enabled in set」，与现状一致）。
- **D39 运行状态由心跳上报**：`EngineHeartbeat.status` 携带 `consumer_running`、`python_workers`、`python_busy`、`lanes`、`recent_runs`（每个因子集最新一次运行摘要）、`catalog_hash`、`catalog_synced_at`。管理端在内存保存最近一次心跳；`GetStatus` 返回其内容并新增 `engine`（`engine_id`、`boot_id`、`version`、`online`、`last_heartbeat_at`、`catalog_hash`、`catalog_synced_at`、`catalog_in_sync`）。`catalog_in_sync` 由管理端用当前快照哈希与心跳中的哈希比较得出，用于前端提示「引擎目录未同步」。`GetFactorSet.last_run` 从心跳的 `recent_runs` 取。管理端重启后、首次心跳前这些字段为空且 `online=false`。
- **D40 源码静态检查留在管理端**：`CreateFactor` / `UpdateFactor` 的「试 LOAD」（`pyexec.ValidateSource`）在控制机上用 `python.bin`（默认 `python3`）起一次性子进程完成，保持同步报错；管理端不启动 Python 进程池。部署脚本目前不为控制机准备带 pandas / numpy 的解释器（`python-runtime` 目录只是 `moox_pyruntime` 源码包），因此控制机部署前置检查增加 `python3 -c "import pandas, numpy"`，不满足时部署失败并提示安装 `modules/factor/pyworker/runtime-requirements.txt`。
- **D41 storage-access 按调用方区分凭据与方法**：现在 storage-access 只有一组入站凭据（SCF 的 `collector`）和一个全局方法白名单。改为多个主体（principal），每个主体包含：入站 `key_id` / `caller` / 密钥文件、上游 Gateway 凭据（`key_id` / `caller` / 密钥文件）、方法白名单。新增主体 `factor-engine`：入站调用方 `factor-engine`，上游以调用方 `factor` 访问 Storage Gateway（现有 Gateway 种子已允许 `factor` 调用下表方法，Storage 路由不用改），方法白名单：

  | Service | 方法 | 现在是否在 storage-access 白名单 |
  | --- | --- | --- |
  | `trpc.moox.storage.PrimaryStore` | `ReadTimeSeriesRows` | 是 |
  | | `WriteFactorRows` | **否，新增** |
  | | `ReportFactorPeriodComputed` | **否，新增** |
  | | `GetFactorPeriodComputed` | **否，新增** |
  | `trpc.moox.storage.Metadata` | `GetDataset`、`ListDatasetColumns`、`ListDatasetSubjects` | 是 |

  `collector` 主体保持现有方法集合不变。`DeleteDatasetRows`、`RestoreDatasetRows`、`CreateDataset`、`UpsertDatasetColumn`、`ActivateDataset`、`DeleteDataset` 只有管理端使用，走控制机本地 Gateway，**不**进 storage-access 白名单。
- **D42 引擎配置**：`storage.gateway_target` 指向 `ip://<Storage 机>:11004`，`gateway_node_id` 填 storage-access 的入站目标节点 ID；新增 `manager` 段（`url`、`node_id`、`ca_file`、`key_id`、`hmac_key_file`）与 `catalog_sync` 段（`interval`、`offset`、`state_file`）；`eventbus` 段指向控制机 TLS 地址与角色凭据。引擎配置**没有** `database` 段。引擎访问管理端的 HTTP 客户端**显式禁用环境代理**（`Transport.Proxy = nil`）：操作员本机常设 `HTTPS_PROXY`（本机实测为 `127.0.0.1:7897`），走代理会改变出口与证书链。
- **D43 部署归属**：控制机部署配置（`--profile control`）包含 `moox-factor-mgr`；引擎用独立的轻量部署脚本安装到本机目录（默认 `~/moox/factor-engine`；macOS 不能放在 `~/Documents` 等受隐私保护的目录，见 §15 #12），macOS 用 `launchd`、Linux 用 systemd user unit 保活。`deploy-moox.sh` 不负责引擎。
- **D44 启用成员的首次同步缺口不补**：成员启用时生成的回填任务覆盖到「启用时刻向下对齐的周期」；引擎要到下一次目录同步（最长约 1 个 `interval`）后才开始实时计算该成员，中间约 1～2 个周期没有结果。个人量化系统接受这一缺口，不做自动补算；前端在启用成员（以及启用因子集）的确认框中提示「计算引擎约 1 分钟内生效，期间的周期不会自动补算，如需补齐可手动提交补算」。

## 5. 进程职责划分

| 能力 | 现位置 | 管理端 `moox-factor-mgr` | 引擎 `moox-factor-engine` |
| --- | --- | --- | --- |
| FactorMgr RPC（19 个方法，:11403 tRPC / :11404 HTTP） | 单体 | ✔ | |
| FactorEngine RPC（4 个方法，:11405 tRPC） | — | ✔（服务端） | ✔（客户端，经 Gateway） |
| SQLite（`t_factor_sets` / `defs` / `set_members` / `recalc_jobs`） | 单体 | ✔ | |
| 结果数据集对账、加列、启用回填窗口计算 | 单体 | ✔ | |
| 源码静态检查（试 LOAD） | 单体 | ✔（一次性子进程） | |
| 目录快照组装与哈希 | — | ✔ | |
| 目录定时同步、本地落盘、源码文件物化 | 单体（物化） | | ✔ |
| EventBus 消费与 lane | 单体 | | ✔ |
| 实时流水线 / 补算分块执行 | 单体 | | ✔ |
| Python 进程池 | 单体 | | ✔ |
| 补算任务登记、取消、查询 | 单体 | ✔ | |
| 补算任务领取、进度上报 | — | ✔（服务端） | ✔（客户端） |
| 运行摘要 / lane / Python 状态 | 单体内存 | ✔（来自心跳） | ✔（产生） |
| health `/readyz` | 单体 :11414 | ✔ :11414（SQLite + Storage Metadata） | ✔ :11417（Python + EventBus + 快照已加载 + 无租约冲突） |

端口：管理端沿用 11403 / 11404 / 11414 / admin 11944 / metrics 12944，新增 11405；引擎 health 11417、admin 11945、metrics 12945（均已核对仓库内无占用）。

## 6. 协议

### 6.1 新服务 `FactorEngine`（`proto/factor.proto`）

```protobuf
service FactorEngine {
  rpc SyncEngineCatalog(SyncEngineCatalogReq) returns (SyncEngineCatalogRsp);
  rpc EngineHeartbeat(EngineHeartbeatReq) returns (EngineHeartbeatRsp);
  rpc PullRecalcJob(PullRecalcJobReq) returns (PullRecalcJobRsp);
  rpc ReportRecalcProgress(ReportRecalcProgressReq) returns (ReportRecalcProgressRsp);
}

message EngineIdentity {
  string engine_id = 1;   // 稳定 ID，如 factor-engine@<hostname>
  string boot_id = 2;     // 每次进程启动生成
  string version = 3;
}

message EngineSet {
  FactorSet factor_set = 1;
  repeated FactorDef factors = 2;   // 仅 enabled 成员，带 source_code
  bool result_ready = 3;            // D35
}

message SyncEngineCatalogReq {
  EngineIdentity engine = 1;
  string known_hash = 2;
}
message SyncEngineCatalogRsp {
  common.RetInfo ret_info = 1;
  string catalog_hash = 2;
  bool not_modified = 3;
  repeated EngineSet sets = 4;      // 仅 enabled 因子集；not_modified 时为空
}

message EngineRuntimeStatus {
  bool consumer_running = 1;
  int32 python_workers = 2;
  int32 python_busy = 3;
  repeated FactorLaneStatus lanes = 4;
  repeated SetRunSummary recent_runs = 5;
  string catalog_hash = 6;
  string catalog_synced_at = 7;     // RFC3339，最后一次成功同步
}
message EngineHeartbeatReq {
  EngineIdentity engine = 1;
  EngineRuntimeStatus status = 2;
}
message EngineHeartbeatRsp {
  common.RetInfo ret_info = 1;
  int64 lease_ttl_seconds = 2;
}

message PullRecalcJobReq { EngineIdentity engine = 1; }
message PullRecalcJobRsp {
  common.RetInfo ret_info = 1;
  bool found = 2;
  RecalcJob job = 3;
  string lease_token = 4;
  EngineSet set = 5;                // 已按任务的 factor_ids 过滤（D38）
}

message ReportRecalcProgressReq {
  EngineIdentity engine = 1;
  string job_id = 2;
  string lease_token = 3;
  string progress_time = 4;         // RFC3339，与 RecalcJob 一致
  string status = 5;                // running / succeeded / failed
  string error = 6;                 // 失败原因或降级说明
}
message ReportRecalcProgressRsp {
  common.RetInfo ret_info = 1;
  string job_status = 2;            // 管理端当前状态；cancelled 时引擎在块边界停止
}
```

**哈希规范化**：因子集按 `set_id` 排序、成员按 `factor_id` 排序，列表字段保持语义顺序（`input_columns` / `outputs` 不排序），时间戳字段（`created_at` / `updated_at`）**不参与**哈希，避免无语义变化引起的重同步。

### 6.2 FactorMgr 变更

- `GetStatusRsp` 增加 `EngineInfo engine = 7`（字段见 D39）。字段 2–6 含义不变，来源改为最近一次心跳。
- `RecalcJob` 增加只读字段 `engine_id = 13`，供前端补算 Tab 显示「执行引擎」。
- 其余 RPC 签名不变。

### 6.3 Gateway 种子

- 部署记录 `moox_factor` 改名 `moox_factor_mgr`，描述改为「因子管理服务」；`gateway_service_id` 仍为 `factormgr`，`routes.go` 中 `factormgr → 11403` 的基准端口映射不变。
- 新增 `gateway_routes` 条目：`service_path = trpc.moox.factor.FactorEngine`、`port = 11405`、4 个方法、`gateway_callers = ["factor-engine"]`。
- 新增 Gateway 服务密钥 `factor-engine`（调用方 `factor-engine`），按现有服务密钥的生成与下发流程放进控制机 secrets；操作员把同一把密钥拷到本机 `secrets/`。
- Storage Gateway 路由不变（上游调用方 `factor` 已有所需权限）。

## 7. 引擎内部结构

```text
cmd/engine/main.go
internal/engine/
  config.go        // Config：manager / catalog_sync / storage / eventbus / python / pipeline / recalc / engine
  managerclient.go // FactorEngine 客户端（gatewayauth.NewHTTPClient + HMAC，禁用环境代理）
  catalog.go       // 快照缓存：hash、sets、synced_at；本地落盘与加载；实现 trigger.SetLocator
  synctimer.go     // 分钟级 + 偏移的对齐定时器
  heartbeat.go     // 心跳循环；收到租约冲突时停止消费者、置未就绪
  recalc.go        // 领取循环：Pull → 分块执行（复用 recalc.Executor）→ Report
  runtime.go       // 装配：Python 池、Runner、消费者、health；运行记录（原 bootstrap/run_tracker.go）
```

- `catalog.go` 实现现有的 `trigger.SetLocator` 接口（`EnabledSetByDataset`、`FilterSubjects`），`eventconsumer.Handler` 与 lane 逻辑不改。因子集 `result_ready=false` 时返回带 `storageio.ErrInfra` 的错误，handler 现有逻辑会把它翻译为 `RETRY`。快照陈旧不影响查询结果（D34）。
- 源码物化使用共享包 `internal/artifacts`（由 `catalog/artifacts.go` 移入）。
- 补算分块逻辑从 `recalc.Worker` 抽出为不依赖 `store` 的 `recalc.Executor`：输入「因子集 + 定义 + 标的 + 时间范围 + 续跑起点 + 进度回调」，输出 `pipeline.Outcome`。管理端 `recalc.Service` 只保留 Submit / PrepareEnableBackfill / Get / List / Cancel。
- `run-once` 子命令移到引擎二进制（`moox-factor-engine run-once`），用本地快照（必要时先同步一次）做单周期执行；`moox-factor-mgr-cli` 保留 `init`、`import`、`import-catalog`、`status`，`recalc` 只提交任务。

## 8. 数据模型（管理端 SQLite）

删库重建，不写迁移。

- `t_factor_recalc_jobs` 增加：`c_engine_id TEXT NOT NULL DEFAULT ''`、`c_lease_token TEXT NOT NULL DEFAULT ''`、`c_lease_expires_at INTEGER NOT NULL DEFAULT 0`；新增索引 `idx_t_factor_recalc_jobs_pull (c_status, c_lease_expires_at)`。
- 不新增目录版本表（v2 删除）；目录哈希每次同步时现场计算。
- 引擎租约与最近心跳**只放内存**：管理端重启后由下一次心跳重建；管理端启动后的第一个 `lease_ttl` 内只接受首个心跳的 `engine_id`，防止重启窗口内被另一个引擎抢占。

## 9. 失败语义

| 场景 | 引擎行为 | 对用户可见 |
| --- | --- | --- |
| 管理端不可达（任意时长） | 用最后快照继续实时计算；同步 / 心跳失败记告警；补算暂停 | 管理端宕机期间前端不可用；恢复后 `GetStatus` 显示目录是否已追平 |
| 引擎启动时管理端不可达 | 有 `catalog.json` 则加载后计算，否则未就绪并重试 | 同上 |
| 租约被其他引擎持有（明确冲突响应） | 停止消费者，`/readyz` 503，持续重试心跳 | `GetStatus.engine` 显示持有租约的引擎 |
| 因子集 `result_ready=false` | 该因子集事件 `RETRY`，补算不发放 | 计算任务页显示对账失败原因（现有） |
| storage-access 不可达 / 超时 | 读写失败归为 `ErrInfra` → `RETRY`；补算块按现有重试后失败 | 补算任务 `failed`，可重新提交 |
| EventBus 断连 | NATS 客户端重连；`consumer_running=false` 进入心跳 | 总览页显示消费者停止 |
| 引擎崩溃或重启 | 未 ACK 的事件由 JetStream 重投；补算任务租约过期后被重新领取，从 `progress_time` 续跑 | 补算进度不回退 |
| 成员刚启用、引擎尚未同步 | 该成员不参与计算（D44） | 启用确认框已提示；引擎同步后总览页「目录已同步」 |

## 10. 安全

- 引擎持有三类凭据：`factor-engine` Gateway 服务密钥（只能调 `FactorEngine` 的 4 个方法）、EventBus 角色凭据（只订阅因子消费者所需 subject）、storage-access `factor-engine` 入站密钥（只能调 D41 的 7 个方法）。三者都放本机 `secrets/`，权限 0600，不入库。
- storage-access 的 tRPC 是明文 TCP + HMAC（与 SCF 相同）：HMAC 保证完整性与身份，不保证机密性。行情与因子结果不是敏感数据，接受。
- `ReportRecalcProgress` 必须校验 `lease_token`，防止旧引擎在租约过期后覆盖进度。

## 11. 性能与容量

延迟已接受（评审意见），以实测为准：

- 估算：1m 因子集约 400 个标的，按 `read_batch_subjects=100`、`read_workers=4` 分 4 批并发读取，约 1～2 个 35 ms 往返加传输；写入按 `write_batch_rows=1000` 一次调用。单周期网络开销预计在亚秒到 1～2 秒，远低于 `period_budget_min`（60 秒）。
- 补算：`chunk_periods=2000` 的单批读取在公网上可能接近 storage-access 的 30 秒超时；引擎默认 `recalc.chunk_periods` 先定为 500，实测后再调。
- 阶段 F 记录 30 个周期的分阶段耗时与一次完整回填的分块耗时，作为调参依据。

## 12. 实现影响（文件级，实施前对照代码核对）

| 范围 | 文件 | 变更 |
| --- | --- | --- |
| 改名 | `modules/factor/cmd/server` → `cmd/mgr`；`scripts/build/build.sh`、`scripts/deploy/deploy-moox.sh`、`scripts/runtime/`、`config/setup/service-deployments.yaml`、`modules/admin/internal/service/sysdeploy/`、`modules/cli`、monitor 配置、`skills/`、`docs/` | `moox-factor` → `moox-factor-mgr`，`moox_factor` → `moox_factor_mgr`，`WITH_FACTOR` → `WITH_FACTOR_MGR`，包内目录 `factor/` → `factor-mgr/`（约 72 个文件引用） |
| proto | `modules/factor/proto/factor.proto`、`proto/factorgen/` | 新服务与消息、`GetStatusRsp.engine`、`RecalcJob.engine_id` |
| 管理端 | `schema/factor.sql`、`internal/store/recalc_jobs.go` 及新 `engine_jobs.go` | 租约列、领取 / 续租 / 进度 |
| 管理端 | `internal/catalog/{service,locks,reconcile}.go` | 删 `flock`；对账结果回写 `result_ready`；不再持有 `Artifacts` |
| 管理端 | 新 `internal/enginehub/` | 租约、心跳缓存、快照组装与哈希 |
| 管理端 | `internal/rpc/{service,convert}.go`、新 `engine_service.go` | `GetStatus` 改读 enginehub；`FactorEngine` 服务 |
| 管理端 | `internal/bootstrap/{bootstrap,config,health}.go`、`config/{app,trpc_go}.yaml` | 去掉消费者、Python 池、recalc worker、run tracker、`eventbus` / `pipeline` 段；新增 11405 服务 |
| 共享 | `internal/catalog/artifacts.go` → `internal/artifacts/` | 移包 |
| 共享 | `internal/recalc/{service,worker}.go`、新 `executor.go` | 拆出无 store 的 `Executor` |
| 引擎 | 新 `cmd/engine/`、`internal/engine/`、`config/engine.yaml`、`config/trpc_go.engine.yaml` | 见 §7 |
| storage | `modules/storage/internal/accessproxy/`、`modules/storage/cmd/access/main.go`、新 `config/access/principals.example.yaml` | 多主体 + 按主体白名单 |
| admin | `modules/admin/internal/service/sysdeploy/defaults.go`（及测试、`period_gateway_contract_test.go`）、`config/setup/service-deployments.yaml` | 改名、`FactorEngine` 路由、`factor-engine` 调用方 |
| 部署 | `scripts/deploy/deploy-moox.sh`、新 `scripts/deploy/deploy-factor-engine.sh`、新 `deploy/launchd/`、新 `deploy/systemd/user/` | control profile 含管理端；引擎独立安装；storage-access 主体配置下发 |
| 契约 | `scripts/check/check-module-boundaries.sh` 或新检查 | 引擎与管理端的依赖边界 |
| web | `src/api/factor/types.ts`、总览页、计算任务页（启用确认框）、补算 Tab | 引擎状态卡、目录同步提示、启用提示（D44）、`engine_id` 列 |
| 文档 | `modules/factor/README.md`、`docs/因子计算模块设计.md`、`docs/存储服务架构与部署.md`、新 `docs/ops/factor-engine.md` | 同步 |

## 13. 取舍与风险

| 风险 / 取舍 | 处理 |
| --- | --- |
| 公网读窗口延迟 | 已接受；阶段 F 实测并调参 |
| 管理端宕机期间无法改目录、补算暂停 | 实时计算不受影响（D34） |
| 管理端宕机期间租约无法校验 | 接受误启动第二个引擎的极小风险；恢复后由租约冲突兜底 |
| 新启用成员首个同步周期前无结果 | 接受，前端提示（D44） |
| 单活跃引擎是单点 | V1 接受；引擎状态只有可重建的缓存，换机器只需拷凭据并启动 |
| storage-access 明文 tRPC | 与 SCF 一致，仅 HMAC 保护完整性；结果数据非敏感 |
| 改名波及约 72 个文件 | 独立 Task、纯改名提交，不与功能改动混合 |

## 14. 测试

- 管理端：快照哈希稳定性（相同内容同哈希、时间戳变化不影响、成员状态 / 源码 / 因子集字段 / `result_ready` 变化必变）；领取原子性（并发 Pull 只有一个成功）；租约过期重新领取；错误令牌拒绝；取消在下一次进度上报时生效；心跳超时判定 `online=false`；第二个 `engine_id` 在租约内被拒；`catalog_in_sync` 判定。
- 引擎：对齐定时器的下一次触发时刻（边界：正好在偏移点、跨分钟、offset=0）；`not_modified` 不重建；新快照物化源码、原子替换、落盘；启动时管理端不可达且有本地快照 → 就绪并计算；无本地快照 → 未就绪；同步失败持续时仍正常计算；`result_ready=false` → `RETRY`；快照中删除因子集 → ACK 跳过；补算在 `cancelled` 响应后停在块边界；租约冲突 → 消费者停止、`/readyz` 503；HTTP 客户端在设置 `HTTPS_PROXY` 时仍直连。
- storage-access：`factor-engine` 主体可调 7 个方法，调 `DeleteDatasetRows` 被拒；`collector` 主体行为不变；未知 `key_id` 被拒。
- 契约：`FactorEngine` 4 个方法只出现在 `factor-engine` 调用方路由；引擎二进制依赖图中没有 `internal/store`。
- 端到端（阶段 F）：本机引擎 + 控制机管理端 + Storage 机 storage-access；导入 11 个定义并启用，确认回填由引擎领取完成、实时周期产出 `FactorPeriodComputed`、前端总览显示引擎在线且目录已同步；停管理端期间实时计算不中断。

## 15. 实施偏差（2026-10-05）

| # | 设计 | 实际实现 | 原因 |
| --- | --- | --- | --- |
| 1 | FactorEngine 在 11405 以 tRPC 协议监听 | 以 **http** 协议监听 | 引擎只能走控制机的 HTTPS 入口（Caddy `:11001` → Gateway `:11002`），Gateway 的 HTTP 入口 `POST /api/service/factormgr/<Method>` 以 HTTP 转发到后端 |
| 2 | 取消 `catalog.Locks` 的 `flock` 文件锁（D36） | 文件锁**保留**，`Locks` 移到共享包 `internal/setlock`；引擎只用进程内锁 | `moox-factor-mgr-cli` 在控制机上直接写管理端 SQLite，仍需与运行中的管理端互斥；"不共享磁盘"只对引擎成立 |
| 3 | 补算分块逻辑拆为 `recalc.Executor` | 独立包 `internal/recalcexec`；`recalc` 只剩管理端的任务受理（Submit / PrepareEnableBackfill / Get / List / Cancel） | `recalc` 依赖 SQLite store，引擎若 import 会链入 store，违反依赖边界 |
| 4 | 管理端启动后第一个 `lease_ttl` 内只接受首个心跳的 `engine_id`（§8） | 不设启动宽限期，只按租约规则：租约空闲时接受第一个心跳，其后另一个 `engine_id` 在租约有效期内被拒 | 租约规则本身已保证同一时刻只有一个引擎消费；宽限期没有额外收益 |
| 5 | 引擎持有三类凭据（§10） | **四类**：另加 `storage-primary-auth.secret` | Storage 主存储会校验请求体中 AuthInfo 的 AppKey（AppId `moox-factor` 的 HMAC），引擎必须持有该密钥 |
| 6 | — | 删除 `trigger.StoreSetLocator`、`Store.EnabledSetByDataset`、catalog 的 `Notifier` 与源码物化 | 拆分后管理端不再消费事件、不执行因子，这些代码没有调用方 |
| 7 | 补算进度上报失败的处理未定义 | 上报失败（网络、管理端不可达）时引擎**放弃**该任务而不标记失败；任务租约过期后被重新领取并从 `progress_time` 续跑 | 管理端短暂不可达不应让补算失败 |
| 8 | storage-access 方法白名单可在主体配置里写 | 方法集合是代码内置的预设（`collector`、`factor-engine`），配置只能引用预设名 | 配置无法放宽白名单，便于审查 |
| 9 | 引擎 `GatewayNodeID`、`ca_file` 等由部署脚本渲染 | 由 `scripts/deploy/deploy-factor-engine.sh` 渲染；`factor-engine` 网关密钥与其他服务密钥一样由 gateway service secret 派生，每次发布不变 | 引擎机只需拷贝一次密钥 |
| 10 | — | 已知限制：补算任务领取时固定了所选成员，执行中途停用某成员，该成员的结果列仍会写到任务结束 | 与「停用后多算若干周期」同类，可接受；需要立即生效时取消任务后重新提交 |
| 11 | 管理端租约只看 `engine_id` | 同时比较 `boot_id`；只有持有引擎租约的引擎能上报补算进度，引擎失去租约后在块边界停止 | 代码审查发现：复制配置的第二个进程、断线后被接管的旧引擎都不能继续写入 |
| 12 | macOS 默认安装到 `~/Documents/moox-deploy`（D43） | 默认 `~/moox/factor-engine` | 阶段 F 实测：`~/Documents` 受 macOS 隐私保护，每次重编译的 ad-hoc 签名二进制被视为新程序，launchd 启动时 dyld 卡在打开可执行文件处，等待一个后台进程看不到的授权 |
| 13 | 引擎启动先同步目录再发心跳 | 先发心跳、再首次同步目录 | 阶段 F 演练：第二个引擎在得知租约冲突前，目录同步的回调先启动了事件消费（约 13 ms） |
