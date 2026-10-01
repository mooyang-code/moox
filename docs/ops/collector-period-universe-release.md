# Collector 周期标的池协调发布手册

## 状态与边界

本手册是执行计划 Task 9 的发布准备文件，不是已发布证明。Task 1 至 Task 8 的实现、编码后独立审查和本地真实进程 E2E 已完成；2026-10-01 增量 Storage 改动的四包 fresh/race 与 period E2E 复跑通过，`env GOARCH=arm64 CGO_ENABLED=1 make verify-pr` 及 compile host 上针对提交 `51209c7a` 的 `moox-cli setup build-linux --module storage` 均通过。周期 E2E 检查真实 Pebble marker/outbox 入队和 payload，但不启动生产 Storage Relay 并等待该 marker 的 JetStream subscriber ACK；独立审查将这项列为非阻断 P3 证据缺口。提交 `51209c7a` 已推送至实施分支。正式环境尚未发布，真实 1m/1h 线上周期也尚未验收。不得把本地 build/E2E 记为生产证据。

**2026-10-01 只读运行态检查：** 用户要求按全新项目处理，不实现旧版本数据结构或协议兼容；这不代表正式环境为空，也不自动授权删除现存业务数据。`setup host-diagnostics` 确认 Control 上 `moox-collector` 与 `moox-collector-subject` 正在运行，`/data/moox/prod/data/collector` 约 8.1 GiB；Storage 主机上的 DataNode、Primary、View 正在运行，`/data/moox/storage/data/storage-node/pebble` 约 450 MiB，View indexes 约 367 MiB。Storage 近期日志已有 crypto 与多个 stockcn K 线 Dataset 的历史索引 backfill 记录。因此正式切换必须先确认当前任务、批次、失败回执与 waiting 周期的运行态，并将任何 ledger 重建和保留对象明确分开；不能把“无需兼容历史”推导成全量清库。该检查未修改生产状态。

同日尝试现有 opt-in SQLite 诊断测试：`TestLiveCollectorE2EDiagnostic` 在 30 秒预算内超时，`TestLiveSchedulerDBDiagnostic` 在 90 秒预算内超时，未获得可用的周期级盘点结果。运行态 inventory 仍未完成；不要把目录容量、进程存活或本地状态当作排空证明。

用户已授权编译、正式发布与线上端到端验证；公开 EventBus CA 按计划打包到 `certs/eventbus-ca.pem`。但 Collector 运行态 Schema 与 Pebble snapshot JSON 名称切换不兼容，period-only 清理/重建须先有真实只读 inventory、明确 Space/Dataset/period 时间范围、可验证备份及保留对象，再取得单独确认。当前仓库没有经验证的有界线上迁移/清理工具，因此在这项前置条件解决前不执行生产部署、不修改生产配置或数据。不能自动迁移、双读旧 Pebble snapshot JSON 或让旧 Collector 与新 Storage 混跑。

执行授权前的切换记录至少固定：目标 Space；受影响 Collector SQLite 文件与 schema/table；Storage node 与 Pebble period key 范围；`dataset_id/frequency/period_time` 上下界；任务定义、结果 Dataset/View 引用及已有行情行数/范围；备份对象和 SHA-256、恢复演练结果；获准重建/删除的精确对象；保留对象；操作者和确认记录。当前尚未取得生产 inventory，以上值不得臆造或以默认全量范围替代。

命令中的 `$MANIFEST` 为受信发布配置的绝对路径，`$VERSION`、`$GIT_COMMIT` 为审查通过的唯一发布版本和完整提交，`$HOST`、`$SERVICE`、`$DEPLOY_DIR` 来自已确认的目标 deployment，`$PACKAGE` 为已校验的无凭据 ZIP。执行前在发布记录中固定这些非敏感值和 ZIP SHA-256，不在日志中输出配置内容、token、app key 或私钥。

## 发布前门禁

- 执行计划 Task 1 至 Task 8 全部完成，并记录实现、RED/GREEN、单测、race、真实进程 E2E 和提交。
- Task 9 的六个模块全量测试、Workspace/boundary/greenfield、Gateway/SCF package、架构文档、所有空库 Schema 和 `proto-check` 全部通过。
- 新起 Storage、Collector 两个 `codeCR` Agent，等待两者完成，主 Agent 独立核验并关闭全部阻断发现。
- 正式发布 inventory 明确主机角色、Space、Dataset、Collector DB、Pebble period key 前缀、SCF account/region/namespace/node、原 Task enabled 和原 Timer cron/enabled；没有云账户全局删除操作。
- 保存受影响 deployment 与 Dataset metadata 的完整原始对象、上一版服务 ZIP/SCF 包和摘要；正式凭据可用且轮换状态已核验。
- 准备匹配版本的 Storage Primary/DataNode、Collector server/subject、Gateway、SCF market_data，以及四个 period RPC 和独立 Claim runtime route。

任何缺失项都保持未完成，不以本地 build、进程 `/readyz` 或已等候一段时间替代。

## 停止新工作并排空

通过既有认证 Admin 会话调用 API，不将凭据放入命令行。逐 Space 分页读取全部 enabled Task，保存完整 `GetTaskDetail` 对象后禁用；不得删除任务或结果 Dataset。

```text
POST /api/admin/collectmgr/GetTaskList
{"space_id":"已确认Space","enabled":true,"page":{"page":1,"size":1000}}

POST /api/admin/collectmgr/GetTaskDetail
{"space_id":"已确认Space","task_id":"已导出TaskID"}

POST /api/admin/collectmgr/DisableTask
{"space_id":"已确认Space","task_id":"已导出TaskID"}
```

分页以实际 `has_more` 为准。CloudNode 的 Space 来自受认证 Admin 会话的 `X-Space-Id` 上下文，不是 `GetNodeListReq` 字段；每次查询及批量更新都固定同一 Space，并逐个覆盖 inventory 中的 account/region/namespace。先分页查询目标市场 Timer fleet，保存每个节点的完整对象和目录期望的 `timer_enabled`、`timer_cron`：

```text
POST /api/admin/cloudnode/GetNodeList
X-Space-Id: 已确认Space
{"cloud_account_id":"已确认AccountID","namespace":"已确认Namespace","region":"已确认Region","node_type":"scf-event","biz_type":"market_fetcher","trigger_type":"timer","page":{"page":1,"size":1000}}
```

上述 `biz_type=market_fetcher` 查询只返回目录，不触发云侧 Timer readback。必须再逐节点查询，省略 `biz_type`，保留准确 NodeID 和其它范围限制；为实际服务端查询提供不少于一分钟的预算。普通批量查询一次最多刷新 4 个 Timer，且有 5 分钟 readback 缓存，不能把一次 fleet listing 当成全量最新云侧状态：

```text
POST /api/admin/cloudnode/GetNodeList
X-Space-Id: 已确认Space
{"node_id":"已导出NodeID","cloud_account_id":"已确认AccountID","namespace":"已确认Namespace","region":"已确认Region","node_type":"scf-event","trigger_type":"timer","page":{"page":1,"size":1}}
```

保存每个节点云侧实际的 `timer_actual_enabled`、`timer_actual_cron` 和 `timer_last_readback_at`；要求实际字段存在、`timer_status_error` 为空、`timer_available_status` 不是 `Unknown`/`Missing`，并有本次盘点开始后的新鲜 readback 证据。若 Admin 转发预算不足、缓存未刷新或字段不完整，则用既有受信云 API/控制台逐节点取得等价证据，不编造强制刷新参数。目录期望与云侧实际不一致时先停止并核对漂移，不能直接按目录状态关闭或恢复。

仅针对该 inventory，以每批最多 100 个节点关闭 Timer，cron 使用已核对的实际值，并保存返回 job ID。提交与查询仍携带同一受认证 Space 上下文：

```text
POST /api/admin/cloudnode/SubmitUpdateNodeRuntimeConfigs
{"nodes":[{"node_id":"已导出NodeID","timer_enabled":false,"timer_cron":"原始cron"}]}

POST /api/admin/cloudnode/GetNodeBatchChange
{"job_id":"提交返回的job_id"}
```

必须轮询同一 job 至终态并核验云侧触发器确已禁用。观察超时不代表任务停止，不能因此重新提交或恢复 Timer。关闭新 planning 后仍保留旧进程的 in-flight、retry、failure reporter 和 Storage finalizer，使其收尾。

仓库没有公开的非破坏性批量 drain API。发布者须通过受信、只读的运行态检查列出实际批次、重试、pending failure 回执和 Storage waiting 周期数量及身份；查询方式、DB 路径与范围要先核验。没有这些证据则停止切换。`DisableTask` 不会取消已经 Invoke 的 SCF，`/readyz` 和服务包 healthcheck 也不证明 drain。

禁止使用 `collector task purge`、删除 Task、删除 Dataset/行情行或 `reset-view-consumers` 代替排空。

## Schema 与 Pebble 切换

旧 Collector Schema 和 Pebble snapshot JSON 名称不能自动兼容。即使已排空，也须在发布记录中明确受影响运行态表与 period key 范围、备份校验结果、保留的任务定义和结果引用，以及保留全部已有物理行情数据的处理步骤。该范围只能根据经批准的生产只读 inventory 填入，至少限定 Space、Dataset 和 `period_time` 边界；执行前核对备份可恢复，并证明不会删除任务、结果 Dataset/View 引用或行情行。

Storage 已新增只读 `moox-storage-cli period-inventory`，只在 DataNode 已停止或一致离线副本上运行；Pebble 数据库锁会使它在 DataNode 正运行时 fail closed。每次必须指定预期 DataNode ID、Pebble path、一个 Space、Dataset、frequency 和闭区间 `period_time`；命令校验 DataNode layout 与 Pebble 中持久化的 node/store identity，并在 JSON 中报告绝对 path、layout 版本、node/store ID 和扫描计数。它输出该范围的周期身份、状态、deadline、成功/失败位图计数、快照格式/规模及 secondary index、marker record、outbox 关联证据；不导出行情行或 Subject 明细、不改写数据库。为了核验没有遗漏的 orphan/duplicate links，命令会流式扫描全库 period indexes、marker records 与 outbox，范围只限制结果集而不限制这部分 I/O；因此只允许离线执行。周期报告最多 100,000 条，超过输出上限、状态损坏、索引孤儿、旧 roster 格式或 marker 关系不一致都会使命令以非零退出；事件校验错误只显示脱敏摘要，不回显 Subject 值。报告只用于定位和审批，不是清理许可。

```bash
./bin/moox-storage-cli period-inventory \
  --path "$PEBBLE_DB" \
  --node-id "$DATA_NODE_ID" \
  --space "$SPACE_ID" --dataset "$DATASET_ID" --frequency "$FREQUENCY" \
  --period-time-min "$PERIOD_TIME_MIN" --period-time-max "$PERIOD_TIME_MAX" \
  > "$INVENTORY_JSON"
```

在完整收集 Collector SQLite 的运行态并确认 producer 已停止后，若确需检查 Pebble，先保存并校验可恢复备份，再停止对应 DataNode；不要在在线 Pebble 目录上复制文件以冒充一致快照。用该只读命令盘点审批候选范围，保存 stdout 和退出码；非零报告必须先逐项解释，不能作为零风险证据。完成盘点后，若尚未获得确切 period ledger 重建授权，原样重启原 Storage 版本并恢复服务，不执行清理。任何有界重建工具仍须提供范围断言、备份校验、dry-run 差异和任务/结果引用/物理行情行保留测试；不得用 `collector task purge` 或 Storage 全量 reset 替代。

## 构建与服务包

在唯一审查通过提交上构建，Go/Proto 生成一致。Gateway、Collector 的现有 Linux 入口如下；Storage 含 CGO，使用配置中 Linux Compile Host 的正式构建入口，不把 macOS 构建当作 Linux 成品：

```bash
VERSION="$VERSION" GIT_COMMIT="$GIT_COMMIT" \
TARGET_GOOS=linux TARGET_GOARCH=amd64 \
./scripts/build/build.sh gateway

VERSION="$VERSION" GIT_COMMIT="$GIT_COMMIT" \
TARGET_GOOS=linux TARGET_GOARCH=amd64 \
./scripts/build/build.sh collector

VERSION="$VERSION" GIT_COMMIT="$GIT_COMMIT" \
./bin/moox-cli setup build-linux --file "$MANIFEST" --module storage
```

服务 ZIP 的现有通用入口如下。staging 必须含 `bin/`、`config/`、`start.sh`、`stop.sh`、`healthcheck.sh`；禁止 `data/logs/run/secrets/certs`、真实密钥、数据库、软链接及测试产物。Collector、Gateway、Storage 各角色的生命周期脚本与 staging 内容必须经过正式包契约核验；目前没有这些组件的专用组装器，不能只放一个 binary 就宣称可发布。

```bash
./scripts/build/package-service.sh --service-dir "$STAGING" --output "$PACKAGE"

./bin/moox-cli setup deploy-service \
  --file "$MANIFEST" --host "$HOST" --service "$SERVICE" \
  --package "$PACKAGE" --deploy-dir "$DEPLOY_DIR"
```

`deploy-service` 执行 ZIP/摘要校验、激活、健康检查和 Admin registry 同步；中途失败自动恢复旧文件。成功后的旧快照会 finalize，仓库没有公开 `rollback-service`：事后回滚须保留并重新发布上一完整 ZIP，不能假设旧目录仍可恢复。

## 有界更新真实路由与 Metadata

Gateway 的实际来源是 Admin active deployment。非空 `gateway_routes` 为 operator-owned，不会因更新工作区 YAML 自动补齐。通过认证 API 导出每个目标节点受影响的完整 deployment，从原对象结构化修改目标 route，保留其它可变字段和 `extra_config` 内容：

```text
POST /api/admin/sysdeploy/GetServiceDeployment
{"node_id":"已确认NodeID","service_name":"storage-primary"}

POST /api/admin/sysdeploy/UpdateServiceDeployment
请求字段：node_id、service_name、deployment。
deployment 使用 GetServiceDeploymentRsp.deployment 中导出的完整 JSON 对象，
仅将其中目标 route 修改为经审核的新值。
```

更新会整体覆盖 `extra_config`，不是 merge；回滚使用原始导出对象再次 Update。仓库没有 `service-deployments export` CLI，不编造该命令。直接 SQLite import 虽支持 `--node-id`、`--only-services`，没有 export/dry-run，不作为默认远程发布入口。

精确更新四个 Collector-only period RPC，并配置独立 `collector-market-runtime` 的 Claim service/endpoint；不得落到 wildcard CollectMgr、复用 Storage node，或放宽其它 caller。对实际受影响 Dataset 通过既有 Metadata 读写 API保存完整对象并只修改 owner/role 属性；不能拿默认 seed 覆盖所有线上对象，也不能把 merged-factor、财务或 mooxsys 标为 Collector raw。

```text
POST /api/admin/sysdeploy/GetGatewayNodeRoutes
{"node_id":"已确认NodeID"}

POST /api/admin/sysdeploy/ListGatewayNodes
{"node_id":"已确认NodeID","page":{"page":1,"size":10}}
```

等待 `applied_route_hash == route_hash`、空 `last_error` 和新鲜 heartbeat。随后实调 Ensure/Commit/Record/GetStatus 与 Claim：合法 Collector 经过实际 resolver/adapter，非 Collector 到不了上游；只读/非 owner/未知 Dataset 在服务边界拒绝且零副作用。路由 hash 一致不是业务 RPC 成功的替代。

## 发布 SCF 与恢复

旧生产者保持停止，以同一版本协调激活 Storage/DataNode/Primary、Gateway 路由、Collector runtime 和 SCF。先核验新的 Schema、native/runtime 实际 listener、凭据消费、EventBus CA、最终完整环境不超过 4096 字节，以及 StockCN 的真实 Invoke retry 容量。

```bash
./bin/moox-cli collector function publish submit \
  --file "$MANIFEST" --control-url "$CONTROL_URL" \
  --space-id "$SPACE_ID" --version "$VERSION"

./bin/moox-cli collector function publish status \
  --file "$MANIFEST" --control-url "$CONTROL_URL" \
  --space-id "$SPACE_ID" --job-id "$JOB_ID"
```

Manifest 模式必须提供 `--control-url`，禁止 `--zip`；依受管理发布路径派生调用 app key，主密钥不进入 ZIP/SCF，公开 CA 仅在固定包路径。记录每个真实 submit 返回的 job ID，持续查原 job，只有终态成功及实际 node/package/version 匹配才允许恢复。

`crypto.timer_function_count` 实际表示 Invoke pool 配额，不能凭字段名当作 Timer 数量。对正式 Space 的 `scf-event / market_fetcher / invoke` 节点分页盘点实际部署容量；manifest 静态额度不能替代云账户实时并发/配额证据。仓库无实时 quota 查询命令，需通过既有云 API/控制台取得该证据。

只恢复原来 enabled 的 Task 与 Timer。Task 用保存的完整对象设 `enabled=true` 后 `UpdateTask`；Timer 只恢复盘点时云侧 `timer_actual_enabled=true` 且已核对目录一致的节点，使用保存的实际 cron 通过批量 runtime-config job 恢复，再逐节点核验云侧实际状态。不全空间 enable，不触发 task purge。若任何节点/Proto/包版本不一致、pending 事实不可解释、ACL 不符或 Schema 初始化失败，立即停止恢复并按原 deployment/metadata 对象及上一完整包回滚。

## 线上端到端证据

发布记录必须列出正式机器、版本/commit、各包摘要、deployment route hash、真实 period identity、canonical deadline、Universe/series hash/count，以及 initial batch/Claim/runtime RequestID、Completion 和 Invoke retry 次数。日志脱敏，保留原始成功/失败响应而不只写结论。

在正式环境分别验证真实 1m 和 1h：周期内标签变化不改当前快照、下一周期使用新成员；成功序列推进 Storage bit；终态失败经过最多三次 Invoke 重试后录入；截止前 waiting、截止后单条 degraded marker；失败 ACK 丢失可恢复、首次迟报明确 missed、终态 marker 不变。失败注入必须使用明确批准的验收对象，不影响未授权 Task 或业务数据。

另证实 Timer 重复 tick/重启只一个 initial batch、错误 Completion 零副作用、历史有效目标不被当前午休/周末吞掉、unknown/waiting/pending 周期不被清理。观察窗口须真正覆盖 1h，不用加速本地时钟或 1m 结果代替。所有证据齐全后才能宣布目标完成。
