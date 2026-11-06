# moox-cli 命令覆盖执行计划

执行计划 v1（2026-10-31，尚未编码），基线 `main@67ae282d`。

## 1. 背景与目标

MooX 的运维方式是：用户只装 moox skill，由 Agent 用 `moox-cli` 完成操作。审查结果是目前做不到：
- 因子、策略、空间、云函数、EventBus、交易没有专用命令，只能 `gateway call` 拼 JSON；
- `system`、`lifecycle`、`replay` 和 `moox-cli storage` 的 4 个命令，按 skill 写法在操作机上跑不起来；
- `gateway call` 能把密钥明文打印进对话；
- 删除、重启、改防火墙这类命令有一部分没有确认步骤，skill 的描述也和实际不符。

目标：
1. 管理台的操作和各模块的主要操作都有专用命令（除第 2 节列出的例外）。
2. 所有命令在操作机上只靠 `moox.toml` 就能跑，不需要手工隧道和环境变量。
3. 破坏性命令默认只预览，加 `--yes` 才执行。
4. skill 写的每条命令都能照抄执行；用一个测试保证管理台新增接口时命令不会漏。

## 2. 已确认的决定

| 项目 | 决定 |
| --- | --- |
| 明文密钥 | `gateway call` 在本地拒绝 `GetSecretValue` 和 `ListNotificationChannels`（返回完整 webhook）；ACL 不动，专用命令继续在进程内使用，不输出明文 |
| `config get --reveal` | 保留，规则不变（只在任务确实需要时用，不打印到对话和日志） |
| 密钥写入 | 不做命令，继续由用户在管理台“设置 › 密钥”填写 |
| 云账户新建、修改 | 不做命令，这两个请求会带密钥；删除和查看要做 |
| 登录与会话 | 不做命令（`trpc.moox.infra.Auth`），CLI 用自己的网关身份 |
| 密钥导入导出 | 不做命令，CLI 继续无权调用 |
| 交易模块 | 只做只读命令；账户修改、下单、平仓、纸面模拟不开放，等交易模块完工后另议 |
| 管理台的 SSH 终端与文件管理 | 用 `host exec`、`host get`、`host put` 代替，不做交互式终端 |
| 兼容 | 不兼容旧参数，改名直接改，skill 同步 |

## 3. 审查依据

下面的数字来自 2026-10-31 对 `main@67ae282d` 的审查。

| 问题 | 证据 |
| --- | --- |
| 明文可达 | `gateway call trpc.moox.ops.SecretMgr GetSecretValue` 用不存在的 ID 实测返回“秘钥不存在”，说明调用已到服务端 |
| 业务失败退出码为 0 | `GetStrategy` 查不存在的 ID 返回 `ret_info.code=5`，退出码 0 |
| 不能选主机 | `HostAgentMgr GetStatus` 实测只到一台主机，`gateway call` 没有主机参数 |
| `system status` 不可用 | 没有环境变量时报“没有指定任何模块的健康检查地址” |
| `lifecycle status` 不可用 | 报“缺少模块本地接口的鉴权密钥” |
| `storage repair-view` 不可用 | 操作机上没有 `moox-storage-cli`，连 `--help` 都报错；部署单元只打包 `moox-storage-cli`，没有 moox-cli |
| 覆盖度 | 管理台用到约 176 个业务方法：专用命令约 40，`gateway call` 且 skill 写了约 53，`gateway call` 且 skill 没写约 81，无权调用 2。按方法名匹配估算，同名方法有重复计数 |

已有的基础，本计划直接复用：
- `setupssh` 已有 `Run`、`RunCommand`、`ForwardLocal`、`Upload`、`Download`（`modules/cli/internal/setup/ssh/client.go`）。
- `gatewayio` 已按主机 ID 开 SSH 隧道。
- `doctorRemote` 已能按 `moox.toml` 连到各主机执行命令，并经 control 中转探测只能出站的主机。
- 中转对 moox-cli 放行了组件健康端口（`healthRelayCallers`）。
- `lifecycle.Client` 和 `seriesreplay` 的客户端都有 `HTTP *http.Client` 字段，可以换连接方式。

## 4. 命令约定

新增命令都遵守；已有命令在第 4 阶段对齐。

| 项目 | 约定 |
| --- | --- |
| 配置文件 | `--file`，默认 `./moox.toml` |
| 标识参数 | `--space-id`、`--dataset-id`、`--task-id`、`--instance-id`，都带 `-id` |
| 频率 | `--frequency` |
| 时间范围 | `--from`、`--to`，RFC3339，左闭右开 |
| 输出 | 标准输出是 JSON，提示和进度写标准错误 |
| 成功判断 | `ret_info.code` 非 0 时，标准输出照常打印响应，同时退出码非 0 |
| 破坏性操作 | 默认只预览（打印将要发出的请求或影响范围），`--yes` 才执行 |
| 分页 | `--page`、`--page-size`，命令内部按服务的分页字段转换（策略用 `page_size`，其他用 `size`） |
| 长文本 | DSL、表达式用 `--dsl-file`、`--spec-file`，不在命令行拼多行 |
| 复杂请求 | 字段多的写操作用 `--data @文件`，请求体与 proto 一致 |

破坏性操作指：删除数据或登记、停止或重启服务、改防火墙、切换线上版本、改网关路由。
纯新增且可重复执行的命令（如 `setup factors`、`metadata apply`）不算，保留现有行为。

## 5. 实施步骤

每步一个可独立合入的提交。改命令的步骤同时改 skill 和 `docs/模块/命令行工具.md`，不留到最后。

### 第 1 阶段：安全与确认

**步骤 1：`gateway call` 拒绝明文方法**
- `modules/cli/internal/command/gateway_call.go` 增加拒绝表 `gatewayCallDenied`，内容是 `trpc.moox.ops.SecretMgr/GetSecretValue` 和 `trpc.moox.monitor.MonitorMgr/ListNotificationChannels`；
  命中时不发请求，报错并指向专用命令或管理台。
- Monitor 的 `notification.go:73` 返回完整 `webhook_url`，打码只在 `config notification` 里做。查看渠道继续用 `config notification`，它不受拒绝表影响。
- `gateway methods` 把这些方法标成“gateway call 已禁用”。
- SKILL.md 安全边界补一句：`gateway call` 不能取明文。
- 测试：拒绝表命中时 `Forward` 没被调用；专用命令走的 `adminclient.GetSecretValue` 不受影响。

**步骤 2：`gateway call` 退出码与主机参数**
- 解析响应顶层 `ret_info.code`：非 0 且不是 `SUCCESS` 时，先打印响应，再返回错误（退出码非 0，不打印用法）。没有 `ret_info` 的响应（如 `HostAgentMgr`）算成功。
- 这段判断抽成公共函数，步骤 7 的命令共用。
- 增加 `--host <主机 ID>`：`gatewayclient.CallMetadata` 加 `HostID`，`endpoint` 设置时只选该主机，主机不在 `directory.Services[service]` 里就报错，不重试到其他主机。
- 测试：业务失败退出码、无 `ret_info` 响应、指定主机的选路。

**步骤 3：统一破坏性命令的确认**

| 命令 | 现状 | 改为 |
| --- | --- | --- |
| `collector task delete` | 无确认，默认连结果删除 | 预览任务和结果数据集，`--yes` 执行 |
| `collector task delete-result` | 无确认 | 预览 View 和数据集，`--yes` 执行 |
| `collector task purge` | `--apply` 加 `--confirm`，另有 `--dry-run` | 只留 `--yes` |
| `collector function delete` | `--confirm`，另有 `--dry-run` | 只留 `--yes` |
| `setup reboot-host` | 直接重启 | 预览主机和实例，`--yes` 执行 |
| `setup firewall` | 直接加规则 | 预览将要加的规则，`--yes` 执行；`setup init` 内部调用不受影响 |
| `setup sync-deployments` | 直接改路由 | 预览，`--yes` 执行 |
| `setup attach-scf-vpc` | 直接绑定 | 预览，`--yes` 执行 |
| `setup watchdog --remove` | 直接移除 | 带 `--remove` 时要 `--yes` |
| `ops tencent lighthouse firewall add`、`open` | 默认执行，`--dry-run` 预览 | 默认预览，`--yes` 执行 |
| `udf activate` | 默认执行，`--dry-run` 预览 | 默认预览，`--yes` 执行 |

- 修正 `skills/moox/SKILL.md` 第 16 行：不再说 “`setup` 类命令默认只预览”，改为“破坏性命令默认预览、`--yes` 执行；部署类命令（`bootstrap`、`deploy-*`、`init`）没有预览，执行前把命令给用户确认”。
- `references/udf.md`、`host-registry.md`、`cli-operations.md` 里受影响的示例同步。

验收：表中命令不带 `--yes` 时，用假网关或假云客户端确认没有发出写请求。

### 第 2 阶段：操作机可用

**步骤 4：连接层与 `host` 命令**
- 把 `doctor_remote.go` 里的主机连接提成 `internal/hostconn`：按 `moox.toml` 解析主机、复用 SSH 连接、执行命令、传文件、拨本机端口。`doctor` 改用它。
- 拨端口规则：`reachability=outbound` 的主机走 control 中转（`DialRelay`），其他主机走 `ForwardLocal`。
- 新命令：

| 命令 | 作用 |
| --- | --- |
| `host exec --host <ID> -- <命令>` | 在主机上执行，输出原样返回，退出码透传 |
| `host get --host <ID> <远端路径> <本地路径>` | 下载文件 |
| `host put --host <ID> <本地路径> <远端路径>` | 上传文件；`--yes` 才覆盖已有文件 |

- `host exec` 是通用入口，skill 写明：写操作先给用户看命令。
- 新增 skill 说明后，删除 `references/` 里手写 `ssh <用户>@<主机>` 的示例，统一成 `host exec`。

**步骤 5：`lifecycle`、`system`、`replay`、`import` 改为按 `moox.toml` 连接**
- 新建 `internal/modulehealth`：
  - 由服务目录和 `moox.toml` 得到 `collector`、`factor-engine`、`strategy`、`trade`、`factor-mgr`、`archive-mgr`、`monitor` 的主机和健康端口；
  - 用步骤 4 的连接层构造 `http.Transport`，把 `lifecycle.Client.HTTP`、`seriesreplay` 客户端的 `HTTP` 换成它；
  - 经 SSH 从主机读健康密钥，只放内存，不打印、不落盘。
- 编码前先确认密钥文件。历史记录里是控制机的 `<部署根>/current/secrets/runtime-monitor.json` 和因子机的 `secrets/health-auth.env`，代码里身份来自 `unitdeploy/core.go` 的 `runtime-identity.json`；要核对路径、字段名，以及系统重置用的 `MOOX_HEALTH_RESET_*` 来自哪里。
- 命令变化：
  - `lifecycle`、`system`、`replay` 增加 `--file`；
  - `lifecycle` 增加 `--module <组件>`，默认 `factor-engine`，`lifecycle shadow` 固定 `trade`；
  - `import --target factor` 同样自动连接因子引擎；
  - 删除 `--collector`、`--factor`、`--strategy`、`--trade`、`--factor-mgr`、`--archive`、`--monitor` 和 `--factor-endpoint`、`--strategy-endpoint`、`--trade-endpoint`；
  - 保留 `--endpoint` 和 `--engine`，给主机本地执行用；显式传了就不走 `moox.toml`，密钥仍取环境变量。
- `system` 只处理 `moox.toml` 里部署了的模块，没部署的跳过并在输出里说明。
- 帮助和 skill 里的隧道、环境变量说明一并删除。

验收（生产只读）：`system status`、`lifecycle status`、`lifecycle status --module strategy`、`replay status` 在 Mac 上不设环境变量直接成功。

**步骤 6：`storage` 维护命令改为远程执行**
- `moox-cli storage repair-view|force-rebuild-view|reset-view-consumers|reset-metadata` 经 `host exec` 的连接，在存储主机执行部署单元里的 `moox-storage-cli`。
- `--storage-conf`、`--package-root` 由 `moox.toml` 推导，用户不再传。
- 补齐 skill 里只能手工到主机执行的三个：`storage delete-view`、`storage purge-dataset-events`、`storage period-inventory`。
- 删除 `retain-views`：它假定固定七个 View，现有环境不能用，文档也已劝阻。
- 这些命令不再 `DisableFlagParsing`，参数用 cobra 声明，`--help` 不依赖远端。
- 远端输出同时写到主机日志文件，防止 SSH 中断时丢掉 `backup_path` 等结果。
- `references/cli-operations.md`、`view-catchup.md` 改写，去掉绝对路径和“只在部署主机执行”的说法。
- 把 `moox-factor-mgr-cli recalc`、`import` 的 skill 用法换成第 3 阶段的 `factor` 命令（`cli-operations.md:23`、`initial-import.md:187`、`strategy-migration.md:250`）。

验收：Mac 上 `storage repair-view` 不带 `--yes` 能打印目标和动作；生产带 `--yes` 的验证要用户同意后再做。

### 第 3 阶段：补命令

**步骤 7：公共调用函数**

新增 `internal/command/rpc_command.go`：打开网关、发请求、打印 JSON、检查 `ret_info`。后面的命令都是“参数 → 请求 → 这个函数”，每个命令一个表驱动测试，核对请求字段名与 proto 一致。

**步骤 8：`factor`**

| 命令 | 方法 |
| --- | --- |
| `factor list`、`show` | `ListFactors`、`GetFactor` |
| `factor create`、`update`、`delete` | `CreateFactor`、`UpdateFactor`、`DeleteFactor` |
| `factor validate`、`preview`、`functions` | `ValidateFactorSpec`、`PreviewFactor`、`ListFactorFunctions` |
| `factor category list`、`upsert`、`delete`、`assign` | `ListFactorCategories`、`UpsertFactorCategory`、`DeleteFactorCategory`、`SetFactorCategory` |
| `factor task list`、`show`、`create` | `ListFactorTasks`、`GetFactorTask`、`CreateFactorTask` |
| `factor task enable`、`disable` | `SetFactorTaskStatus` |
| `factor task delete [--purge] --yes` | `DeleteFactorTask` |
| `factor task params` | `GetFactorParams` |
| `factor member add`、`remove`、`enable`、`disable` | `AddTaskMember`、`RemoveTaskMember`、`SetTaskMemberStatus` |
| `factor recalc submit`、`list`、`show`、`cancel` | `RecalcFactors`、`ListRecalcJobs`、`GetRecalcJob`、`CancelRecalcJob` |
| `factor status` | `GetStatus` |

`factor recalc submit` 自动生成 `request_id`，也可以用 `--request-id` 指定。
本地校验脚本 `scripts/factor-expr.sh` 保留，skill 写明两者的关系：离线改写用脚本，线上校验用命令。

**步骤 9：`strategy`**

| 命令 | 方法 |
| --- | --- |
| `strategy list`、`show` | `ListStrategies`、`GetStrategy` |
| `strategy validate`、`create`、`update` | `ValidateStrategy`、`CreateStrategy`、`UpdateStrategy` |
| `strategy delete --yes` | `DeleteStrategy` |
| `strategy instance list`、`show`、`create`、`update` | `ListStrategyInstances`、`GetStrategyInstance`、`CreateStrategyInstance`、`UpdateStrategyInstance` |
| `strategy instance enable`、`disable` | `SetStrategyInstanceEnabled` |
| `strategy result list`、`show` | `ListStrategyResults`、`GetStrategyResult` |
| `strategy target list` | `ListStrategyTargets` |
| `strategy replay start`、`list`、`show`、`bars`、`cancel` | `StartReplay`、`ListReplays`、`GetReplay`、`ListReplayBars`、`CancelReplay` |
| `strategy template list`、`show`、`save`、`delete`、`render` | `ListStrategyTemplates`、`GetStrategyTemplate`、`SaveStrategyTemplate`、`DeleteStrategyTemplate`、`RenderStrategyTemplate` |
| `strategy display get`、`set` | `GetStrategyDisplaySettings`、`SetStrategyDisplaySettings` |

`strategy replay` 是策略服务里的历史回放；顶层 `replay` 是因子、策略、纸面成交的逐期回放。两者的区别写进帮助和 skill。

**步骤 10：`space` 与 `metadata`**

| 命令 | 方法 |
| --- | --- |
| `space list`、`create`、`update` | `SpaceMgr` 的 `ListSpaces`、`CreateSpace`、`UpdateSpace` |
| `space show` | `Metadata` 的 `GetSpace` |
| `space delete --yes` | `SpaceMgr` 的 `DeleteSpace` |
| `metadata source list`、`show`、`create`、`update`、`delete` | `ListDataSources`、`GetDataSource`、`CreateDataSource`、`UpdateDataSource`、`DeleteDataSource` |
| `metadata subject list`、`show`、`upsert`、`resolve` | `ListSubjects`、`GetSubject`、`UpsertSubject`、`ResolveSubjects` |
| `metadata tag list`、`show`、`upsert`、`delete`、`member-status` | `ListTags`、`GetTag`、`UpsertTag`、`DeleteTag`、`SetTagMemberStatus` |
| `metadata dataset list`、`show`、`columns`、`subjects`、`activation` | `ListDatasets`、`GetDataset`、`ListDatasetColumns`、`ListDatasetSubjects`、`CheckDatasetActivation` |
| `metadata dataset create`、`update`、`add-column`、`activate`、`rebind` | `CreateDataset`、`UpdateDataset`、`UpsertDatasetColumn`、`ActivateDataset`、`RebindDatasetDataNode` |
| `metadata dataset delete --yes` | `DeleteDataset` |
| `metadata field list`、`show`、`create`、`update`、`batch-update` | `ListFields`、`GetField`、`CreateField`、`UpdateField`、`BatchUpdateFields` |
| `metadata field-group list`、`show`、`create`、`update`、`delete` | `ListFieldGroups`、`GetFieldGroup`、`CreateFieldGroup`、`UpdateFieldGroup`、`DeleteFieldGroup` |
| `metadata view list`、`show`、`columns`、`create`、`update`、`add-column`、`delete` | `ListViews`、`GetView`、`ListViewColumns`、`CreateView`、`UpdateView`、`UpsertViewColumn`、`DeleteView` |
| `metadata device list`、`show` | `ListDevices`、`GetDevice` |
| `storage node list`、`show` | `ListDataNodes`、`GetDataNode` |

`metadata view create`、`add-column` 的帮助要提示 View 内存估算（见 `references/storage.md`）。
带 `delete` 的命令都按第 4 节默认预览，`--yes` 才执行。

**步骤 11：`collector` 与 `cloud`**

采集补齐：

| 命令 | 方法 |
| --- | --- |
| `collector task list`、`show`、`update`、`disable` | `GetTaskList`、`GetTaskDetail`、`UpdateTask`、`DisableTask` |
| `collector task inventory` | `GetTaskResultInventory` |
| `collector data-type list`、`show` | `GetDataTypeConfigs`、`GetDataTypeConfigWithFields` |
| `collector subjects status`、`listers` | `ListTagSyncStatus`、`ListSubjectListers` |
| `collector resample preview` | `PreviewKlineResampleSource` |

`collector task update` 先取回完整任务，改指定字段后整体提交，用户不用自己拼 `task`。

新增 `cloud` 分组（云账户、函数实例、代码包属于 cloudfunction 模块，不放在 `collector function` 下）：

| 命令 | 方法 |
| --- | --- |
| `cloud account list`、`regions` | `ListCloudAccounts`、`ListCloudRegions` |
| `cloud account delete --yes` | `DeleteCloudAccount` |
| `cloud function list`、`job` | `ListFunctionInstances`、`GetFunctionJob` |
| `cloud function create`、`deploy`、`delete` | `SubmitCreateFunctionInstances`、`SubmitDeployFunctionInstances`、`SubmitDeleteFunctionInstances` |
| `cloud function update-config` | `SubmitUpdateFunctionInstanceRuntimeConfigs` |
| `cloud function invoke` | `InvokeFunction` |
| `cloud scf preview`、`import` | `PreviewSCFFunctions`、`ImportSCFFunctions` |
| `cloud package list`、`show`、`url`、`delete` | `GetPackageList`、`GetPackageDetail`、`GetPackageDownloadURL`、`DeletePackage` |

函数实例的写命令要和管理台一样持有发布租约（`AcquireCollectorPublishLease` 等），复用 `adminclient/publishlease.go`。
代码包上传沿用现有的 `collector function upload`。

**步骤 12：`monitor`、`eventbus`、`host`、`secret`、`data`**

| 命令 | 方法 |
| --- | --- |
| `monitor health` | `GetHealthOverview` |
| `monitor arrival` | `QueryPipelineArrivalHistory` |
| `monitor hosts`、`host-metrics` | `ListHostAgents`、`QueryHostMetricHistory` |
| `eventbus overview`、`streams`、`consumers`、`consumer`、`events` | `GetOverview`、`ListStreams`、`ListConsumers`、`GetConsumer`、`ListEvents` |
| `host list`、`deployments`、`routes` | `ListHosts`、`ListDeployments`、`GetHostRoutes` |
| `host catalog`、`directory` | `GetCatalog`、`GetDirectory` |
| `host agent status`、`snapshot`、`run-once` | `HostAgentMgr` 的 `GetStatus`、`GetSnapshot`、`RunOnce`，都要 `--host` |
| `secret list`、`show` | `ListSecrets`、`GetSecret`（只返回打码后的值） |
| `data records search` | `SearchRecordRows` |
| `data rows count` | `CountTimeSeriesRows` |

`setup hosts` 留作“moox.toml 里的候选主机”，`host list` 是 Admin 登记的主机，帮助里写清区别。

**步骤 13：`trade` 只读**

| 命令 | 方法 |
| --- | --- |
| `trade account list`、`show` | `ListTradingAccounts`、`GetTradingAccount` |
| `trade logical list`、`show`、`target` | `ListLogicalAccounts`、`GetLogicalAccount`、`GetLogicalAccountTarget` |
| `trade order list`、`show` | `ListOrders`、`GetOrder` |
| `trade fills`、`positions`、`holdings`、`equity` | `ListFills`、`ListPositions`、`ListHoldings`、`QueryEquityCurve` |
| `trade capabilities`、`action` | `GetExecutionCapabilities`、`GetOperatorAction` |

SKILL.md 的交易说明改成：可以查询，不代用户创建账户、下单或清仓。

### 第 4 阶段：收尾

**步骤 14：覆盖测试**

新增 `modules/cli/internal/command/command_coverage_test.go`：
- 读内嵌服务目录，取所有 `console` 能调用的方法；
- 每个方法必须出现在 `commandCoverage`（方法 → 命令路径）或 `consoleOnly`（方法 → 不做命令的原因）里；
- `commandCoverage` 里的命令路径必须能在 `rootCmd` 里解析到。

以后管理台加接口忘了做命令，这个测试会失败。

**步骤 15：参数统一**

| 现在 | 统一为 | 涉及命令 |
| --- | --- | --- |
| `--space` | `--space-id` | `data rows export`、`doctor alerts`、`gateway call`、`import`、`lifecycle shadow`、`setup export-skill-config` |
| `--dataset` | `--dataset-id` | `collector ingest-mode get`、`set`，`data rows export`，`import` |
| `--freq` | `--frequency` | `data rows export`、`import` |
| `--start-time`、`--end-time` | `--from`、`--to` | `data kline get`、`data skill kline get`、`data rows export` |
| `--task` | `--task-id` | `collector resample backfill`、`jobs`，`lifecycle`，`udf preview`，`replay` |
| `--instance`、`--replay` | `--instance-id`、`--replay-id` | `replay` |
| `--manifest` | `--file` | `import`、`metadata tag clone`、`metadata tag members` |
| `--file`（seed）加 `--manifest` | `--seed` 加 `--file` | `metadata apply`、`metadata import` |
| `--manifest-pack` | `--pack` | `import` |

`skills/moox/scripts/data-kline.sh` 和 `test-data-query-contract.sh` 要同步。

**步骤 16：清理与文档**
- `root.go`：删除 logo 和手写命令清单（其中的 `auth` 命令和 `config/cli-example.yaml` 都不存在），`Run` 只显示帮助；改写 `Long`。
- 改过时的简介：`storage`（“存储数据导入与读写工具”）、`setup`（“初始化 MooX 控制面”）、`setup factors`（配置已来自 `moox.toml`）。
- `references/modules.md`：改掉第 103 行（配置只能在页面改，与 `config get/set` 矛盾），各模块常用命令换成新命令。
- `references/api-recipes.md`：改成“管理台操作 → 命令”的对照表，只保留没有专用命令的少量接口；`gateway call` 退为兜底。
- SKILL.md 的“命令行约定”改成优先用专用命令。
- 更新 `docs/模块/命令行工具.md`。

## 6. 验证

每步：
```bash
cd modules/cli && go test ./...
```
构建一律带 `-o` 输出到模块外，避免把二进制提交进仓库。

每阶段结束跑 `make verify`，再对生产做一轮只读验证，CLI 用主仓库的 `moox.toml`：

| 阶段 | 只读验证 |
| --- | --- |
| 1 | `gateway call` 拒绝明文方法；查不存在的记录时退出码非 0；`--host` 打到指定主机 |
| 2 | `system status`、`lifecycle status`、`replay status` 不设环境变量成功；`host exec` 在 `moox.toml` 的所有主机都能执行 `hostname` |
| 3 | 每组命令抽 `list`、`show` 各一条，与管理台显示一致 |
| 4 | 按 skill 文档抽查命令照抄能跑；覆盖测试通过 |

写命令的生产验证要用户逐项同意。

## 7. 上线

- 这些改动只在 moox-cli 和 skill 里，服务端不用发布，`moox.toml` 不用改。
- `packages/gatewayclient` 加的 `HostID` 只影响显式设置它的调用方，其他模块不重新发布。
- 按阶段合入 `main`。skill 和 CLI 要同版本使用，对外版本号由用户决定是否发布。
- 主仓库和 Agent 用的 CLI 都换成新版本后，旧的隧道脚本作废。

## 8. 风险与待确认

| 风险 | 处理 |
| --- | --- |
| 健康密钥的文件和字段名只来自历史记录 | 步骤 5 编码前先核对；各模块密钥不一致时，按模块从自己的主机读 |
| SSH 中断时 `repair-view` 这类命令可能停在中途 | 输出写主机日志；文档保留“失败后确认 storage-view 已拉起”的步骤 |
| `host exec` 是通用入口，能绕开第 3 步的确认 | 不做技术限制，skill 规定写操作先给用户看命令 |
| 明文拦截挡不住有意绕过的本机程序 | 已接受，目的是防止明文进入对话 |
| 新增命令一百多个，工作量大 | 用步骤 7 的公共函数和表驱动测试压缩；可以按阶段停下，第 1、2 阶段独立有价值 |
| 字段名写错只在运行时暴露 | 测试里用 proto 的字段名核对请求，不靠手写字符串 |

## 9. 不做的事

- 不给 `Auth`、`Ssh`、`ExportSecrets`、`ImportSecrets`、密钥写入、云账户新建和修改做命令。
- 不做交易写操作。
- 不做交互式 SSH 终端。
- 不合并 `setup metadata-import` 与 `metadata import`，它们的连接方式不同。
- 不改 ACL，不改服务端接口。
- 不把现有文本输出命令全部改成 JSON；只有新命令强制 JSON。
