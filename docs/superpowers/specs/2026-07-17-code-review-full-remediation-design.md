# MooX 代码审查问题彻底整改设计

## 背景

本次整改处理六类已经在当前 `main` 上确认的问题：

1. Storage ViewBuilder 在 DuckDB 或 Bleve 真正落盘前确认 JetStream 消息，随后静默丢弃派生错误。
2. Strategy 已出现在默认 SysDeploy 路由和管理台菜单中，但没有进入标准构建、发布和部署链路。
3. Strategy 查询接口忽略非法 RFC3339 时间，导致错误请求退化为更宽范围查询。
4. 架构文档、模块清单、模块说明和当前实现明显漂移。
5. package boundary、Go 格式、Prettier 和 ESLint 规则没有形成统一的只读 CI 门禁。
6. Storage、Monitor 和两个前端页面承担过多职责，已经成为维护热点。

MooX 是全新项目。本次整改不保留错误行为的兼容层，不接受“先隐藏问题、以后再修”的中间状态。Strategy 采用完整 M6 发布集成，不采用隐藏菜单或默认禁用路由作为最终方案。

## 目标

- 让 Storage 派生消费恢复真正的 at-least-once 语义：派生成功后 ACK，失败后 Nak 并重试。
- 让每次派生失败都可从日志和指标定位，并能通过故障注入证明自动恢复。
- 把 Strategy 作为正式模块纳入 workspace、构建、发布、部署、健康检查、EventBus、监控和远端验收。
- 只暴露 Strategy 当前真正支持的运行能力；默认配置禁止未完成的 Live 执行。
- 对 Strategy 时间范围和枚举参数执行统一、严格、可测试的边界校验。
- 建立零 warning、零格式漂移、零 package boundary 违规的 `make verify` 门禁。
- 让架构文档和 `go.work`、发布脚本、端口配置之间存在可执行的一致性检查。
- 按职责拆分维护热点，同时保持 RPC、数据、部署和用户交互行为不变。
- 完成独立代码审查、本地 E2E、标准 release/deploy、远端运行和浏览器验收。

## 非目标

- 不改变 Pebble 作为 Storage 事实主存、DuckDB/Bleve 作为派生查询层的边界。
- 不为 ViewBuilder 增加第二套持久化 inbox。JetStream durable consumer 已经是待处理消息的权威队列。
- 不在本次整改中实现 Strategy 尚未完成的 Live Trade 业务链路、跨节点调度或多实例高可用。
- 不为旧的 Strategy 端口、旧文档描述或旧格式风格保留兼容入口。
- 不以行数阈值机械拆分所有大文件。本次只处理已经确认的四个维护热点。
- 不在 CI 中运行 `gofmt -w`、Prettier `--write` 或 ESLint `--fix`。

## 总体实施策略

整改按风险和依赖顺序分成六个独立交付单元：

1. Storage 派生可靠性。
2. Strategy 参数契约和运行能力边界。
3. Strategy M6 构建、发布、部署和运行闭环。
4. 代码格式基线与 CI 门禁。
5. 文档单一事实源和一致性检查。
6. 维护热点拆分。

每个单元先补失败测试，再实现，再运行模块 E2E，再独立提交。最终统一执行 `make verify`、标准发布部署、远端故障演练和浏览器验收。

由于六个单元跨越独立子系统，详细实施材料采用“一个总索引加六个可独立执行的子计划”。总索引固定依赖顺序、统一完成标准和最终验收；子计划分别覆盖 Storage、Strategy 参数、Strategy 发布、CI 与文档、后端拆分和前端拆分。执行者不能跳过总索引中的跨模块验收。

## 1. Storage 派生可靠性

### 1.1 交付语义

Storage 行变更事件采用以下固定语义：

```text
JetStream delivery
  -> decode event
  -> enqueue all valid rows
  -> batch DuckDB/Bleve work
  -> wait until every row reaches a terminal result
  -> all derived writes succeeded: AckSync
  -> any derived write failed: NakWithDelay
```

ViewBuilder handler 不能在队列接收成功后返回。它必须等到该事件的全部派生项完成后再返回。SubscriberBus 继续拥有 ACK/Nak，因为 transport delivery 不进入 `core/eventbus` 和业务层。

派生写采用 at-least-once，而不是 exactly-once。DuckDB/Bleve 写入必须继续保持幂等；同一消息因 ACK 失败或 Nak 重投而重复执行时，结果不能重复累加或损坏索引。

### 1.2 事件级完成跟踪

每个 `TimeSeriesRowsUpdated` 或 `RecordRowsUpdated` 事件创建一个事件级完成对象。完成对象记录：

- 尚未完成的有效行数；
- 第一个派生错误；
- 只关闭一次的完成信号；
- 消息 ID、Space、Dataset 和数据形态等诊断上下文。

每个进入 batcher 的 derive item 引用该完成对象。一个事件被拆到多个批次时，只有最后一个 item 报告后才能唤醒 handler。多个事件合入同一批次时，批次错误会使该批次内涉及的事件全部失败并重投；已经成功写入的部分依靠幂等写安全重放。

空事件和只包含 nil row 的事件立即成功。入队中途失败时，未入队项直接记为失败，已入队项仍需完成或随 context 取消退出，handler 最终返回错误。

### 1.3 批处理和并发

保留现有 batch size、batch wait 和 worker 数配置。处理函数从“无返回值并丢弃错误”改为返回批次错误，并把同一个结果报告给批次内每个 derive item。

SubscriberBus 对 delivery 使用有界并发，最大并发由 `eventbus.max_in_flight` 控制。这样 handler 等待派生落盘时，多个 delivery 仍可进入 batcher 合批，但并发数不会超过 durable consumer 的 `max_ack_pending`。

每个仍在处理的 delivery 以 `ack_wait / 3` 为周期发送 `InProgress`。心跳失败只记录 transport 错误；最终派生成功后仍尝试 ACK，派生失败后仍尝试 Nak。ACK、Nak 和 InProgress 都使用有超时的独立 context，不能无限阻塞关闭流程。

`ack_wait_ms`、`max_deliver` 和 `max_in_flight` 必须从 Storage 配置传入 SubscriberBus，删除当前硬编码的 2 分钟、-1 和 128。启动时验证：

```text
ack_wait_ms >= 3000
max_in_flight >= 1
max_in_flight <= durable max_ack_pending
```

### 1.4 错误、日志和指标

日志使用结构化上下文，包含 message ID、delivery count、Space、Dataset、data kind、batch row count、engine、view ID 和 error。日志不能包含完整行、策略源码、密钥或用户数据。

Storage 增加以下低基数指标：

```text
moox_storage_view_derive_total{kind,result}
moox_storage_view_derive_batch_duration_seconds{kind,result}
moox_storage_view_derive_inflight{kind}
moox_storage_view_delivery_total{kind,action,result}
moox_storage_view_redelivery_total{kind}
```

`kind` 只允许 `time_series` 和 `record`；`result`、`action` 使用固定枚举。不得把 message ID、Space、Dataset 或 view ID 放入指标标签。

### 1.5 关闭顺序

关闭时先停止 Fetch 新消息，再等待已分发 handler 完成；随后关闭 batcher 输入、flush 尾批次、等待 worker，最后关闭 JetStream client 和 ViewIndex engine。达到关闭超时时，未完成 handler 返回 context error，让 JetStream 在服务恢复后重投。

### 1.6 验收

故障注入必须证明：

- DuckDB 首次写失败时消息不会 ACK，会 Nak，第二次投递成功后才 ACK。
- Bleve 首次写失败时行为相同。
- 多事件合批中的单次失败会重投所有相关事件，最终索引内容不重复。
- ACK 失败会记录指标并依靠 JetStream 重新投递。
- 长批次持续发送 InProgress，不会因 `ack_wait` 到期并发重复执行。
- 服务关闭时已接收事件不会静默消失。
- MemoryBus 也会等待派生完成并把错误返回 Access service；Access 记录派生失败，但已经提交的主存写仍返回成功。文档明确 MemoryBus 不提供写后立即可查询契约，因为主写只对 Pebble 事实存储负责。

## 2. Strategy 参数和能力契约

### 2.1 时间范围

Strategy RPC 提供一个统一的严格时间范围解析函数。规则如下：

- 空字符串表示无界，不报错。
- 非空值必须满足 `time.RFC3339Nano`。
- 返回值统一转换为 UTC。
- `from` 和 `to` 同时存在时必须满足 `from < to`。
- `ListStrategyRuns` 和 `GetStrategyPerformance` 使用完全相同的解析逻辑和错误文案。
- 非法输入通过 `RetInfo` 返回参数错误，repository 不得收到退化后的零值过滤器。

### 2.2 枚举参数

`GetStrategyPerformance.interval` 只接受空值、`auto` 和 `daily`。空值与 `auto` 使用明细点并由服务端采样；`daily` 使用日聚合。其他值返回参数错误。

`performance_source` 继续只接受 `backtest`、`observe`、`paper` 和 `live`。分页参数继续在 store 层归一化，但 RPC 测试必须覆盖负数、零和超大 page size。

### 2.3 Live 能力边界

默认发布配置保持 `live_enabled: false`。当 Live 未启用时：

- `SetExecutionMode(live)` 返回明确的 capability-disabled 错误；
- 管理台不展示可选的 Live 操作；
- 历史 Live 数据仍可在只读筛选和绩效查询中查看；
- Observe 和 Paper 保持可用。

后端是能力边界的权威执行者，前端隐藏只用于避免误操作。未来只有在 Trade 执行闭环完成并通过独立设计评审后，才能把生产配置改为 `live_enabled: true`。

## 3. Strategy M6 发布闭环

### 3.1 Workspace 和构建

根 `go.work` 加入 `modules/strategy/proto/strategygen`，与其他 proto module 保持一致。根 `Makefile proto` 增加 Strategy proto target。

`scripts/build.sh` 增加：

```text
all      -> moox-strategy + moox-strategy-cli
strategy -> moox-strategy + moox-strategy-cli
strategy-cli -> moox-strategy-cli
```

构建必须使用 `modules/strategy/cmd/server` 和 `modules/strategy/cmd/cli`，禁止绕过根脚本创建另一套制品命名。

### 3.2 Release 内容

标准 release 增加独立 `strategy/` 目录，包含：

```text
strategy/bin/moox-strategy
strategy/bin/moox-strategy-cli
strategy/config/app.yaml
strategy/config/trpc_go.yaml
strategy/pyworker/worker.py
strategy/pyworker/runtime-requirements.txt
strategy/pysdk/moox_strategy/**
strategy/strategies/example/**
```

Strategy SQLite schema 已嵌入 Go 二进制并由启动过程应用，不重复复制为运行时 SQL。发布脚本删除 `__pycache__`、`.pytest_cache`、`.pyc` 和本地数据库文件。release contract 必须校验所有必需文件存在、缓存文件不存在、二进制可执行。

### 3.3 Python runtime

部署目录创建 Strategy 专用 Python virtualenv，安装固定 requirements，并以部署包内 SDK 和 worker 运行。部署过程不能依赖源码工作树中的相对路径。

`app.yaml` 中的 `python_bin` 和 `worker_path` 在 staging 阶段改写为部署目录内的稳定路径。启动前执行 worker 握手 smoke test；失败则阻止 Strategy 启动和路由激活。

### 3.4 Deploy 编排

`scripts/deploy-moox.sh` 增加 `WITH_STRATEGY=1` 和 `--no-strategy`。Strategy 必须进入以下全部阶段：

- 参数解析和 profile 处理；
- build target 选择；
- staging 目录创建；
- server/CLI/config/worker/SDK/示例复制；
- remote rsync include/exclude；
- 旧制品清理；
- data、logs、run 和 virtualenv 目录准备；
- 启动、停止、PID 清理和进程存活检查；
- `/readyz` 签名健康检查；
- SysDeploy 状态和 Gateway 路由验收。

业务依赖启动顺序为：

```text
EventBus -> Storage -> metadata apply -> Monitor -> Strategy -> remaining business services
```

Admin 和 Gateway 作为部署控制面按现有流程启动，但只有 Strategy readiness 成功后才能激活 Strategy 路由。

Strategy 数据库固定为 `<deploy-dir>/data/strategy/strategy.sqlite`，日志固定为 `<deploy-dir>/logs/strategy/`。部署不得写回源码目录。

### 3.5 EventBus 和 Outbox

现有 `MOOX_STRATEGY` stream 和三个 Strategy topic 保留。Strategy 增加独立 `strategy-eventbus` 凭据，只允许：

- 发布 `moox.strategy.action.accepted.v1`、`moox.strategy.group_target.ready.v1` 和 `moox.strategy.execution.requested.v1`；
- 访问发布所需的 JetStream API 和 reply inbox；
- 不订阅 Storage、Trade、Metrics 或其他 Strategy consumer。

Strategy bootstrap 必须把现有 outbox relay 接到共享 JetStream publisher。relay 使用稳定 message ID，定时扫描未发布 outbox；发布失败释放 lease 并重试，发布成功后再标记完成。健康检查报告 EventBus 连接状态、最老 outbox age 和 pending count。EventBus 不可用时：

- 只读 RPC 继续工作；
- 会产生 outbox 的 commit 操作仍可原子提交；
- readiness 变为 false，Monitor 告警；
- relay 恢复后自动追平。

### 3.6 SysDeploy、Gateway 和 Monitor

端口统一采用当前运行配置：

```text
StrategyMgr: 127.0.0.1:11430
Strategy Health: 127.0.0.1:11431
Prometheus plugin: 127.0.0.1:12930
```

删除设计文档中的旧端口 11408/11418。SysDeploy 默认记录、Strategy 配置、部署脚本、文档和测试必须使用同一组端口。

Strategy 在部署和 readiness 验收成功后保持 `active`、`gateway_enabled=true`。标准部署包含 Strategy，因此不再需要“已配置但未发布”的特殊豁免。`monitor_enabled` 改为 true，并纳入系统服务检查。

管理台 Strategy 菜单保持可见，但必须通过部署契约测试证明标准发布一定包含可用服务。使用 `--no-strategy` 时，部署流程必须把该节点的 Strategy deployment 设为 inactive 或 gateway-disabled，不能留下死路由。

### 3.7 远端验收

远端验收至少覆盖：

- Strategy `/readyz` 通过签名健康检查；
- Gateway 能调用 `ListRunningStrategies`；
- CLI 能执行示例策略 validate 和 run-once；
- commit run 产生 outbox，EventBus 中出现稳定 message ID，outbox 最终变为 published；
- 管理台 Strategy 概览、运行列表、详情和绩效页面可打开且无请求错误；
- `--no-strategy` 部署不展示可用 Strategy 路由。

## 4. 质量基线和 CI 门禁

### 4.1 一次性基线清理

格式清理单独提交，不与功能或重构提交混合。基线目标：

- 所有受管 Go 文件通过 `gofmt`；
- 所有 `web/src` 受管文件通过 Prettier；
- ESLint 为 0 error、0 warning；
- package boundary 检查通过。

生成文件只有在生成器输出无法控制且确有必要时才能加入 ignore。`web-host/internal/statik/statik.go` 由生成流程负责稳定输出；如果生成器输出不符合 gofmt，生成后立即 gofmt，并在生成契约测试中固定这一行为。

### 4.2 只读脚本

根脚本新增清晰、可单独运行的检查入口：

```text
scripts/check-go-format.sh
scripts/check-web-format.sh
scripts/check-web-lint.sh
scripts/check-package-boundaries.sh
scripts/check-doc-consistency.sh
```

Go format check 输出全部不合规文件并返回非零。Prettier 使用 `--check`。ESLint 不带 `--fix`，并使用 `--max-warnings=0`。

### 4.3 Makefile

`make verify` 的顺序固定为：

```text
module boundaries
package boundaries
Go format
Web format
Web lint
Go tests and vet
Web tests and production build
release contract
Gateway deploy contract
Caddy contract
documentation build and consistency
```

格式和边界检查放在昂贵测试之前，快速失败。CI 不修改工作树；验证结束后 `git status --short` 必须为空。

## 5. 文档单一事实源

### 5.1 Workspace 清单

`docs/架构总览.md` 不再手工维护完整 workspace 表格。新增只读脚本从 `go.work` 读取 module 路径，并与文档中的受管清单比较。检查必须发现缺项、重复项、非 workspace 项和“所有 module 位于 modules/”之类错误陈述。

清单必须覆盖 `modules/`、`packages/` 和 `web-host`。Strategy proto module 加入 workspace 后，文档计数以新的实际数量为准，不在设计中硬编码 38。

### 5.2 需要统一的文档

本次至少更新：

- 根 `README.md`；
- `modules/README.md`；
- `modules/storage/README.md`；
- `modules/strategy/README.md`，若当前不存在则创建；
- `docs/架构总览.md`；
- `docs/大仓架构.md`；
- `docs/存储引擎架构.md`；
- `docs/策略模块架构设计.md`；
- Strategy Python 接入手册；
- 构建、发布、部署和端口相关运维文档。

统一后的事实包括：

- Admin 是中央浏览器控制面，Node Gateway 是每台机器的服务代理；
- Factor 已经是可运行模块；
- Storage subject 使用 `rows_updated.v1`；
- ViewBuilder 派生成功后 ACK，失败 Nak 并重试；
- 自动 maintenance 是补偿机制，不替代实时消费可靠性；
- Strategy 已进入标准 release/deploy，默认 Live 能力关闭；
- Strategy 端口、制品目录、数据目录和健康检查地址与脚本一致。

### 5.3 文档状态

设计文档明确区分“已实现”“默认禁用”“非目标”。完成本次整改后，`docs/策略模块架构设计.md` 的 M6 标记为已完成，并链接发布和远端验收证据。不存在的后续 Live Trade 能力不得写成当前能力。

## 6. 维护热点拆分

### 6.1 Storage server

`modules/storage/cmd/server/main.go` 只保留：

- flags 注册；
- config 路径解析；
- runtime 构建调用；
- server 启动和关闭。

其余职责移动到 `modules/storage/internal/bootstrap` 下的聚焦文件：

```text
runtime.go          总体生命周期
roles.go            role 判定和组合
view_runtime.go     ViewIndex、ViewBuilder、maintenance
eventbus.go         eventbus client 和 subscriber
health.go           health snapshot 和 reporter
config.go           runtime 配置加载和校验
```

拆分不改变端口、flags、配置文件格式或角色语义。原 `cmd/server` 测试迁移到对应 bootstrap package，入口只保留 smoke test。

### 6.2 Monitor bootstrap

`modules/monitor/internal/bootstrap/bootstrap.go` 保留 `Initialize` 和 Runtime 生命周期，具体装配拆成：

```text
host_metrics.go
metric_pipeline.go
probe_runtime.go
peer_runtime.go
health.go
retention.go
```

每个文件只负责一种后台循环及其关闭注册。所有 goroutine 必须通过同一个 Runtime 管理，禁止拆分时引入无主 goroutine。

### 6.3 CloudNode 页面

`cloud-node.vue` 保留页面级 tab、筛选状态和路由协调。拆分为独立展示组件和 composable：

```text
components/cloud-node-table.vue
components/cloud-node-editor.vue
components/cloud-node-detail.vue
composables/use-cloud-nodes.ts
composables/use-cloud-node-actions.ts
```

现有云账户和函数包管理继续使用各自页面或组件，不把不同业务对象重新塞进一个父卡片。API、表格列、确认流程和错误提示保持不变。

### 6.4 View Browse 页面

`view-browse/index.vue` 保留页面布局和 tab 协调。拆分为：

```text
components/view-query-toolbar.vue
components/view-column-selector.vue
components/view-filter-editor.vue
components/view-result-table.vue
components/view-row-detail.vue
composables/use-view-query.ts
composables/use-view-columns.ts
```

查询参数序列化、分页、排序、列宽、详情打开和错误状态分别由测试覆盖。拆分不能改变现有 Storage API 请求结构。

### 6.5 拆分验收

每个热点先补 characterization test，固定外部行为，再移动代码。完成后要求：

- Storage 和 Monitor 的 package 测试、race-sensitive 生命周期测试通过；
- 前端 unit test 和生产构建通过；
- Playwright 在桌面和移动视口验证 Strategy、CloudNode 和 View Browse 页面无重叠、无空白、交互可用；
- 不以新建“utils”大文件替代原来的职责混合。

## 7. 测试和验收矩阵

### 7.1 本地测试

- Storage builder、eventbus、DuckDB、Bleve 和 module E2E。
- Strategy RPC、store、outbox、bootstrap、CLI、Python SDK/worker 和 module E2E。
- Admin SysDeploy 和 EventBus credential tests。
- EventBus topology 和 ACL tests。
- release contract、deploy contract 和 shell syntax checks。
- Web Strategy API、store、组件测试和生产构建。
- 完整 `make verify`。

所有 Go 测试使用 `-count=1` 获取新鲜证据。涉及共享状态或 goroutine 的 package 额外运行 `go test -race`。

### 7.2 独立审查

完成实现后进行独立 CR，重点检查：

- ACK/Nak 是否仍存在提前确认路径；
- shutdown、context cancel 和 heartbeat 是否可能泄漏 goroutine；
- Strategy release 是否遗漏 CLI、worker、SDK、配置、凭据或清理路径；
- `--no-strategy` 是否留下 active route；
- CI 是否可能修改工作树或忽略 warning；
- 文档和脚本是否继续使用不同端口或 subject；
- 拆分是否只移动代码而没有形成清晰边界。

审查发现必须修复并重新运行相关验证，不能只记录为后续工作。

### 7.3 远端验收

在真实部署目标使用标准 `scripts/deploy-moox.sh`，不手工复制缺失文件。验收证据包括：

- 所有服务 signed readiness 成功；
- Strategy 路由存在且 RPC 成功；
- Storage 故障注入期间出现 Nak/redelivery，恢复后 View 与 Pebble 对账一致；
- Monitor 能看到 Strategy 和 Storage 新指标；
- 浏览器 Strategy、CloudNode、View Browse 页面可用；
- release 包和远端目录没有 cache、源码数据库或工作树路径；
- `git status` 干净，`main` 与 `origin/main` 同步。

## 8. 提交和完成标准

建议按以下逻辑边界提交：

1. Storage 派生 ACK/Nak 和可观测性。
2. Strategy 参数与能力校验。
3. Strategy EventBus/outbox 运行接线。
4. Strategy build/release/deploy 集成。
5. 格式基线机械清理。
6. CI 只读门禁。
7. 文档一致性修复和检查器。
8. Storage/Monitor 拆分。
9. CloudNode/View Browse 拆分。
10. 最终验收修复。

只有同时满足以下条件才算完成：

- 六类 CR 问题均有实现和回归测试；
- 不存在静默派生错误或提前 ACK；
- 标准部署中的 Strategy 菜单对应真实可用服务；
- `make verify` 全绿且工作树不被修改；
- 文档一致性检查全绿；
- 独立 CR 无未解决问题；
- 远端 Storage 故障恢复、Strategy API/worker/EventBus 和浏览器验收通过；
- 所有改动提交并推送，`main` 与 `origin/main` 一致。
