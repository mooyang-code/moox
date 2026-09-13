# Factor 引擎拆分实施进度

## 当前可部署边界（时序路径）

- 控制面入口 `InitializeControl`：不启动 Python worker / 实时 consumer；提供 FactorMgr 与目录快照。
- 引擎入口 `InitializeEngine`：时序 `factor_source_subject` durable + Python worker；`cache.enabled` 必须为 false；目录若含截面因子则拒绝激活。
- EventBus：Storage 可发 `ViewSourceSubjectReady`；控制面只应答 `moox.factor.internal.catalog.snapshot`；新角色 `factor-engine-eventbus` 出站建 durable。
- Storage Primary 写因子结果允许 AppId `moox-factor-engine`。
- 独立 `package-factor-engine.sh` / 生命周期脚本；跨平台 CGO 走 compile host（`MOOX_LINUX_CGO_TARGET=factor|factor-engine`）。
- 12 个仓库 Python 因子均为 `compute(df, params, context)` + catalog `factor_type=timeseries`。
- 明确未接线、本轮不作为上线能力：缓存 read-through、截面 runner、周期 barrier、异步 Recalc 执行器。

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
- 标的事件中间审查曾提出首次构建仅写 B 后 ACK、需要 journal 并在激活后补发。该方案已明确废弃：凡是重建（from-scratch 与 A/B 回填）都不 journal、不发 `ViewSourceSubjectReady`。遗留 `pending-subjects` 只删除、不发布。下游只消费激活后 active 索引上的 live 写入。
- 来源位置协议已继续补齐：DataNode 在原子 outbox 批次中写入 `source_node_id/source_store_id/source_sequence`，View 批处理保留并转发，序号只在同一 node/store 内比较。新空库持久生成 store incarnation，普通重启保持，重新创建的库使用不同 incarnation；已存在数据但缺少身份的库直接拒绝启动，不做兼容迁移。源码 ID 和默认 outbox ID 均隔离 store incarnation。复制/回滚旧数据库快照不等于重新建库，不能把这种人工回滚当作普通重启使用。
- 公共行事件及标的事件都要求完整来源位置，内部待绑定消息不再走公共编码器。新增重启序号、存储实例隔离及重绑拒绝测试，定向竞态测试通过。引擎的任务修订比较仍按 live subject-ready 接入，不依赖重建补发。
- 重建不再补发 `ViewSourceSubjectReady`。from-scratch 写 B 后直接 ACK，不落 `pending-subjects`；启动/维护/激活遇到遗留 journal 只删除。live 且已有 active 的写入仍按行身份发布（`mooxsys` 除外）。因子在重建窗口漏掉的标的，靠之后的 live subject-ready / period-ready，不靠重建补发。
- 本阶段测试覆盖：from-scratch 不 journal 不发布、遗留 journal 删除且不发布、启动清理不误发、真实 DuckDB 首次构建不发布且激活后 live 写入才发布。active 上的 live 批次身份与发布失败重试仍保留。

## 时序微批调度接入中

- 新增 SubjectBatcher：默认 200ms 窗口、64 条上限、256 条等待队列、单批执行超时 2m，配置由独立 engine 配置加载并严格校验。按 space/View/dataset/frequency/period/物理索引/输入契约/series_tag 分组，保留每条来源身份，不擅自合并不同来源序号。
- Submit 在实际批次执行成功后才返回成功，队列满时背压；取消和执行失败向调用方返回错误。新增 SubjectHandler 使用公共标的事件解码器，非法消息 TERM，执行失败 RETRY，成功才 ACK。没有复用周期完成标记，不能把一批时序标的完成当作全集完成。
- 聚合、单标的不等待全集、合同分组、队列有界与取消测试通过。独立审查发现已取消的后续分组仍可能执行，新增确定性失败测试后修复为每组执行前再次过滤取消提交。真正的任务构造、批量回源、周期汇总和程序入口仍待接通。
- 新增 NewSubject 实际 durable 装配：复用现有消费者重连生命周期，独立 `factor_source_subject` durable，DeliverNew、无限重投、有限 MaxAckPending。初版 IndependentBatch 存在 Fetch 批次屏障，独立审查重复测试暴露晚到消息不能进入窗口；已改为 MaxBatch 个独立有界单条 pull loop，各自复用 Runner heartbeat/ACK，session 退出取消并等待所有 loop 与批处理器。嵌入式 JetStream 测试先确认 BTC 已投递再发布 ETH，验证两条仍组成一批，执行阻塞期间未 ACK，成功后 ACK 清零。尚未接入最终程序入口。
- 下一项已确认的集成问题：`taskrunner.clusterPeriodReadGroups` 把 triggerEventID 放在读批次键中，标的事件各自具有不同 ID，会抵消上述聚合。必须分开任务来源身份与可共享读取身份，同时把 input contract/series_tag 的读取约束贯穿任务与 Storage 查询，不能用覆盖原始事件 ID 的办法掩盖问题。
- 已修正上述读聚类：subject_ready 且输入契约非空时，同一读取合同不再按各自事件 ID 拆批；合同进入单标的和批次分组键，任务原事件 ID 保持不变。新增 RunAll 测试验证两条不同来源事件只调用一次 bulk read 且结果任务保留各自身份；定向 race 重复 20 次通过，独立审查限定该改动无阻断。Storage 侧合同/series_tag 查询约束仍待完成。该改动位于已有未提交批量读取改动之上，暂留工作树，后续需连同必要读取依赖审查提交，不应只提交新测试而漏掉依赖。

## 当前读取集成审查

- SourceSeriesTag/FilterSourceSeriesTag 已从 TaskScope、任务、读分组、单条/批量 WindowKey 传到 View 查询的 series_tag EQ，显式空标签与不过滤严格区分。任务 ID 也隔离该输入分区。
- 完整依赖审查发现并修复三项数据错误：lookback 由全批次时间并集改为每标的独立计数；缺失标的 TargetPeriods 不再继承其他标的；跨页 revision 变化拒绝混合结果。新增稀疏 A/B 与 MISSING 回归，修正原先接受混合 revision 的错误测试。
- 客户端分页仍不满足最终目标：某标的不足 N 根时可能扫到全历史 EOF，全局 revision 在持续写入时又会使多页读取反复失败。下一步必须落地 Storage/DuckDB 单次快照、按标的 top-N 查询，不能将当前分页实现当作可部署终态。
- DuckDB 核心查询已新增 RowsPerSeries 单 SQL 模式：QUALIFY ROW_NUMBER 按 subject_id/freq/series_tag 分区取最新 N 行，不追加全局 LIMIT/OFFSET，也不另发 COUNT。真实 DuckDB 测试覆盖 400 个稀疏标的及同标的其他频率/标签，总计 1206 行不受默认 1000 行截断；DuckDB 包竞态测试通过。RPC 字段、请求边界、输入合同与生命周期 gate 尚未接入，Factor 仍不能使用该模式，不能据此关闭上一项运行风险。
- 来源分区还影响动态输出所有权。Manifest 主键新增来源 tag 与过滤标志，完整传递 Get/Replace/Delete/ListOwned 和 cleanup task 校验；同一 binding/generation/subject/period 的不同来源分区若认领同一动态输出行，事务内拒绝，不共享输出所有权。SQLite+写回测试验证 A/B 不互相清除、冲突在远端写之前拒绝、B 清空不影响 A。旧 Factor DB 直接拒绝启动，须重建，不做在线迁移。
- 当前 Factor 全套测试通过，storageio/store 竞态测试通过，但上述单次快照查询、最终任务构造及独立入口尚未完成；相关读取依赖仍留在工作树，未部署。
- 输出分区冲突已提供 `domain.ErrOutputOwnershipConflict` 类型，taskrunner 的三条写回路径均转为 NonRetryableError，避免按 Storage 故障在任务内部重试。最终 subject executor 仍须先持久化 degraded/失败终态再返回可 ACK 结果；handler 当前不会直接吞该错误，此项不能视为端到端终态处理已完成。
- 多因子共用 WriteFactorPatches 时的 ownership 失败隔离已补齐：只有该确定性冲突才回退原逐 patch 写入路径，健康 binding 正常写入，冲突 binding 单独失败；其他批量错误保持原策略。健康 A+冲突 B 回归修改前已失败，修改后全 taskrunner race 与独立复核重复 50 次通过。持久终态与最终入口仍未完成。
- View RPC 已增加有界 rows_per_series 模式和输入契约校验，要求明确标的、频率、标签及预期物理索引；不允许混用分页、排序或全局 revision。一次 SQL 期间持有 runtime 与物理索引 gate，返回实际服务索引和契约，不把查询成功声明为全集 Complete。新增非法参数、契约变更、无全局 revision 探测及查询期间生命周期锁测试，定向 race 重复 3 次通过。Factor 客户端尚未接通该模式，因此分页风险仍未关闭；独立 RPC 审查进行中。
- RPC 独立审查发现 Prepare 释放物理 gate 后、发布内存 schema 前可能短暂暴露“新文件 + 旧契约”。读取现已在 gate 内检查 preparing/retiring generation 标记并返回 VIEW_NOT_READY；准备标记覆盖文件重建至 schema 发布的整个阶段。补充该阶段回归，修正测试 fixture 的空 map 后定向 race 再次重复 3 次通过。View 套件跳过已确认基线失败的单个容量维护测试后通过，view/eventconsumer 通过；尚无正式部署证据。
- 已补真实 DuckDB + Service RPC 集成测试：显式空标签、同标的其他标签/频率、宇宙外标的、缺失标的和排他的结束时间均正确隔离，返回按标的最新 N 行；race 通过。Factor subject_ready 三条读取入口传递输入契约，客户端改用该 RPC，不采样全局 revision、不分页历史；校验实际返回索引/契约及行范围，窗口成功不设置全集 Complete。Factor 全套测试及 storageio/taskrunner race 通过。
- 客户端独立审查发现大微批可能超出 RPC 50000 行预算；现按 min(512,50000/lookback) 拆成独立标的子请求，每个标的仍单 SQL 快照，51x1000 与 513x2 边界测试通过。单标的 lookback 大于 10000 仍会拒绝，后续必须统一目录校验/容量政策，避免接受定义后到执行才失败。最终 subject task 构造、缺失终态、缓存接入和独立程序入口仍未完成，因此不能据局部读取测试宣称正式计算链路已可用。
- 审查还发现 subject_ready 缺少目标 K 线时被当作空成功。批量和单任务执行现均返回错误，避免尚未计算就允许 ACK；新增双路径回归通过，目标缺失的持久终态/回源恢复政策仍待最终 executor 完成。批量日志同步改为保留真实 TriggerType，不再统一标成 view_ready。独立复核客户端/分组/缺失目标的 race 重复 10 次通过。
- 已增加 BuildSubjectTasks：从单份已激活 CatalogSnapshot 构造匹配标的的时序任务，截面不进入该链路；保留输入契约、物理索引、显式空标签及事件身份，相关 pending binding 返回可重试未就绪。任务构造和 batcher 共用完整事件身份校验。独立定向 race 重复 10 次通过；该构造器尚未装配最终消费者执行器，来源 node/store/sequence 仍需随终态账本持久化并用于旧修订隔离。
- 进一步校正 View 别名读取：Factor SourceDataset 当前为逻辑 View 别名，series-window selector 不再将其作为 Primary dataset 发送，由已锁定合同的 View 服务补真实 Primary。已加回归测试。时序 lookback 上限 10000 现由定义规范化、目录 artifact 激活和任务构造共同检查，避免 10001 定义被接受后才在 RPC 必然失败；10000/10001 边界已测试。跨截面读取的资源限制仍须随其实现明确。
- 新增 SubjectRunner 协调层：强制传入共享 OperationGate 与结果提交函数，在同一锁区内读一次目录、构造全部任务、RunAll、校验结果身份并提交。缺失/重复/未知结果拒绝成功，提交失败阻止 ACK，计算错误在提交结果后仍返回供重试。任务现携带 CatalogRevision 与 SourceNodeID/SourceStoreID/SourceSequence/SourceEventID，避免后续账本从不完整任务猜测来源。定向 race 独立重复 20 次通过，Factor 全套测试通过。持久提交实现、执行前旧修订隔离仍未落地，协调层尚未接入正式 consumer，因此并未完成端到端持久终态保证。
- 新增 SQLite subject admission/run/head 仓储：先原子记录来源水位与 pending，再允许计算；complete 去重、failed/pending 重启可重试，旧来源序号及旧目录版本不允许覆盖新 head，不可比较 node/store 拒绝。定义 A->B->A 会重新执行 A 以恢复输出，已完成 no-op 仍推进目录水位。事务失败不留孤立 head，uint64 最大来源序号可保存并按数值比较。仓储尚未接入 SubjectRunner，不能据此声明已经做到线上写前隔离。
- 独立审查指出账本按因子/标的/周期增长的容量风险。新增基于“已持久完成边界”的 GC 原语及 period 索引，拒绝删除 pending/failed；替代任务将旧 pending/failed 原子标成 superseded。GC 同事务推进永久 exclusive cutoff 后删 run/head，使迟到实时重投无法复活已回收任务。不得用墙钟 TTL 伪造该边界；真实周期 barrier、timer、永久 failed/删除绑定的终态处置及历史补算绕开实时截止线仍待集成，因此容量上线门槛尚未关闭。相关仓储 race 独立重复 20 次通过，最新定向 race 重复 3 次通过。
- SubjectRunner 现已强制接入 SubjectTaskLedger：锁内先按来源序号降序 admission，再运行获准任务，逐项持久 complete/failed 后才提交事件回执；同批旧事件与 completed 重投不再重复运行。重复 TaskID 若来源身份冲突则拒绝，非 nil 但空错误字符串仍记 failed。真实 SQLite 回归验证 newest-first、回执失败重投不重算、旧重投不运行和空错误重试；独立 race 重复 30 次通过。正式回执实现仍必须从持久账本补齐 admission=false 的完成/过期记录，不能以本次 results 数量推断全集完成；consumer、永久失败终态和周期链路尚未装配。
- 新增 subject durable receipt 与适配器：SubjectBatchReceipt 保留全部计划任务（包括 admission=false），提交按事件归组并从 head/run 查询 complete/failed/superseded，拒绝 pending，不把 newer pending 的旧事件说成 complete。失败任务重试后回执可更新成功；同一目录版本的计划 TaskID 集不允许变更，同一 space/event 的 protobuf payload 跨目录版本也不可复用。回执随持久 GC cutoff 同事务删除，cutoff 以下事件不复活。真实 SQLite runner 测试已接实际 receipt adapter，回执失败重投不依赖本次 results 恢复历史结果；Factor 全套测试通过，最新定向 race 重复 3 次和独立 race 重复 20 次通过。正式 consumer、周期结果汇总与 GC timer/barrier 仍未装配。

## 独立运行时接线进度

- StartEngineSubject 已把已激活目录、共享 OperationGate、SubjectRunner、SQLite admission/receipt 和独立 subject durable 接通。嵌入式 JetStream 测试验证计算阻塞期间无回执且未 ACK，成功后持久回执再 ACK；重启 consumer 后同事件重投不重算。测试显式携带协议要求的 Nats-Msg-Id，并跨过服务端去重窗口，避免把服务端去重误当作引擎去重。独立 race 重复 3 次通过；此证据使用 fake 计算器，不代表真实 Python/Storage E2E。
- 本轮重新执行 Factor 模块 `CGO_ENABLED=1 go test ./...` 全部通过。独立 engine 主入口、截面调度、缓存 read-through 和周期 barrier 仍未完成，不能部署为最终版本。
- 开始补独立入口前的资源生命周期：catalog timer 关闭必须禁止新同步、取消并等待在途同步后才关闭 NATS/SQLite，不能仅断开目录连接。新增 shutdown fence、取消与 drain 测试，定向 race 重复 10 次通过；最终 main 仍需按此契约装配逆序关闭。
- 新增 EngineResources 分阶段资源层：Open 仅建立 replica SQLite、认证 Storage 和共享 gate；目录 revision 为正后 StartCompute 才创建 Python pool/taskrunner，任务校验使用 replica generation，不创建本地权威 registry/FactorMgr。采用独立 moox-factor-engine 认证身份。取消不提前关闭 SQLite，要求外部 consumer/catalog/cache drain 后 Close；失败和重复关闭路径已覆盖。真实 worker.py 启动、重复启动复用 pool、关闭后不再 Ready 的回归及资源层定向 race 重复 3 次通过。该资源层尚未接入独立 main，不能据此声称引擎程序已可部署。
- 资源层独立审查发现旧 newTaskValidator 仅校验 scope，未校验执行 generation。已用真实 SQLite 复现“BTC 宇宙改为 BTC+ETH 后旧 BTC 任务仍放行”，再修复为按当前 binding/factor 重算 ExecutionGeneration，并校验因子类型/输出及精确 binding ID。定向 race 重复 10 次通过；该校验不是执行锁的替代，最终实时/补算调用仍须持共享 gate 覆盖验证至写回。

## 部署调查，不是部署证据

- 新增 inputcache.Manager：按 space/View 管理完整 schema 的空文件代，Handle 同时绑定 source incarnation 与物理 epoch。schema 变化或容量压缩后，旧回源 Handle 不允许写入；新 schema 创建失败前也先使旧代失效。维护统一检查总目录大小，超限或维护失败暂停 Fill，已有有效缓存仍可读，后续维护可恢复。真实 DuckDB 包 race 重复 3 次通过。Manager.Get 必须接受源端权威契约，不能拿任意旧事件 opaque version 回滚缓存；此约束仍须由读取适配层实现。
- View series-window 空投影增加同锁内完整 schema 返回（含四个时序主键列），即使无数据也返回列描述；DuckDB 完整窗口显式返回 NULL 字段。独立审查发现逻辑/物理 schema 可能不一致，已在完整读取拒绝非法列名、重复列及未知类型；完整窗口另限 500000 cells，在查询前拒绝宽窗口，避免显式 NULL 放大内存。真实 DuckDB NULL/空窗口与边界测试通过；最终缓存适配器还需据列数调整批量，不能直接沿用只按行数的微批预算。
- 缓存 read-through、覆盖证明、来源修订/删除失效和 timer 接线尚未完成，engine 仍拒绝启用缓存。Manager 的目录独占及有所有权依据的 crash orphan 回收已补充，见下面记录；不能据此宣称完整缓存读取链路已完成。
- 二次局部审查已关闭新 schema 创建失败时旧 handle 未失效、缓存目录可写性未启动检查、完整 schema 与物理列不符及空投影宽响应四项问题。Manager 增加真实临时文件可写探测并清理；readonly/no-residue 测试和包 race 重复 3 次通过。
- 统一 series-window 预算：共享 helper 同时限制 512 标的、50000 行及 500000 cells（含四个主键列），显式投影与全列请求都在服务端 Query 前检查。客户端按列宽拆标的批，定义拒绝单标的也无法容纳的窗口，taskrunner 不再把分别合法但合并后过宽的因子强行合批。相关 domain/storageio/taskrunner race、Storage 协议和 View 定向测试通过；局部独立审查未发现阻断问题。空投影客户端仍需在完整缓存适配器中取得 schema 后按真实宽度拆批。
- 缓存目录新增持久 flock 锁文件与随机 session 所有权标记。只有持锁且 marker/layout 校验通过才回收崩溃遗留 session；未知文件保留计入预算，符号链接及异常 marker 拒绝启动。正式 marker 通过 pending 文件写入、Sync/Close、rename 发布；中途崩溃的无标记目录保留，不猜测删除。真实子进程竞争、SIGKILL 后恢复和 Close 等待读者期间仍持锁测试通过，root inputcache race 重复 3 次通过。此证明针对进程崩溃，不包含断电持久性或恶意外部修改防护。
- 新增完整复合主键 DeleteKeys 原子删除原语，供后续 tombstone/修订失效接入；批量失败回滚，不接受部分主键，提交后更新 mutation 栅栏。独立审查发现 sql.NullString 等 wrapper 可绕过裸 nil 检查，已红测复现并改为绑定前按标准 driver converter 解析一次、拒绝 NULL（包括 typed nil bytes）。这不是完整来源失效协议，仍需同时撤销覆盖证明并阻止旧回源写入。
- 高位 uint64 绑定问题已补齐：仅按 schema 为 UBIGINT 的 unsigned 参数转无损十进制字符串，覆盖 Upsert、ReadWindow filter、Rebuild 和 DeleteKeys，其他列及每层 pointer 的 Valuer 语义保持不变。真实 DuckDB 最大 uint64 写入、查询、重建和删除贯通测试通过，保留低值及 NULL 校验回归。root 缓存 race 重复 3 次、独立复核定向 race 重复 10 次通过；该证明只覆盖本地缓存参数，不替代尚未实现的源 RPC 完整 typed-row 适配器。
- 新增 storageio 完整列响应纯解码层：保留零行 schema，强制四个时序主键，按 Storage 七种逻辑类型无损解码；拒绝缺列、错型、重复键/列及非法纳秒时间，区分 SQL NULL、JSON null 和空 BLOB。支持源 View 当前 JSON StringValue 表达及协议 JsonValue，不解析 JSON 数字为 float。真实 DuckDB 解码结果写入/读取保持最大 int64 与纳秒值；root 定向 race 重复 3 次、独立复核解码层 race 重复 10 次通过。尚未接入 Client 回源/Manager，因此完整缓存 read-through、覆盖证明和来源变更栅栏仍待实现。
- Client 已新增完整列回源方法，复用公共 querySeriesWindow 的单 RPC、索引/契约与逐行 scope 校验，并从同一响应解码 schema/rows；旧时序返回语义保持不变。新增红测发现空请求契约和空 served 契约可互相匹配，现 RPC 前拒绝空契约。root storageio/taskrunner race 重复 3 次和独立定向 race 重复 10 次通过。尚未将该方法接入 Manager 或生产计算读路径，不以这个局部方法证明缓存命中或回源次数下降；完整列的实际宽度拆批、覆盖证明及并发来源失效仍须继续实现。
- 已补充给定权威完整 schema 的回源批处理：按真实业务列数和共享预算拆分独立标的，单标的窗口不分页；逐批验证 schema 一致后同步交付，避免方法内部聚集所有批数据。结果携带 requested Subjects，空结果也保留批范围；回调错误/取消立即停止，已交付局部数据不代表全集成功。root storageio race 重复 3 次、独立定向 race 重复 10 次通过。当前仍需上层获取权威 schema、接入 Manager、建立覆盖和来源变更栅栏；该方法不自行证明跨批快照一致性，不能供截面直接拼面板。
- 完整回源支持 schema 缺失时按需探测：对请求中的首标的使用同一索引/契约/时间范围、limit=1 取得完整 schema，再按原 lookback 与实际列宽分批。探测行不作为历史完成结果交付；后续任一批 schema 漂移或探测契约失配均返回错误，已交付首批不冒充整体成功。root storageio race 重复 3 次、独立定向 race 重复 10 次通过。该探测仅由读取需求触发，不做启动预回填；仍未接通 Manager 命中、窗口覆盖及来源修订失效。
- 新增 inputcache.Runtime，统一拥有 Manager 与默认延迟首轮的同步维护 Job。Close 在同一 admission 锁下停止新维护、取消并 drain 已进入的维护，再由 Manager drain 读写/关库/释放目录锁；并发 Close 幂等。真实目录增长测试验证首 tick 不执行、到期检查暂停写入；阻塞 Handle.Use 验证两个 Close 都等待读者退出，之后目录可重开。root cache race 重复 3 次、独立定向 race 重复 10 次通过。Runtime 构造不注册 tRPC 服务、不启动 goroutine；仍须接入引擎资源与服务注册，并完成实际缓存读路径后才能解除 engine 的 cache.enabled 阶段限制。
- EngineResources 已持有可选 Cache Runtime：DB 前验证缓存配置，启用才创建；失败取消并关闭已打开的运行库，正常关闭顺序为 Python、Cache、Store。测试验证启用期间目录独占、关闭后可重开、禁用不建目录，以及路径错误修正后可恢复。root bootstrap race 重复 3 次、独立定向 race 重复 10 次通过。InitializeEngine 仍在打开资源前拒绝 cache.enabled，等待服务注册和正确的 read-through/覆盖失效链路，未据资源装配解除阶段限制。
- 已加入引擎缓存 tRPC 维护注册点：资源打开且失败清理 defer 建立后，目录/计算启动前注册 owned Job；nil Cache 不注册，缺 service/Job 或注册失败返回错误。默认 YAML 仅注释展示可选 timer 配置，避免禁用缓存时启动未注册的服务，并注明 transport timeout 不短于 rebuild_timeout。root bootstrap race 重复 3 次、独立定向 race 重复 20 次通过。入口 enabled gate 仍保留，因此尚未在真实引擎启动中运行该 timer；实际 read-through/覆盖失效完成后再解除。

- 已新增 `cmd/engine` 独立入口及 engine app/tRPC 示例，串接资源、共享 gate 下目录激活、Python、subject consumer 和独立 Health；无 FactorMgr。关机先取消再 drain subject/catalog，最后关闭资源。当前显式拒绝 cache.enabled 和非时序目录，避免把未接通能力静默当作成功；这是实施阶段限制，后续必须随缓存/截面接通移除，不是最终设计取舍。
- `scripts/build/build.sh factor-engine` 实际构建成功（本机 darwin/amd64），二进制 `-h` 验证独立配置入口。factor/all 构建加入 engine；Factor 现有共享 bootstrap 导入 DuckDB，因此 server/cli/engine 构建需 CGO=1。新增构建路由契约测试通过；binary release 列表包含 engine，但部署脚本和正式 Linux 构建仍未验证。
- 新增真实 NATS catalog transport + SQLite replica + Python worker + subject consumer 的 InitializeEngine 接线集成测试，race 重复 3 次通过。tRPC 服务仅捕获注册、不监听端口；目录为空的 revision=1，因此不证明 Storage 回源、实际因子计算或写回。测试补齐 health 鉴权环境后通过，未绕过鉴权。
- 新增 ControlResources：打开权威 SQLite/schema、Registry 和沿用认证的 Metadata client，仅恢复并校验源码制品，不启动 Python、计算器、实时 consumer 或网络监听。取消不关闭在途 RPC 使用的数据库，调用者 drain 后并发幂等 Close；源码 hash 失败关闭资源，修正后可重开。root bootstrap race 重复 3 次及 Factor 全模块测试通过。旧 validateStartupFactorContracts 实际执行远程 reconcile，新资源层没有调用它；尚未替换旧 server main，不能据此认为控制面进程已完成拆分。
- 控制资源局部审查发现空 gateway_node_id 会在启动成功后使全部 Metadata 签名失败，已红测复现并在打开数据库前拒绝。gatewayauth 新增 ValidateTargetNode，复用 Sign/Verify 原有同一校验规则，不放宽身份；控制资源要求显式 node，不隐式环境回退。root bootstrap race 重复 3 次、gatewayauth race 和独立复核两者定向 race 重复 10 次通过，P2 已关闭。后续 engine 角色启动/配置也需核对相同可签名约束，不能由本控制资源测试推断正式 engine 鉴权已验证。
- 引擎资源现也复用同一 target-node 校验，非法节点即使配置了环境兜底仍拒绝，并且不创建运行库。控制/引擎校验均已前移到 validateRoleStorage 之前，保证配置了缺失凭据文件时仍先报告非法节点、不提前读取凭据；相关审查 P3 已关闭。正例 fixture 显式配置 storage-test，示例配置标注 node 为部署前必填。root bootstrap race 重复 3 次、独立定向 race 重复 10 次通过；这证明启动校验，不证明真实 Gateway/Storage 连通性。
- 控制目录关闭增加读者 admission 栅栏与 drain：真实 NATS 红测证明仅 Close 连接会在 SQLite 快照读取退出前返回；现关机先拒绝新读取、取消并等待已进入的读取，再 Close 连接，启动订阅失败也走相同清理。真实嵌入式 NATS 阻塞 reader 测试证明取消可达且等待 release，重复关闭幂等；root bootstrap race 重复 3 次、独立定向 race 重复 10 次通过。未注入 flush 失败同时存在回调的场景，完整控制面 server/main 接线仍未完成。

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

现已具备独立时序引擎入口，但控制面旧入口仍是一体化，缓存/截面/周期链路也未完整接通，不应部署当前代码作为最终交付。未删除任何线上数据，未对正式服务执行启停或发布。
