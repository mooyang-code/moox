# Factor 引擎拆分实施进度

## 目标仍未完成

完整目标保持为执行全部拆分计划、独立 Agent 审查、正式环境部署以及实际因子计算验证。下面的基础代码通过不代表引擎拆分、缓存或线上验收完成。

## 工作区

- 实施工作树：`moox/.worktrees/factor-engine-split`，分支 `feature/factor-engine-split`。
- 基线提交：`de6b22bb`；创建时带入原工作树所有未提交改动的快照，未撤销或提交用户原有改动。
- 原仓库 `moox.toml` 是主机配置权威位置，未复制到工作树或输出凭证。
- 后续测试与构建必须在实施工作树运行；依赖 `web/node_modules` 是指向原仓库已安装依赖的软链接，不跟踪提交。

## 已完成的基础子任务

- 因子定义 `factor_type` 必填，domain/schema/store/registry/proto/RPC/导入 CLI 保留类型；实际 catalog 有 12 个时序因子，均显式标注。
- 公共 setup CLI 的配置读取、CreateFactor 请求、已有契约比较，以及前端表单/reset/API 类型增加 factor_type。
- 缓存 Config 定义总容量、N、磁盘余量、超时和默认 37m13s；已有 tRPC timer 注册函数、真实 DuckDB 完整行读写、按完整主键分组的历史查询、最近 N 行重建及文件代际管理。尚未接入引擎启动和完整回源覆盖协议。
- 已有单事务目录快照、版本冲突检查、引擎本机不可变源码准备、NATS 快照请求传输；控制面/引擎独立配置已定义，但程序装配仍未拆分。
- Python 统一使用 `compute(df, params, context)`，现有脚本和编辑器模板已更新；截面结果逐标的身份已进入校验和写回，缺标默认拒绝。截面调度尚未接入。
- `23fda150` 增加绑定 incarnation、执行 generation、分代 manifest 和持久化旧 cleanup task；`990f6e1c` 修复手动入口漏传及不同 ID 抢占自然键问题，独立复核关闭这两项。跨进程 drain/cleanup/确认仍未实现。
- EventBus 已支持限时、限次数回复权限，并用真实嵌入式 NATS 验证正常回复和拒绝无关 inbox 发布；凭证生成器与正式 ACL 尚未接入。
- `05a50bc8` 为类型基础提交；`c07de427` 为缓存配置与运行测试 fixture 提交。后续集成修复单独提交。

## 已获得的验证证据

- 当前 Factor 模块 `CGO_ENABLED=1 go test ./... -timeout 120s` 通过；引入与 Storage 相同的 DuckDB 驱动后需要 CGO，默认 CGO=0 不能构建缓存包。
- inputcache `go test -race ./internal/inputcache` 通过；独立 codeCR 还运行 count=100、race 和 go vet，无已确认问题。构造 gate 前仍须先 Validate 配置。
- Web 定向 8 测试通过，`npm run build:prod` 通过；有既有 Browserslist/Sass/大 chunk 警告。未做界面运行检查，也未发布 web-host。
- CLI 定向类型/setup 因子测试及 config 测试通过。
- CLI 全套曾出现两类失败：新增类型导致 setup_init fixture 缺字段，已修；`TestDefaultMetadataUsesUnifiedCryptoMarket` 的 disabled/active 不匹配，在原工作树独立重现，是现有基线问题，未擅自改动元数据口径。不得报告 CLI 全套通过。
- 类型独立审查指出 setup/UI 漏传以及配置加载晚校验的问题，已逐项修复并加测试；完整实现之后仍需重新启动最终 codeCR 审查。
- 类型基础最终 codeCR 复核无剩余阻断；真实导入 catalog 得到 timeseries 12 项。审查发现的 integration 请求漏类型和 CLI 测试污染文件也已修复；integration 仅编译通过，未运行真实环境测试。同 View 混合类型专项测试仍需在后续调度集成补齐。
- CLI command 排除已在原工作树复现的 `TestDefaultMetadataUsesUnifiedCryptoMarket` 后通过；这不等于全套无失败。
- 缓存专项审查发现重建并发写、等待取消、重复无效重建、JSON 类型和主键大小写问题；`6fdc7b3e` 修复上述问题，`3fee129f` 追加修复容量检查遇忙库的问题，独立复核均已关闭。维护期间 `Use` 快速返回 `ErrCacheBusy`，后续回源层必须处理它，不能将其当计算失败。`c2c334c1` 验证等待 gate 期间实际超时不切换。

## 目录和标的事件阶段

- 新增目录启动装配和独立 `catalog_sync_timeout`（默认 10m）；启动必须成功同步，周期协调复用 tRPC timerjob。中间 codeCR 的任务超时耦合及注册错误丢失问题已修复。尚未接入两个最终程序入口。
- View 行写入原本就不等待采集周期。现已增加 active 写成功后按原始事件/行身份发布 `ViewSourceSubjectReady`，发布失败返回上游，利用 durable delivery 重投恢复；不是新增 DuckDB 事务 outbox。批处理保留每条原事件身份，重投拆批不改变事件 ID。
- 新增真实 DuckDB 测试，在 publisher 回调内查询并验证刚写入的行；按标的发布测试及竞态测试通过。全套 View 测试中的 `TestSeriesCapacityMaintainerRebuildsWhenOneSeriesExceedsLimit` 因 `audit log=<nil>` 失败，已在未改动的原工作树独立复现；排除这一已确认基线用例后 View 与 eventconsumer 测试通过，不等于全套通过。
- 标的事件尚未接入引擎消费、缓存失效及微批调度，不能据此宣称实时因子已独立运行。输入契约 hash/version 与逐行可比较数据变更位置仍需在读缓存协议中区分。
- 标的事件中间审查发现两个未关闭问题：首次构建仅写 B 索引后会 ACK，需要持久待发布记录并在激活后恢复发布；现有 `readIndexRevision` 是 UpdatedAt 的 hash，不能当作可比较的变更序号，标的事件仍缺少持久来源位置。当前发布实现仅覆盖 active 正常写入路径，不得作为完整可靠交付部署。事件 ID 包含物理代际，同一来源在新代际重新就绪时会产生新 ID，后续任务去重须明确这一范围。
- 来源位置协议已继续补齐：DataNode 在原子 outbox 批次中写入 `source_node_id/source_store_id/source_sequence`，View 批处理保留并转发，序号只在同一 node/store 内比较。新空库持久生成 store incarnation，普通重启保持，重新创建的库使用不同 incarnation；已存在数据但缺少身份的库直接拒绝启动，不做兼容迁移。源码 ID 和默认 outbox ID 均隔离 store incarnation。复制/回滚旧数据库快照不等于重新建库，不能把这种人工回滚当作普通重启使用。
- 公共行事件及标的事件都要求完整来源位置，内部待绑定消息不再走公共编码器。新增重启序号、存储实例隔离及重绑拒绝测试，定向竞态测试通过。引擎的任务修订比较和首次构建待发布队列仍未实现，不能据此宣布上述端到端风险均已关闭。
- 首次构建补发继续实现：B-only 写入成功后，按原始行身份将待发布记录以临时文件、fsync、rename、目录 fsync 持久化，完成后才允许上游 ACK。日志位于 View 根目录 `pending-subjects`，不是可清除的 Factor 输入缓存。激活后及周期维护会确认 active 中该行可读再发布；发布失败保留记录，重启可继续，不重写旧字段值。记录绑定原 schema hash/version 与 primary dataset；当前契约变化或行已超出 View keep_duration 时清除失效记录。相同契约且仍在保留期的缺失行保留等待恢复，不设置任意丢弃 TTL。
- 本阶段测试覆盖进程对象重建后的补发、发布失败重试、B-only 写入前后顺序、契约变化不误发、并发回放等待及真实 DuckDB 的首次构建补发。View/eventconsumer 测试排除先前独立复现的容量审计基线失败后通过。程序入口与引擎消费仍未接线，不是正式运行验收。
- 独立复查发现关闭 View 定时维护时启动可能遗漏 journal 回放，已在 StartEventConsumer 安装 publisher 后、返回成功前同步恢复；失败清理消费者并返回启动错误。真实嵌入 JetStream 测试验证不启动维护也能补发并删除记录，定向 race 通过；补充 primary 替换和保留期过期不误发测试，View/eventconsumer 排除上述基线用例后重新通过。独立复核未发现剩余阻断问题，完整系统最终审查仍未进行。

## 时序微批调度接入中

- 新增 SubjectBatcher：默认 200ms 窗口、64 条上限、256 条等待队列、单批执行超时 2m，配置由独立 engine 配置加载并严格校验。按 space/View/dataset/frequency/period/物理索引/输入契约/series_tag 分组，保留每条来源身份，不擅自合并不同来源序号。
- Submit 在实际批次执行成功后才返回成功，队列满时背压；取消和执行失败向调用方返回错误。新增 SubjectHandler 使用公共标的事件解码器，非法消息 TERM，执行失败 RETRY，成功才 ACK。没有复用周期完成标记，不能把一批时序标的完成当作全集完成。
- 聚合、单标的不等待全集、合同分组、队列有界与取消测试通过。独立审查发现已取消的后续分组仍可能执行，新增确定性失败测试后修复为每组执行前再次过滤取消提交。真正的任务构造、批量回源、周期汇总和程序入口仍待接通。
- 新增 NewSubject 实际 durable 装配：复用现有消费者重连生命周期，独立 `factor_source_subject` durable，DeliverNew、无限重投、有限 MaxAckPending。初版 IndependentBatch 存在 Fetch 批次屏障，独立审查重复测试暴露晚到消息不能进入窗口；已改为 MaxBatch 个独立有界单条 pull loop，各自复用 Runner heartbeat/ACK，session 退出取消并等待所有 loop 与批处理器。嵌入式 JetStream 测试先确认 BTC 已投递再发布 ETH，验证两条仍组成一批，执行阻塞期间未 ACK，成功后 ACK 清零。尚未接入最终程序入口。
- 下一项已确认的集成问题：`taskrunner.clusterPeriodReadGroups` 把 triggerEventID 放在读批次键中，标的事件各自具有不同 ID，会抵消上述聚合。必须分开任务来源身份与可共享读取身份，同时把 input contract/series_tag 的读取约束贯穿任务与 Storage 查询，不能用覆盖原始事件 ID 的办法掩盖问题。

## 部署调查，不是部署证据

- 原 moox.toml 内网 factor-1 为 192.168.0.102，用户 mooyang，SSH 22；外网 control/compile 为 106.53.107.122，用户 ubuntu。
- 两主机 `ssh -o BatchMode=yes` 均认证失败；现有 CLI SSH 客户端支持从配置取凭据，后续复用其机制，不在日志打印秘密。尚未验证主机架构、磁盘和在线进程。
- 当前 setup control 包仍显式 `--with-factor`，需要拆掉一体化程序中的 Python/NATS 计算依赖；脚本没有 engine 独立 artifact/lifecycle。

## 紧接着的实施顺序

1. 当前类型集成审查已关闭，后续每个运行行为改动继续独立审查；检查变更仅含本次所有权文件。
2. 完成目录 generation 复核及 T1 剩余协议：异步回执、心跳、周期身份和公共 View 标的事件；T1 不能整体标完成。
3. 实施 T2/T3 两入口、目录快照与版本应用，保证 engine 不开管理端、control 不启动 Python/实时 consumer。
4. 实施 View 提交后事件/快照读取、按需完整列缓存、tRPC timer 和 N 行文件重建。
5. 在统一 Python 契约之上接通时序微批、截面面板、持久周期跟踪和结果 View 可读 barrier。
6. 完成补算/UI状态、打包部署脚本、真实 DuckDB/重启/故障测试；独立最终审查后再按 moox.toml 部署内外网并验证新鲜周期计算结果。

当前程序仍是一体化运行方式，不应部署这批基础提交作为最终交付。未删除任何线上数据，未对正式服务执行启停或发布。
