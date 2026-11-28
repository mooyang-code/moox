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
| `archive runs list` | 列出运行记录（默认不列成功的跟随运行，`--include-follow` 列出全部） |
| `archive tasks backfill` | 补拉指定数据时间区间 |
| `archive tasks clear-unrecoverable` | 删掉任务累积的、整段落在 [start, end) 里的无法补齐区间（导入补齐历史缺口后由人确认再清，`--yes`） |
| `archive tasks create` | 创建归档任务（之后每分钟按 Storage 的变更日志跟随；首次运行从回补起点整段读到现在；`--backfill-from` 为空取线上保留期起点，forever 时必填，建好后不能改） |
| `archive tasks delete` | 删除没有分区的任务 |
| `archive tasks list` | 列出归档任务（--space-id 为空表示全部空间） |
| `archive tasks prepare` | 查看数据集的频率、保留期、最早回补起点与建议的目录名 |
| `archive tasks run` | 整段重读线上保留期内的全部数据（之后每分钟的跟随分段读完，数据量大时要几个小时） |
| `archive tasks update` | 启停任务（`--status`）或清除无法补齐区间（`--clear-unrecoverable`） |

## collector：采集：任务、运行状态、补采与合成、标的、采集方式、云函数、云账号与代码包

| 命令 | 说明 |
| --- | --- |
| `collector account delete` | 删除云账号登记（默认只预览，--yes 才执行；还被 scf_plan 引用、还有代码包或名下还有函数时拒绝。旧账号下建的函数 collector function delete 删不到，要用 setup inspect-scf 查出来再到腾讯云控制台手工删） |
| `collector account list` | 列出云账号（不含密钥明文） |
| `collector account regions` | 列出腾讯云函数可用的地域 |
| `collector data-type list` | 列出采集数据类型 |
| `collector data-type show` | 查看数据类型及其字段 |
| `collector fetch-plan pause` | 暂停空间的采集：停用全部 SCF 定时器，并停止采集计划对账与补采（默认只预览，--yes 才执行） |
| `collector fetch-plan reregister` | Storage 数据被清空后，让采集计划重新登记序列快照并重置补采状态 |
| `collector fetch-plan resume` | 恢复空间的采集：重新启用 SCF 定时器并继续采集计划对账与补采 |
| `collector function delete` | 按名字或前缀删除空间的云函数（计划里的函数删掉后下一轮对账会重建；默认只预览，--yes 才执行） |
| `collector function list` | 列出空间的采集函数与补采函数：计划里应有的内容、云上读回的状态，计划外的函数单列（--drift 只看不一致的） |
| `collector function package` | 构建数据采集器 SCF zip 包 |
| `collector function probe-egress` | 调用已生效计划里的各采集函数，取它们此刻的公网出口 IP |
| `collector function upload` | 构建（或使用现成的）SCF 代码包并只上传，不创建函数 |
| `collector ingest-mode get` | 查看各 K 线任务的采集方式、交接中的生效周期与 collector-stream 实例状态 |
| `collector ingest-mode set` | 切换采集方式（按生效周期交接；不带 --dataset-id 表示空间全部 K 线任务） |
| `collector history-seed skip` | 不等首次接入的历史补齐了，立即放行下游：取消剩余分块、状态记已手动放行，已补进去的数据保留（`--dataset-id` 留空表示空间内全部补齐中的数据集） |
| `collector kline absences-clear` | 清掉数据集已确认的“行情源缺失”（行情源后来补上数据时），之后的检查与补采重新判断这些 K 线 |
| `collector kline backfill` | 补采缺失的 K 线（默认补检查出的缺口，--force 强制重拉整个范围） |
| `collector kline backfill-cancel` | 取消补采作业（运行中的分块等其结束） |
| `collector kline backfill-retry` | 重试已失败补采作业的失败分块 |
| `collector kline backfill-status` | 查看补采作业进度；不带 --job-id 时列出空间最近的作业 |
| `collector kline check` | 按采集计划检查时间范围内的 K 线是否齐全，输出缺口 |
| `collector package delete` | 删除代码包（先删 COS 对象再删记录；默认只预览，--yes 才执行） |
| `collector package list` | 列出空间的云函数代码包（按上传先后倒序，每个包名保留最新 3 个） |
| `collector resample backfill` | 提交历史合成作业：把时间范围内的目标 K 线按源数据重新合成 |
| `collector resample cancel` | 取消排队或运行中的历史合成作业（已写入的保留） |
| `collector resample jobs` | 查看历史合成作业；带 --job-id 查看单个作业，否则按任务、状态列出最近的作业 |
| `collector resample preview` | 预览合成的源任务与目标频率是否可行 |
| `collector status` | 查看空间的采集运行状态：总体健康、各数据集的自动巡检进度与缺口、补采汇总、首次接入的历史补齐进度 |
| `collector delist register` | 手工登记标的下架，补漏报或演练（会发布“标的即将下架”事件，Trade 随即禁止买入，到 T−24h 自动卖出策略持有的仓位；默认只预览，--yes 前向用户确认标的和下架时间；演练用 --paper-only） |
| `collector subjects exclude` | 把标的从采集计划里临时去掉（交易所公告下架、数据异常等） |
| `collector subjects exclusions` | 列出空间当前生效的排除（设置人、原因、设置与到期时间） |
| `collector subjects listers` | 列出可用的标的来源（交易所列表器） |
| `collector subjects status` | 各标签最近一次标的同步的状态 |
| `collector subjects sync` | 立即同步币安/东财的标的标签成员，不等整点定时任务 |
| `collector subjects unexclude` | 解除排除：标的从新的生效周期起回到采集（期间的历史需要时用补采） |
| `collector task create` | 创建 K 线采集任务或合成任务（--source-task-id） |
| `collector task delete` | 删除采集任务及其结果（默认只预览，--yes 才执行） |
| `collector task delete-result` | 删除没有任务引用的采集结果（数据集、行数据与它的查询索引；默认只预览，--yes 才执行） |
| `collector task disable` | 停用采集任务（默认只预览，--yes 才执行） |
| `collector task inventory` | 查看采集结果清单（任务拥有的结果数据集） |
| `collector task list` | 列出采集任务 |
| `collector task show` | 查看一个采集任务 |
| `collector task update` | 修改采集任务的名称、说明或启用状态 |

## config：管理本机的 moox.toml 与 moox.secrets.toml

| 命令 | 说明 |
| --- | --- |
| `config edit` | 在本机打开配置页，查看并修改两份配置 |
| `config encrypt` | 把明文保存的 moox.secrets.toml 加密保存 |
| `config get` | 读取 moox.toml 或 moox.secrets.toml 里的配置（--file 指定哪一份，密钥类默认打码） |
| `config init` | 在本机打开一次性表单，填写部署信息，生成 moox.toml 与 moox.secrets.toml |
| `config set` | 修改 moox.toml 或 moox.secrets.toml 里一个已有的单值配置（默认只预览） |

## data：量化数据读写工具

| 命令 | 说明 |
| --- | --- |
| `data kline get` | 获取 K 线数据 |
| `data records search` | 检索记录型数据集（Bleve 索引）的数据 |
| `data rows count` | 按数据集统计查询索引里的时序行数（selectors 与时间范围写在 --data 里） |
| `data rows export` | 导出 Dataset 行为 JSON |
| `data skill kline get` | 获取 K 线数据 |
| `data view query` | 按数据集查询时序行（读它的查询索引；管理台看采集结果、因子结果用的接口；selectors、过滤与排序写在 --data 或对应参数里） |
| `data view state` | 查看数据集查询索引（View）的状态：活动索引、代次、已建列、覆盖范围与跟随变更日志的进度 |
| `data view wait-applied` | 等数据集的查询索引跟上此刻的变更日志头部（之前的写入都已可查） |

## doctor：部署与运行诊断：告警、新部署检查、按监控事实诊断模块

| 命令 | 说明 |
| --- | --- |
| `doctor alerts` | 读取 Monitor 的告警与不健康项，供 Agent 处理 |
| `doctor bootstrap` | 检查新部署各模块的安装与配置 |
| `doctor diagnose` | 按 Monitor 的事实诊断模块 |

各组件的健康端口（`/healthz`、`/readyz`、`/metrics`）只听 127.0.0.1、不验签：`doctor bootstrap` 支持检查本机或经 SSH 探测 `moox.toml` 登记的远端主机（无远端 SSH 能力时报错提示）；`doctor diagnose` 在组件给 Monitor 的上报缺失或过期时，经 SSH 在目标机上请求它的本机 `/readyz`（SSH 连不上记为无法判断），`systemd` 用户服务的状态同样经 SSH 读取。因子机只能出站，SSH 只有在家里的局域网才连得上，所以因子机的 doctor 与 `/readyz` 检查只能在局域网里做；不在局域网时看 Monitor 的上报状态（`doctor alerts`）和 `moox-cli host agent status --host factor`。

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
| `factor import` | 按目录批量导入因子定义（目录下全部 *.yaml；任一因子已存在或编译失败时整批拒绝） |
| `factor list` | 列出因子定义 |
| `factor member add` | 给计算任务加因子成员（固定参数用 --param 名=数值） |
| `factor member disable` | 停用因子成员（默认只预览，--yes 才执行） |
| `factor member enable` | 启用因子成员 |
| `factor member remove` | 移除计算任务的因子成员（默认只预览，--yes 才执行） |
| `factor preview` | 在真实数据上试算因子表达式（不落库） |
| `factor purge-dataset` | 删除没有因子任务引用的孤立因子结果数据集（行与元数据，不可恢复；默认只预览，--yes 才执行） |
| `factor recalc cancel` | 取消补算作业 |
| `factor recalc list` | 列出补算作业 |
| `factor recalc show` | 查看一个补算作业 |
| `factor recalc submit` | 提交历史补算（时间左闭右开；不给 --request-id 时自动生成） |
| `factor show` | 查看一个因子定义 |
| `factor status` | 因子运行状态（各任务的预热、排队、暂停与计算前检查；单个任务的检查看 factor task status） |
| `factor task apply` | 按因子任务文件新建因子定义、任务与成员（只建不改；线上已有且内容不同时逐项列出差异，一项都不写） |
| `factor task create` | 创建计算任务（源数据集 + 频率 → 结果数据集）；批量配置优先写成因子任务文件后用 factor task apply |
| `factor task delete` | 删除计算任务（先 factor task disable 停用任务，再逐个 factor member disable、factor member remove 移除全部成员才能删；已关联策略实例的不能删，随实例删除；--purge 连结果一起删，不可恢复；默认只预览，--yes 才执行） |
| `factor task disable` | 停用计算任务（默认只预览，--yes 才执行） |
| `factor task enable` | 启用计算任务 |
| `factor task list` | 列出因子计算任务 |
| `factor task params` | 查看计算任务各成员当前的参数 |
| `factor task show` | 查看一个计算任务 |
| `factor task status` | 查看因子任务能不能计算：引擎计算前的逐项检查（窗口载入、数据覆盖、变更同步、回看数量、自定义算子模块） |
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
| `host agent status` | 主机代理的采集与上报状态 |
| `host catalog` | 服务目录：组件、端口与服务 |
| `host deployments` | 列出组件部署 |
| `host exec` | 经 SSH 在 moox.toml 登记的主机上执行一条命令 |
| `host get` | 把主机上的文件下载到本地（本地已存在时需要 --yes 覆盖） |
| `host list` | 列出 Admin 登记的主机（moox.toml 里的候选主机用 setup hosts） |
| `host put` | 把本地文件上传到主机（远端已存在时默认只预览，--yes 才覆盖） |
| `host routes` | 查看一台主机的网关路由 |

## import：导入历史时序数据（写 Storage 和因子机本地存储）

| 命令 | 说明 |
| --- | --- |
| `import` | 导入历史时序数据（.csv 或 .csv.gz；写 Storage、因子机本地存储与归档，`--target storage,factor,archive` 任意组合） |

## lifecycle：查看和控制模块的生命周期（运行、暂停、恢复追赶）

| 命令 | 说明 |
| --- | --- |
| `lifecycle catchup` | 查看恢复追赶的进度 |
| `lifecycle pause` | 暂停：不再计算新周期、不下单（默认只预览，--yes 才执行；--for 可设定自动恢复） |
| `lifecycle release` | 放行：追赶已追平的模块回到运行 |
| `lifecycle resume` | 恢复：先追赶暂停期间的周期，追平后放行 |
| `lifecycle status` | 查看生命周期状态 |

## metadata：存储元数据管理工具

| 命令 | 说明 |
| --- | --- |
| `metadata apply` | 导入元数据种子：缺的建上，已有的核对一致（空间、标签以管理台为准不比对），可重复运行 |
| `metadata dataset add-column` | 给数据集加列（或修改列；新建列必须带 --display-name，--group 可选，类型建后不能改） |
| `metadata dataset columns` | 列出数据集的列 |
| `metadata dataset create` | 创建数据集（保留期不在这里设：由 Storage 的保留策略文件按空间与频率决定） |
| `metadata dataset delete` | 删除数据集（服务端先删元数据再删行，查询索引由 storage-view 下一轮维护删掉；默认只预览，--yes 才执行；删除采集结果用 collector task delete-result） |
| `metadata dataset list` | 列出数据集 |
| `metadata dataset show` | 查看数据集 |
| `metadata dataset subjects` | 列出数据集里有数据的标的 |
| `metadata dataset update` | 修改数据集的名称、说明、状态与维护先后（数据来源、类型、频率创建后不可变；停用即摘掉它的查询索引，索引文件保留） |
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

## monitor：Monitor：健康总览、数据到达历史、主机指标与告警屏蔽

| 命令 | 说明 |
| --- | --- |
| `monitor arrival` | 数据到达历史：某天每个周期何时到达 |
| `monitor health` | 健康总览：各模块、数据新鲜度与待处理项（排障先看 doctor alerts） |
| `monitor host-metrics` | 主机资源指标的历史曲线（`--host-id`） |
| `monitor logs` | 重要日志：各服务的 Error 及以上日志和关键事件，新的在前；`--component`、`--host-id`、`--level`、`--keyword`、`--from`、`--to` 筛选，`--before-id` 向更早翻页（普通日志在 CLS 里查） |
| `monitor hosts` | 列出各主机最近一次资源上报（每台主机一行） |
| `monitor mute` | 屏蔽告警推送一段时间（到期自动恢复，最长 24 小时） |
| `monitor mute-status` | 查看告警屏蔽状态 |
| `monitor unmute` | 提前结束告警屏蔽，并补发一条汇总 |

## ops：云资源运维工具

| 命令 | 说明 |
| --- | --- |
| `ops tencent cls bootstrap` | 幂等开通 CLS 并创建 MooX 日志集、主题和索引 |
| `ops tencent cls resolve` | 查询既有 MooX CLS 资源，不创建或修改资源 |

## secret：密钥：只列清单与打码后的信息；新建、修改请用户在管理台“系统设置 › 密钥管理”填写

| 命令 | 说明 |
| --- | --- |
| `secret list` | 列出密钥（只含打码后的信息） |
| `secret show` | 查看一个密钥的元信息（值已打码） |
| `secret export` | 把全部非系统密钥加密导出为迁移包（响应里只有密文） |
| `secret import` | 解开加密迁移包并按原 secret_id 导入密钥（默认只预览） |

## setup：部署与初始化：主机、组件部署、防火墙、发布与初始数据

| 命令 | 说明 |
| --- | --- |
| `setup attach-scf-vpc` | 为腾讯云函数绑定 VPC 子网（默认只预览，--yes 才绑定） |
| `setup build-linux` | 在本机 Docker 里构建需要 CGO 的 Linux 二进制 |
| `setup deploy` | 把一台主机部署成 moox.toml 描述的样子：先按 moox.toml 整份覆盖 Admin 的主机与部署登记（含启停），再放二进制、渲染配置、按声明启停并重启有变化的组件，等它们就绪（`--component` 只部署个别组件） |
| `setup export-skill-config` | 导出 Skill 使用的最小数据访问配置（moox-skill 的签名私钥在本机生成，公钥登记到控制机） |
| `setup firewall` | 初始化所有目标主机的腾讯云防火墙端口（默认只预览，--yes 才执行） |
| `setup host-diagnostics` | 检查远端主机磁盘和服务进程 |
| `setup hosts` | 列出可选的部署主机（不输出密码） |
| `setup init` | 导入默认空间与元数据，写入初始秘密（管理员、云密钥、交易所密钥、告警渠道，只建不改） |
| `setup verify` | 验收一个空间：K 线入库、因子前进、策略在运行才算通过，只读 |
| `setup logs` | 只读查看组件在主机上的日志：先看文件日志（logs/<组件>/），没有再看 journald 里这个服务的输出 |
| `setup inspect-scf` | 检查腾讯云函数网络配置 |
| `setup install` | 一条命令装完：防火墙、逐台部署、初始化、启动和验收（默认只预检） |
| `setup invoke-scf` | 直接调用腾讯云函数 |
| `setup package-components` | 按服务目录把编译好的 Linux 二进制打成每个组件一个包（发布脚本用） |
| `setup operator` | 准备这台电脑的操作身份：生成 moox-cli 的签名私钥；--register 把公钥登记到已经装好的控制机 |
| `setup reboot-host` | 重启无响应的腾讯云主机（默认只预览，--yes 才重启） |
| `setup restart` | 重启一个组件（systemctl --user restart；默认只预览，--yes 才执行） |
| `setup start` | 启动一个空间：启用采集任务，导入默认因子任务，创建默认示例策略 |
| `setup start-component` | 恢复一个被停掉的组件并开机自启（systemctl --user enable --now；默认只预览） |
| `setup status` | 只读查看各主机的部署状态：version.json 里的版本，以及每个组件的用户服务是否在运行 |
| `setup stop` | 临时停掉一个组件并不再开机自启（systemctl --user disable --now；不改登记，下次部署按 moox.toml 的声明重新拉起；默认只预览） |
| `setup trust-browser` | 检查并安装管理台浏览器证书信任 |
| `setup trust-host` | 确认并记录 SSH 主机指纹 |
| `setup update` | 覆盖更新已装好的系统：按安装顺序把这个版本的程序和配置重新部署到全部主机，数据保留（默认只预览） |
| `setup validate` | 校验两份配置和连接，检测每台主机上各组件是否已部署，给出可选的部署方式（modes）和接下来要运行的命令（next_steps） |
| `setup wipe` | 卸载：彻底清空 moox.toml 所列主机上的 MooX 部署（默认只预览，不可恢复） |

## space：空间：一组相互隔离的业务数据（只登记在 Storage）

| 命令 | 说明 |
| --- | --- |
| `space create` | 新建空间（建好即可建数据来源、数据集等元数据，见 metadata apply） |
| `space delete` | 删除空间：会清空该空间全部数据（数据集、查询索引与全部行情、因子数据），不能恢复；采集任务、交易、策略里按该空间存的记录不跟着删（默认只预览，--yes 才执行） |
| `space list` | 列出空间 |
| `space show` | 查看一个空间 |
| `space update` | 修改空间（停用后 Collector 不再采它） |

## storage：查询索引（View）维护

| 命令 | 说明 |
| --- | --- |
| `storage repair-view` | 重建指定数据集的查询索引（默认只预览，--yes 才执行） |

## strategy：策略定义、实例、结果、历史回放与展示设置（调用 StrategyMgr）

| 命令 | 说明 |
| --- | --- |
| `strategy create` | 创建策略定义（DSL 写在 --dsl-file 里，写法见 skill 的 strategy-dsl） |
| `strategy delete` | 删除策略定义（默认只预览，--yes 才执行） |
| `strategy display get` | 查看实例的展示设置 |
| `strategy display set` | 设置实例要隐藏的分类、成员与列 |
| `strategy instance confirm` | 确认试运行中的实例转为实盘：下一期起按目标下单（先用 `trade shadow list` 看影子目标；策略池预热没完成时被拒；默认只预览，--yes 才执行；不给 --session-id 时预览里显示当前会话号） |
| `strategy instance create` | 创建策略实例（默认停用，确认无误再 enable；也可加 --enable 创建即启用） |
| `strategy instance delete` | 删除已停用的策略实例，连同绑定到它的因子任务和结果数据集（没绑定到它的任务如公共任务保留不动；有回放或补算在进行、结果数据集有归档任务时拒绝；默认只预览，--yes 才执行） |
| `strategy instance disable` | 停用策略实例；对已停用的实例再执行一次会重新通知 Trade 关闭最近一次挂账户的会话（默认只预览，--yes 才执行） |
| `strategy instance enable` | 启用策略实例（挂了账户的实例启用后先试运行：目标只记为影子目标、不下单，确认实盘后才下单；默认只预览，--yes 才执行） |
| `strategy instance list` | 列出策略实例（--space-id 过滤空间，默认 crypto） |
| `strategy instance show` | 查看一个策略实例 |
| `strategy instance update` | 修改停用中的策略实例绑定的策略定义与账户（因子任务创建时写定，不能改） |
| `strategy list` | 列出策略定义 |
| `strategy replay bars` | 查看回放逐期明细 |
| `strategy replay cancel` | 取消回放 |
| `strategy replay list` | 列出策略历史回放 |
| `strategy replay show` | 查看一次回放 |
| `strategy replay start` | 开始一次策略历史回放（支持现货、永续与混合市场）；`--input view` 读因子结果数据集的查询索引（默认），`--input compute` 由因子引擎现算（声明了 factor_params 的策略只能用它）；`--slippage-bps` 滑点 |
| `strategy result list` | 列出实例每期的结果 |
| `strategy result show` | 查看一期结果 |
| `strategy show` | 查看一个策略定义 |
| `strategy target list` | 查看实例当前的目标权重 |
| `strategy update` | 修改策略定义（引用它的实例要先停用） |
| `strategy validate` | 试算策略 DSL（不落库） |

## system：系统级暂停与恢复（按依赖顺序逐个模块，恢复先追赶补采、全部追平后统一放行）

| 命令 | 说明 |
| --- | --- |
| `system pause` | 系统暂停：下游到上游逐个暂停（默认只预览，--yes 才执行） |
| `system resume` | 系统恢复：先追赶补采，全部追平后统一放行 |
| `system status` | 查看各模块的阶段与恢复追赶进度 |

## trade：交易：账户、组合、订单、成交、持仓、权益、收益拆解、操作记录、下架名单；写操作默认只预览，--yes 才执行

| 命令 | 说明 |
| --- | --- |
| `trade account list` | 列出交易账户 |
| `trade account show` | 查看交易账户 |
| `trade action` | 查看一次操作的执行结果 |
| `trade actions` | 列出组合的人工操作记录（人工单、撤单、清仓、减仓、下架清仓）；`--action-type` 按类型过滤 |
| `trade blocked list` | 列出下架名单（全局，名单里的标的只减不增） |
| `trade capabilities` | 查看交易账户的执行能力 |
| `trade delist list` | 列出下架记录（全局，按创建时间倒序）：状态、已规划轮次、各轮各组合的清仓动作；`--active-only` 只列进行中的 |
| `trade inspect-credential` | 检测一把交易所密钥的账户类型和权限（只读，不落库，不下单）；`--markets` 指定要开通的市场 |
| `trade instruments` | 列出交易账户可下单的标的 |
| `trade equity` | 查询权益曲线；起止为 0 时默认最近 30 天，跨度超过 2 天按 15 分钟、超过 30 天按小时取每桶最后一点，最多 3000 点 |
| `trade fills` | 列出成交 |
| `trade holders` | 列出策略持有某个标的的执行账户（跨空间，全局；零头和模拟盘有标记）；`--exchange`、`--market-type`、`--exchange-symbol` 必填 |
| `trade holdings` | 列出账户的持仓明细 |
| `trade ledger` | 查询交易账户的资金流水（手续费、资金费、已实现盈亏、划转）；`--market-type` 指定市场 |
| `trade logical list` | 列出逻辑账户 |
| `trade logical show` | 查看逻辑账户 |
| `trade logical target` | 查看逻辑账户当前的目标 |
| `trade order list` | 列出订单；`--owner-type` 按来源过滤（TARGET、OPERATOR、EXTERNAL、SYSTEM） |
| `trade order show` | 查看订单 |
| `trade pnl` | 收益拆解：手续费、已实现盈亏（成交表汇总）与资金费（交易所流水，时间范围最长 90 天，取不全时标明截断） |
| `trade positions` | 列出持仓 |
| `trade shadow list` | 列出试运行中的策略实例最近的影子目标（权重、换算出的数量，从新到旧） |
| `trade account create` | 新建实盘或测试网交易账户（`--environment`、`--credential-secret-id`；默认只预览，--yes 才执行） |
| `trade account update` | 修改交易账户：只改给出的字段（名称、密钥、状态、模拟账户订单保留天数等；默认只预览，--yes 才执行） |
| `trade account leverage` | 设置杠杆（`--instrument-id '*'` 是账户默认值；默认只预览，--yes 才执行） |
| `trade account sync` | 立即同步交易账户的余额与持仓 |
| `trade account reinspect` | 重新检测交易账户的类型与权限（检测出问题时可能连带暂停所在组合） |
| `trade account flatten` | 平掉交易账户的全部持仓（默认只预览，--yes 才执行） |
| `trade logical create` | 新建逻辑账户（默认只预览，--yes 才执行） |
| `trade logical update` | 修改逻辑账户名称（默认只预览，--yes 才执行） |
| `trade logical order-settings` | 修改组合的下单设置：先取回已有设置，只改给出的字段（默认只预览，--yes 才执行） |
| `trade logical fund-settings` | 修改组合的资金设置，如 `--reserve-ratio 0.05`：先取回已有设置，只改给出的字段（默认只预览，--yes 才执行） |
| `trade logical add-member` | 把交易账户加进组合或修改已有成员：每次整条覆盖，`--enabled` 与 `--priority` 都要给（默认只预览，--yes 才执行） |
| `trade logical remove-member` | 把交易账户移出组合（默认只预览，--yes 才执行） |
| `trade logical pause` | 暂停组合的自动执行（默认只预览，--yes 才执行） |
| `trade logical resume` | 恢复组合的自动执行（默认只预览，--yes 才执行） |
| `trade logical flatten` | 清仓整个组合（默认只预览，--yes 才执行） |
| `trade order submit` | 人工下单（默认市价；限价单要给 `--limit-price` 与 `--fill-policy`；超时后先查 `trade actions` 再用 `--data` 带原 ID 重试；默认只预览，--yes 才执行） |
| `trade order cancel` | 撤单（默认只预览，--yes 才执行） |
| `trade paper create` | 新建模拟账户，并新建或加入一个组合（默认只预览，--yes 才执行） |
| `trade paper close` | 关闭模拟账户（默认只预览，--yes 才执行） |

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
