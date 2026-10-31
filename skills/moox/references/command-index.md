# moox-cli 命令清单

`moox-cli` 的全部命令，按一级命令分组；每个命令的参数用 `./bin/moox-cli <命令> --help` 查看（帮助里写明对应的接口与默认值）。
说明里写“默认只预览，--yes 才执行”的是破坏性命令：不带 `--yes` 只输出将要发出的请求，把预览给用户确认后再加 `--yes`。
用法速查与场景见 [`modules.md`](modules.md)，管理台页面到命令的对照见 [`api-recipes.md`](api-recipes.md)。
本清单由测试保证完整：新增命令没有写进这里，`modules/cli` 的 `TestSkillCommandIndexListsEveryCommand` 会失败。

## activity：全局活动日志

| 命令 | 说明 |
| --- | --- |
| `activity list` | 查看活动日志（如采集标的变更），按时间倒序 |

## archive：数据归档：任务、运行与分区（调用 ArchiveMgr）

| 命令 | 说明 |
| --- | --- |
| `archive overview` | worker、正在执行的运行与告警总览 |
| `archive partitions get` | 查看分区详情（状态、文件、sha256、预览） |
| `archive partitions list` | 列出分区 |
| `archive runs cancel` | 取消排队中或运行中的运行 |
| `archive runs get` | 查看一次运行（含无法补齐的区间） |
| `archive runs list` | 列出运行记录 |
| `archive tasks backfill` | 补拉指定数据时间区间 |
| `archive tasks create` | 创建归档任务 |
| `archive tasks delete` | 删除没有分区的任务 |
| `archive tasks list` | 列出归档任务（--space-id 为空表示全部空间） |
| `archive tasks prepare` | 查看数据集的频率、保留期、推荐执行计划与最早回补起点 |
| `archive tasks preview` | 校验执行计划并预览接下来 5 次运行与读取区间 |
| `archive tasks run` | 立即运行一次 |
| `archive tasks update` | 修改执行计划或启停任务 |
| `archive worker retire` | 退役一个不再使用的 worker 登记（例如换机后的旧记录） |

## cloud：云函数：云账户、函数实例、SCF 导入与代码包（调用 CloudFunctionMgr）

| 命令 | 说明 |
| --- | --- |
| `cloud account delete` | 删除云账户登记（默认只预览，--yes 才执行） |
| `cloud account list` | 列出云账户（不含密钥明文） |
| `cloud account regions` | 列出云厂商可用的地域 |
| `cloud function create` | 创建函数实例（请求体用 --data，字段 instances；创建后用 job 查看进度） |
| `cloud function delete` | 删除函数实例（--instance-id 可重复；默认只预览，--yes 才执行） |
| `cloud function deploy` | 把代码包部署到函数实例（线上切换版本；默认只预览，--yes 才执行） |
| `cloud function invoke` | 调用一个函数实例（会真实执行函数；默认只预览事件内容，--yes 才执行） |
| `cloud function job` | 查看函数作业进度 |
| `cloud function list` | 列出函数实例 |
| `cloud function update-config` | 修改函数实例的运行配置与定时器（默认只预览，--yes 才执行） |
| `cloud package delete` | 删除代码包（默认只预览，--yes 才执行） |
| `cloud package list` | 列出函数代码包 |
| `cloud package show` | 查看代码包 |
| `cloud package url` | 取代码包的下载地址 |
| `cloud scf import` | 把云上已有的 SCF 函数导入登记（函数列表写在 --functions 里；默认只预览，--yes 才执行） |
| `cloud scf preview` | 预览云上有哪些 SCF 函数可以导入 |

## collector：采集：任务、运行状态、补采与合成、标的、采集方式与云函数包

| 命令 | 说明 |
| --- | --- |
| `collector data-type list` | 列出采集数据类型 |
| `collector data-type show` | 查看数据类型及其字段 |
| `collector fetch-plan pause` | 暂停空间的采集：停用全部 SCF 定时器，并停止采集计划对账与补采（默认只预览，--yes 才执行） |
| `collector fetch-plan reregister` | Storage 数据被清空后，让采集计划重新登记序列快照并重置补采状态 |
| `collector fetch-plan resume` | 恢复空间的采集：重新启用 SCF 定时器并继续采集计划对账与补采 |
| `collector function delete` | 异步删除当前 space 中全部 SCF 云函数 |
| `collector function package` | 构建数据采集器 SCF zip 包 |
| `collector function probe-egress` | 逐个调用短时 SCF 诊断公网出口和 Provider 连通性 |
| `collector function timer-inventory` | 只读盘点指定 SCF Timer fleet 并校验云侧 readback |
| `collector function upload` | 构建（或使用现成的）SCF 代码包并只上传，不创建函数 |
| `collector ingest-mode get` | 查看数据集的采集方式、执行端、交接中的生效周期与 collector-stream 实例状态 |
| `collector ingest-mode set` | 切换数据集的采集方式（按生效周期交接；不带 --dataset-id 表示空间全部数据集） |
| `collector kline absences-clear` | 清掉数据集已确认的“行情源缺失”（行情源后来补上数据时），并让历史扫描重新进行 |
| `collector kline backfill` | 补采缺失的 K 线（默认补检查出的缺口，--force 强制重拉整个范围） |
| `collector kline backfill-cancel` | 取消补采作业（运行中的分块等其结束） |
| `collector kline backfill-retry` | 重试已失败补采作业的失败分块 |
| `collector kline backfill-status` | 查看补采作业进度；不带 --job-id 时列出空间最近的作业 |
| `collector kline check` | 按采集计划检查时间范围内的 K 线是否齐全，输出缺口 |
| `collector kline history-confirm` | 按最近一次历史扫描发现的缺口补采；行情源本来没有的 K 线会被确认并不再报告 |
| `collector resample backfill` | 提交历史合成作业：把时间范围内的目标 K 线按源数据重新合成 |
| `collector resample cancel` | 取消排队或运行中的历史合成作业（已写入的保留） |
| `collector resample jobs` | 查看历史合成作业；带 --job-id 查看单个作业，否则按任务、状态列出最近的作业 |
| `collector resample preview` | 预览合成的源任务与目标频率是否可行 |
| `collector status` | 查看空间的采集运行状态：总体健康、各数据集的自动巡检进度与缺口、补采汇总 |
| `collector subjects exclude` | 把标的从采集计划里临时去掉（交易所公告下架、数据异常等） |
| `collector subjects exclusions` | 列出空间当前生效的排除（设置人、原因、设置与到期时间） |
| `collector subjects listers` | 列出可用的标的来源（交易所列表器） |
| `collector subjects status` | 各标签最近一次标的同步的状态 |
| `collector subjects sync` | 立即同步币安/东财的标的标签成员，不等整点定时任务 |
| `collector subjects unexclude` | 解除排除：标的从新的生效周期起回到采集（期间的历史需要时用补采） |
| `collector task create` | 创建 K 线采集任务（可直接指定 SCF 定时模式与每函数标的数） |
| `collector task delete` | 删除采集任务及其结果（默认只预览，--yes 才执行） |
| `collector task delete-result` | 删除没有任务引用的采集结果（View、行数据与数据集；默认只预览，--yes 才执行） |
| `collector task disable` | 停用采集任务（默认只预览，--yes 才执行） |
| `collector task inventory` | 查看采集结果清单（任务拥有的结果数据集与 View） |
| `collector task list` | 列出采集任务 |
| `collector task purge` | 清理 Collector 任务运行数据和结果引用 |
| `collector task show` | 查看一个采集任务 |
| `collector task update` | 修改采集任务的名称、说明或启用状态 |

## config：管理本机的 moox.toml 与告警渠道

| 命令 | 说明 |
| --- | --- |
| `config backup` | 把 moox.toml 加密备份到自己的管理后台 |
| `config edit` | 在本机打开配置页，查看并修改 moox.toml |
| `config encrypt` | 把明文保存的 moox.toml 加密保存 |
| `config get` | 读取 moox.toml 里的配置（密钥类默认打码） |
| `config init` | 在本机打开一次性表单，填写部署信息，生成 moox.toml |
| `config migrate` | 把 moox.toml 改写成当前格式（默认只预览） |
| `config notification` | 对比 moox.toml 与 Monitor 的告警渠道，确认后写入 Monitor，并显示各主机缓存版本 |
| `config restore` | 换电脑时从管理台恢复 moox.toml |
| `config set` | 修改 moox.toml 里一个已有的单值配置（默认只预览） |

## data：量化数据读写工具

| 命令 | 说明 |
| --- | --- |
| `data kline get` | 获取 K 线数据 |
| `data records search` | 检索记录型 View（Bleve）的数据 |
| `data rows count` | 按 View 统计时序行数（selectors 与时间范围写在 --data 里） |
| `data rows export` | 导出 Dataset 行为 JSON |
| `data skill kline get` | 获取 K 线数据 |
| `data view query` | 按 View 查询时序行（管理台“数据浏览”用的接口；selectors、过滤与排序写在 --data 或对应参数里） |

## doctor：部署与运行诊断：告警、新部署检查、按监控事实诊断模块

| 命令 | 说明 |
| --- | --- |
| `doctor alerts` | 读取 Monitor 的告警与不健康项，供 Agent 处理 |
| `doctor bootstrap` | 检查新部署各模块的安装与配置 |
| `doctor diagnose` | 按 Monitor 的事实诊断模块 |

## eventbus：EventBus：流、消费者与事件概览（调用 EventBusMgr，只读）

| 命令 | 说明 |
| --- | --- |
| `eventbus consumer` | 查看一个消费者的积压与投递状态 |
| `eventbus consumers` | 列出消费者 |
| `eventbus events` | 列出最近的事件 |
| `eventbus overview` | EventBus 概览：连接数、流、消费者与积压 |
| `eventbus streams` | 列出流 |

## factor：因子定义、分类、计算任务、成员与补算（调用 FactorMgr）

| 命令 | 说明 |
| --- | --- |
| `factor category assign` | 把因子归入分类 |
| `factor category delete` | 删除因子分类（默认只预览，--yes 才执行） |
| `factor category list` | 列出因子分类 |
| `factor category upsert` | 新建或修改因子分类 |
| `factor create` | 创建因子定义（表达式写在 --spec-file 里，写法见 skill 的 factor-dsl） |
| `factor delete` | 删除因子定义（默认只预览，--yes 才执行） |
| `factor functions` | 列出表达式里可用的函数 |
| `factor list` | 列出因子定义 |
| `factor member add` | 给计算任务加因子成员（固定参数用 --param 名=数值） |
| `factor member disable` | 停用因子成员（默认只预览，--yes 才执行） |
| `factor member enable` | 启用因子成员 |
| `factor member remove` | 移除计算任务的因子成员（默认只预览，--yes 才执行） |
| `factor preview` | 在真实数据上试算因子表达式（不落库） |
| `factor recalc cancel` | 取消补算作业 |
| `factor recalc list` | 列出补算作业 |
| `factor recalc show` | 查看一个补算作业 |
| `factor recalc submit` | 提交历史补算（时间左闭右开；不给 --request-id 时自动生成） |
| `factor show` | 查看一个因子定义 |
| `factor status` | 因子运行状态（各任务的预热、排队与暂停） |
| `factor task create` | 创建计算任务（源数据集 + 频率 → 结果数据集）；批量配置优先写进 moox.toml 后用 setup factors |
| `factor task delete` | 删除计算任务（--purge 连结果一起删，不可恢复；默认只预览，--yes 才执行） |
| `factor task disable` | 停用计算任务（默认只预览，--yes 才执行） |
| `factor task enable` | 启用计算任务 |
| `factor task list` | 列出因子计算任务 |
| `factor task params` | 查看计算任务各成员当前的参数 |
| `factor task show` | 查看一个计算任务 |
| `factor update` | 修改因子定义（被启用成员引用的定义不能改） |
| `factor validate` | 线上校验因子表达式（不落库） |

## gateway：直接调用内部服务（没有专用命令的管理接口）

| 命令 | 说明 |
| --- | --- |
| `gateway call` | 调用内部服务方法（JSON） |
| `gateway methods` | 列出 gateway call 可调用的内部服务与方法（只读，不连网） |

## host：主机：登记查询、远程执行与文件传输

| 命令 | 说明 |
| --- | --- |
| `host agent run-once` | 让主机代理立即采集并上报一次 |
| `host agent snapshot` | 主机代理最近一次的资源快照 |
| `host agent status` | 主机代理的采集与上报状态 |
| `host catalog` | 服务目录：组件、端口与服务 |
| `host deployments` | 列出组件部署 |
| `host directory` | 服务目录的路由视图：每个服务部署在哪些主机 |
| `host exec` | 经 SSH 在 moox.toml 登记的主机上执行一条命令 |
| `host get` | 把主机上的文件下载到本地（本地已存在时需要 --yes 覆盖） |
| `host list` | 列出 Admin 登记的主机（moox.toml 里的候选主机用 setup hosts） |
| `host put` | 把本地文件上传到主机（远端已存在时默认只预览，--yes 才覆盖） |
| `host routes` | 查看一台主机的网关路由 |

## import：导入历史时序数据（写 Storage 和因子机本地存储）

| 命令 | 说明 |
| --- | --- |
| `import` | 导入历史时序数据（写 Storage 和因子机本地存储） |

## lifecycle：查看和推进模块、对象的生命周期（未就绪 → 就绪 → 试运行 → 运行）

| 命令 | 说明 |
| --- | --- |
| `lifecycle catchup` | 查看恢复追赶的进度 |
| `lifecycle check` | 启动前检查：列出不通过的项和处理办法，不改变阶段 |
| `lifecycle confirm` | 确认：试运行 → 运行 |
| `lifecycle init` | 初始化：未就绪 → 就绪（准备本地状态与历史） |
| `lifecycle pause` | 暂停：不再计算新周期（--for 可设定自动恢复） |
| `lifecycle release` | 放行：追赶已追平的对象回到暂停前的阶段 |
| `lifecycle reset-abort` | 放弃失败的重置：模块置为未初始化，已做的步骤不会回来 |
| `lifecycle reset-apply` | 发起重置（--token --scope）或续做（--operation-id）；模块必须已暂停 |
| `lifecycle reset-plan` | 重置计划：只读，列出每一步、保留什么、不备份什么，并给出一次性令牌（10 分钟） |
| `lifecycle reset-status` | 查看重置状态与步骤日志 |
| `lifecycle resume` | 恢复：先追赶暂停期间的周期，追平后放行 |
| `lifecycle shadow` | 查看试运行中的策略实例的影子目标（固定连接 trade 模块） |
| `lifecycle start` | 启动：就绪 → 试运行（完整计算，但不真实下单） |
| `lifecycle status` | 查看生命周期状态 |

## metadata：存储元数据管理工具

| 命令 | 说明 |
| --- | --- |
| `metadata apply` | 创建并校验存储元数据 seed |
| `metadata dataset activate` | 激活数据集 |
| `metadata dataset activation` | 检查数据集能否激活 |
| `metadata dataset add-column` | 给数据集加列（或修改列） |
| `metadata dataset columns` | 列出数据集的列 |
| `metadata dataset create` | 创建数据集（保留期不在这里设：由 Storage 的保留策略文件按空间与频率决定） |
| `metadata dataset delete` | 删除数据集的元数据（默认只预览，--yes 才执行；删除采集结果用 collector task delete-result） |
| `metadata dataset list` | 列出数据集 |
| `metadata dataset rebind` | 把数据集换绑到另一个存储节点（默认只预览，--yes 才执行） |
| `metadata dataset show` | 查看数据集 |
| `metadata dataset subjects` | 列出数据集里有数据的标的 |
| `metadata dataset update` | 修改数据集的名称、说明与状态（数据来源、类型、频率、存储节点创建后不可变，换节点用 rebind） |
| `metadata device create` | 登记存储设备 |
| `metadata device list` | 列出存储设备 |
| `metadata device show` | 查看存储设备 |
| `metadata device update` | 修改存储设备 |
| `metadata field batch-update` | 批量调整字段的分组或状态 |
| `metadata field create` | 创建字段 |
| `metadata field list` | 列出字段 |
| `metadata field show` | 查看字段 |
| `metadata field update` | 修改字段 |
| `metadata field-group create` | 创建字段分组 |
| `metadata field-group delete` | 删除字段分组（默认只预览，--yes 才执行） |
| `metadata field-group list` | 列出字段分组 |
| `metadata field-group show` | 查看字段分组 |
| `metadata field-group update` | 修改字段分组 |
| `metadata import` | 导入存储元数据 seed |
| `metadata source create` | 创建数据来源 |
| `metadata source delete` | 删除数据来源（默认只预览，--yes 才执行） |
| `metadata source list` | 列出数据来源 |
| `metadata source show` | 查看数据来源 |
| `metadata source update` | 修改数据来源 |
| `metadata spaces` | 列出 metadata seed 可导入的业务空间 |
| `metadata subject list` | 列出标的 |
| `metadata subject resolve` | 把标签展开成标的 |
| `metadata subject show` | 查看标的 |
| `metadata subject upsert` | 新建或修改标的 |
| `metadata tag clone` | 由已有标签的前 N 个启用成员创建一个手动标签（用于 canary 等小规模验证） |
| `metadata tag delete` | 删除标签（被数据集引用时会被拒绝；默认只预览，--yes 才执行） |
| `metadata tag list` | 列出标签 |
| `metadata tag list-members` | 列出标签成员 |
| `metadata tag member-status` | 启用或停用标签成员（--status active\|inactive） |
| `metadata tag members` | 给手动标签增删成员（用于验证标的变更对采集计划的影响） |
| `metadata tag show` | 查看标签 |
| `metadata tag upsert` | 新建或修改标签 |
| `metadata view add-column` | 给 View 加列（会增加存储机内存占用） |
| `metadata view columns` | 列出 View 的列 |
| `metadata view create` | 创建 View（会增加存储机内存占用，先按 skill 的 storage.md 估算） |
| `metadata view delete` | 删除 View（默认只预览，--yes 才执行；离线删除用 storage delete-view） |
| `metadata view list` | 列出 View |
| `metadata view show` | 查看 View |
| `metadata view update` | 修改 View 定义 |

## monitor：Monitor：健康总览、数据到达历史、主机指标与告警屏蔽

| 命令 | 说明 |
| --- | --- |
| `monitor arrival` | 数据到达历史：某天每个周期何时到达 |
| `monitor health` | 健康总览：各模块、数据新鲜度与待处理项（排障先看 doctor alerts） |
| `monitor host-metrics` | 主机资源指标的历史曲线 |
| `monitor hosts` | 列出已上报资源的主机代理 |
| `monitor mute` | 屏蔽告警推送一段时间（到期自动恢复，最长 24 小时） |
| `monitor mute-status` | 查看告警屏蔽状态 |
| `monitor unmute` | 提前结束告警屏蔽，并补发一条汇总 |

## ops：云资源运维工具

| 命令 | 说明 |
| --- | --- |
| `ops tencent cls bootstrap` | 幂等开通 CLS 并创建 MooX 日志集、主题和索引 |
| `ops tencent cls prepare` | 发布前从 MooX 云账户准备固定 CLS 资源 |
| `ops tencent cls resolve` | 查询既有 MooX CLS 资源，不创建或修改资源 |
| `ops tencent lighthouse firewall add` | 添加轻量应用服务器防火墙规则 |
| `ops tencent lighthouse firewall open` | 通过控制面云账户凭证开放轻量防火墙端口 |

## replay：逐期回放：按实盘时序用历史数据验证因子、策略与成交（不产生实际下单）

| 命令 | 说明 |
| --- | --- |
| `replay` | 逐期回放：按实盘时序用历史数据验证因子、策略与成交（不产生实际下单） |

## secret：密钥：只列清单与打码后的信息；新建、修改请用户在管理台“设置 › 密钥”填写

| 命令 | 说明 |
| --- | --- |
| `secret list` | 列出密钥（只含打码后的信息） |
| `secret show` | 查看一个密钥的元信息（值已打码） |

## setup：部署与初始化：主机、组件部署、防火墙、发布与初始数据

| 命令 | 说明 |
| --- | --- |
| `setup apply` | 写入初始用户与云凭据 |
| `setup attach-scf-vpc` | 为腾讯云函数绑定 VPC 子网（默认只预览，--yes 才绑定） |
| `setup bootstrap` | 通过已核验 SSH 初始化核心或完整控制服务 |
| `setup build-linux` | 在本机 Docker 里构建需要 CGO 的 Linux 二进制 |
| `setup deploy-host` | 通过已核验 SSH 部署指定主机的 Host Gateway 与 Host Agent |
| `setup deploy-unit` | 通过已核验 SSH 部署业务单元 |
| `setup deployment disable` | 停用部署：外部程序先停服务并核对已停止，再改登记；普通组件只改登记 |
| `setup deployment enable` | 启用部署：外部程序要求主机已启用，先改登记再启动服务；普通组件只改登记 |
| `setup export-skill-config` | 导出 Skill 使用的最小数据访问配置 |
| `setup factors` | 按 moox.toml 的 [[factors.definitions]] 与 [[factors.tasks]] 补齐因子定义、任务与成员（只补缺不删除） |
| `setup firewall` | 初始化所有目标主机的腾讯云防火墙端口（默认只预览，--yes 才执行） |
| `setup host disable` | 停用主机：先停掉它上面启用的外部程序，全部成功后才改登记（网关路由随之撤销） |
| `setup host enable` | 启用主机：先改登记，再只启动启用部署的外部程序（单独停用的程序保持停用） |
| `setup host-diagnostics` | 检查远端主机磁盘和服务进程 |
| `setup hosts` | 列出可选的部署主机（不输出密码） |
| `setup init` | 导入默认空间、数据集、字段和因子 |
| `setup inspect-scf` | 检查腾讯云函数网络配置 |
| `setup invoke-scf` | 直接调用腾讯云函数 |
| `setup metadata-import` | 通过 Storage SSH 隧道导入选定业务空间 |
| `setup package inspect` | 校验部署包的组件边界、平台、文件与摘要 |
| `setup reboot-host` | 重启无响应的腾讯云主机（默认只预览，--yes 才重启） |
| `setup release build` | 在本机构建并打包一个版本的全部纯 Go 单元 |
| `setup remove-host` | 从 Admin 删除一台已不在 moox.toml 里的主机（默认只预览） |
| `setup render-runtime-config` | 从 moox.toml 渲染 Egress/Collector 运行配置 |
| `setup status` | 检查初始化记录状态 |
| `setup storage-auth` | 把 Storage 内部写入身份保存到本机操作员目录（导入、导出数据用，只需一次） |
| `setup sync-deployments` | 把 moox.toml 中指定主机的组件部署同步给 Admin（生成网关路由；默认只预览，--yes 才执行） |
| `setup trust-browser` | 检查并安装管理台浏览器证书信任 |
| `setup trust-host` | 确认并记录 SSH 主机指纹 |
| `setup validate` | 校验初始化配置和连接，并报告目标机器的部署情况与发布路线（route） |
| `setup watchdog` | 在各主机安装（或移除）主机级守护与告警策略种子 |
| `setup wipe` | 彻底清除 moox.toml 所列主机上全部 MooX 旧程序和数据（默认只预览，不可恢复） |

## space：空间：一组相互隔离的业务数据（调用 SpaceMgr）

| 命令 | 说明 |
| --- | --- |
| `space create` | 新建空间（还要在 Storage 建元数据：数据来源、数据集、字段，见 metadata apply） |
| `space delete` | 删除空间（默认只预览，--yes 才执行） |
| `space list` | 列出空间 |
| `space show` | 查看 Storage 里一个空间的元数据 |
| `space update` | 修改空间 |

## storage：存储节点登记、View 维护与元数据重置

| 命令 | 说明 |
| --- | --- |
| `storage delete-view` | 离线删除一个 View 的元数据（不带 --yes 时存储机上的程序会拒绝执行） |
| `storage force-rebuild-view` | 删除旧 View、清空 durable consumer 状态并从头重建（默认只预览，--yes 才执行） |
| `storage node delete` | 删除一个存储节点登记 |
| `storage node list` | 列出存储节点登记 |
| `storage node show` | 查看存储节点登记 |
| `storage node update` | 修改存储节点的名称或状态 |
| `storage period-inventory` | 只读盘点 DataNode 的周期完成标记（要求 DataNode 没有打开该数据库） |
| `storage purge-dataset-events` | 精确清理一个数据集在 EventBus 里的历史事件（默认只预览，--yes 才执行） |
| `storage repair-view` | 清理 View 消费积压并重建索引（默认只预览，--yes 才执行） |
| `storage reset-metadata` | 备份后清空全部业务定义并重新导入种子（Storage 必须已停止；默认只预览，--yes 才执行） |
| `storage reset-view-consumers` | 删除所有 View durable/历史消息/索引并按回溯窗口重新构建（默认只预览，--yes 才执行） |

## strategy：策略定义、实例、结果、历史回放与模板（调用 StrategyMgr）

| 命令 | 说明 |
| --- | --- |
| `strategy create` | 创建策略定义（DSL 写在 --dsl-file 里，写法见 skill 的 strategy-dsl） |
| `strategy delete` | 删除策略定义（默认只预览，--yes 才执行） |
| `strategy display get` | 查看实例的展示设置 |
| `strategy display set` | 设置实例要隐藏的分类、成员与列 |
| `strategy instance create` | 创建策略实例（默认停用，确认无误再 enable；也可加 --enable 创建即启用） |
| `strategy instance disable` | 停用策略实例（默认只预览，--yes 才执行） |
| `strategy instance enable` | 启用策略实例（挂了账户的实例启用后会按目标下单；默认只预览，--yes 才执行） |
| `strategy instance list` | 列出策略实例（--space-id 过滤空间，默认 crypto） |
| `strategy instance show` | 查看一个策略实例 |
| `strategy instance update` | 修改停用中的策略实例绑定的策略定义与账户（因子任务创建时写定，不能改） |
| `strategy list` | 列出策略定义 |
| `strategy replay bars` | 查看回放逐期明细 |
| `strategy replay cancel` | 取消回放 |
| `strategy replay list` | 列出策略历史回放 |
| `strategy replay show` | 查看一次回放 |
| `strategy replay start` | 开始一次策略历史回放 |
| `strategy result list` | 列出实例每期的结果 |
| `strategy result show` | 查看一期结果 |
| `strategy show` | 查看一个策略定义 |
| `strategy target list` | 查看实例当前的目标权重 |
| `strategy template delete` | 删除模板（默认只预览，--yes 才执行） |
| `strategy template list` | 列出策略模板 |
| `strategy template render` | 用变量渲染模板，得到策略 DSL |
| `strategy template save` | 保存模板 |
| `strategy template show` | 查看一个模板 |
| `strategy update` | 修改策略定义（引用它的实例要先停用） |
| `strategy validate` | 试算策略 DSL（不落库） |

## system：系统级暂停与恢复（按依赖顺序逐个模块，恢复先追赶补采、全部追平后统一放行）

| 命令 | 说明 |
| --- | --- |
| `system pause` | 系统暂停：下游到上游逐个暂停（默认只预览，--yes 才执行） |
| `system reset abort` | 放弃未完成的系统重置（只标记日志，不撤销已做的步骤，不动各模块） |
| `system reset apply` | 执行系统重置（--token）或续做（--operation-id） |
| `system reset plan` | 系统重置计划（只读），给出令牌 |
| `system reset status` | 查看系统重置日志 |
| `system resume` | 系统恢复：先追赶补采，全部追平后统一放行 |
| `system status` | 查看各模块的阶段与恢复追赶进度 |

## trade：交易：只读查询（账户、订单、成交、持仓、权益）；写操作不提供命令

| 命令 | 说明 |
| --- | --- |
| `trade account list` | 列出交易账户 |
| `trade account show` | 查看交易账户 |
| `trade action` | 查看一次操作的执行结果 |
| `trade capabilities` | 查看交易账户的执行能力 |
| `trade equity` | 查询权益曲线 |
| `trade fills` | 列出成交 |
| `trade holdings` | 列出账户的持仓明细 |
| `trade logical list` | 列出逻辑账户 |
| `trade logical show` | 查看逻辑账户 |
| `trade logical target` | 查看逻辑账户当前的目标 |
| `trade order list` | 列出订单 |
| `trade order show` | 查看订单 |
| `trade positions` | 列出持仓 |

## udf：自定义算子（wasm UDF）：new → build → publish → preview → activate

| 命令 | 说明 |
| --- | --- |
| `udf activate` | 切换启用版本（默认只看影响清单与前提，--yes 才切换） |
| `udf build` | 本地门禁并打包 |
| `udf list` | 列出自定义算子 |
| `udf new` | 生成 UDF 骨架 |
| `udf preview` | 用未启用的版本在真实数据上试算 |
| `udf publish` | 上传并复核，存为不可变版本（未启用） |
| `udf show` | 查看一个自定义算子的版本、引用与引擎模块状态 |
