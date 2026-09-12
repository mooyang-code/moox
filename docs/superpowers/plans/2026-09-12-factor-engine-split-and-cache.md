# Factor 控制面、计算引擎与本地缓存执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. 使用复选框记录进度；独立代码审查使用 codeCR，主 Agent 核验后再关闭任务。

**Goal:** 将 Factor 控制面和计算引擎拆成独立程序，实现标的驱动的时序计算、周期驱动的截面计算、独立周期完成跟踪，以及按需积累和定时限容的本地 DuckDB 缓存。

**Architecture:** 保留 `modules/factor` 为代码模块，增加独立 engine 二进制，不为了两个进程再复制一个 Go module。View 服务负责发布源就绪和结果可读事件；Factor 引擎统一拥有缓存、执行状态和 Python worker。控制面为目录唯一权威，引擎只持有版本化副本。

**Tech Stack:** Go、tRPC-Go、JetStream、SQLite、DuckDB、Python、现有 Vue 控制台。

**状态:** 本文仅为执行计划，不表示代码已实现、测试已通过或部署已完成。以本次讨论为准，不保留历史兼容协议。文档中的新增文件、字段、测试名和状态机均为计划目标。

## 一、已确认决策

1. `moox-factor` 运行在外网，提供定义、绑定、补算受理与状态查询；`moox-factor-engine` 运行在内网，只需出站访问依赖。
2. 第一版单实例引擎；不实现多节点调度、分布式锁、因子依赖 DAG。
3. 因子定义表增加 `factor_type`，取值为 `timeseries` / `cross_section`；由引擎读取并选择触发方式、准备输入及校验输出。Python 不重复声明类型，也不将其加入函数签名；lookback 独立配置，现有因子明确标记时序。
4. 时序消费新增的 `ViewSourceSubjectReady`，不等待全部标的；截面消费 `ViewSourcePeriodReady`。
5. 周期完成跟踪只负责统计预期任务是否终态，不把源周期事件变成时序计算的前置条件。
6. View 服务虽位于 `modules/storage` 内，但与 Storage Primary 是不同运行职责。源 View 提交后的事件、结果 View 可读事件均由 View 服务发布。
7. 缓存按源 View 完整列建表，不按因子字段并集建表；仅在需要读取时回源积累数据。
8. View 输入契约变化时废弃旧文件，新建空文件，不在线 ALTER，不主动回填。
9. tRPC timer 检查缓存总容量；超限后保留最近修改的 N 行到新文件，切换并释放旧文件。N 在配置文件中设置。
10. 缓存缺失由上层按需回源补齐；任务回执、输出清单、目录副本和周期状态不得放入可淘汰缓存文件。

## 二、现有入口与修改边界

| 范围 | 已有入口 | 改造责任 |
|---|---|---|
| 启动与配置 | `modules/factor/internal/bootstrap/{bootstrap,config}.go` | 分离控制面和引擎装配 |
| 定义与存储 | `modules/factor/internal/domain/factor.go`、`schema/factor.sql`、`internal/store/` | 类型、版本与运行状态分离 |
| 管理接口 | `modules/factor/proto/factor.proto`、`internal/rpc/{service,recalc}.go` | 异步补算、应用状态、心跳查询 |
| 当前周期计算 | `modules/factor/internal/trigger/view_ready_runner.go` | 拆时序、截面与完成跟踪 |
| 读取与写回 | `modules/factor/internal/storageio/{client,writeback,marker}.go` | 缓存接入、版本保护、完成标记 |
| 批量执行 | `modules/factor/internal/taskrunner/read_pipeline.go`、`pyworker/worker.py` | 跨事件微批、截面契约 |
| View 输入应用 | `modules/storage/internal/service/view/{event_apply,period_event_apply}.go` | 源事件与结果事件可靠发布 |
| View 查询 | `modules/storage/internal/service/view/query.go` | 快照、覆盖与读取版本契约 |
| 公共事件 | `packages/storagepb/storage_events.proto` | 标的事件及周期语义 |
| timer 参考 | `modules/storage/internal/bootstrap/view_index_cleanup_timer.go` | 复用 timerjob + tRPC timer 注册方式 |
| 部署与界面 | `scripts/deploy/deploy-moox.sh`、`web/src/views/factor/` | 两程序独立部署与状态展示 |

路径均相对于仓库根。实施前重新读取工作树差异，特别是已有 Factor 配置、读取管线、部署脚本改动；不得覆盖或打包提交他人未提交内容。

## 三、必须落实的协议

### 3.1 输入与任务身份

```text
CacheSource = (space_id, source_view_id, input_contract_version)
RowIdentity = (完整源 View 主键，包含 subject、frequency、data_time、series_tag 等实际维度)
TaskIdentity = (binding_id, binding_generation, input_contract_version,
                frequency, period_time, subject_or_cross_section, execution_revision)
PeriodIdentity = (space_id, source_view_id, frequency, period_time, catalog_revision, run_id)
```

不要假设每个 View 只有 subject + time 两个主键字段。第一版时序事件只支持单 Dataset 源 View；多 Dataset 时序绑定返回明确不支持错误。跨标的依赖在截面契约中表达。

`input_contract_version` 表示字段类型、主键、字段映射、过滤、单位及投影语义；不能用每次写入变化的全局 revision 代替。物理索引切换但输入语义未变，不必清空缓存，必须重新确认读取来源和覆盖。

### 3.2 事件与责任

| 事件 | 发布者 | 必须保证 |
|---|---|---|
| `DatasetRowsUpserted` | Storage Primary | Primary 已提交；不代表 View 可读 |
| `ViewSourceSubjectReady` | View 服务 | 指定输入已提交；携带完整行身份、契约和数据变更位置 |
| `ViewSourcePeriodReady` | View 服务 | 源周期收集已终态，携带预期集合、失败集合及可用于检查本地应用进度的信息 |
| `FactorPeriodComputed` | 沿用现有结果标记链路，由引擎报告后发布 | 对同一 PeriodIdentity 的预期计算已全部终态 |
| `ViewFactorPeriodReady` | View 服务 | 上述周期结果确已应用到结果 View，携带完整/降级状态 |

上一版设计中的 `FactorPeriodCompleted` 统一为现有名称 `FactorPeriodComputed`，避免同时存在两个含义相同的事件。修改其负载以表达新的版本和完成集合，不保留兼容分支。

标的事件重复投递使用稳定事件 ID；修订使用新的变更身份。比较新旧数据必须使用来源可比较的变更位置，不比较随机 event_id、墙钟时间或全局 revision hash 的大小。发布失败通过可恢复的提交/发布状态重试；不要求构建通用跨库事务平台。

### 3.3 Python 契约

```python
# 两类因子统一入口，factor_type 由引擎从因子定义表读取。
def compute(df, params, context):
    ...
```

`df` 保留原名称和 DataFrame 角色：timeseries 输入是单标的、按时间排序、截至目标周期的历史窗口；cross_section 输入是多标的历史面板，至少包含 `subject_id` 和 `data_time`。`params` 保留算法配置参数，例如窗口长度，不混入引擎执行状态。新增 `context` 提供 `period_time`、`frequency`、`input_contract_version`；时序必填 `subject_id`，截面必填 `expected_subjects`、`available_subjects`、`missing_subjects`。引擎按定义表中的类型校验所需上下文字段，脚本不维护第二份类型声明。

截面输出必须包含合法标的身份及声明的输出列，不允许写入宇宙外标的或重复结果键。默认严格完整；允许缺失时显式配置策略并记录参与集合。不自动把现有时序脚本解释为截面脚本。

### 3.4 缓存配置

```yaml
cache:
  enabled: true
  dir: ./data/view-cache
  max_bytes: 21474836480
  check_interval: 37m13s
  rebuild_keep_rows: 100000
  min_free_bytes: 5368709120
  rebuild_timeout: 120s
```

以上容量和 N 是可执行初始示例，不是压测结论。检查间隔默认采用 `37m13s`（2233 秒），减少与整分钟、整小时采集任务周期性重合，但不保证完全不碰撞。启动后等待完整间隔再首次检查，不立即触发，不额外引入随机抖动；实现时核对 tRPC timer 的实际调度语义并通过测试验证。`max_bytes` 是所有缓存文件的总预算；`rebuild_keep_rows` 是每个被选中 View 最多保留的行数。所有值为正；未知键、非法时长、不可写路径启动报错。禁用缓存时保持原回源读取能力。

## 四、任务依赖

```text
T1 协议与数据模型 → T2 程序拆分 → T3 目录同步
T1 → T4 View 事件与读取契约
T1 → T5 缓存按需读取 → T6 失效/限容
T3 + T4 + T5 → T7 时序 / T8 截面 → T9 周期完成
T2 + T3 + T9 → T10 补算与控制台 → T11 部署 → T12 验收
```

每项采用“先新增失败用例 → 运行确认失败 → 最小实现 → 重跑通过 → 检查差异 → 独立提交”。不要把整份计划一次性实现后才做测试。下列状态机和步骤是实现契约，不是要求照抄未经编译的完整代码。

### T1：协议、类型与持久化模型

**修改：** `packages/storagepb/storage_events.proto`、对应生成文件与测试；`modules/factor/proto/factor.proto`、`internal/domain/factor.go`、`schema/factor.sql`、`internal/store/`。

- [ ] 新增测试：类型缺失/未知拒绝，现有四个因子均为 timeseries，同 View 混合类型绑定合法。
- [ ] 定义目录快照版本、绑定 generation、任务与周期身份、异步补算回执、引擎心跳；事件增加输入契约与来源变更身份。
- [ ] 将可清除输入缓存与不可随之删除的运行状态分开；为任务、输出清单、周期预期集合建立唯一约束。
- [ ] 写周期 manifest 测试：同周期后到的绑定修改不能改变已经固定的预期任务集合。
- [ ] 执行 `make -C packages/storagepb generate` 和 `make -C modules/factor/proto all`；按现有事件注册表更新注册、权限和拓扑测试。
- [ ] 从 `packages/storagepb` 执行 `go test ./...`，从 `modules/factor` 执行 `go test ./internal/domain ./internal/store`；预期协议往返、唯一约束和类型验证通过。
- [ ] 提交 `feat(factor): define engine and input contracts`。

### T2：两个入口与独立配置

**新增：** `modules/factor/cmd/engine/main.go`、`internal/bootstrap/control.go`、`internal/bootstrap/engine.go`、`config/engine.yaml`。
**修改：** 原 server 入口、`internal/bootstrap/bootstrap.go`、`config.go` 及其测试、构建配置。

- [ ] 新增装配失败测试：控制面禁止启动实时 consumer/Python，engine 不注册外部 FactorMgr，两个进程不打开同一个 SQLite 路径。
- [ ] 控制面保留管理服务及目录权威库；engine 创建运行库、只读目录快照、Python 管理器和独立健康检查。
- [ ] 从旧组合启动逻辑移除重复 consumer、执行器及无用依赖，不保留双模式兼容路径。
- [ ] 从 `modules/factor` 执行 `go test ./internal/bootstrap ./cmd/...`、`go build ./cmd/...`；预期两入口均可独立构建。
- [ ] 提交 `refactor(factor): separate control and engine processes`。

### T3：版本化目录、制品与应用确认

**新增：** `modules/factor/internal/catalogsync/{snapshot,consumer,artifacts}.go` 及对应测试。
**修改：** `internal/registry/service.go`、`internal/rpc/service.go`、`internal/taskrunner/builder.go`。

- [ ] 测试快照原子替换、通知丢失后校对修复、乱序通知不回退、源码 hash 不匹配拒绝执行。
- [ ] 控制面通过带版本的内部快照协议供 engine 出站获取；变更事件只负责通知，不作为唯一恢复来源；凭证使用最小权限，不公开目录内部接口。
- [ ] 将定义与绑定以同一版本应用，源码存入不可变 hash 路径；心跳上报 applied_catalog_revision。
- [ ] 普通修改按明确周期边界生效；停用进入 applying，待引擎阻止新任务并处理在途写回/清理后确认 disabled。
- [ ] 测试停用与写回并发时不会在停用完成后出现旧任务重新写入；删除不靠进程内 FactorGate 假装跨进程保护。
- [ ] 执行 `go test ./internal/catalogsync ./internal/registry ./internal/rpc ./internal/taskrunner`，工作目录 `modules/factor`；预期无版本回退和半套目录。
- [ ] 提交 `feat(factor): synchronize versioned engine catalogs`。

### T4：View 标的事件及可读契约

**新增：** `modules/storage/internal/service/view/subject_ready.go`、`subject_ready_test.go`。
**修改：** `event_apply.go`、`period_event_apply.go`、`query.go`、`eventconsumer/` 及对应测试。

- [ ] 写测试：提交前不发布、提交后发布失败可重试、重复投递稳定 ID、同根修订新身份、批量处理保留各原始行来源。
- [ ] 在 active View 提交完成后生成标的就绪记录；补齐重启后发布恢复，不能仅内存回调 Publish。
- [ ] 源周期事件携带可确认数据集合/应用进度的信息；不得用跨 consumer 的到达顺序推断行已应用。
- [ ] 测试读取 BTC 时 ETH 更新不会无故拒绝 BTC；将全 View 前后 revision 检查改为正确的单次读取快照契约，保留索引切换检测。
- [ ] 增加完整列投影和覆盖返回能力；明确 cache 回源对真实空区间的表示，不把分页未取完当成完整窗口。
- [ ] 从 `modules/storage` 执行 `go test ./internal/service/view/...`，带现有 DuckDB 构建环境执行，不能以跳过 DuckDB 的测试替代。
- [ ] 提交 `feat(view): publish subject input readiness`。

### T5：完整列缓存与按需读取

**新增：** `modules/factor/internal/inputcache/{manager,schema,reader,coverage}.go` 及对应测试。
**修改：** `internal/storageio/client.go`、`internal/taskrunner/read_pipeline.go`。

- [ ] 测试启动不回填；首次窗口 miss 一次回源；再次读取命中；新增依赖 volume 的因子不改已有完整列结构。
- [ ] Manager 以 CacheSource 管理独立文件，内部列使用保留命名空间并校验业务列冲突；持久化契约和覆盖元数据。
- [ ] 实现 `读取覆盖 → 合并缺口请求 → 获取完整业务列 → 版本校验 → 写缓存 → 返回窗口`；读取的实际列可仍按因子投影。
- [ ] 覆盖记录区分未加载与权威查询确认为空，结合来源变更位置失效；乱序补数不能用简单行数充当窗口完整性。
- [ ] 测试同窗口并发请求合并、较长 lookback 补缺口、合法空值保留、旧回源不覆盖新修订、删除 tombstone 防止旧值复活。
- [ ] 缓存连接仅由 engine 进程使用，worker 只接收 DataFrame；连接数、回源并发和批次内存有界。
- [ ] 执行 `go test ./internal/inputcache ./internal/storageio ./internal/taskrunner`，工作目录 `modules/factor`；预期 miss/hit 与来源调用计数精确匹配。
- [ ] 提交 `feat(factor): add lazy view input cache`。

### T6：schema 失效与 tRPC 定时限容

**新增：** `modules/factor/internal/inputcache/{lifecycle,compact}.go`、`internal/bootstrap/cache_timer.go` 及对应测试。
**修改：** `internal/bootstrap/config.go`、`config/engine.yaml`。

- [ ] 配置测试覆盖 N、容量、间隔、超时非法值，以及默认 `37m13s` 精确解析为 2233 秒；timer 注册沿用 `packages/timerjob` 和 tRPC timer 服务，不另起裸 ticker。验证启动不立即检查、完整间隔后首次触发及维护防重入。
- [ ] schema 变化状态机：`active → retired → closed → deleted`；创建新空版本，不 ALTER、不主动回填。新任务禁止引用 retired 文件。
- [ ] 缓存写入/更新设置本地 `cache_updated_at`；纯读取不刷新；重建复制保留原时间，不把复制行为视为业务更新。
- [ ] 实现下列限容算法，绑定变量 N 不拼 SQL 值，表名/列名来自受控 schema 标识符编码：

```text
timer tick:
  若本轮维护仍运行则退出
  统计 active/retired/temp 文件并回收零引用 retired
  若总大小未超 max_bytes 则退出
  按文件大小降序逐个选择 active，每轮每文件最多一次
  获取该 View 生命周期/写互斥，确认版本仍 active
  预检磁盘余量及预计新文件开销
  新建临时 DB，复制 ORDER BY cache_updated_at DESC, 主键 LIMIT N
  保留业务列、来源版本和必要 tombstone/覆盖安全信息
  清空不能由保留行证明的覆盖声明
  关闭并重新校验新文件，更新当前版本指针
  等待中的写入重新解析当前文件；旧读引用释放后删除旧文件
  若仍超限则处理下一文件；无可回收空间时禁止新增缓存写入
```

- [ ] 明确 tombstone 或已淘汰行的旧回源防护：压缩前发出的读取携带缓存 generation，切换后旧 generation 不允许写入；跨 generation 重新获取权威数据。
- [ ] 截断后实际文件仍超预算或 N 大于现有行数时不反复重建同文件；后续请求可绕过持久缓存。容量是软限制，空间不足不写满磁盘。
- [ ] 测试恰好保留 N 行及稳定排序、修改历史行被保留、覆盖清空后触发回源、重建期间写入进入新文件、schema 变更与 timer 串行、进程中断恢复、磁盘不足和 timer 防重入。
- [ ] 运行 `go test ./internal/inputcache ./internal/bootstrap` 和 `go test -race ./internal/inputcache`，工作目录 `modules/factor`；若 DuckDB 环境不支持 race，分别运行真实 DB 集成和抽象生命周期 race 测试并记录限制。
- [ ] 提交 `feat(factor): bound cache with timed file rebuilds`。

### T7：时序事件微批执行

**新增：** `modules/factor/internal/trigger/subject_runner.go` 及测试。
**修改：** `internal/trigger/eventconsumer/`、`internal/taskrunner/read_pipeline.go`。

- [ ] 写测试：只有 BTC 到达即可执行，ETH 未到不阻塞；多条不同 event_id 同周期事件能合并一次兼容回源。
- [ ] 合批键使用源 View、契约、频率与兼容窗口，不包含单条 triggerEventID；各消息仍保留独立完成/ACK 状态。
- [ ] 先检查当前因子版本与完整输入，再执行 Python，结果和任务回执持久化后确认消息。临时错误可重试，永久输入错误记终态。
- [ ] 同一结果键串行处理或按可比较输入版本拒绝旧写，避免晚结束的旧修订覆盖新结果；补算与实时不得互相无序覆盖。
- [ ] 测试一个标的失败不阻断其他标的，重投不重复破坏结果，关闭进程后未完成任务可恢复。
- [ ] 执行 `go test ./internal/trigger/... ./internal/taskrunner`，工作目录 `modules/factor`。
- [ ] 提交 `feat(factor): compute timeseries on subject readiness`。

### T8：截面计算契约

**新增：** `modules/factor/internal/trigger/cross_section_runner.go`、对应测试；`factors/CrossSectionRank.py` 作为最小可验收示例。
**修改：** `pyworker/worker.py`、`pyworker/test_worker.py`、执行请求与响应编解码。

- [ ] 写测试：周期未终态不执行；严格模式缺标的不计算；显式降级模式返回参与集合；带 lookback 的跨标的面板合法。
- [ ] 根据固定绑定宇宙读取本地面板，缺口回源，不以另一 consumer 已收到事件作为缓存已就绪证据。
- [ ] 两类因子均调用 `compute(df, params, context)`；更新现有时序脚本和测试，不保留双参数兼容入口或独立截面函数名。引擎从定义表读取 factor_type，分别构造窗口/面板与对应 context，保留 params 作为算法参数。
- [ ] 测试脚本无类型声明仍可由定义驱动执行，两类输入和上下文均按类型严格校验；截面返回拒绝重复主键、宇宙外标的、未知输出列。
- [ ] 使用固定 3 标的样本断言 Rank 值及缺失处理；与时序共用资源预算但不共用单标的输入契约。
- [ ] 执行 `go test ./internal/trigger/...`；在 `modules/factor` 执行 `python3 -m unittest discover -s pyworker -p 'test_*.py'`，使用项目既有 Python 环境。
- [ ] 提交 `feat(factor): support cross section panel execution`。

### T9：周期完成跟踪与结果 View barrier

**新增：** `modules/factor/internal/periodtracker/{tracker,store}.go` 及测试。
**修改：** `internal/trigger/view_ready_runner.go`、`internal/storageio/marker.go`、Storage View `period_event_apply.go` 及测试。

- [ ] 固定周期绑定版本，持久预期集合和 task receipts；周期 manifest 尚未获知时可以记录标的完成，但不能提前发布全集事件。
- [ ] 跟踪器仅统计，不调用计算器；引擎恢复逻辑补派缺失任务，超时形成明确 failed，不把未收到事件当成成功或自然跳过。
- [ ] 写测试：周期事件先到、标的先到、混合类型、失败标的、重复回执、进程重启和周期中途修改目录，均只产生对应执行版本的一个逻辑完成结果。
- [ ] 沿用 ReportFactorPeriodComputed 路径，但改为消费全体终态汇总；结果写入携带可验证的应用进度，View 消费完成标记后必须确认对应结果已经物化再发 ViewFactorPeriodReady。
- [ ] 测试故意延迟结果 View 写入时策略事件不发；明确降级状态和实际缺失集合；补算使用独立 run_id，避免覆盖实时周期完成证明。
- [ ] 从 `modules/factor` 执行 `go test ./internal/periodtracker ./internal/trigger/... ./internal/storageio`；从 `modules/storage` 执行 `go test ./internal/service/view/...`。
- [ ] 提交 `feat(factor): track complete periods independently`。

### T10：异步补算、状态与控制台

**修改：** `modules/factor/internal/rpc/{service,recalc}.go`、`modules/factor/proto/factor.proto`、`web/src/views/factor/definitions/{index.vue,factor-form.ts}`、`bindings/index.vue`、`__tests__/factor-contract.spec.ts`、`modules/cli/internal/command/factor.go`。

- [ ] Recalc 返回 task_id 和 accepted，不返回伪计算成功；request_id 去重，重启后可查询 running/succeeded/failed。
- [ ] UI 增加因子类型和截面缺失策略；展示绑定期望/已应用版本、引擎心跳 stale、补算状态和周期未完成名单。
- [ ] 引擎离线仍可查看目录；禁止把无心跳显示成正常零任务。补算与实时共享有界资源，避免历史任务耗尽全部实时预算。
- [ ] 为 API、CLI 和 UI 增加类型、accepted/finished、stale 和 failed 状态测试；从 `modules/factor` 执行 `go test ./internal/rpc`，从 `modules/cli` 执行 `go test ./internal/command`。
- [ ] 在 `web` 执行 `npx vitest run src/views/factor/__tests__/factor-contract.spec.ts` 与 `npm run build:prod`；人工核查新增控件与原界面一致。
- [ ] 提交 `feat(factor): expose engine and recalc progress`。

### T11：独立打包与部署

**修改：** 根构建/发布入口、`scripts/deploy/deploy-moox.sh`、`scripts/test/contract/test-deploy-moox-factor.sh`、`modules/factor/README.md`、示例配置。
**新增：** `scripts/test/contract/test-deploy-moox-factor-engine.sh`。

- [ ] 增加独立 engine artifact、配置、工作目录、运行库、缓存目录及服务生命周期；控制面不安装 Python 计算依赖。
- [ ] 测试部署控制面不会启动 engine consumer，部署 engine 不改变 Gateway 控制面地址；两者可单独重启升级。
- [ ] 配置最小权限 NATS subjects、Storage 访问和目录同步凭据，不写入仓库秘密，不要求新增内网入站端口。
- [ ] 先建立 durable 与事件权限，再开启 View 新事件发布，最后启动 engine；首次 DeliverNew 起点明确，重启不删除 durable。
- [ ] 运行两个 deploy contract 脚本；真实部署作为后续单独阶段，执行前确认主机、配置路径与现有服务状态，不从旧文档推定在线环境。
- [ ] 提交 `feat(deploy): package factor engine independently`。

### T12：独立审查、综合验证与交付

**新增：** `scripts/test/e2e/test-factor-engine-cache-e2e.sh`、`docs/factor-engine-operations.md`。
**修改：** `scripts/test/e2e/test-factor-view-ready-e2e.sh`。

- [ ] 用隔离测试 Space 和固定输入验证完整链路：Primary → View → 时序/截面 → Primary → 结果 View → 策略事件；不得对未授权业务数据制造故障。
- [ ] 将缓存阈值和 N 调小，验证真实文件大小、N 行保留、冷缺口回源和删除旧文件；重建期间并发读写，并在切换前后分别终止进程后恢复。
- [ ] 注入源码变更、字段改名、类型变更和输入表达式变化，验证新空库及因子失效提示；新增使用已有列的因子不触发 DDL。
- [ ] 请求 codeCR 独立审查四类风险：数据版本与乱序、周期 barrier、缓存生命周期与磁盘边界、控制面与引擎隔离。各结论附文件/行号，主 Agent 核验并修复后再关闭。
- [ ] 分模块运行 Factor、Storage、公共协议和相关 CLI 测试，执行前述 Python/Web/脚本测试及 `git diff --check`；不使用根目录 `go test ./...` 代替多模块验证。
- [ ] 记录基线与新链路的输入到结果 p50/p95、缓存命中率、回源次数/字节、Python 时间、缓存大小与重建暂停时间；禁止只以“64 worker”或服务启动证明性能通过。
- [ ] 运行手册写明缓存可删除、运行状态不可删除、N/容量配置语义、超限绕过、坏缓存恢复、durable 重建影响及补算操作。
- [ ] 交付列出提交 SHA、各层验证证据和未运行项；本地/mock E2E 与真实内外网部署验收分开报告。上线执行时再按授权完成部署及实际链路验证。

## 五、验收清单

- [ ] 控制台在外网可用，引擎停止不导致定义列表丢失。
- [ ] BTC 到达即可计算，不等待 ETH；截面不在源周期未终态时执行。
- [ ] 时序和截面结果共同参与正确版本的周期完成集合，策略事件不早于结果 View 可读。
- [ ] 首次启动与 schema 变化均不主动回填；新增因子已有列无需修改缓存表。
- [ ] 缓存使用完整 View schema 与列数据，历史窗口不足时正确回源，无旧版本覆盖新版本。
- [ ] timer 真正使用 tRPC 组件，N 配置生效，无在线 ALTER、无全库周期性清空、无并发删除在用文件。
- [ ] 默认检查间隔为 `37m13s`，启动等待完整间隔再首次触发；两类因子统一三参数 compute 入口，类型只由定义表管理。
- [ ] 限容压缩后未保留的数据能触发回源，覆盖证明和 tombstone 不造成错误命中。
- [ ] 容量软限制、临时双份空间、N 行仍超限及磁盘不足均有可验证退让行为。
- [ ] 目录版本、任务回执、结果清理清单和周期状态不随输入缓存删除。
- [ ] 未引入历史兼容分支、多引擎调度或隐式因子依赖。

## 六、实施纪律

本计划只授权后续执行的范围描述，不代表当前已获部署执行指令。任务完成前先验证再提交，只提交实施者负责的文件。开始执行时重新核对仓库现状与本计划入口；接口生成命令失败应修复工具链，不手工修改生成代码。所有新配置均进入文档与契约测试，所有环境限制单独记录，不将 SKIP 记为通过。
