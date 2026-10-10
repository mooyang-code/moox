# Console Proxy 内嵌 Caddy 执行计划

日期：2026-10-09

状态：第二版，实施中；已按 `feature/mooyang` 当前代码与网关重构主计划复审。独立 worktree 已交付主计划 A/B/C 与代理原型，业务调用方和部署迁移、完整交付及正式环境验收仍未完成。

源码基线：`feature/mooyang@8133cc41`。初版文档来自 `c52c2af4`，初版源码基线为 `d64c4075`。执行前重新确认相关文件，保留并适配其他任务的改动。

本文是[网关与服务部署重构执行计划](计划/网关与服务部署重构执行计划.md)（下文简称「主计划」）的 console-proxy 子计划。网络、服务发现、MooX 私有 CA 与调用方迁移遵循[网关重构设计](计划/网关与服务部署重构设计.md)。复审基线尚无 `modules/consoleproxy`、`modules/hostgateway`、`modules/access` 或 `packages/servicecatalog`；实施进度另见第 0 节，不能把设计决策当作已交付能力。

## 0. 实施记录（2026-10-09）

实施分支为从 `feature/mooyang@503f5a37` 新建的 `feature/console-proxy-embedded-caddy`，使用独立 worktree。文档复审和修订已在 `503f5a37` 提交；第 3 节保留复审时的源码事实。本节记录后续实施证据，不用原型测试替代 P0～P7 退出条件。

已确认没有其他任务交付网关重构依赖，本任务须一并完成主计划要求的对应共享包、入口、调用方和部署改造，再执行整批正式切换。

| 阶段 | 当前进度 | 尚未满足的门禁 |
| --- | --- | --- |
| P0 | 已通过 CLI 只读检查 control、storage、compute-1 的监听与进程，核对 control 旧 CA；compute-1 确认仍有 storage 与 trade-move 两套网关进程；CLI 已支持本机 SSH agent/默认私钥，compile_host 认证成功，并单独完成 Go 1.26.9 预置及 GCC/G++ 检查；配置凭据未输出 | compute-1 配置目录与实际运行目录不一致，需核对切换归档范围及旧监听归属；完整迁移表待补齐 |
| P1 | 已新增模块、版本/配置命令和构建 target；固定 Caddy `v2.11.4`，工作区与 CI/CNB 配置使用 Go `1.26.9`，构建入口拒绝未预置的工具链；远程编译机已完成预置；完整模块图与当前 55 个 Go 模块的统一 test/vet 回归通过，格式门禁通过 | 自建 runner 未完成预置；后续改造完成后须验证最终候选的完整门禁 |
| P2 | 真实 internal HTTPS、路由、CA 连续性、流式/WS 完整排空、启动端口冲突清理已测；public-only 配置不创建 internal CA；本机交叉编译的四个测试包已在 Linux amd64 及 Linux arm64（QEMU 10.2.1 用户态模拟）运行通过 | public 本地 ACME 签发/续期及完整模式切换、HTTP/3、真实 SSH/SFTP 和实际启停脚本仍需验证；arm64 证据为模拟执行，未声称原生机验证 |
| P3 | 内部鉴权诊断已实现；匿名、错误 HMAC、重放被拒绝；上游失败与 readiness 分离，排空时 not-ready 且保持 live；`servicecatalog` 已登记 control / single / protected 的代理与 `127.0.0.1:19528`，Doctor 已改读该目录 | Monitor 的新部署/探针与完整新部署登记尚未接入 |
| P4～P7 | 尚未完成 | 三类最终包、双部署路径、网关主计划依赖、旧源码清理、最终全链路验证及独立 Agent 审查均保留 |
| 正式切换 | 未执行 | 必须先通过 P7，再执行主计划第 13 节并验证真实业务及 SCF |

已通过的本地检查：新模块 `go test -race -count=1 ./...` 与 `go vet ./...`；CLI 的 SSH、部署、命令包全量测试与新预检测试；`packages/storagepolicy` 测试；模块/包/Factor 边界、文档架构和远程 Storage 构建契约。Linux amd64/arm64、Darwin amd64/arm64、Windows amd64 五平台代理原型已通过实际构建入口编译并核对文件架构，Linux 实际运行和三类最终包仍未验收。CLI 基线缺少 `Policy.Coverage()`，已补齐并通过现有覆盖校验测试，未修改现场存储策略。

构建策略按用户确认执行：只有需要 CGO 的 Linux 目标交给编译机；其余目标在本机编译或交叉编译。远程 Storage 构建已拆出 `storage-cgo`，无 CGO 的 Storage Access 留在本机。`setup build-linux --source-dir <worktree>` 从指定 worktree 同步源码，配置仍由原仓库的 CLI 读取；认证优先使用现有 SSH key/agent。`--prepare-only` 独立预置 `.go-version` 指定的 Go 并检查 C/C++ 编译器；实际构建使用 `GOTOOLCHAIN=local`，不会临时下载工具链。

早期原型曾经由远端构建并回传 Linux amd64 的 Factor Manager/CLI、Storage Primary/Node/View/CLI；当时脚本强制打开 CGO，前两类产物均为动态链接。D1 复核已确认 Factor Manager/CLI 不需要 CGO，现已改为本机关闭 CGO 构建并验证 Linux 静态程序，删除 Factor 的远端 CGO 构建入口。Storage 四个 CGO 目标继续在 Linux 编译，Storage Access 在本机交叉编译；均为阶段原型，不是正式候选发布包。

`make test-go` 已在固定 Go `1.26.9` 下对当前工作区的 55 个模块统一执行测试与 vet，全部通过。验证环境使用 Python `3.12.14`、pandas `2.2.3`、numpy `2.3.5` 和 PyYAML `6.0.2`，覆盖 Factor 的 Python 依赖与 CLI 的脚本测试。早期 Admin 的全局函数补丁测试在 macOS ARM64 下触发 goom 内存权限错误，原分支 Go `1.25.0` 也可复现；B2 已用真实 tRPC 查询 SQLite 的断言替代该重复测试，没有跳过测试。原 RPC 适配器测试包也已由本机交叉编译并在 Linux amd64 全包运行通过。此为当前源码的 Go 回归证据，自建 CI runner 与后续最终候选仍需验证。

后续已解决完整依赖解析阻塞：旧 Slime 重试库间接引用了无法下载的 `go_reuseport`，现已改为项目内的两次只读重试，保留退避、上下文取消、服务端 pushback 与每次尝试的输出隔离，并补齐元数据字节复制。重试包、Gateway 转发及 Archive/Monitor 调用方的 race 回归和 vet 通过。当前完整模块图为 851 项；与 `503f5a37` 可解析的 417 项元数据比较，61 项已有模块版本升级、439 项新增、5 项移除（包括共享路由包改名）。基线仍有上述失效模块的元数据缺失，差异清单不推断其未知依赖。当前依赖最低 Go 最高为 `1.25.1`，固定的 `1.26.9` 满足要求。[完整版本差异清单](计划/console-proxy-dependency-audit.json)记录了全部条目；这是模块元数据审计，不代表所有依赖均进入最终二进制。Zap 升至 `1.28.0`、x/net 升至 `0.55.0`、x/crypto 升至 `0.52.0`；Prometheus client_golang 保持 `1.23.2`。`make check-go-module-graph` 已在禁止网络代理的条件下通过，已接入 `make verify-pr` 和 CNB；上述完整依赖解析门禁已通过，自建 runner 预置与最终候选的完整门禁仍待完成。

A8 已让 Doctor 使用共享目录，删除 `components.yaml`，发布与 shell 部署改为复制 `config/servicecatalog/catalog.yaml` 及其字节校验值。Doctor/目录/相关诊断调用方的 race 测试、CLI 全量测试、Monitor 各包测试及 vet 通过；CLI 回归使用 Python `3.12`，避免系统 Python `3.9` 在临时 HOME 下生成缓存引发的清理竞态。Doctor 聚焦 E2E、监控覆盖契约和发布契约通过；旧 Factor 构建包装脚本的 target 已修正为 `factor-mgr`。这些契约检查不等于三类最终发布包验收，部署种子与实际探针仍须随 B/F/G 迁移。

A5 已新增第 55 个工作区模块 `gatewayclient`，本机完成实际 tRPC 对象调用、原始 PB/JSON 转发、目录协议和 TLS 校验测试；本机交叉编译的测试包已在 Linux amd64 全包运行通过。为避免 SDK 在 Linux amd64 自动切换 tnet 与自有池不匹配，客户端显式使用 go-net。共享包的 race 回归与 vet 通过。客户端按 TLS 身份隔离连接池，同时验证相同身份连接复用、错误 CA/SNI 拒绝、缓存写入失败时目录撤回仍生效，以及写方法与业务错误不重试。运行网关和业务调用方尚未接入，仍不满足 P4～P7 或正式切换条件。

主计划 B1 及 B2 的 DAO 已加入：三张新表、整体校验与事务登记、保留启停状态、目录撤回和离线恢复所用的数据方法。两个独立写入者的并发测试验证 single 限制，模拟中途写入失败验证整批回滚；Admin 的连接池逐连接启用 SQLite 外键。相关 race 回归与 vet 通过，全部 15 份 schema 可载入空库。对应协议已由下述 B2 接入，GatewayControl 与运行网关仍待交付。本次安装了锁定的前端依赖用于完整检查；`make check-format` 的 Go 检查通过，前端检查发现 58 个未改动基线文件的格式问题，随后已单独按项目 Prettier 配置统一格式，完整格式门禁通过。

B2 已接入九个新 SysDeploy 方法与 RPC 适配器。真实 tRPC/SQLite 测试覆盖分页、完整目录、定义路由、登记、启停撤回和空主机删除；Monitor 取得读取权限，写方法保持拒绝。Admin 本机全量测试通过，新拓扑聚焦测试和 RPC 适配器全包也在 Linux amd64 运行通过，测试包均由本机交叉编译。原先触发 macOS 内存权限问题的全局函数补丁测试已由真实查询数据库的测试替代，没有跳过测试。最终带密钥的快照哈希、GatewayControl、PKI、离线初始化和运行网关仍待交付。

B4 已实现调用方密钥加密存储、复用、导出和手动轮换。新旧 KeyID 保持有效，确认替代密钥生效后用 `keys retire --confirm` 停用旧密钥；未完成停用时拒绝连续轮换。系统密钥与通用密钥管理 API 隔离，Access 导出仅含外部调用方，私密文件权限和原子发布均有测试。专项 race 回归及 Linux amd64 密钥服务全包、新 CLI 用例通过；Linux 程序由本机交叉编译。此阶段没有安装运行服务，快照、网络接入和部署侧密钥分发顺序仍待 B3/C/G 完成。

B3 已接入 loopback `11112` 的 GatewayControl：先对原始 PB/JSON 字节验签再解码，同主机身份绑定、持久化 nonce 防重放；SQLite 一致性快照同时读取拓扑和所需内部校验密钥，最终哈希覆盖实际密钥、KeyID、有效期、路由及目录。心跳区分正常替换和短时间交替，冲突停止满 5 分钟后清除；路由页面实时计算期望哈希，轮换无需等待心跳。专项 race、真实 tRPC 与 Admin 实际过滤器链测试通过；本机交叉编译的 GatewayControl 测试在 Linux amd64 全包通过。新主机网关、PKI、网络迁移和部署分发仍待后续交付，未进行正式环境安装。

B3 变更后，55 个模块统一 Go 测试与 vet、格式、模块/包边界、模块图及文档架构检查通过，15 份 schema 均可载入空库。SysDeploy 的真实 RPC 测试在本机和 Linux amd64 均验证轮换会立即改变最终期望哈希，且状态读取不产生心跳。

B5 已交付 MooX 私有 CA 与主机网关证书的离线命令。已有 CA 和连续性摘要被保留，损坏或丢失时拒绝更换信任根；主机证书包含主机 ID、公网/私网地址，使用新的私密暂存目录输出后交给部署安装。此 CA 专供主机网关 TLS，与本子计划要求保留指纹的 Caddy 代理 CA 分开。Admin 全量、PKI/CLI race、实际 TLS 正反用例及 Linux amd64 运行验证通过；五个平台的 Admin CLI 均在本机关闭 CGO 编译通过。部署分发、主机网关、证书巡检和正式环境切换仍待后续阶段完成。

B7 已单独完成 Admin Console 的包、服务和配置改名；`trpc.moox.admin.Console` 继续监听 loopback `11000`，配置为 `admin/config/console.yaml`。Admin 全量、受影响包 race/vet、Linux amd64 Console 全包及 control 打包契约通过。打包契约已单独改为本地 Caddy 测试制品，生产旧 manager/archive 链路仍须 P6 清理。B8 的转发行为与前端服务名、B6/C2 的初始化配置集成，以及后续正式验收仍待交付。

B4 变更后，55 个 Go 模块的统一测试与 vet 再次全部通过；协议生成一致性、格式门禁、模块/包边界、模块图及文档架构检查通过。编译策略继续遵循上述 CGO 边界，Linux 纯 Go 测试程序在本机交叉编译后，只在编译机执行验证。正式切换尚未执行。

主计划 C2～C7 已接入新运行端：三个监听、完整私密快照缓存、基于目录和 ACL 的原始字节转发、本机 Directory、TLS tRPC 控制客户端与带实例/版本的心跳，以及新配置诊断命令。全量 race/vet、实际网关进程启动与转发、目录/缓存/权限撤回、CLI K 线回归通过；五个平台网关及 CLI 在本机关闭 CGO 编译，13 个 Linux amd64 测试包由本机交叉编译后在 Linux 上运行通过。C 阶段真实 Admin 与双生产网关进程联调也已通过，新增 `make test-host-gateway-control-e2e` 单独门禁，逐项检查六组场景，实际等待 90 秒陈旧阈值并验证离线重启。联调修复 GatewayControl 签名转发丢失，保留其六个已验证签名字段供 Admin 再次校验，该转发层不复用 nonce 重试；普通业务仍剥离认证元数据。快照与目录默认均每 5 秒轮询，满足两级同步的 15 秒可见性预算。旧部署脚本尚未迁移配置，现有部署契约因此失败，须在 G 阶段与身份分发一起修复；不部署当前中间版本。前端在本机构建，只有 CGO 目标使用 Linux 编译机。

跨模块的 30 个 Collector 周期场景和 Storage 辅助程序全包测试已在 Linux amd64 通过；编译严格遵循 CGO 边界。Collector 三代重试配额用例现已在本机 race 通过：恢复 15 秒、派发 30 秒的预算保持不变。修复复用数据库预编译语句、减少重复成员查询，并优化派发查询和索引；显式设置 SQLite 时间格式，保证日期函数与截止时间查询一致。Collector 全包 race 与关闭 CGO 的 vet 通过，包含事务回滚、查询计划和跨空间拒绝回归；修正并发 RPC 测试夹具的竞态，时间断言保留纳秒精度并统一为 UTC。服务、CLI、标的同步与 SCF 四个程序均在本机关闭 CGO 交叉编译为 Linux 静态程序；相同代码的 Store 全包、三代重试配额与全部 30 个周期场景在 Linux amd64 再次运行通过。边界、848 模块图、架构文档、Go 格式及全部 15 份空库 schema 检查通过。Collector 的 D1 内部调用迁移与后续验收记录见下文。细节见主计划 C 阶段实施记录。

主计划 B8 已完成控制台转发迁移：服务名与权限统一来自 servicecatalog，Admin 自身七个 RPC 服务复用实例及生成 handler 进程内调用，并再次校验 console ACL；其他服务经 gatewayclient 原样发送 JSON，可信用户/角色/空间/trace 通过固定元数据传递。控制台目录直接读取 Admin 拓扑表，启动不依赖本机网关，签名身份读取加密数据库中的 console 密钥；初始化失败与关闭会释放客户端。旧 HTTP 网关控制路由、Storage/Trade 专用转发、认证字段注入及机器方法硬编码判断已删除。SSH WebSocket/SFTP 保留操作绑定的一次性 ticket 和原始处理器，交易/采集空间授权及管理员手工视图重建限制保留。目录中交易归属三方法的 console 权限已收紧，Strategy/CLI 权限保留；前端 API、直接页面调用与测试同步使用 collector、factor-mgr、monitor、trade。

B8 的 Admin/共享客户端/目录/主机网关全量 race、相关 vet、原始字节及可信元数据的实际 tRPC 回归、Linux Console/bootstrap 全包运行验证通过。Linux 程序由本机关闭 CGO 交叉编译。前端 90 个文件、448 个 Vitest 用例与生产构建在本机通过；测试使用本机已有 Node 24.19，避开默认 Node 22.11 对现有 jsdom ESM 依赖的加载失败，未修改依赖。格式、模块/包边界、模块图、架构文档、全部 15 份 schema 与 diff 检查通过。B 阶段现已完成；D～J、代理后续阶段、最终独立审查及正式发布仍待完成。旧部署脚本尚未迁移，正式环境继续保留当前运行版本。

主计划 D1 的 Archive 调用迁移已完成：常驻进程、COS 同步和历史回填使用 gatewayclient，删除旧目标配置、环境回退、未使用的 Metadata 回填客户端和重复重试。公共 FileConfig 显式配置 caller、Admin 分配的 KeyID 与密钥文件，从同级 hostgateway 配置读取主机身份、loopback 地址及私有 CA；密钥读取复用严格的普通文件、0600、禁止符号链接和长度限制。隔离 Storage outbox 验收用例也切换到 moox-cli 网关身份，完整部署联调留待最终门禁。

Archive/共享客户端/签名包/主机网关全量 race 与 vet、边界、848 模块图、架构文档、Go 格式及 15 份 schema 检查通过。Archive 原启动用例只检查 ACK，现修正空间订阅配置并要求实际 Journal、ACK、Parquet 物化及 tRPC Metadata 注册，关闭阶段不得豁免错误；实际目录查询和缓存回退也有验证。Archive 服务与 CLI 的 Linux amd64 静态程序及测试包均在本机关闭 CGO 构建；bootstrap、CLI 与共享客户端测试包在 Linux 全包运行通过。D1 其他模块、D2～J、最终独立审查与正式切换仍未完成，G 阶段须渲染新的身份与配置。

主计划 D1 的 Storage View 迁移已完成：Metadata 的 17 个能力经共享客户端调用，角色认证保留；PrimaryStore 内部直连改用 tRPC 20102，保留历史读取的五分钟超时。配置使用 caller、Admin 分配的 KeyID 和密钥文件，移除旧 `storage_rpc`、无用服务名字段及目标/协议/网络回退；严格拒绝旧字段、未知字段和重复键。真实 tRPC 回归验证签名、只读刷新重试、写调用单发、独立 nonce 和禁止 SDK 选项覆盖目标。本机无 CGO 的全包测试/vet、Linux 全量 CGO race 与 vet 通过；实际 `storage-cgo` 入口在编译机生成并核对四个 Linux amd64 动态链接程序。Linux SDK 自动选择 tNET 引发的 checkptr 问题已通过显式 go-net 修复，没有禁用检查。持续检查同时修复主机网关关闭监听时未取消 TLS 握手的问题，本机 race 和本机交叉编译后的 Linux 监听器全包回归通过。D2f 旧 HTTP 监听、G 身份分发、其他 D1 模块与后续正式验收继续保留。

主计划 D1 的 Monitor 迁移已完成：服务指标、主机快照/历史/元数据检查、行情金丝雀及标签巡检共享进程级客户端并统一关闭，删除旧 Storage 配置与重复重试。网关 KeyID 与原请求的角色认证分开；补齐目录漏列的 Monitor `PrimaryStore.UpsertFields` 权限，并验证其他写操作仍拒绝。真实 tRPC 验证部署身份、私有 CA、读重试/写单发、nonce、目录缓存及关闭；Monitor、目录、共享客户端、主机网关和 Admin 全量 race 与相关 vet 通过。服务与 CLI 的 Linux amd64 静态程序及五个测试包均在本机关闭 CGO 编译，五包在 Linux 全包运行通过。格式、边界、848 模块图和 Storage 边界通过；Collector 清单 HTTP 调用留到 D2a，SysDeploy 留到 D2c，完整已部署 Storage 回环与身份分发仍待最终门禁及 G 阶段完成。

主计划 D1 的 Factor 内部调用迁移已完成：Manager 与 CLI 的 14 个 Storage 方法使用共享客户端，网关签名 caller/KeyID 与请求体内的角色认证分开，严格配置拒绝旧字段，删除环境回退，关闭时释放客户端。目录补齐三个既有写方法，仅放行 `factor-mgr`，验证外部引擎身份仍拒绝直访主机网关。Factor、目录、共享客户端、主机网关及 Admin 全量 race 与相关 vet 通过；真实 tRPC 覆盖读重试、写单发、签名、独立 nonce 和私密缓存。两个 Linux amd64 静态程序与三个测试包均在本机关闭 CGO 编译，三包在 Linux 全包运行通过。构建脚本已按实际依赖修正 Factor 的编译位置；SCF 外部工厂、旧 HTTP 监听、部署身份分发与正式验收仍待 E2、D2f、G/J 完成。

主计划 D1 的 Strategy Storage/Factor 调用迁移已完成：七个依赖方法共用进程级网关客户端，保留 Metadata/DataView 各自的角色认证，删除固定目标与地址环境覆盖，严格配置与启动身份检查已接入。正常关闭和失败清理统一释放客户端，事件 runner 与实例协调循环在关闭数据库前退出。Strategy、共享客户端与目录全量 race、相关 vet 和真实 tRPC 回归通过；服务/CLI 的 Linux amd64 静态程序及四个测试包均在本机关闭 CGO 构建，四包在 Linux 全量运行通过。边界、848 模块图、架构文档、格式与隔离部署契约通过；Trade HTTP 调用、实际身份分发和正式验收仍待 D2d、G/J 完成。

control 的只读核验确认：旧 manager 实际使用 `<部署目录>/data/caddy/caddy`；持久 root、发布 root 和发布指纹一致，root 私钥匹配且权限正确，持久指纹基线尚不存在。现场 SHA-256 为：

```text
50:DC:4A:53:B7:A5:26:19:E2:24:3F:F8:53:73:3F:16:6D:EC:36:D6:FF:ED:A6:E5:65:F8:7C:70:A6:62:40:01
```

现场仍有旧 Caddy 及 `11001`、2019 监听；本任务未切换、停止或安装运行服务。上述结果只是迁移前核验，不能作为新组件正式环境验收证据。

主计划 D1 的 Collector 内部调用迁移已完成：计划、结果 Metadata、重采样、周期账本、失败上报及标的同步共用 Collector 签名身份，32 个 Storage 方法使用共享网关适配器并保留原角色认证。删除内部固定目标、直接 Metadata 回退和旧密钥配置；严格拒绝未知字段、重复键及额外 YAML 文档。内部写请求单发，Ensure 未知结果只查询状态；关闭时取消并等待后台任务，再释放客户端和数据库。补齐既有 ActivateDataset 权限，仅放行 Collector。Collector、目录、共享客户端、主机网关和 Admin 全量 race、Collector vet 通过；32 方法真实 tRPC 回归、生命周期及取消检查通过。四个 Linux amd64 静态程序和七个测试包均在本机关闭 CGO 构建，七包在 Linux 全量执行通过，与既有 CGO Storage 辅助程序联调的全部 30 个周期场景也通过；配额预算保持 15/30 秒。边界、848 模块图、格式、文档及 15 份 schema 检查通过。SCF 外部调用、DNS、旧监听、标的进程合并及实际部署仍待对应 E/D2/G/J 阶段。

主计划 D1 的 CLI K 线与金丝雀 Storage 调用迁移已完成：独立的操作员 caller/KeyID/密钥配置通过真实 SSH 隧道读取 control Directory，按可信 manifest 主机转发 loopback 11002；Directory 公网地址不能覆盖 SSH 目标。保留 Storage 请求角色认证，删除数据配置中的旧网关密钥与导出逻辑，Admin bootstrap bundle 生成对应配置。连接复用、断连重建、目录切换及关闭有实际回归；CLI 日志写 stderr，JSON 输出可独立解析。CLI 全包 race、Admin CLI/辅助程序 race、vet、Skill 查询契约及实际 K 线全链路通过；两个静态程序和五个 Linux 测试包均在本机关闭 CGO 构建，五包在 Linux 全量执行通过。独立模块构建、851 模块图、边界、架构文档、格式及 15 份 schema 检查通过。依赖图新增三个间接元数据模块，已有选择版本不变，差异清单已更新。CLI 的其他 HTTP 入口与对应服务在 D2 同批迁移，D2f 已补齐运维入口清单；身份安装、独立审查与正式部署仍待 G/J。

主计划 D2a 的 CollectMgr 与全部调用方迁移已完成：11402 改为原生 tRPC，删除 11418，MarketFetchRuntime 保留 11422；部署登记与监听契约同步。Monitor 清单复用进程级客户端，删除旧 HTTP 配置及凭据读取，只增加清单读取 ACL；CLI 任务 CRUD 与金丝雀清单共用已有 SSH 客户端，分别使用 JSON 与 PB，旧 HTTP 路径拒绝回退。全包 race、相关 vet、真实 SSH/原生 RPC 和 Monitor HMAC 清单回归通过；四个静态程序及 11 个 Linux 测试包均在本机关闭 CGO 构建，11 包在 Linux 全量运行通过。边界、851 模块图、架构文档、格式及 15 份 schema 检查通过。CloudNode Timer 盘点留到 D2b，其他 D2～J、最终独立审查与正式切换仍待完成。

主计划 D2b 的 CloudNodeMgr 与全部调用方同批迁移已完成：11401 改为原生 tRPC，生产处理器从元数据读取空间标识；Collector 四个方法复用进程客户端，Admin 垃圾回收使用独立 `admin` 身份，CLI 全部 CloudNode 操作经同一 SSH 网关客户端。保留 COS HTTPS 上传、发布围栏、写入单发与未知结果处理；取消后的清理可复用隧道，退出统一关闭。函数调用采用目录中的 960 秒预算，删除固定 5 秒覆盖及远端执行 CLI 的出口探针回退。相关模块全包 race、vet、真实 SSH/原生 RPC、生产处理器空间隔离、垃圾回收 ACL 与长调用验证通过；五个静态程序及 14 个 Linux 测试包均在本机关闭 CGO 构建，14 包在 Linux 全量运行通过。协议生成、边界、851 模块图、文档、格式及 15 份 schema 检查通过。Admin 租约与密钥调用、其他 D2～J、最终独立审查及正式发布仍待完成。

主计划 D2c 的 Admin RPC 与全部调用方已迁移：七个内部服务使用 loopback 原生 tRPC，端口不变；Collector、CloudNode、Trade、Monitor 与 CLI 的密钥、租约、Setup、部署诊断及测试空间管理共用各自网关客户端。删除旧 HTTP 发现、相关目标配置与重复签名身份读取，保留发布 fencing 和写入单发；CloudNode 退出等待批处理循环结束。真实 SSH/原生 RPC、权限与 nonce、严格配置和关闭回归通过；相关模块全包 race/vet、七个本机交叉编译的静态程序及 25 个 Linux 测试包运行通过。Collector 性能配额用例在停止并行构建后按原预算单独通过。CloudNode/Trade 独立模块构建、协议生成、851 模块图、格式、边界、架构文档及 15 份 schema 检查通过。SysDeploy v1 的实际方法 ACL 保留至 F/G4 删除最后一个调用方；D2d～J、代理后续阶段、独立审查与正式切换仍未完成。

主计划 D2d 的 Trade 与全部相关调用已迁移：TradeConsole 11200 使用 loopback 原生 tRPC，空间标识来自原生元数据并拒绝冲突。Strategy 的四个账户授权方法复用进程级客户端，保留业务超时、身份检查与 fencing，删除专用 HTTP 目标、CA、环境覆盖及独立客户端。删除旧服务别名，部署登记与 CLI 探针同步。三进程真实 Directory、私有 TLS 网关和 Trade SQLite 链路覆盖授权、释放、会话、空间隔离、nonce 重放、下单拒绝及原生 JSON 探针；本机全模块 race、vet、原生链路 race 和质量门禁通过。六个静态程序、11 个测试包及三个原生链路测试制品均在本机关闭 CGO 编译，11 包与完整链路在 Linux 运行通过。Strategy 独立模块构建、851 模块图、格式、边界与 15 份 schema 检查通过。D2e～J、代理后续阶段、独立审查与正式切换继续推进。

主计划 D2e 的三类管理服务与相关调用方已迁移：MonitorMgr、EventBusMgr、HostAgentMgr 使用 loopback 原生 tRPC，健康 HTTP 保留，部署登记同步。CLI Doctor 的 Monitor 与部署诊断共用 SSH 客户端，删除旧目标和未使用配置，严格拒绝非法 YAML；核对 EventBus/HostAgent 无其他直接管理调用方。真实原生 PB/JSON、SSH/Directory 链路、相关全模块 race/vet 和 CLI 独立构建通过。八个静态程序与 16 个测试包均在本机关闭 CGO 编译，16 包在 Linux 全量运行通过；851 模块图、边界、格式、架构文档及 15 份 schema 检查通过。D2f～J、代理后续阶段、独立审查与正式切换继续推进。


主计划 D2f 已完成：FactorMgr、StrategyMgr 与 Storage 删除旧 HTTP 监听，使用规范原生服务名与实际端口登记；Strategy 的原生空间隔离有真实 PB/JSON 回归。CLI Factor/Storage 初始化、诊断、Metadata 导入、CSV 导入、数据导出和任务清理复用命令级 SSH 客户端，保留独立 Storage 角色认证、空间及同步围栏，写入单发并返回未知结果。相关全模块 race/vet、CLI/Strategy 独立构建与质量门禁通过；七个静态程序和 16 个测试包在本机关闭 CGO 编译，16 包在 Linux 全量运行通过。Storage Server 在 Linux 编译机的 CGO race/vet 通过，实际构建入口产出四个 CGO 程序；编译继续遵循 CGO 边界。FactorEngine 外部调用、旧部署脚本、D3～J、代理后续阶段、独立审查与正式环境切换继续保留。

D3 首批清理删除已无生产调用方的 CLI Admin HTTP 客户端、旧认证配置及两个无引用的目标辅助接口；CLI 全包 race、gatewayauth race、相关 vet、独立构建与质量门禁通过；CLI 静态程序和三个测试包均在本机编译，三包在 Linux 全量运行通过。剩余旧公共接口随 SCF/因子引擎及 DNS 的最后调用方迁移删除；命令参数、部署脚本与正式环境验收继续按后续阶段推进。

主计划 E1 的模块拆分已完成：外部代理迁入 `modules/access`，二进制为 `moox-access`，运行目录/profile 为 `access`，环境统一使用 `MOOX_ACCESS_`；构建、发布、部署、测试夹具和因子引擎密钥路径同步。健康检查使用共享包，Access 不依赖 Storage 内部实现或 CGO。模块图增加到 852 项，已审计的外部依赖版本不变。相关全包 race/vet、独立构建、852 模块图及质量门禁通过。实际构建入口在本机关闭 CGO 生成五平台代理制品并核对架构，本机编译的代理测试包在 Linux amd64 全包通过；Storage 远端构建、Storage profile、Factor Engine 部署改名和发布/服务包契约均通过，15 份 schema 检查通过。E2 的权限/外部调用方切换、旧部署体系替换、后续阶段与正式验收继续保留。

主计划 E2 的共享依赖已补齐：`ExternalFileConfig.OpenExternal` 从明确的配置路径加载外部身份私密文件，固定 Access 地址与实例 ID，不查目录或环境凭据，不推导 KeyID。三个外部身份的真实 tRPC 回归验证 PB/JSON 字节、签名、元数据、权限和连接释放；查询重新签名重试，领批次及周期写入单发。相关全包及独立模块 race/vet 通过；17 个测试包与主机网关在本机关闭 CGO 编译，17 包在 Linux 全量运行通过，852 模块图及质量门禁、15 份 schema 检查通过。审计补充覆盖 Factor Engine `run-once`、SCF 领批次与 Storage 共用连接，以及由 CLI 生成的 Skill 私密配置；Access 和全部外部调用方仍待同一提交整体切换，正式验收尚未开始。

主计划 E2 的整体切换已实现：Access 使用目录白名单、分配的 caller/KeyID、`access@host` 签名目标、持久化 nonce 和共享内部客户端；SCF 的 Claim 与 Storage 共用调用级连接，Factor Engine 的常驻和 `run-once` 共用外部客户端，11405 改为原生 tRPC。Skill 使用独立外部入口，CLI 从 Admin 导出身份，以版本化密钥侧文件和原子配置完成分发；CloudNode 清理内部凭据，公开元数据不含私密字段。真实 Access、主机网关和 CGO Storage 的全部 30 个周期场景通过，Skill 的真实生产进程链路通过；相关本机全包 race、vet 和独立模块整理通过，Collector 保持原有 15/30 秒配额。13 个测试制品及三个静态程序均在本机关闭 CGO 编译，13 项在 Linux amd64 运行通过（CLI 制品运行新的 Skill/Access 场景，其余全包运行）；852 模块图的选择版本、边界/格式/文档及 15 份空库 schema 检查通过。经实例发现核验的地域/VPC 地址落盘、安装体系替换、后续 E3～J、独立审查与正式验收仍待完成。

主计划 E3 已完成：新增独立出口代理及协议模块，提供受限 HTTPS 请求与迁入的 DNS 解析；原生 11440、鉴权健康 11441 均为 loopback。白名单、固定 HTTPS、公网地址检查、证书验证、取消、重定向隔离、原样 HTTP 错误状态及 32 MiB 解压上限有回归，真实 PB/JSON 均传输完整 32 MiB 正文。同步修复共享原生帧预算与健康 HTTP 监听退出行为，相关全包 race/vet、生产运行入口的鉴权/重放/关闭检查通过。五平台程序与 12 个 Linux 测试包均在本机关闭 CGO 编译，12 包在 Linux 全量运行通过；独立构建、协议再生成、854 模块图和质量门禁、15 份 schema 检查通过，既有依赖选择版本不变。Collector HTTP/DNS 调用与 Trade 旧入口删除仍须在 E4 同批完成，后续阶段、独立审查及正式部署继续保留。

主计划 E4 已完成：Collector 增加受控 HTTP 出口传输并以共享网关请求 DNS，Binance 标的客户端支持注入；保留 HTTP 状态、取消、路径/查询和解压正文，过滤认证头，失败与快照 IP 都不能绕过代理。远端仅解析明确的出口域名，本地 DNS 补齐其余域名。Trade 的 DNS 协议、运行时、监听、配置和部署登记在同一提交删除，Collector 对 Trade 协议的依赖同步移除。上游配置改为 `egress_proxy`，服务位置单独用 `placements` 声明；CLI 渲染/配置发布及契约同步。删除最后调用方已迁走的旧 HTTP SDK、环境凭据回退和 HTTP 转发。Collector、CLI、Trade、HostGateway 及相关认证/路由/部署回归和 vet 通过，真实 PB/JSON 网关链路验证 HTTP/DNS 共用客户端及完整 32 MiB 响应。18 个测试包和纯 Go Linux 程序均在本机编译，18 包在 Linux 通过；DNS 范围修正后相关包复验通过，真实 Access/网关/CGO Storage 的 30 个必需周期场景全部通过。协议再生成、独立构建、854 模块图与质量门禁、15 份空库 schema 检查通过。E5～J、完整安装体系、最终独立审查及正式部署继续推进。


主计划 E5 已完成：标的同步合入 Collector，共用网关与受控 HTTP 客户端；启动时登记行情源能力，标签和属性各用一个有重叠保护、10 分钟预算的定时器。属性使用带时区的无状态一分钟窗口，默认在北京时间 08:10:45 执行；首次触发、相邻窗口、不可能的计划与退出资源顺序有回归。删除独立标的进程及配置、轮询状态、构建/部署/种子/告警入口，相关文档同步。Collector 全包 race/vet、Admin 部署相关 race、独立模块构建、真实 PB/JSON 网关与现货/合约标签同步、出口代理停止后的失败上报均通过；四个本机编译的纯 Go 测试制品在 Linux 全量通过，实际构建入口的两个静态程序也在本机完成。854 模块图、格式/边界/文档/质量门禁、15 份空库 schema 检查通过。控制包中的新配置与旧进程删除断言通过，完整旧控制包契约仍因旧启动逻辑读取 `native_addr` 失败，须在 G11/G12 整体重写后通过。F～J、最终独立审查及正式部署继续推进。

主计划 F1/F6 已完成：Monitor 与 Doctor 改用 SysDeploy v2 和共享组件目录，检查身份/来源统一为 placement；事务同步部署启停与删除，保留探测历史并防止发现失败或身份冲突造成部分更新。控制台代理只探测 loopback readyz，页面另设带公开 Host/SNI 与可信 CA/系统信任的 HTTPS 检查，两种结果独立。Monitor 全包 race/vet、19 个本机编译测试包的 Linux 运行、两个本机编译静态程序及质量门禁通过。实际入口配置由 G 部署渲染接入；F2/F3、F4/F5 与前端、H/G 后续步骤及最终独立审查、生产部署仍在推进。

主计划 F2 已完成：网关状态通过进程客户端按启用主机采样，非敏感状态及路由收敛计时写入 Monitor；无心跳、实例冲突、哈希未收敛分别告警，正常替换不告警，读取失败不伪造恢复，停用和重启场景有回归。Monitor 全包 race/vet 及四个本机编译测试包的 Linux 运行通过，纯 Go 程序继续在本机交叉编译。Monitor schema 变化已补充到主计划：服务/CLI 成对发布，G 阶段在备份后离线重建运行库并在 CLI 内保留通知配置。F3、F4/F5、H/G 后续步骤、最终独立审查与正式发布继续推进。

主计划 F3 已完成：Monitor 保存完整部署快照并按主机/组件精确对账，识别近期上报的未登记进程及从未上报的登记部署，保留停用、none 和外部 principal 的区别。组件发布身份统一，移除基于探测拒绝上报的入口；探测与上报时间独立，延迟报告和目录清理不会伪造在线或丢失曾经上报的事实。Monitor 全包 race/vet、相关发布方与 CLI 测试通过，两个静态程序和五个测试包在本机关闭 CGO 编译，五包在 Linux 全量通过。质量门禁与 15 份空库 schema 通过；旧运行身份契约随 G12 重写。F4/F5 与前端、H/G 后续步骤、最终独立审查及正式部署仍在推进。

主计划 F4/F5 与 H3 已完成：结构化健康概览、组件矩阵、数据链路、主机及未登记进程、主机告警、探测/Reporter 分离、原始错误和状态起始时间同批接入；页面/API 更名为 monitor，删除模糊名称与前端翻译表。主机资源使用显式 host_id，G 必须渲染此字段并完成 Monitor 离线运行库重建。28 个相关 Go 包 race/vet、前端 451 项测试/生产构建及契约、854 模块图、格式/边界和 15 份空库 schema 通过；四个静态程序和 28 个测试制品均在 macOS 编译，28 包在 Linux 全量通过，最终边界修正后四包再次通过。H1/H2/H4/G 后续步骤、最终独立审查与正式部署继续保留。

主计划 H1/H2/H4/H5 已完成，并在 G4 删除 v1 后端之前同批迁移全部浏览器调用方：资源与运维默认进入监控告警，服务部署页按主机展示组件与逐方法路由，控制主机/受保护组件没有停用入口，定位精确消费 host_id/component_id。删除旧服务管理/部署页面和 v1 API。新增的快照格式/本次编译时间明确区别于应用时间；待同步起点使用 Monitor 持久观测并校验对应哈希。首页服务在线与健康率改读组件统计，去除示例健康分、服务/主机与虚构业务数字；缺失和失败显示未知。前端 467 项测试及生产构建、相关 Admin/Monitor race/vet、24 个本机构建测试包在 Linux 全量执行通过。四个纯 Go 程序仍全部在本机交叉编译；后续 G/I/J、P 后续步骤、完整编码后的新 Agent 审查与正式部署继续保留。

主计划 G1 已完成：`moox.toml` 使用主机 ID、嵌套 SSH 信息和组件放置，角色/SCF 公网候选从放置派生，旧键给出明确迁移提示；编译专用主机默认不部署。无密码 SSH 和编译位置按 CGO 边界执行，真实运维 manifest 尚未迁移。CLI 全包 14 包 race/vet、13 个本机交叉编译制品在 Linux 全量测试及静态 CLI 示例验收通过，格式/边界/模块图/文档/质量门禁和 15 份空库 schema 通过。G9 的 VPC 核验及私网部署结果持久化、G2～G13、I/J、P 后续部署集成、全部编码后的新 Agent 审查与正式部署继续保留。

主计划 G2 的软件与模板层已实现：六类组件包和独立检查命令共用严格清单，主机网关只进入 host 包；仅收集已编译制品和 Git 跟踪模板。CLI 全包 14 包 race/vet、两包最终 race、六类双架构边界与损坏归档测试、构建位置契约通过。五类纯 Go 制品在本机交叉编译，Storage 在 Linux 编译机使用 CGO 编译；六类实际包在本机和 Linux 检查通过，Host、Access、出口代理跨系统重打包摘要一致。现有部署 packager、运行配置/身份与安装生命周期尚未切换，G2 保持进行中；G3～G13、I/J、全部编码后的新 Agent 审查与正式发布继续保留。

主计划 G4 的协议与现有部署调用已迁移：control/storage/deploy-service 同步完整主机放置，控制包使用规范主机 ID；Doctor 使用 v2 目录，EventBus 凭据从有效放置派生地址。SysDeploy v1、两张旧表、种子/导入/启动调用、旧客户端登记与恢复路径及八个旧方法授权在同一改动中删除。53 包全量 race/vet、真实网关和 tRPC 回归通过；44 个本机构建的 Linux 测试制品执行通过（43 包全量、命令包定向），静态 CLI 脱敏配置验收通过，Linux 未编译。格式/边界/854 模块图/文档/质量、新登记与构建契约、15 份空库 schema 通过。deploy-host 的登记接线待 G3 新增入口后验收，G4 暂不标全部完成；安装器、身份材料、最终新 Agent 审查及正式环境验收继续保留。

主计划 G5 的身份材料生产端已接入：离线 bootstrap 与普通 `host-bundle` 共用私密包格式，主机叶证书每次重签，调用方 KeyID/密钥、CA/主密钥和启停状态保留。普通导出按目标放置限定身份，Access 附独立外部验证表，操作员身份仅在 control bootstrap 中单独导出；文件均为 0600、目录 0700，元数据记录逐文件摘要，CA 私钥和主密钥留在 control。运行配置路径统一为 `host-gateway/config/app.yaml`，共享客户端可通过业务根的目录链接访问独立主机包并正确解析相对 CA。62 个受影响测试包全量 race/vet、13 个本机构建测试制品在 Linux 全量执行、真实 Linux Admin CLI 的空库离线 bootstrap/重跑/三目标导出通过；Linux 未编译，部署组件未启动。格式/边界/模块图/文档/质量、新身份/打包/登记与 CGO 构建契约、15 份空库 schema 通过。部署端传输、注入、操作员本机安装和实际五步 bootstrap 尚待 G2/G3/G5～G7/G11 接通，G5 仍不标完成；最终新 Agent 审查与正式验收继续保留。

主计划 G6 的共用运行层已实现：host 软件包携带纯 Go `moox-runtime`，统一私密运行计划、维护锁及继承 FD、跨发布暂停标记、PID/排空记录与正逆序启停。Linux pidfd 核验实际进程；启动屏障保证先登记再 exec，中断可恢复。健康探针使用共享 HMAC 并绑定制品摘要与本次 boot ID，守护依据 liveness，not-ready 或错误身份不自动重启。console-proxy 的停止预算保存实际三阶段之和，旧发布预算不被新配置缩短；取消后保留排空状态，强杀后确认退出。实际安装脚本、回滚、`setup pause/resume` 和 bootstrap 接线仍待 G2/G3/G6/G7/G11 完成，本共用层不代表代理已经正式部署。

G6 本批验证：本机 CLI 16 包全量 race/vet；两个架构的助手、真实代理和三个测试制品均关闭 CGO 在 macOS 构建。Linux 完整门禁十组必跑场景无跳过通过，含真实 HTTPS 长请求排空、暂停保护、恢复后 CA 保持和启动中断恢复；另外运行命令与部署包全包测试通过。实际 host 包在本机/Linux 摘要核对一致，包含两项主机服务与共用助手；格式/边界/854 模块图/文档/质量、相关软件包/身份/登记与 CGO 构建契约、15 份空库 schema 通过。Linux 未编译，正式环境未切换；生成脚本、WebSocket/SFTP/HTTP3、公开 ACME、最终新 Agent 审查和正式验收仍保留。

主计划 G2 的软件准备层已接入共用助手：打包/检查/解包归属独立 `unitpackage`，CLI 与 `moox-runtime extract` 共用校验。软件生产端摘要、平台、组件、清单和逐文件内容全部核对后，才将私密暂存目录原子发布到新目录；已有对象不覆盖，并发同目标只成功一次。远端入口拒绝其他架构。软件准备不写 current、身份、数据、私密运行计划或进程状态；配置注入、持久状态、激活/回滚和五步 bootstrap 尚须接通，G2/G3/G5～G7/G11 继续保留，正式环境未切换。

G2 本批验证：CLI 17 包全量 race/vet，三个共用包的独立模块 race/vet 与 tidy-diff 通过。助手双架构、CLI 和测试制品均在本机关闭 CGO 构建；Linux 软件包十组必跑场景及运行层十组必跑场景无跳过通过，包含实际 host 包解包/包内助手执行、错误架构拒绝、并发发布与真实代理 HTTPS 排空/暂停/CA 保持。运行命令与 CLI 打包入口通过，host 包两端摘要一致。格式/边界/854 模块图/文档/质量、软件包与 CGO 构建契约、15 份空库 schema 和 diff 通过。Linux 未编译；本批不替代激活/回滚、空环境 bootstrap、WebSocket/SFTP/HTTP3、公开 ACME、最终新 Agent 审查和正式验收。

主计划 G5 的身份消费层已接入 `unitbundle`：两端共用公开清单，可信 SSH 下载绑定 Admin 响应并逐文件限长/核对摘要，继续校验目标拓扑、放置、CA/快照、配置、证书链/精确 SAN/私钥和 Access 外部身份。新私密目录原子发布、已有对象不覆盖；服务材料去掉操作员身份，`moox-runtime inspect-bundle` 只输出公开清单。实际部署调用、业务配置注入和操作员本机安装仍待接通，G5 不标完成。另修复 Linux exec 切换时的进程路径/inode 快照竞态，旧实现由新压力测试复现，固定执行镜像后连续十轮通过；运行层完整门禁在隔离网络执行，现有业务未停止。

G5 消费层本批验证：Admin、CLI 与服务目录 56 个测试包全量 race/vet 通过，CLI 18 包；消费、软件包与助手命令的独立模块 race/vet 和三个模块 tidy-diff 通过。所有 Linux 纯 Go 制品均在本机关闭 CGO 构建；Linux 身份消费八组、软件准备十组、隔离网络运行层十一组必跑场景均无跳过通过，包含真实 Admin bootstrap/重跑/三主机导出和真实助手检查。原生 SSH 客户端以已核验指纹连接 Linux，从四份真实包取回私密材料、逐文件验证并生成服务材料，本机 race 通过。Linux 未执行编译。最终 host 软件包使用本批助手重新打包并验证；格式、边界、854 模块图、文档/质量、身份/软件包契约、15 份空库 schema 与 diff 检查通过。本批不替代实际配置注入、五步 bootstrap、I/J、全部编码后的新 Agent 审查或正式部署验收。

## 1. 目标与已确定的决策

将 Caddy 作为 Go 库集成到 `moox-console-proxy`，由同一二进制、同一进程承担控制台 HTTPS 和前端反向代理。部署端不再安装、下载或管理独立的 Caddy 可执行文件。

本次讨论已经确定：

| 事项 | 决策 |
| --- | --- |
| 组件职责 | `console-proxy` 仅作为前端入口，不承载节点控制通信或服务间数据面 |
| 统一身份与目录 | 组件 ID、服务名均为 `console-proxy`；注册到主计划新增的 `packages/servicecatalog`，仅 control、单副本、受保护 |
| 二进制 | `moox-console-proxy`，不另带 `bin/caddy` |
| 实现方式 | MooX 自有 main 调用 Caddy 库，不以 `caddycmd.Main()` 作为服务入口 |
| 进程模型 | Caddy 在 console-proxy 进程内运行，不通过 exec、子进程或系统 Caddy 启动 |
| HTTPS 与反向代理 | 使用 Caddy 标准模块；只允许为进程内排空增加最小生命周期适配，不重复实现证书管理和代理引擎 |
| 依赖 | 新模块加入 `go.work`，允许统一升级依赖；升级后执行工作区完整回归 |
| 静态资源 | 保留独立 `web-host`，本计划不把静态资源打进 console-proxy |
| 认证与业务 | 保留在 Admin；console-proxy 不接管登录、授权、数据库或 BFF 业务 |
| TLS 模式 | 保留现有 `internal`、`public` 两种能力，不把二进制集成变成证书体系重建 |
| 状态目录 | 保留实际证书、CA、ACME 账户和续期状态，不因组件更名而重新签发 |
| 安装来源 | 使用 MooX 自编译制品，取消官方 Caddy archive、checksum 清单和缓存变量 |
| 第一版配置更新 | 校验候选配置后受控重启；不新增远程热重载协议或 Caddy 管理 API |
| 诊断与探测 | `127.0.0.1:19528` 提供 health HMAC 保护的诊断；组件健康用 `readyz`，浏览器 HTTPS 另做端到端检查 |
| 整体交付 | 在 `feature/mooyang` 完成源码清理、最终制品验证和独立审查后，随主计划一次停机切换 |
| 项目原则 | 不保留双实现、旧命令兼容层或静默回退到官方 Caddy 的路径 |

“取消独立 Caddy”不等于把代理嵌入 Admin、Gateway 或其他业务进程。console-proxy 自身仍是独立的 MooX 组件。

## 2. 范围与非目标

### 2.1 本计划范围

1. 创建 console-proxy 模块并内嵌 Caddy。
2. 明确前端路由、TLS、生命周期、配置、日志和诊断契约。
3. 接入工作区、构建、完整安装包、binary-only 包和组件包。
4. 同步 shell 部署与 CLI 安装两条路径。
5. 更新组件注册、Doctor、现有健康检查和运维文档。
6. 与主计划的新入口和调用方完成本地集成后，删除新源码中的旧 Caddy 安装、启动和回滚实现；现场旧进程在正式切换时才停止。
7. 对清理后的最终候选制品完成本地全链路验证、独立审查和回归，再随主计划执行正式切换与环境验收。

### 2.2 非目标

- 不合并 web-host、Admin、Gateway 进程。
- 本子计划不重写 Admin 认证、服务签名、ACL、防重放或路由快照协议；主计划要求的 tRPC、密钥和调用方迁移由对应阶段交付。
- 不在 console-proxy 中实现 `/api/service/*` 或 `/api/gateway-control/*`。
- 不引入第三方 Caddy 插件，不开发业务型代理插件、自定义 ACME 客户端或通用反向代理配置平台；必要的排空适配见第 6.3 节。
- 不顺带修改 Monitor 的采集、存储、告警和调度实现；只同步新组件必需的身份与探针配置。
- 不自动信任本机系统证书库，不自动修改公网安全组或防火墙。
- 不把生成本计划视为实施代码、生产部署或删除线上进程的授权。

## 3. 当前实现与目标的差异

以下是本次检查的源码事实，不是对目标环境的在线验证。

| 范围 | 当前实现 | 目标 |
| --- | --- | --- |
| 代理组件 | 尚无 `modules/consoleproxy`；Doctor 仍使用 `admin_gateway` 描述公开入口 | 在 `servicecatalog` 独立注册 `console-proxy`，二进制 `moox-console-proxy` |
| 浏览器入口 | Caddy `9527` 转发 web-host `9528` 和 Admin `11000` | console-proxy 内嵌 Caddy，保持浏览器入口职责 |
| 控制通信 | `/api/gateway-control/*` 也经过浏览器 HTTPS 端口 | 改为 Admin `GatewayControl` tRPC；远端经 control 主机网关 TLS `11003`，control 网关直连本机 `11112` |
| 服务通信 | Caddy `11001` 转发 Gateway loopback HTTP `11002` | 内部经主机网关，外部调用方经 access；删除 `11001` 和 `/api/service/*` |
| Gateway HTTP | 当前限制为 loopback，使用普通 HTTP server，未提供服务器 TLS 配置 | 主计划交付本机 tRPC `11002` 与跨主机 TLS tRPC `11003`，不直接放开旧 HTTP 监听 |
| 公开诊断 | 浏览器入口的 `/healthz`、`/readyz`、`/metrics` 返回 404 | 继续公开拒绝，另用 MooX 内部鉴权诊断入口 |
| 组件健康 | 当前 `admin_gateway` 登记借用 Admin 的健康地址；主计划初稿对代理探测 HTTPS 首页 | 改用代理独立 `readyz`；HTTPS 页面单独报告端到端状态 |
| Caddy 管理 | `caddy-managed.sh` 管理 `bin/caddy`、PID、2019 管理端口和官方 archive | MooX 管理 console-proxy 生命周期，不再调用旧 manager |
| 部署路径 | shell 与 Go CLI 内嵌安装脚本都调用旧 manager | 两条安装路径同步改造 |
| 外层停止等待 | 生成的 `start.sh` 重启已运行进程时只等 1 秒，`stop.sh` 只等 5 秒，之后可能强杀 | 外层等待覆盖排空、引擎停止和清理预算 |
| 工具链 | workspace 为 Go 1.25.0，PR CI 为 1.24.0，CNB 使用 1.24 镜像；自建 runner 只打印版本 | 固定满足全部依赖要求的具体工具链，并校验 runner |

主要依据：

- [当前 Caddy 路由](../deploy/caddy/Caddyfile)。
- [Gateway 配置约束](../modules/hostgateway/internal/config/config.go) 与 [启动实现](../modules/hostgateway/internal/bootstrap/bootstrap.go)。
- [原生共享客户端与连接约束](../packages/gatewayclient/client.go)。
- [Caddy 管理脚本](../scripts/lib/caddy-managed.sh)。
- [shell 部署](../scripts/deploy/deploy-moox.sh) 与 [CLI 部署](../modules/cli/internal/setup/deploy/deploy.go)。
- [共享组件目录](../packages/servicecatalog/catalog.yaml)（复审时 Doctor 的独立清单现已在 A8 并入此处）。

## 4. 目标架构与路由契约

```text
Browser HTTPS
      |
moox-console-proxy
  MooX config / lifecycle / diagnostics
  Embedded Caddy HTTP + TLS + reverse proxy
      |
      +-- /api/admin/* --> Admin loopback browser API
      +-- page/assets  --> web-host loopback HTTP

Remote host-gateway -- TLS tRPC :11003 --> control host-gateway --> Admin GatewayControl :11112
Control host-gateway -- loopback tRPC --> Admin GatewayControl :11112
Internal service --> target host-gateway --> local tRPC service
SCF / factor-engine / moox-skill -- signed tRPC :11004 --> access -- TLS tRPC :11003 --> host-gateway
```

### 4.1 允许与拒绝的路由

| 请求 | 行为 |
| --- | --- |
| `/api/admin/*` | 转发 Admin，保留 path、query、method、body 和业务认证头 |
| 页面、前端静态资源、SPA 路由 | 转发 web-host，由 web-host 继续负责静态资源及 SPA fallback |
| `/api/gateway-control/*` | 明确返回 404，不转发 Admin，不落入静态资源 fallback |
| `/api/service/*` | 明确返回 404，不转发 Gateway |
| 其他未注册 `/api/*`，以及 `/api` | 明确返回 404，不能返回 index.html |
| `/healthz`、`/readyz`、`/metrics` | 公开入口返回 404 |
| Caddy 管理路由 | 不注册、不监听管理端口，不可从公开入口访问 |
| ACME HTTP challenge | public TLS 所需的协议入口，只处理证书挑战，不开放业务路由 |

明确拒绝的前缀要覆盖尾斜线、子路径和路径规范化后的访问。测试不能只检查一个字面 URL。

### 4.2 代理与安全语义

- 默认上游保留为同机 loopback：Admin `127.0.0.1:11000`、web-host `127.0.0.1:9528`。
- 第一版不支持任意远程上游、动态用户 URL 或 request 参数指定代理目标。
- 保留现有响应头、压缩策略和浏览器同源行为，不同时扩大 CSP 或跨域权限。
- 保留 HTTP/WebSocket/流式响应所需的转发语义；不读取或缓存完整业务 body，不替代上游认证。
- 不默认信任客户端 `X-Forwarded-*`。只有已核验的前置代理来源才能加入 trusted proxies。
- HMAC 等签名路径、query 和 body 不得被代理改写；使用实际登录和签名请求回归，不能只测匿名首页。
- HTTP/3 是否启用应保持现有有效配置；若启用，同时检查 UDP 监听归属和安全组要求，不只检查 TCP。

## 5. 非前端入口迁移门禁

这是彻底删除外置 Caddy 的前置依赖，不是让 console-proxy 临时兼任服务网关的理由。

### 5.1 已确定的替代入口与责任边界

目标依据为主计划 A/B/C/D/E/G 阶段及网关重构设计 3.3～3.7；P0 核对实施进度和现场映射，不重新选择协议和证书所有者。

| 旧通路 / 配置 | 目标通路 | 迁移范围 | 主计划责任 |
| --- | --- | --- | --- |
| `/api/gateway-control/routes`、`/status`，`control_plane.base_url` | `trpc.moox.admin.GatewayControl` 的 `PullSnapshot` / `ReportStatus`；control 网关直连 `127.0.0.1:11112`，其他主机经 control TLS tRPC `11003` | 所有主机网关的控制客户端、服务身份和部署配置；删除旧 control HMAC 配置 | B 的 GatewayControl、C5、G3/G5 |
| 内部 `11001/api/service/*`、旧 HTTP 目标配置 | 调用方查目录；本机 tRPC `127.0.0.1:11002`，跨主机 TLS tRPC `<主机>:11003`，网关只转发本机服务 | Trade Secret、Strategy TradeOwner、Admin 控制台转发、Collector 等内部调用方 | A 的 gatewayclient、B8、C、D |
| SCF、因子引擎、moox-skill 的旧网关地址 | 签名 tRPC `<access 主机>:11004` → access → TLS tRPC `<目标主机>:11003` | 所有外部调用方；SCF 使用 `scf-collector` 身份及新的 access 环境变量，因子引擎和 skill 切换 access 配置 | E1/E2、G9 |
| `defaults.go`、部署种子、持久化旧服务地址 | `servicecatalog` ＋ `moox.toml` 部署表，经 `SyncHostPlacements` 生成部署、目录与路由 | 旧种子和 SysDeploy v1 由主计划删除；本组件只登记 control 上的 `console-proxy` | A/B、G1/G3/G4 |
| 服务调用对 Caddy CA 的依赖 | 主机网关只信任独立 MooX 私有 CA；console-proxy 继续使用自身 internal CA 或 public ACME | TLS tRPC 客户端和主机网关证书；浏览器 CA 发布工具单独保留 | B 的 PKI、C2、G5 |

当前调用链的检查入口：

- `modules/hostgateway/internal/controlplane/client.go`：Pull/Report。
- `modules/trade/internal/secretclient/client.go`：Trade Secret。
- `modules/strategy/internal/bootstrap/logical_account_gateway.go`：Strategy TradeOwner。
- `modules/admin/internal/console/forward.go`：Admin TradeConsole（B7 已完成包改名）。
- `modules/collector/internal/scfinvoker/client.go`：Collector 服务调用。
- `scripts/deploy/deploy-moox.sh`：`MOOX_SCF_SERVICE_GATEWAY_TARGET` 生成。
- `modules/admin/internal/service/sysdeploy/defaults.go`：服务入口发布。

### 5.2 P0 核对与迁移约束

逐项记录旧地址、新地址、监听地址、协议、证书 SAN/CA、调用方、配置事实源、主计划任务、切换顺序和验证方法。独立 MooX CA 由主计划 PKI/部署流程创建和签发，不能复用 console-proxy 的 Caddy CA 代替。

`11002` 将从 HTTP 改成 tRPC，不能在同一监听上要求新旧协议并行。按照主计划第 13 节统一停机切换，先在本地临时拓扑验证；当前三台运行主机不承担 P7 之前的中间版本演练。

执行约束：

1. 不把 `11000`、`11002` 改为 `0.0.0.0` 明文监听来替代 HTTPS。
2. 不关闭客户端 CA/主机名校验，不使用 `InsecureSkipVerify` 绕过迁移。
3. 跨主机的主机网关链路必须使用 TLS、独立 MooX CA 和正常的主机名校验。
4. 不为替代入口新增外置 Caddy 进程，也不把它们重新塞回 console-proxy。
5. 浏览器 API 和 web-host 的同机 loopback HTTP 保留。外部 access `11004` 按主计划采用明文 tRPC＋签名，access 到主机网关使用 TLS；本子计划不对 access 另加 HTTPS 迁移门禁。
6. 替代入口尚未交付时可以开发和测试新模块；源码清理条件见第 8 节 P6，现场停旧条件见主计划第 13 节，二者分别验收。

### 5.3 本地集成与正式切换的证据

- 每个主机网关通过新 tRPC GatewayControl 获取本次发布的新 route hash，并成功 ReportStatus。只看到旧缓存可用不算通过。
- Trade Secret、Strategy TradeOwner、Admin TradeConsole 和远程 Collector 实际成功调用。
- SCF 已发布函数的真实环境配置指向新入口，至少一次真实调用成功；源码模板修改不等于已迁移。
- TLS 链路上的错误 CA/主机名、错误服务身份、缺失或错误服务签名、重放请求均被拒绝；access 按外部调用方白名单验收。
- 本地全链路测试禁用旧 Caddy 二进制和入口，使用最终候选制品验证；正式切换时停旧 Caddy，再重复真实调用验收。两类证据分别记录。

本地模拟 SCF 只能证明协议和调用链集成。真实云函数版本、环境变量、VPC 与调用结果属于主计划正式切换验收，不作为删旧源码的前置条件，也不能被本地结果替代。

## 6. 模块、配置与进程契约

### 6.1 模块布局

新增内容按现有 Go 模块习惯组织，不建立新的共享代理框架：

```text
modules/consoleproxy/
  go.mod
  go.sum
  cmd/server/main.go
  config/app.yaml
  internal/config/
  internal/bootstrap/
  internal/engine/
  internal/health/
  README.md
```

`internal/engine` 只封装本组件对 Caddy 的配置加载、启动和停止，不对外暴露通用 Caddy SDK。具体文件拆分由实现规模决定，避免空壳分层。

统一身份：模块目录 `consoleproxy`，组件 ID 与服务名均为 `console-proxy`，二进制 `moox-console-proxy`。`servicecatalog` 声明 `scope: control`、`replicas: single`、`protected: true`、`health.kind: readyz` 和健康端口 `19528`，不增加 `console_proxy` 别名，不把 Admin 的健康状态当作代理进程状态。

目录及部署登记分别由主计划 A/B/G 提供。本子计划新增条目、二进制/PID 身份与探针接入；`packages/doctor/components.yaml`、`config/setup/service-deployments.yaml`、`sysdeploy/defaults.go` 的最终处置是随主计划删除，不为 console-proxy 再新增一套旧种子。

### 6.2 配置真值

运行配置以组件 `app.yaml` 为唯一事实源，部署脚本负责生成它。第一版字段至少包含：

| 配置组 | 内容与约束 |
| --- | --- |
| public | 主机名/IP、绑定地址、浏览器 HTTPS 端口；保持现有默认 9527 和显式覆盖能力 |
| tls | `internal` / `public`、证书持久化目录、public ACME 所需设置 |
| upstreams | 同机 Admin、web-host 地址，校验为允许的 loopback 目标 |
| health | `127.0.0.1:19528`，仅 loopback；P0 核对现场端口无冲突，不复用 Caddy 2019 |
| lifecycle | 停机排空上限、引擎停止上限、资源清理余量、启动及 TLS 就绪等待上限；外层脚本读取同一组预算 |
| logging | 复用 MooX 日志输出和轮转配置，敏感字段不进入日志 |

如复用 Caddyfile，将其作为代码仓库维护的固定模板，由类型化配置渲染后经 Caddy adapter 转为 JSON。不要同时允许任意外部 Caddyfile 覆盖组件路由边界；渲染使用可靠的转义或结构化构造，禁止直接拼接用户字符串。

生成配置必须显式关闭 Caddy admin endpoint、动态配置拉取和 autosave/resume，防止出现第二份配置真值。internal 模式显式设置 `apps.pki.certificate_authorities.local.install_trust=false`；使用 Caddyfile adapter 时生成等价设置并断言最终 JSON，不依赖 Caddy 默认值。validate、启动和重部署都不得触发系统、Java 或 Firefox 信任库安装。

显式指定 storage root，默认 `<部署目录>/data/caddy/caddy`；不依赖默认环境变量改变监听和存储位置。配置路径与预算由同一渲染结果供 shell/CLI 消费，不在脚本中另写 1 秒或 5 秒的停止期限。

### 6.3 启动、运行与退出

1. 加载并校验 MooX 配置，初始化统一日志和内部诊断；ready 初始为 false。
2. 确认数据目录权限、监听配置和必要的文件能力，拒绝接管无关进程的端口。有 internal 历史或留存 CA 材料的安装在 Load/Run 前按第 7.1 节核验持久 CA、密钥及指纹；缺失或不一致时先失败，禁止自动生成新 CA 替代旧状态。
3. 构造受约束的 Caddy 配置，注册标准模块与 `time/tzdata`。
4. 使用 `caddy.Load` 或 `caddy.Run` 启动同一进程内的 HTTP/TLS 应用。调用返回后 main 仍等待退出信号，不能直接结束。
5. internal 首装按第 7.1 节建立指纹基线和发布副本，再用实际 HTTPS 握手确认对应 SNI/Host 的证书可用。public 模式按系统信任校验目标证书；ACME 尚未完成时保持 not-ready，不把 Load 成功当作证书已签发。业务准入在本机状态和 TLS 校验完成后才打开，上游依赖临时失败单独报告。
6. SIGTERM/SIGINT 到来后先标记 not-ready，关闭业务请求准入并执行有界排空，再停止 Caddy，最后清理证书锁、诊断服务和日志资源。
7. 启动失败退出非零；可恢复的上游业务故障不导致进程自杀或重启循环。

Caddy 使用进程级状态，应用中只建立一个 engine 所有者；集成测试不得并行启动多个相互覆盖的 Caddy 配置。

不调用 `caddycmd.Main()` 后，不能假定 CLI 的初始化和退出处理仍会执行。实现时显式核对 ACME 条款确认、User-Agent、信号处理及 CertMagic 锁清理；不得为了复用 CLI 初始化而让 CLI 接管整个 MooX 进程。

**排空实现门禁：** v2.11.4 的 HTTP App 仅在 `caddy.Exiting()` 为 true 时等待所有 shutdown goroutine；普通库调用 `caddy.Stop()` 不自动进入该状态。因此不能把 Stop 返回当作旧请求已完成，也不能用固定 sleep 或访问未导出的退出标志代替排空。

P2 先完成真实请求原型，再固定以下最小适配：在前端业务路由最外层注册本组件自有的准入/活动请求计数 handler，所有实际代理仍使用标准模块。退出时关闭准入，新业务请求返回 503，等待已有 HTTP/流式/WS 请求结束；到配置 deadline 后取消剩余工作，停止引擎并退出，保证遗留连接最终关闭。排空计数必须覆盖 WS/流式请求的完整存续期，不能只统计 response header 或升级握手。

配置非零且有上限的 Caddy `grace_period`，但它不能替代上述应用级等待。使用真实长请求证明 deadline 内可完成、超过 deadline 能结束；清锁、释放依赖和 main 退出必须晚于正常排空完成或明确的超时强制结束。若公开 API/适配原型无法满足这一契约，P2 不通过，先调整生命周期设计，不通过 fork Caddy 或反射私有字段绕过。

**外层启停契约：** `start.sh` 替换已运行进程、`stop.sh`、`restart.sh`、CLI 安装激活、失败回滚和 watchdog 必须遵守同一停止预算：

```text
外层停止等待时间 >= 请求排空上限 + 引擎停止上限 + 资源清理余量
```

先发送 SIGTERM 并等待进程退出，只有超过该预算且重新确认进程身份后才允许强制结束。持有维护锁或存在暂停标记时，watchdog 不得把排空中的进程重新拉起。P2 冻结各预算默认值及上限，P5 用实际脚本入口验收，不能只向测试进程直接发信号。

准入/计数适配优先不包装 ResponseWriter；确需包装时保留 Hijacker、Flusher 和 ResponseController 的访问能力。用 SSH WebSocket、SFTP 上传下载和流式响应验证真实协议行为，避免排空适配破坏升级或刷新响应。

### 6.4 命令与配置更新

第一版提供以下 MooX 命令契约，具体参数排版沿用现有组件模式：

- `serve --config <path>`：运行代理。
- `validate --config <path>`：解析配置、生成目标配置并检查组件不变量；不监听业务端口，不签发证书，不改生产状态。
- `version`：输出 MooX 版本、commit、构建时间和 Caddy 引擎版本，不能只输出 Caddy 版本。

`validate` 不等于完整 module provisioning，也不等于 TLS 就绪。需要执行 `caddy.Validate` 的测试使用隔离的测试状态目录，避免 provisioning 对生产 CA/账户产生写入。

第一版配置变更通过 validate 后受控重启应用，不保留 `caddy reload`、`caddy upgrade`、2019 管理 API 或双重热重载协议。Caddy 在内部自动续期证书不需要重启组件。若未来需要无中断热重载，单独扩展 MooX 统一配置生命周期。

### 6.5 健康、诊断和日志

- 使用 `packages/healthz` 的现有 payload、liveness/readiness 区分和鉴权包装，不新增匿名诊断端点。
- liveness 表示进程及引擎仍在工作；上游 Admin/web-host 临时失败不应被解释为必须重启代理。
- readiness 表示已加载预期配置、代理入口可用、对应 TLS 握手可用且不在排空阶段。
- Admin/web-host 的业务可达性作为依赖状态单独报告，部署端到端验收仍必须检查它们。
- 组件状态使用 `health.kind: readyz`，Monitor、Doctor 和本机脚本以 health HMAC 探测 `127.0.0.1:19528/readyz`；自愈只以 liveness 和进程身份判断重启，not-ready 或首页失败不触发重启循环。
- console-proxy 与 Monitor 都在 control，内部诊断仅从 control 本机探测；部署发现不得把这个 loopback 地址改写成主机公网地址。公开入口继续拒绝 health/metrics，不为远程采集放开诊断监听。
- 浏览器 HTTPS 首页作为单独的端到端检查，分别报告 TLS 与上游页面结果，不替代组件健康。检查显式区分 TCP 连接地址、SNI/Host 和信任根：internal 加载已验证的 `certs/caddy/root.crt`，public 使用系统信任；本机连接 loopback 时仍验证配置中的公开主机名，不使用 `InsecureSkipVerify` 或自动信任库安装。
- 记录组件版本、Caddy 版本、有效监听、TLS 模式、证书过期时间和加载失败原因；不记录 token、HMAC secret、Cookie、私钥或完整敏感 body/query。

## 7. 证书、状态与部署契约

### 7.1 持久状态

第一版保留现有 `data/caddy`、`certs/caddy` 的状态归属和运维路径，避免仅因二进制更名引入数据目录迁移。当前旧 manager 设置 `XDG_DATA_HOME=<部署目录>/data/caddy`，实际默认 storage root 是 `<部署目录>/data/caddy/caddy`。组件配置显式指向这一层级，P0 核对现场覆盖值；不能把上层 `data/caddy` 当成 root，否则会生成另一份 CA。

| 状态 | 默认位置与契约 |
| --- | --- |
| Caddy storage root | `data/caddy/caddy`；相对它保存 `pki/authorities/local/`、证书和 ACME 账户 |
| internal CA 持久指纹基线 | `data/caddy/internal-ca.sha256`；跨 TLS 模式保留，与 storage 一同备份、保留和恢复 |
| internal CA 发布副本 | `certs/caddy/root.crt`、`root.sha256`；供浏览器 CA 导出和校验使用 |
| 主机网关信任材料 | 主计划的 MooX CA/主机证书；不从上述 Caddy 路径生成或取代 |

- 保留 internal CA 的完整链和私钥、public ACME 账户、证书、锁及续期状态。
- 普通重新部署、版本回滚和业务 `reset-data` 都不删除证书状态。
- 复制状态目录前停止其唯一写入者；新旧代理不能同时写同一份生产存储。
- 权限沿用最小权限原则；制品包不携带现场私钥、账户、secrets 或 data。
- CA 导出和明确授权的信任库安装工具继续保留；不机械删除所有文件名含 caddy 的工具。
- internal CA 指纹在切换前后保持一致，除非另有明确的证书轮换操作。

**启动前核验：** 有 internal 历史或留存 CA 材料的安装，在 Load/Run 前加载持久 root 及其私钥，校验证书 CA 属性、有效期、密钥匹配及可信指纹基线；已有发布材料与持久基线也必须一致。首次接替旧 manager 时，以 P0 核对的旧存储和可信发布材料建立持久基线。此类安装出现材料不完整、缺失或指纹冲突时先失败，不能作为首装自动重新生成。明确的空目录首装，或确认从未创建 internal CA 的 public 安装首次启用 internal，才允许创建第一份 CA；不能仅凭文件缺失推断为首装。

**显式接替旧 `publish_ca`：** internal 首次启动等待实际 root 创建，完成同样的证书和密钥检查，再原子保存持久基线并发布 `certs/caddy/root.crt`、`root.sha256`。后续启动先核验已有材料，加载完成后复核并发布；不匹配时失败并要求显式轮换，不覆盖旧的信任材料。

指纹算法为证书 DER 的 SHA-256。现有 `root.sha256` 来自 `openssl x509 -fingerprint -sha256` 去掉前缀的结果，格式为大写十六进制、逐字节用冒号分隔。Go 实现按字节比较指纹，读取时校验长度及编码，写入沿用该格式并以单个换行结尾；不能把大小写或冒号差异误判为 CA 轮换。

public 模式成功激活时清理过期的 internal CA 发布副本及其发布指纹，避免工具把 public 入口误判为 internal；保留 `data/caddy/internal-ca.sha256` 和持久 storage 中供回滚使用的 CA/账户。首次 public 安装可以没有 internal CA，但已有 internal 状态不得因切换模式而损坏或失去指纹基线。失败回滚恢复对应模式的发布副本，回到 internal 时仍核对持久基线。CA 导出和信任库工具只消费当前 internal 模式下经过验证的发布结果，而不是任意扫描一个 root 文件。

### 7.2 权限与监听

保留非 root 运行方式。Linux 下如需要 80/443 等特权端口，把所需最小文件能力施加到 `moox-console-proxy`，不再施加到已删除的 `bin/caddy`；每次替换二进制后重新核对能力。

public 模式保留当前 ACME challenge 策略及端口需求。不能因为浏览器端口是 9527 就假定不需要 80，也不能为解决挑战失败自动抢占其他服务端口或修改防火墙。

### 7.3 发布制品

完整包、binary-only 包和组件包都必须包含新二进制及所需配置/生命周期脚本。把新制品纳入现有 MooX SHA256 清单，不新增一套 Caddy 专用成品签名体系。

`go.sum` 负责依赖源码完整性；发布包和二进制继续走 MooX 制品校验。官方 archive SHA512 清单随 archive 消费路径一起删除。

保持现有构建矩阵：Linux amd64/arm64、Darwin amd64/arm64、Windows amd64。Linux 两架构必须完成真实运行验证；其他平台至少完成编译和打包校验，不将编译成功描述为已支持对应平台生产部署。

### 7.4 激活与回滚

1. 在主计划第 13 节切换前完成本地集成、最终候选制品验证、独立审查与完整回归；候选失败时不停止现场旧服务。
2. 保存当前版本、配置、证书状态位置、权限及运行身份的回滚信息。
3. 停止唯一的旧代理写入者，完成一致的状态保留/复制，再激活新版本。
4. 启动 console-proxy，验证内部鉴权诊断、TLS 握手、页面及已认证 API。
5. 失败则恢复完整上一版制品与配置、权限和服务地址，停止失败的新进程后再启动旧版本。
6. 首次从旧架构切换时，把 Admin/Gateway 地址迁移纳入同一发布批次的回滚清单；只回滚代理二进制不一定能恢复旧通信链路。

回滚使用归档的完整上一版制品，不为旧 manager 保留永久运行兼容分支，也不在失败时临时联网下载 Caddy。第一版受控重启有短暂入口不可用窗口，发布前明确维护窗口，不宣称零停机切换。

P1～P6 的原型、安装、切换和回滚测试使用本地临时目录、端口及模拟 control/storage 拓扑，不要求新增长期测试环境，也不在当前运行主机安装中间版本。P6 删除新源码中的旧 manager/下载路径后重建最终候选制品；P7 对该制品完成主计划阶段 J 的全链路验证、独立审查和回归。正式切换统一进入主计划第 13 节，并复用已验证的步骤。

删除新源码中的旧实现，不等于删除现场旧制品或提前停止旧进程。旧完整制品、生命周期脚本、网络/数据库配置和证书状态按主计划回滚清单归档；现场旧版本一直保留到正式切换和规定的清理窗口。协议复用同一端口时，在维护锁内整批停旧/启新，不能要求冲突监听同时成功。

## 8. 分阶段执行清单

阶段之间必须满足退出条件。A～J、G2/G7 等编号均指[主计划](计划/网关与服务部署重构执行计划.md)中的实际阶段。console-proxy 的原型可与网络工作并行，但最终注册、部署和清理必须与对应主任务一起集成。

### P0：冻结边界与依赖门禁

- [ ] 重新核对源码基线、并行改动和当前运行拓扑。
- [ ] 按第 5 节已确定的协议、端口和 CA 边界补齐迁移表，核对主计划 B/C/D/E/G 的实际交付进度。
- [ ] 核对 `console-proxy` 身份、control/单副本/受保护约束、`127.0.0.1:19528` 诊断监听、配置路径及实际 storage root。
- [ ] 记录现有 internal CA 指纹、public 证书身份和 ACME 状态位置，不输出私钥或凭据。
- [ ] 明确第一版采用受控重启，不依赖 Caddy admin endpoint。
- [ ] 记录检查基线并修复文档清单遗漏；本次复审发现的 `packages/frequency`、`packages/storagepolicy` 缺项已在本轮文档修订中补齐，实施开始时重新运行检查。
- [ ] 明确本地临时拓扑与正式环境的证据边界；当前运行主机只参与主计划第 13 节的一次正式切换。

退出条件：实现输入无歧义；没有把仍被节点/SCF 使用的旧入口误列为可直接删除。

### P1：工作区、工具链与最小模块

- [x] 新增 `modules/consoleproxy` 的 go.mod、go.sum、main、配置加载和版本输出。
- [x] 初始固定 Caddy `v2.11.4`，加入标准模块和 tzdata。
- [x] 加入 go.work，统一解析依赖图，审查 zap、Prometheus、x/net、x/crypto 等实际升级，完整差异见第 0 节链接。
- [ ] Caddy 该 tag 自身最低 Go 为 1.25.1；选定满足完整依赖图的具体工具链版本并统一到 CI、CNB、开发与发布构建。
- [ ] 不只写“Go 1.25”，也不依赖 `GOTOOLCHAIN=auto` 在发布时临时下载未预置工具链。
- [ ] 更新 `scripts/ci/check-runner.sh`，对自建 runner 实际 Go 版本做失败即退出的校验；同步 runner 工具链预置和远程编译机，不只修改 PR CI/CNB。
- [ ] 新模块纳入现有 workspace test/vet/格式与边界检查，保留其他组件的 CGO 设置。
- [x] 新模块加入 `go.work` 的同一提交更新 `docs/总体设计.md` 模块表，避免文档门禁漏项。

退出条件：版本输出和配置测试通过；依赖升级有完整清单；新模块没有被全量测试静默跳过。

### P2：进程内引擎与前端路由

- [x] 实现受约束配置渲染/适配，关闭 Caddy admin、autosave、动态配置拉取及 PKI 自动信任安装；断言最终配置。public 不加载 PKI，internal 显式 `install_trust=false`。
- [ ] 实现同进程 Load/Run、main 等待、启动失败清理和退出排空。
- [ ] 先通过第 6.3 节排空原型门禁，再实现准入/完整活动请求跟踪，禁止用 Stop 返回或 sleep 充当完成信号。
- [ ] 实现 internal/public TLS，显式指定持久存储，不改变现有证书身份。
- [ ] 实现 Load/Run 前已有 CA/密钥/指纹检查、首装建立基线、OpenSSL 指纹格式及 public 模式发布副本清理；持久基线跨模式保留。
- [ ] 实现前端允许路由和非前端拒绝路由；保留 headers、压缩、WS/流式请求行为。
- [x] 实现无生产副作用的 validate 命令，测试独立引擎版本输出。
- [ ] 冻结排空、引擎停止及清理预算，验证 WS/流式完整存续期与到期取消；不破坏 ResponseWriter 的协议能力。
- [ ] 使用测试证书/本地 ACME 测试服务验证，不让普通单元测试访问生产 CA 或修改本机信任库。

退出条件：不依赖 PATH 中的 caddy，也没有 Caddy 子进程；实际 HTTPS、页面、API 转发及真实长请求排空通过；internal 首装能取得正确的 CA 发布副本。

### P3：组件诊断与注册

- [x] 接入 healthz 共享状态与鉴权，建立独立内部诊断监听。
- [x] 分开 liveness、readiness 和上游依赖故障；TLS 未就绪不得报 ready。
- [ ] 接入主计划 A/B 的 `servicecatalog` 条目，登记 `console-proxy`、`moox-console-proxy`、control/单副本/受保护和 `readyz:19528`；同步 Doctor/PID/二进制身份。
- [ ] 保持 Admin、web-host、console-proxy 三种进程身份分离，不创建重复的虚假入口组件。
- [x] 随主计划 F1 接入 control 本机鉴权探针；不把 loopback 改写为公网地址。HTTPS 页面单独做端到端检查，显式配置 CA、SNI/Host；实际入口配置由 G 的部署渲染接入。
- [ ] 随主计划 G4 切换部署登记到 `SyncHostPlacements`，旧目录和种子随主计划删除，不为本组件增加旧身份别名。

退出条件：内部合法鉴权可观测，匿名被拒绝；公开诊断不可达；组件状态与页面端到端状态分开，代理故障能定位到新组件。

### P4：构建与安装包重组，对应 G2

- [x] `build.sh` 新增 `console-proxy` target 并加入 `all`；只对本组件使用 CGO_ENABLED=0。
- [ ] 完整 release 创建组件目录，装入新二进制、配置和生命周期脚本。
- [ ] binary-only release 的显式 binary_names 和 SHA256 manifest 加入新二进制。
- [ ] binary-only publish/restart 识别新服务；确认所选新制品有对应摘要并经过校验。
- [ ] 组件 ZIP/单服务安装路径接入 console-proxy，包内不含 data/run/logs/secrets/certs。
- [ ] 五平台矩阵都检查制品内容，避免某条打包路径仍夹带旧 archive 或遗漏新组件。

退出条件：三类候选制品内容正确，构建不下载官方 Caddy archive，制品摘要包含新二进制。P6 清理后重建最终制品；P7 放行前不发布或替换当前运行版本。

### P5：双部署路径实现与隔离测试

- [ ] 改造 shell 部署生成的 start/stop/status/healthcheck、stage、activation 和 rollback。
- [ ] 改造 CLI 的控制节点安装内嵌脚本，包括停止写入者、状态复制、启动失败恢复和 browser readiness。
- [ ] 更新端口归属、PID 身份、文件能力及失效 PID 处理，不误杀其他 Caddy 或其他服务。
- [ ] 全部外层停止入口读取统一预算，移除本组件路径上的 1 秒/5 秒强杀等待；只在总预算超时并复核身份后强制结束。
- [ ] 与主计划 G6 的维护锁、暂停标记和 `--maintenance-lock-held` 对齐；排空期间 watchdog 不重启进程，重复安装不绕过暂停。
- [ ] 候选版本的代理自愈只重启 moox-console-proxy，不再调用 `caddy-managed.sh`；不提前修改当前运行版本的生命周期。
- [ ] `reset-data` 和版本目录切换保留证书/ACME 状态；回滚不生成新的 CA。
- [ ] 覆盖 internal 首装、internal/public 模式切换、指纹异常拒绝和失败恢复发布副本。
- [ ] 更新控制节点 profile；非 control profile 不打包、不启动 console-proxy。
- [ ] 经实际 `stop.sh`、`restart.sh`、shell/CLI 安装激活及失败回滚入口验证长 HTTP、流式、SSH WebSocket 和 SFTP 请求；包含短于与长于 deadline 两类请求。

退出条件：shell 和 CLI 均能在本地临时拓扑首次安装、重复安装、失败回滚，状态、权限、排空预算与暂停行为正确。当前运行环境继续使用完整旧版本，直到主计划第 13 节正式切换。

### P6：网络集成、旧源码清理与最终候选制品，对应 G7

本阶段只在本地临时拓扑集成和清理源码。开始条件是主计划 B/C/D/E 的对应入口及调用方已具备本地验证能力，G 的部署工具已可生成候选包；现场版本保持不变。

- [ ] 按第 5 节迁移表核对新的 tRPC GatewayControl、主机网关、access、内部和外部调用方，不再生成旧服务地址和控制 HTTP 配置。
- [ ] 在本地运行真实新入口与调用方，验证新 route hash/ReportStatus、业务服务调用、模拟 SCF 流程、浏览器登录与已认证 API。
- [ ] 删除旧 manager、官方 checksum 文件、archive 下载与缓存变量、旧 2019 探针和 dead code。
- [ ] 删除旧 `11001` 站点和 `*.no-admin` 模板；前端模板如仍复用，只作为本组件固定的受约束模板。
- [ ] 删除或替换旧 Skill prerequisite；保留仍有调用方的 CA 导出/信任库工具。
- [ ] 更新所有现行测试，移除对旧 helper/archive 的正向依赖，增加旧依赖不存在的反向断言。
- [ ] 更新架构、运维和 Skill 文档。历史计划如仍保留，标明被本计划替代的范围，不把历史描述当作当前命令。
- [ ] 重建三类最终候选制品，记录版本、commit、平台和摘要；禁用旧入口后重新验收安装、重启、排空和失败回滚。
- [ ] 整批回滚使用本地归档的完整旧制品；最终新制品不携带旧 manager。正式现场所需的旧制品、数据库、证书、SCF/VPC 和防火墙恢复信息由主计划第 13 节归档。

退出条件：最终候选制品及本地新架构不需要旧 Caddy 二进制、脚本或网络下载；模拟非 control 节点不运行 console-proxy/Caddy。现场旧版本尚未切换，真实 SCF 证据在正式验收取得。

### P7：最终制品全链路验证、独立审查与交付门禁

- [ ] 使用 P6 清理后构建的最终候选制品，完成主计划阶段 J 的本地全链路验证及本计划第 10 节验证。
- [ ] 新起 codeCR Agent，审查路由边界、TLS/证书状态、生命周期、两条部署路径和依赖升级回归。
- [ ] 主 Agent 独立核验所有审查结论并处理有效问题；所有审查 Agent 完成后再汇总结论。
- [ ] 审查修复后若代码、配置或脚本变化，重建候选制品、更新摘要并复测相关场景，保证放行对象与测试对象一致。
- [ ] 在干净集成工作树完成第 10 节验证；不能把依赖污染、漏测或未验证的平台标为通过。
- [ ] 保存最终构建清单、测试结果和本地临时拓扑的进程/监听/证书/请求证据；把现场/云端未验证项单独列出。
- [ ] 按当前任务授权完成提交、推送；发布放行前确认正式目标、版本、维护窗口、回滚制品及主计划全部依赖。

退出条件：代码、最终制品、本地全链路、独立审查和回归分别有证据，满足主计划正式切换前门禁。现场切换和真实 SCF 验收另行记录，不把本地放行写成正式交付完成。

P7 通过并获正式环境操作授权后，按主计划第 13 节执行一次切换，完成本计划第 11 节真实环境证据验收；失败时使用已验证的整批回滚流程。

依赖关系：`P0 -> P1 -> P2 -> P3/P4 -> P5 -> P6 -> P7 -> 主计划第 13 节正式切换`。P1/P2 原型可与主计划 A～E 并行；P3 登记和发现接入依赖 A/B/F，P4/P5 与 G2/G6/G11/G12 集成，P6 同时依赖上述部署实现和 B/C/D/E 对应迁移，P7 对应主计划 J 及交付审查。旧源码在 P6 退出，现场旧入口在正式切换时退出；不放宽 console-proxy 路由规避依赖。

## 9. 实施文件清单

下表为修改入口，不要求为了形式修改每一个文件；执行时沿真实调用链更新，删除无调用的旧分支。

| 范围 | 文件/目录 | 动作 |
| --- | --- | --- |
| 新模块 | `modules/consoleproxy/` | 新增引擎、配置、生命周期、诊断与测试 |
| 依赖 | `go.work`、实际升级模块的 go.mod/go.sum | 工作区集成与依赖统一 |
| 工具链 | `.github/workflows/ci.yml`、`release.yml`、`.cnb.yml`、`scripts/ci/check-runner.sh`、编译机预置 | 统一具体 Go 工具链，校验自建 runner 实际版本 |
| 构建 | `scripts/build/build.sh`、`Makefile` | 新目标与 test-console-proxy 门禁 |
| 完整发布 | `scripts/release/release.sh`、`release-matrix.sh` | 新组件及平台矩阵 |
| binary-only | `scripts/build/build-release-binaries.sh`、`scripts/release/publish-release-binaries.sh` | 制品列表、摘要和重启 |
| 组件包 | `scripts/build/package-service.sh`、`scripts/test/contract/test-package-service.sh` | 标准组件包结构 |
| shell 部署 | `scripts/deploy/deploy-moox.sh` | stage、配置、启动、自愈、激活及回滚 |
| CLI 安装 | `modules/cli/internal/setup/deploy/deploy.go`、`deploy_test.go` | 同步 Go 内嵌安装流程 |
| 身份与注册 | 主计划新增 `packages/servicecatalog`；`modules/cli/internal/doctor/bootstrap.go`、SysDeploy v2 | 注册 `console-proxy`、control/单副本/受保护、`readyz:19528`；Doctor 读取统一目录 |
| 部署登记 | `modules/cli/internal/setup/client/registry.go`、`moox.toml.example` | 随主计划调用 `SyncHostPlacements`，只登记 control 上的代理部署 |
| 监控接入 | 主计划的 `modules/monitor/internal/placement/`、`internal/probe/`、`internal/healthview/` | 内部鉴权组件健康与 HTTPS 端到端分开；control 本地探测不改写为公网地址 |
| 旧目录与种子 | `packages/doctor/components.yaml`、`config/setup/service-deployments.yaml`、`modules/admin/internal/service/sysdeploy/defaults.go` | 随主计划 A/G4 删除，不新增本组件旧身份别名或种子 |
| 旧代理实现 | `scripts/lib/caddy-managed.sh`、`scripts/deps/caddy-v2.11.4-checksums.txt` | P6 本地集成后从新源码和制品删除，现场归档旧制品保留 |
| 配置模板 | `deploy/caddy/` | 保留所需固定前端模板；P6 删除 11001 站点、`*.no-admin` 和下载依赖 |
| CA 工具 | `scripts/deploy/install-caddy-ca.sh`、`skills/moox/scripts/caddy-ca.sh` | 保留或调整实际 CA 路径，不与下载代码一起删除 |
| Skill prerequisite | `skills/moox/scripts/caddy-prerequisite.sh` 及对应测试 | 删除独立 Caddy 安装前置条件 |
| 总体与运维文档 | `docs/总体设计.md`、`docs/部署与运维.md` | 实施时同步真实模块表、构建、端口、证书、启停与回滚说明 |
| 模块文档 | `docs/模块/管理后台.md`、`docs/模块/节点网关.md`（主计划改为主机网关）、`前端.md`、`监控.md`、`命令行工具.md`；新增 `控制台代理.md` | 更新组件边界、独立健康、页面端到端与 CA 信任 |
| 计划与导航 | `docs/计划/网关与服务部署重构设计.md`、`网关与服务部署重构执行计划.md`、`docs/README.md`、`docs/.vitepress/config.ts` | 同步子计划链接、统一身份、探针与一次切换顺序 |
| Skill 文档 | `skills/moox/SKILL.md`、`skills/moox/references/` 下的 HTTPS/release/setup 文档 | 更新安装、升级、证书和恢复流程 |

非前端替代入口的具体文件清单与实施责任以主计划 B/C/D/E/G 为准。P0 核对其进度和调用链，本子计划不复制整套网络重构任务。

## 10. 验证矩阵与命令

### 10.1 必测场景

| 类别 | 场景 | 通过条件 |
| --- | --- | --- |
| 单进程 | PATH 无 caddy，禁止外部 archive 下载 | 代理能独立运行，无 Caddy 子进程 |
| 配置 | 非法端口、非法上游、缺失状态目录权限 | 明确失败，启动失败非零，不改生产状态 |
| TLS internal | 合法 CA、错误 CA、错误 SAN/SNI | 合法信任成功，错误信任或主机身份失败 |
| TLS public | 测试 ACME 签发/续期、启动等待、状态恢复 | 正确就绪，续期无需外部进程，重启保留账户 |
| 信任库副作用 | validate、internal 首装、重部署 | 最终配置 `install_trust=false`，不调用系统/Java/Firefox 信任库安装 |
| 路由 | 首页、hash 静态资源、SPA 页面 | 内容及缓存语义正确 |
| API | 登录、带 session HMAC 的已认证请求 | 上游认证正常，body/path/query 不被改写 |
| 非前端拒绝 | gateway-control、service、未知 api、路径变体 | 明确 404，未进入 Admin/Gateway/静态 fallback |
| 代理协议 | WS、流式响应、上游断连 | 升级/透传正确，无重复重试导致业务副作用 |
| 诊断 | 内部合法签名、缺失/过期/重放签名、公开 URL、control 本机发现 | 内部按现有鉴权契约处理，公开诊断 404；`19528` 不监听公网，不把 loopback 改写为远程地址 |
| 健康归因 | TLS 未就绪、Admin/web-host 停止、HTTPS 首页失败 | 组件 readiness、liveness 与端到端结果分开；上游失败不导致代理重启循环 |
| HTTPS 探针 | internal/public、loopback 连接与公开 SNI、错误 CA/主机名 | 显式使用正确 CA/系统信任和 SNI/Host，不跳过验证、不靠自动安装私有 CA |
| 启停 | 启动端口冲突、实际 stop/restart/安装回滚入口、真实长 HTTP/流式/SSH WS/SFTP、重复启停 | 外层等待覆盖总预算；deadline 内存量请求完成，到期遗留连接结束；不抢占无关进程，无遗留监听或锁 |
| 维护与暂停 | 排空期间运行 watchdog、安装持锁与暂停组件 | 不重新拉起排空/暂停进程，不因重复加锁死锁 |
| 配置变更 | 候选配置无效、新版本启动失败 | validate 不停旧服务；激活失败可恢复上一版 |
| 状态 | 首装、重部署、reset-data、模式切换、版本回滚、CA/密钥缺失或不匹配、指纹格式 | 已有 internal 状态异常在 Load/Run 前拒绝；OpenSSL 格式不会误报轮换，跨模式保留持久指纹和 ACME 状态 |
| 权限 | 特权端口、新二进制替换、无需特权时 | 新二进制能力正确且最小化 |
| 安装 | shell 与 CLI 两种路径、control/非control | 两条安装链一致；非control不装代理 |
| 打包 | 完整包、binary-only、单服务包、五平台 | 新制品齐全、摘要正确、无现场私密状态 |
| 网络集成 | tRPC PullSnapshot/ReportStatus、Trade/Strategy/Admin/Collector、模拟 SCF/access | 本地最终制品禁用旧入口后调用成功；真实 SCF 和现场停旧证据另按第 11 节验收 |
| 依赖升级 | 整个 Go workspace 及受影响前端/部署契约 | 无共享依赖回归，失败有归因和修复 |

### 10.2 当前已有的验证入口

以下命令来自当前 Makefile。代码实施时按阶段执行，不把本文列出命令视为已经通过：

```bash
make test-go
make test-release
make verify-custom-setup
make test-script-contracts
make test-skill-contracts
make test-docs-architecture
make verify
```

当前 `make test-caddy` 还绑定旧 manager 测试。实施中用新的 `make test-console-proxy` 替换其运行时契约，并保留仍有效的 CA 工具测试；不能简单删除旧测试而不补新行为覆盖。

复审基线 `8133cc41` 上实际运行 `make test-docs-architecture`，首先发现 `docs/总体设计.md` 缺少 `go.work` 中的 `packages/frequency`；继续检查又发现 `packages/storagepolicy` 漏项。本轮补齐这两项现有模块清单，源码功能仍待实施；新增 `modules/consoleproxy` 时须在同一提交补充模块表及相关导航。按新模块尚未存在的事实，不提前把它写成当前已实现模块。

Go 版本门禁同时覆盖 PR CI、CNB、自建 runner 和远程编译机；自建 runner 不能只输出 `go version`，必须拒绝不满足统一具体版本的环境。版本选择以实际完整依赖图为准，不仅核对 Caddy 自身 `go` 声明。

`make verify` 的 `proto-check` 要求干净工作树；在已提交改动的干净集成树执行，不清理或覆盖用户的未提交文件。新模块加入 go.work 后检查 workspace 测试脚本确实枚举到它。

### 10.3 计划新增的验证入口

模块和 target 已新增；当前 target 聚合模块测试与 vet，双部署路径和安装契约仍需随 P5 补齐：

```bash
cd modules/consoleproxy
go test -count=1 ./...
go vet ./...
```

回到仓库根目录：

```bash
make test-console-proxy
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh console-proxy
TARGET_GOOS=linux TARGET_GOARCH=arm64 ./scripts/build/build.sh console-proxy
```

`test-console-proxy` 应聚合配置、TLS/路由集成、生命周期和双部署路径契约，并接入 `make verify` 与 CI。race 测试在支持的本机工具链下单独运行，不把 `CGO_ENABLED=0` 的交叉编译环境误用于要求 race runtime 的测试。

测试 public ACME 使用本地测试服务或测试 CA；正式 CA 签发只属于获授权后的环境验收。普通测试和打包契约不得依赖 GitHub release archive 下载。P7 和主计划 J 复用清理后的最终制品验证，记录实际平台和摘要；涉及代码修复时重建并复测，不复用修复前证据。

## 11. 发布与正式环境验收

本节是未来实施后的交付门禁，不是本次文档任务的操作清单。

发布前确认目标主机、节点角色、版本/commit、制品摘要、证书状态备份、维护窗口及完整回滚制品。先完成本地最终制品验证和 P7 放行，再统一按主计划第 13 节切换；不在三台运行主机提前安装中间版本，不把本机测试等同于部署完成。

正式环境至少保存以下脱敏证据：

1. control 上运行的是 `moox-console-proxy`，有效 HTTPS 监听归该进程，未启动外置 Caddy。
2. 非 control 节点不运行 console-proxy/Caddy，非前端入口由已确认的网络方案承载。
3. 使用正常 CA/系统信任完成 HTTPS 校验，internal CA 指纹与切换前一致；不以 `curl -k` 证明信任正确。
4. control 本机鉴权诊断正常，组件 `readyz` 与 HTTPS 页面端到端检查分别可观测；浏览器可加载实际页面及静态资源，登录和已认证 API 正常；公开非前端/诊断路由拒绝。
5. 每个主机网关经 tRPC GatewayControl 获取新 route hash 并 ReportStatus，内部业务调用及经 access 的真实 SCF 调用正常；服务信任独立 MooX CA，不依赖 Caddy CA。
6. 停机/重启、自愈、证书状态保留和可执行回滚步骤通过；public 续期有受控测试证据和正式状态观测。
7. 安装和重启不发起 GitHub Caddy archive 下载；没有运行时回退安装分支。

若无法取得生产或云端访问条件，保留本地临时拓扑与最终制品结果，并把正式环境门禁标为未完成，不据此宣布整项交付完成。

## 12. 完成定义

- [ ] console-proxy 是前端专用入口，Caddy 是其进程内引擎。
- [ ] 只有 `moox-console-proxy` 代理制品，没有外置 Caddy 安装/启动依赖。
- [ ] Admin、web-host、Gateway 的职责保持独立，非前端替代入口及调用方已验收。
- [ ] 组件目录统一使用 `console-proxy`，control/单副本/受保护、内部 `readyz` 和独立 HTTPS 端到端检查一致。
- [ ] 配置只有一个事实源，Caddy 管理 API 和 autosave/resume 未形成旁路。
- [ ] 证书、CA、ACME 状态、最小权限和进程身份在安装、重部署与回滚中正确。
- [ ] shell/CLI、三类包、五平台构建与相关注册/运维入口均覆盖。
- [ ] 工作区依赖升级完成全量回归，独立 codeCR 审查的问题已处理。
- [ ] 外层停止等待覆盖排空与清理预算，实际启停、安装回滚及 watchdog/暂停场景通过。
- [ ] 本地最终制品全链路验证与按授权要求的正式环境验证分别完成并留有证据。
- [ ] 当前生效的旧 manager、archive/checksum/cache 调用和文档指令已退出，无双实现。

## 附录：上游依据

- [Caddy v2.11.4 的 Go 版本及依赖](https://github.com/caddyserver/caddy/blob/v2.11.4/go.mod)：该 tag 自身声明 `go 1.25.1`，最终工具链还须满足完整依赖图。
- [Caddy Run/Load/Stop](https://github.com/caddyserver/caddy/blob/v2.11.4/caddy.go)：支持 Go 进程内加载配置和应用生命周期管理。
- [Caddy AdminConfig/ConfigSettings](https://github.com/caddyserver/caddy/blob/v2.11.4/admin.go)：管理端点与配置持久化必须显式控制。
- [Caddy CLI 初始化](https://github.com/caddyserver/caddy/blob/v2.11.4/cmd/main.go)：内嵌模式不能假定 CLI 的 ACME 初始化仍自动执行。
- [Caddy HTTP 启停实现](https://github.com/caddyserver/caddy/blob/v2.11.4/modules/caddyhttp/app.go)：停机排空和配置生效要通过实际生命周期测试确认。
- [Caddy PKI CA 配置](https://github.com/caddyserver/caddy/blob/v2.11.4/modules/caddypki/ca.go)：自动信任安装的默认行为须显式关闭，并核对最终配置。
- [Caddy 官方 main](https://github.com/caddyserver/caddy/blob/v2.11.4/cmd/caddy/main.go)：标准模块及 tzdata 的引入方式。

上游约束按固定 tag 核对，不能把最新 Caddy 文档的工具链要求直接套到 v2.11.4，也不能仅固定 Caddy tag 而忽略共享工作区实际选中的传递依赖。

主计划的发布准备层现已连接软件、身份和运行助手：`unitinstall` 在维护锁下完成所选组件身份投影、配置注入、私密运行计划与环境、七个生命周期脚本，验证后原子发布新候选。host TLS、Access 外部身份和操作员材料的边界保持独立；业务单元使用同主机包的目录视图。`inspect-release` 在切换前逐项核对软件、配置、身份和脚本的摘要/大小/权限，拒绝发布后改动。该进展不切换 current、不复制运行数据、不代表完整安装，激活/回滚、五步 bootstrap、I/J、最终 Agent 审查和正式部署验收继续保留。

发布激活层已增加持锁停止、状态复制、current 切换、依赖启动、失败恢复与显式快照回滚；持久安装标记防止安装器退出后守护拉起未恢复单元。代理增加只读 `check-state`，在升级停止、复制、启动阶段核对 CA，升级配置必须关闭 initialize_ca。真实完整软件包的十三组安装门禁、十三组运行门禁已无跳过通过，覆盖旧预算内的 HTTPS 长请求完整排空、升级/回滚 CA 连续性、失败恢复和暂停保留；全部纯 Go 制品在本机构建，Linux 仅执行。阶段中断测试不替代实际 SIGKILL、首次 CA 授权的一次性消费、历史状态导入、外置链路清理、完整 CLI/bootstrap 接线和 P7/J 的最终候选验收。

离线状态封存/导入接口随后已接入激活层，安装门禁扩展为十七组且无跳过通过。真实 Admin 离线初始化和重跑的数据库验证了摘要绑定、独立副本、篡改拒绝与快照回滚，主密钥和 MooX CA 私钥留在独立持久目录；代理导入状态即使不启动也须通过只读 CA 检查。完整 bootstrap 与历史 CA 迁移编排、首次 CA 授权的一次性消费、源码清理和最终候选验收仍未完成，正式环境尚未切换。

目标主机 bootstrap 编排器已接入离线 Admin、封存状态、两个单元的激活/撤销及 Admin → HostGateway → EventBus/其余服务的就绪顺序；日志记录原运行集合，完成后重跑可修复相同发布。实际启动发现并修复 HostGateway 指标实例身份、标准健康 JSON、status 就绪报告及服务数据默认权限的遗漏，升级前拒绝缺失或不属于发布快照的 Admin 数据库。36 个 Go 测试包全量 race/vet，以及 Linux 五组 bootstrap、十八组安装、十四组运行必跑场景均通过；覆盖真实 HTTPS、升级失败恢复和 Admin 启动阶段实际 SIGKILL 后保持 MooX CA/KeyID 的整机恢复。纯 Go 制品均在本机编译，Linux 只执行。原生 CLI/SSH、完整三主机/九组件链路、其他中断阶段、代理 CA 一次性授权与最终候选验收尚未完成；主计划 G3 继续保留未完成，正式环境尚未切换。

目标编排随后补齐 EventBus 的离线事务签发、TLS/角色鉴权和按组件投影，运行配置只引用发布内凭据副本，重跑复用已有角色及 CA。21 个相关测试包全量 race/vet、Linux 五组 bootstrap、二十组安装和七项凭据测试通过；真实 broker 拒绝无角色客户端，并验证升级和强杀恢复后的身份连续性。该安全依赖不替代原生 CLI 接线、代理 CA 一次性授权、完整系统与正式部署验收。
