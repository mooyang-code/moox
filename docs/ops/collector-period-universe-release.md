# Collector 周期标的池协调发布手册

## 状态与边界

本手册是执行计划 Task 9 的发布准备文件，不是已发布证明。Task 1 至 Task 8 的实现、编码后独立审查和本地真实进程 E2E 已完成；2026-10-01 本轮新增 Storage cgo E2E，使用真实 Pebble finalizer、生产 `outbox.Relay`/`JetStreamPublisher` 与 durable pull subscriber，核验 degraded `CollectorPeriodCompleted` payload、同步 ACK、AckFloor，并证明 marker 只在 publisher 返回成功后从 outbox 清除。`make test-collector-period-universe-e2e` 已通过原 30 个进程场景及 marker ACK 场景；新测试的 focused race `-count=5` 通过。新起 `codeCR` 及其独立复核均未发现 P1/P2/P3。提交 `ff01e53f` 后 `env GOARCH=arm64 CGO_ENABLED=1 make verify-pr` 通过，Proto 生成无漂移、工作树干净。Collector SQLite 只读 inventory 于 `96c9e324` 实现并推送；CLI/Collector 全量测试、两模块 vet、聚焦 race、SCF package contract 和 architecture docs contract 通过，编码后 codeCR 未发现 P1/P2/P3。此前版本 `51209c7a` 的 compile host Storage Linux build 通过。正式环境尚未发布，真实 1m/1h 线上周期也尚未验收。不得把本地 build/E2E 记为生产证据。

**2026-10-01 只读运行态检查：** 用户要求按全新项目处理，不实现旧版本数据结构或协议兼容；这不代表正式环境为空，也不自动授权删除现存业务数据。最新 `setup host-diagnostics` 再次确认 Control 的 `moox-collector` 与 `moox-collector-subject` 正在运行，`/data/moox/prod/data/collector` 约 8.5 GiB，EventBus JetStream 约 6.1 GiB；Storage 的 DataNode、Primary、View、Access 与 Gateway 正在运行，`/data/moox/storage/data/storage-node/pebble` 约 476 MiB，View indexes 约 399 MiB。Storage 近期日志仍有 crypto 与多个 stockcn/stockus K 线 Dataset 的历史索引 backfill 记录。该主机级检查没有取得完整 SQLite 运行态或 period-key inventory，也未停止任何进程或修改生产状态。因此正式切换必须先确认当前任务、批次、失败回执与 waiting 周期的运行态，并将任何 ledger 重建和保留对象明确分开；不能把“无需兼容历史”推导成全量清库。

同日尝试现有 opt-in SQLite 诊断测试：`TestLiveCollectorE2EDiagnostic` 在 30 秒预算内超时，`TestLiveSchedulerDBDiagnostic` 在 90 秒预算内超时。随后通过 SQLite `mode=ro`、`query_only=ON` 的定向只读 SQL 获得了部分运行态计数，但这不是完整 period inventory，也不证明排空：`crypto` 有 4 个 enabled task；`stockcn` 有 1 个 disabled task。`crypto` 有 122 个 `dispatched` 与 146 个 `planned` fetch batch；retry 表有 47,267 个 `pending` 项（15,935 个 `1H`、31,332 个 `1m`）及 124 个 `dispatched` 项，记录时间覆盖 2026-09-30 至 2026-10-01。`t_collector_runs` 没有 `planned/active` 行，所查 Space 没有 readiness 行。这些计数不能抵消仍未完成的 batch/retry。

本轮只读检查还确认 Collector SQLite 文件约 9.2 GB，Control 根分区约剩 10 GB；Collector 日志有最长约 62 秒的慢 SQL/`context deadline exceeded`。因此不在同一分区创建近等大的备份，也不对在线库启动可能全表扫描的候选 inventory。生产 Collector Schema 是 inventory CLI 不支持的混合旧结构：缺少 `t_collector_period_storage_states`、`t_collector_timer_period_batches`、`t_collector_task_period_series`，retry 表既无旧 `c_period_failure_reported`，也无当前失败回执字段；旧 `t_collector_task_series` 与 readiness 表仍存在。候选工具应对此 fail closed，不得据此自动迁移或将缺表解释为无周期数据。Storage DataNode 仍在运行，尚未取得可离线验证的 Pebble period-key inventory。

只读 CloudNode catalog 检查列出 80 个 `crypto` invoke 节点，未发现 `stockcn` 或 Timer 节点；这不是按 Timer trigger 做的新鲜云侧 readback，不能据此认定没有已启用 Timer。全程未禁用任务、停进程、上传/提交 SCF、改配置或数据。生产 inventory 与一致备份仍未完成，且已有 retry 和 batch 未收尾，故本轮不执行部署或切换。

用户已授权编译、正式发布与线上端到端验证，并明确选择按计划将公开 EventBus CA 打包到 `certs/eventbus-ca.pem`；当前两个本地 SCF 候选均按该选择构建。正式切换仍被上述生产状态阻断：Collector 运行态 Schema 与 Pebble snapshot JSON 名称切换不兼容，period-only 清理/重建须先有可完成的只读 inventory、明确 Space/Dataset/period 时间范围、异机可验证备份及保留对象，再取得针对精确数据范围的单独确认。Control 磁盘余量不足以安全地原地复制 Collector DB，且当前没有经验证的有界线上迁移/清理工具。在这些前置条件解决前不执行生产部署、不修改生产配置或数据。不能自动迁移、双读旧 Pebble snapshot JSON 或让旧 Collector 与新 Storage 混跑。

**2026-10-01 上一版 SCF 本地候选（已被新版本取代）：** 代码版本 `9b537075` 的 crypto/stockcn ZIP 及其 SHA-256 留作历史审计；它们不包含 Collector SQLite inventory CLI，不得作为当前发布候选。此版本内公开 CA SHA-256 为 `f5a5d9e63389312ccfbabd0cbd63e172a31904b7d07bb1b63e42c1c486d9f977`，此前包的摘要与旧二进制仍可从原工作记录追溯。正式 submit 前仍须经受管理发布流程核验包内 CA 与 Control 信任 CA 的信任链；构建产物不等于该项证明。

**2026-10-01 上一版 Linux 候选（已被新版本取代）：** source commit `9b537075b7e05ed812d44b0a3130f4e68b5a8539`，版本 `9b537075`。Collector/Gateway/Storage 二进制摘要留作历史审计；它们不含本次 `collector period-inventory`，不作为同一版本发布集合。此前未完成含生命周期脚本、配置和 healthcheck 的正式服务 ZIP 组装及部署。

**2026-10-01 最新 CLI 与 SCF 本地候选：** source commit `96c9e324e649d597872943a9a842bfa5394e19eb`，版本 `96c9e324`，已推送到 `codex/collector-period-universe-remediation`。linux/amd64 CLI 候选 `/tmp/moox-cli-period-inventory-96c9e324` 为 x86-64 ELF，SHA-256 `d602d75f4fb351845e0f0710faca10e06fec83f0db1183205e249627ca90e636`。Darwin/arm64 smoke CLI `/tmp/moox-cli-period-inventory-96c9e324-darwin-arm64` 的 SHA-256 为 `77505e25743f66408d329ef227310ffb227ba27ecbe7a618dc7b674b4e13e487`。真实本地旧 Schema fixture 的只读盘点因其 pending failure receipt 缺少 `TaskJSON.market_type` 而按预期 fail closed；原 fixture 未修改。为确认合法 `pre_remediation` profile 的成功路径，仅在 fixture 临时副本中补齐合法 `market_type=spot` 后，Darwin smoke CLI 返回退出码 0、`complete=true`、`scope_present=true`、无 integrity issue、无截断；副本在 `/tmp/moox-period-inventory-smoke-96c9e324/`，不是生产数据库。

crypto 和 stockcn 包均由 `scripts/build/build-collector-scf-package.sh` 以 linux/amd64 构建，并嵌入既有用户确认的公开 CA，固定位置 `certs/eventbus-ca.pem`；包内 CA SHA-256 均为 `f5a5d9e63389312ccfbabd0cbd63e172a31904b7d07bb1b63e42c1c486d9f977`。`unzip -t` 均通过；包内容仅包括 `main`、相应运行/市场源配置、StockCN calendar/route 与 CA。crypto 包 `/tmp/moox-collector-scf-crypto-96c9e324-public-ca.zip` SHA-256 `0591fe34ea68523dcd671a48db054a96b2253a7b4de22743262170c10d50b258`；stockcn 包 `/tmp/moox-collector-scf-stockcn-96c9e324-public-ca.zip` SHA-256 `45b6b9bb89d2e598436a572e08a71afc83508f1c792f1fc35de6fec733fd9b1b`。两者均为本地候选，未 submit、未发布；已验证包内 CA 是公开 CA，不等于验证它与 Control 信任 CA 相同。

`./bin/moox-cli setup e2e-eventbus --file ./moox.toml` 返回 `public_tls=true`、`worker_bind_fetch_ack=true`、`worker_create_denied=true`、`worker_publish_denied=true`。该验证从本机连接正式 EventBus，创建并清理隔离的系统测试 consumer、发布/确认测试事件并验证 worker ACL；它确认 Control 提供的 CA 可用于该连接，但**不证明**本地 SCF 包内 CA 与 Control CA 字节/信任链一致，也不替代真实 1m/1h Collector 验收。

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

保存每个节点云侧实际的 enabled、cron、type、qualifier、message 和 `timer_last_readback_at`；要求字段存在、`timer_status_error` 为空、`timer_available_status=Available`，实际 type=`timer`、qualifier=`$LATEST`、message=`market_fetch_timer_v1`，且 readback 不早于盘点开始、距观察时间不超过 5 分钟。服务端对 5 分钟内的 readback 会命中缓存，不保证每次列表调用都会生成新的时间戳；缓存旧值不能当作本次云侧读回，等待其超过缓存窗口后重试，或用既有受信云 API/控制台逐节点取得等价证据。不可伪造强制刷新参数。目录期望与云侧实际不一致时先停止并核对漂移，不能直接按目录状态关闭或恢复。

仓库 CLI 提供只读的有界 fleet 盘点，可替代逐页手工读回；先按 `biz_type=market_fetcher` 取得目录，再逐个指定 NodeID、在 provider readback 查询中省略 `biz_type`。每个 provider 请求每页最多 1 项，查询预算超过一分钟；同范围下相似 NodeID 按服务端 LIKE 返回时，报告只接受精确 ID。命令最多运行 15 分钟，报告原始节点字段但不回显 provider 错误详情。`complete=true` 且退出码为 0 才能作为完整读回证据；其语义是范围内至少有一个非删除 Timer、所有节点都有完整目录/实际配置、启用状态和 cron 一致、trigger type/qualifier/message 分别匹配 `timer`、`$LATEST`、`market_fetch_timer_v1`、状态严格为 `Available`，且 readback 在命令开始后生成并且观察时不超过 5 分钟。若服务端对尚未过期的缓存跳过 provider 查询，命令会 fail closed；应等待缓存窗口过期后重试或用受信云 API 取得等价证据。它不会改动 Timer。

```bash
"$CANDIDATE_MOOX_CLI" collector function timer-inventory \
  --control-url "$CONTROL_URL" --file "$MANIFEST" --space-id "$SPACE_ID" \
  --cloud-account-id "$ACCOUNT_ID" --namespace "$NAMESPACE" --region "$REGION" \
  > "$TIMER_INVENTORY_JSON"
```

没有节点、scope mismatch、重复 NodeID、缺字段、旧 readback 或非 `Available` 状态都会非零退出；零节点不表示 Timer 已停。若命令未完整通过，应停止，不把部分 JSON 当作可恢复/可关闭的 fleet 清单。

仅针对该 inventory，以每批最多 100 个节点关闭 Timer，cron 使用已核对的实际值，并保存返回 job ID。提交与查询仍携带同一受认证 Space 上下文：

```text
POST /api/admin/cloudnode/SubmitUpdateNodeRuntimeConfigs
{"nodes":[{"node_id":"已导出NodeID","timer_enabled":false,"timer_cron":"原始cron"}]}

POST /api/admin/cloudnode/GetNodeBatchChange
{"job_id":"提交返回的job_id"}
```

必须轮询同一 job 至终态并核验云侧触发器确已禁用。观察超时不代表任务停止，不能因此重新提交或恢复 Timer。关闭新 planning 后仍保留旧进程的 in-flight、retry、failure reporter 和 Storage finalizer，使其收尾。

仓库没有公开的非破坏性批量 drain API。由确认过的受信控制面/云 API 停止新 planning 和 Timer 触发后，使用发布前独立复审通过、且与唯一发布提交一致的 `moox-cli` 候选版在 Control 主机执行 Collector SQLite 一致只读盘点；固定绝对 DB 路径和单个 Space，保存 JSON 与退出码：

```bash
"$CANDIDATE_MOOX_CLI" collector period-inventory \
  --db-path "$COLLECTOR_DB" --space-id "$SPACE_ID" --max-items 100000 \
  > "$COLLECTOR_INVENTORY_JSON"
```

报告只包含按 Space 统计的表/状态计数、匿名化的 active fetch batch/retry 引用，以及不含 Subject 明细的周期键汇总；失败回执只投影安全的 Dataset ID，不输出原始 JSON。当前回执检查按 reporter 实际输入验证 `TaskJSON.market_type`、有效频率/非零周期、非空且无重复 ID 的同 Space WriteTarget；新 Schema 还校验已持久化结果 JSON 中每个目标的 Space/Dataset/frequency/period/hash/count/index/disposition 身份及重复项。工具严格识别 `pre_remediation`（旧 boolean 回执）和 `current` 两种完整 Schema profile，不迁移数据库；预部署的旧正式库应报告 `schema.profile=pre_remediation`，新 period 表会列在 `schema.unavailable_tables`，不能把它们误当成数据缺失。混合/部分 Schema、缺表列、未知状态、无效快照或 Timer runtime request、损坏回执、全库无法归属 Space 的 readiness orphan、输出截断都会非零退出。工具使用 SQLite `mode=ro`、`query_only` 和单连接一致读事务；测试比较数据库与 WAL 的内容哈希，SQLite reader 仍可能更新 `-shm` 的内部读标记。`--max-items` 分别限制周期键、active batch、pending retry 三类数组，每类最多 100000 条。`fetch_batch_items.status` 只作历史关系计数：生产只插入 `pending`，不随父 batch 完成更新；排空判断看父 batch 的 planned/dispatched 状态，不要求 item 状态归零。`complete` 必须为 true、退出码必须为 0、`period_keys_truncated`/`activity_truncated` 必须为 false；否则停止切换。重复采集报告直到 planned/dispatched batch、未结 retry/failure receipt 与 waiting/pending readiness 均可解释并完成收尾。该报告不证明外部 Timer 已停、SCF invocation 已结束或 DataNode 已停止；云侧 readback、进程状态和 Storage 离线盘点仍须各自留证。`DisableTask` 不会取消已经 Invoke 的 SCF，`/readyz` 和服务包 healthcheck 也不证明 drain。

**Inventory 前置核对：** 在运行命令前，以受信部署配置/控制面核实目标 Control 主机、Collector DB 绝对路径和 `SpaceID`；将报告中的 `space_id` 与控制面确认值逐字比对，并将 `table_counts.t_collector_tasks` 与该 Space 的已知任务数交叉核对。报告要求 `scope_present=true`；空库或拼错 Space 会 fail closed，不能作为“已排空”。该工具无法独立证明一个非空但错误的 Space 身份正确，任何一项不符都停止发布。`work_type=resample` readiness 接受 Storage 保存的 canonical 固定周期（例如 `4H`、`7m`），没有 `PeriodSeriesSnapshot` 时报告 `snapshot_required=false`。Storage period state 必须有有效 deadline 和 confirmed 时间。Timer manifest 检查同周期 Task/FirstRun/route version 一致、非空 deadline 与初始 attempt、严格拒绝 runtime 不接受的未知或超限 RequestJSON，并核对 manifest/batch/request 的 schedule/batch kind/frequency/shard/planned-count/Function/RequestID/NodeID/Region/route-version、真实 Claim 限制的 item 数及持久 batch item/write target 关系；item Provider/Source 与请求级实际路由绑定、item Subject/Symbol/Market 与快照 index 身份、WriteTarget ID/Task/Space/周期/series identity 以及全周期 index 完整覆盖也必须成立。StockCN 的快照 Provider/Source 是逻辑路由身份，而请求 Provider/Source 是被选中的实际数据源，两者不要求相等。无 Timer manifest 的快照不建立全量 Subject map。

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

正式部署也有受控原位路径，但它们不是离线角色 ZIP：Control 上 Collector/Gateway 可在维护门禁完成后使用 `scripts/deploy/deploy-moox.sh --component-overlay --skip-build`，从仓库 `bin/` 读取已校验二进制。Overlay 要求目标已有受支持的 `start.sh` 与 `config/components.env`，强制排除 Admin/Storage，并会停止所选服务、替换所选组件文件及配置；执行前必须显式排除所有不属于本次发布的 profile 组件，并检查待覆盖配置差异。它不能与 `--package-only` 一起使用。

Storage 使用 `moox-cli setup deploy-storage --host "$STORAGE_HOST"` 的受管理部署路径；当已准备好 Storage Linux 二进制时，可在受控发布环境设 `MOOX_SKIP_STORAGE_BUILD=1` 复用 `./bin/moox-storage-*`。该命令会部署并更新相关 Control placement/routes/firewall/client 状态，不是离线打包或只读操作；两个 reset flag 必须保持关闭，除非另有精确范围和单独授权。`setup deploy-service` 仍只适合经过专门 staging 契约验证的独立服务目录，不能把共享 monolith 的根目录生命周期脚本当普通单服务包覆盖。

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
