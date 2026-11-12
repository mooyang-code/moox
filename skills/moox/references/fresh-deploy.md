# 全新部署（清空生产后按默认配置重装）

适用：用户明确说“全新部署 / 清空重装 / 数据全部可以删、配置重建”，并要求用仓库默认配置初始化。
这是破坏性运维：**必须先得到用户的明确确认**，并复述要清空的主机和数据。升级已安装的单元不走这里，见 [`native-release.md`](native-release.md)。
本文是维护者在源码仓库里重装自己生产环境的记录（四台主机、从源码构建）；外部用户第一次安装按 [`install.md`](install.md)，用发布产物，不需要构建。

整体顺序：核对前提 → 备份 → 构建制品 → 停机清空 → 本机状态清理 → 安装（admin → 主机 → 单元 → control）→ 默认初始化 → 验收。
耗时的命令都放后台运行，用日志文件加 Monitor 盯结果（一次完整安装约 20 分钟，另加 5 分钟构建）。

## 0. 前提与约定

- 命令在**主仓库根目录**执行：`moox.toml` 必须放在“仓库根”，CLI 不接受别的位置。代码在独立 worktree 时，仍在主仓库根目录运行，用 worktree 里构建的 CLI，
  并用 `--source-dir <worktree>` 指向源码；种子文件用绝对路径（`--seed`、`--config-dir`）。不要用旧的 `./bin/moox-cli`（种子格式可能已变）。
- 跑 CLI 前 `env -u HTTP_PROXY -u HTTPS_PROXY`（本机代理会挡住到主机的直连）。
- `moox.toml` 是加密的，Agent 只通过 CLI 读写，不直接读文件，也不打印密钥。改它之前先备份：`cp -p moox.toml moox.toml.bak-<说明>-<时间>`。
- 各主机地址在 `~/.moox-release.env`（`MOOX_RELEASE_HOST_*`），SSH 只用公钥。
- 本机就是编译机：所有制品都在本机构建（纯 Go 交叉编译，Storage 与前端进本机 Docker），不登录任何主机去编译。

## 1. 清空前核对

1. Trade 只有纸面账户：看 Trade 库（海外机 `/data/moox/trade/current/trade/data/moox_trade.db`，只读）里账户、持仓、订单是否为空；有实盘持仓或未完成订单就停下来问用户。
2. 四台主机（control、storage、overseas、factor）都能 `ssh -o BatchMode=yes` 登录；控制机磁盘小（约 39G），装完应还有 ≥8G。
3. 记下要保留的东西：因子机 `/data/moox/import`（历史导入包，重新下载很贵，清空前挪到 `~/moox-import-keep`）。其余数据按用户说法全部删除。
4. 列出清空后会丢失的手工配置（`docs/部署与运维.md` “线上的手工配置”，以及记忆里的手工改动：引擎 `workers`、保留期等），汇报时一并说明。保留期以 `moox.toml` 的 `[storage_retention]` 为准，重装自动沿用。

## 2. 构建制品（清空之前做完）

构建失败发生在清空之后，生产会长时间空着，所以先把能构建的都构建并验证。

```bash
WT=<worktree 绝对路径>          # 代码所在目录
# Mac 版 CLI（来自要发布的提交）
cd $WT && env -u HTTP_PROXY -u HTTPS_PROXY ./scripts/build/build.sh cli
# Storage 的 Linux CGO 制品（本机 Docker；macOS 先 colima start），产物在 $WT/bin
cd <主仓库根> && $WT/bin/moox-cli setup build-linux --module storage --source-dir $WT
```

- **前端随 web-host 现场构建**：`scripts/build/build.sh web-host` 编译时按当前源码构建前端（默认在本机 Docker 里，macOS 先 `colima start`；
  `setup bootstrap`/`setup release build` 先用本机 pnpm 构建好 `web/dist` 再交给它），内嵌资源生成到临时目录、用 `go build -overlay` 编译，
  不用也不改仓库里提交的 `web-host/internal/statik/statik.go`。想先跑前端测试和代码检查：`cd $WT && bash scripts/build/build-web-docker.sh --check`。
  `web/dist` 缺少 `static/js` 时构建直接失败（否则会把占位页嵌进 web-host，管理台打不开）。
- 纯 Go 组件由 bootstrap / deploy-unit 在本机交叉编译。想提前确认能编译：逐个 `go.work` 模块 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`，
  **跑完立刻检查并删除模块目录里生成的二进制**（单个 main 的模块会在目录里留文件，如 `modules/replay/replay`、`tools/moox-mcp/moox-mcp`；`git add -A` 会把它们提交）。
- 改动已提交并推送，CI 通过；线上二进制必须能对应到一个提交。

## 3. 停机并清空

清除一律用 `setup wipe`，不要手工 `rm`。它按 因子机 → 海外机 → 控制机 → 存储机 的顺序，去掉定时看护、停止并删除外部服务、停止各单元、终止部署目录内的进程，再清空部署目录（保留目录本身，否则要 sudo 重建），最后归档本机状态。

因子机的 `/data/moox/import` 也会被清除，要留下先挪走：`mkdir -p ~/moox-import-keep && mv /data/moox/import/* ~/moox-import-keep/`。

```bash
$CLI setup wipe --file ./moox.toml                                   # 预览：主机、目录、不认识的条目、digest
$CLI setup wipe --file ./moox.toml --yes --expect <预览里的 plan.digest>
$CLI setup validate --file ./moox.toml                               # deployment.route 应为 fresh
```

中途失败后再运行一次会接着清。CLI 不清理云函数，见第 8 节。

### 本机状态

`setup wipe` 把操作员身份和这台控制机的旧管理台证书移到 `~/.config/moox/.bak-<时间>/`，`config.key` 和 `known_hosts` 留在原处；状态目录 `~/.local/state/moox/setup/<摘要>` 改名为 `<摘要>.bak-<时间>`。
状态目录**必须**换新：制品和请求哈希绑定在 admin 阶段的状态里，control 阶段重试会复用 admin 时构建的软件。想换制品（改了代码）只能换新状态目录，从 admin 重来。

## 4. 安装顺序

首次安装的**所有命令都不带 `--release`**（`--release` 只用于升级）。以下命令都在主仓库根目录执行（`setup init` 没有 `--source-dir`，不用带），`CLI=$WT/bin/moox-cli`，公共参数 `--file ./moox.toml --source-dir $WT`。
每一步成功的标志是输出里的 `*-ready` 与 `STEP-EXIT=0`；任何一步失败先看日志，不要盲目重试。

| 步 | 命令 | 备注 |
| --- | --- | --- |
| 1 | `$CLI setup bootstrap --stage admin` | Admin、HostGateway、HostAgent、EventBus 凭据；输出 `admin-ready`。约 3 分钟，含本机构建 |
| 2 | `$CLI setup deploy-host --host storage`，再 `overseas`、`factor` | 因子机要求 `[hosts.factor] reachability = "outbound"`，且已有 `/data/moox`（可写） |
| 3 | `$CLI setup deploy-unit --host storage --profile eventbus` | EventBus 装在 `[deployments]` 登记它的主机（默认存储机） |
| 4 | `$CLI setup deploy-unit --host storage --profile storage --binary-dir $WT/bin` | Storage 必须带第 2 节构建的 Linux CGO 制品 |
| 5 | `$CLI setup init --stage storage --config-dir $WT/config/setup --storage-host storage` | 业务空间、导入元数据（数据集建好即可写入）。必须在 control 之前：Monitor 要等系统数据集就绪 |
| 6 | `$CLI setup deploy-unit --host storage --profile access`，再 `--host overseas --profile access`，再 `--host overseas --profile egress-proxy` | **存储机和海外机都要装 Access**，漏了海外机会出现“外接入（overseas）连接被拒绝”告警 |
| 7 | `$CLI setup bootstrap --stage control` | 首次安装自动创建管理台内部 CA；失败的发布目录由下一次尝试自动清理 |
| 8 | `$CLI setup init --config-dir $WT/config/setup --storage-host storage` | 云账户、日志服务和默认因子任务；业务空间和元数据第 5 步已写入，这里只核对 |
| 9 | `$CLI setup deploy-unit --host overseas --profile collector-stream`，再 `--profile trade` | 必须在 control 就绪之后：collector-stream 启动后向 Collector 发心跳。`[deployments]` 里登记了 `callback-dispatcher`（事件中心）的主机，再 `--profile callback-dispatcher`（也在 trade 之后；它连 EventBus，不依赖 control） |
| 9b | `$CLI trade inspect-credential --exchange EXCHANGE_BINANCE --environment ACCOUNT_ENVIRONMENT_PRODUCTION --credential-secret-id exchange-binance-production-main` | 仅当 `moox.toml` 里有 `[[exchange_keys]]`，且在 `trade` 部署之后：`setup apply` 排在 Trade 部署之前，它当时只写入密钥、不检测。**逐把检测 `readonly = true` 以外的密钥**（固定 ID 是 `exchange-<交易所>-<环境>-<名称>`，OKX 用 `EXCHANGE_OKX`，测试网用 `ACCOUNT_ENVIRONMENT_TESTNET`），在海外机上经 Trade 检测，不向交易所下单；看 `blockers` 有无阻断项。输出里 `检测没有完成` 只是交易所接口暂时不可用，稍后重试，不代表密钥有问题 |
| 10 | `$CLI setup sync-deployments --host factor --yes`，再 `deploy-unit --host factor --profile factor-engine`，再 `--profile archive-worker` | 因子机外部程序，systemd 用户服务；引擎安装时自动按本机内存校准窗口与内存预算（`tune --write`） |

### 信任内部 CA（用户自己做）

`setup init` 不碰信任库，也不等这一步。信任要这台电脑的管理员授权，Agent 的 shell 没有终端，做不了。`setup validate` 把它放在 `manual_steps`，安装结束时转告用户：

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ~/.moox/certs/moox-caddy-root-<控制机地址>.crt
```

用户也可以在自己的终端运行 `setup trust-browser`。不做时管理台能打开，但浏览器有安全提示；其他设备从登录页下载根证书。重装后 CA 是新的，旧证书留在钥匙串里，由用户自行清理。

### 步骤 8 的已知限制

`setup start` 会逐项导入 `moox.toml` 里这个空间的 `[factors]` 任务；**任一任务的源数据集不存在就整体失败**，后面的任务不会建。默认有三个因子任务：crypto 的 `ftask_spot_1m`、`ftask_swap_1m` 与 A 股的 `ftask_cn_5m`（源数据集都由 Collector 的默认采集任务创建）；`config init` 在没有因子机时关闭 `factors.enabled`；有因子机、没有海外机时保留因子，只去掉读 crypto 数据的任务。
`moox.toml` 里指向自定义数据集的任务（例如手工建的 5m 采集任务的数据集）在全新系统里没有源数据，会让 init 在那里失败；先把它们从导入范围中拿掉，建好对应采集任务后再 `setup factors` 补导入。

## 5. 默认初始化的范围

“系统默认配置”取字面意思：仓库 `config/setup/*.yaml` 的种子（Space、元数据、默认采集任务、数据集健康策略）加上 `moox.toml` 里默认的因子任务。Collector 首次启动会把随发布安装的 `collector/config/collection-tasks.yaml` 写成默认采集任务
（只在任务表为空时写，任务 ID 固定，因子任务的源数据集依赖这些 ID；不足时 `moox-collector-cli init --seed-file` 补种）。默认有币安现货 1m、币安合约 1m（crypto 空间）、A 股 5m 和两条 A 股日线（stockcn，沪深 300 成分股；一条纯日线，一条带复权、申万行业、财务的附加字段，见 [`ashare-daily.md`](ashare-daily.md)），都是停用状态，`setup start --space-id <空间>` 才启用（会启用这个空间里停用的 K 线任务；两条日线任务标了 `start: manual`，`setup start` 跳过并在输出里提示，要用 `collector task update --enabled=true` 显式启用），安装开始时问用户选哪个空间；不再默认采集 1h，要 1h 数据时用 `moox-cli collector task create` 自己建。内置任务只在 Collector 启动时检查，运行中删空任务不会重新写入。crypto 有计划后采集方式默认是 collector-stream（stream + local），不需要手工切换，也不依赖云函数；首份计划要等 collector-stream 在线，用 `moox-cli collector status` 和 `setup verify` 确认，计划建好前 `ingest-mode` 查不到数据集。

### 默认展示策略（合约双均线）

`setup start --space-id crypto` 自动创建：[`examples/strategies/dual_ma_swap_1m.yaml`](../../../examples/strategies/dual_ma_swap_1m.yaml)，绑定合约 1m 因子任务 `ftask_swap_1m`，固定 ID `default_dual_ma_swap_1m`，观察实例，不挂账户、只计算不投递。重复运行不会重复创建。

快线 `ma__5` 在慢线 `ma__10` 之上的合约都持有，等权，单个最多 5%。前几根 `ma__10` 为空时记 `too_many_missing`，第一个 ok 之前按积累处理，上限 30 分钟或 20 个周期，所以约 15 分钟内看到第一个结果。这个因子任务已绑给用户自己的实例时跳过默认示例，要改绑先停用，再 `strategy instance update`。

### A 股空间（stockcn）

`config init` 第一步验证密钥时同时取到账号 AppId，识别服务器时拿到它们的 VPC、子网与私网 IP，据此自动生成 stockcn 的 `[scf_plan]`：函数入口优先选存储服务器所在地域（函数与 Storage 同地域，写入全程走内网），存储服务器不可用时退而选其他带 Access 且有可用 VPC 的主机（如海外服务器）（云账号引用 Admin 里的 `tencent-default`，代码包桶 `moox-scf-<管理台地域>-<AppId>` 首次上传时自动创建）；没有任何可用入口主机时不生成、要手工补（入口主机要有可绑定的 VPC、所在地域支持云函数、同地域的其他主机在同一个 VPC），或者子用户没有 CAM 读权限取不到 AppId（页面会提示）。只有生成计划时才给入口主机写 `private_address`。`setup init` 据此登记云账号。

`setup start --space-id stockcn` 的前提是 `[scf_plan]` 打开并配了 stockcn（见 `moox.toml.example`：`[[scf_plan.spaces]] space_id = "stockcn"`，`markets.equity` 写入口主机所在地域，优先存储机所在地域，该地域要有带 access 组件、配了 `private_address` 的主机）。它依次：

1. 启用任务时，任务绑定的标签还没有成员（全新部署时自动标签要等 Collector 下一轮标签巡检才首次同步）就先立即同步再启用：沪深 300 成分标签 `cn_hs300`（属性 `sync.index = "000300"`，东方财富成分接口，之后每天 08:40 自动同步，接口失败时保留旧成员）；
2. 启用“A 股 K 线 5m”任务并触发对账，Collector 建出 SCF 计划（300 只，函数个数按各源权重、限速和标的数估算，约 4 个，每 5 分钟触发一次、收盘后 1 秒同时取数，午休、收盘后、非交易日函数直接返回 `no_work`）；
3. 导入因子任务 `ftask_cn_5m`（只有 `ma__5`、`ma__10`），创建观察实例 `default_dual_ma_cn_5m`（[`examples/strategies/dual_ma_cn_5m.yaml`](../../../examples/strategies/dual_ma_cn_5m.yaml)，不挂账户）。

A 股按交易时段推进：每个交易日 48 期（09:30～11:25、13:00～14:55），11:25 那根之后是 13:00 那根；午休、隔夜、周末 Monitor 不报停滞。
数据是不复权 5m（通达信、东方财富按权重 4:2 对股票固定分配，每家最多试 3 次，失败时依次换下一家，新浪、腾讯只做备源——它们收盘后头几秒给未定稿值，理由见 `docs/模块/采集.md`；腾讯成交额由收盘价 × 成交量估算），除权日前后跨越除权的均线会有一次跳变。A 股实例可以挂 A 股模拟账户（交易模块新建账户时选“A 股”）；A 股实盘的 QMT 执行端还没接入。

### 交易日历的年度更新

交易日历内嵌在 `packages/marketcalendar/data/`（采集、因子、策略、Monitor、CLI 共用），每份日历两个文件、各自更新：A 股是 `cn_stock.json` 与 `cn_stock.manifest.json`，港股是 `hk_stock.json` 与 `hk_stock.manifest.json`。A 股覆盖期到当年 12 月 31 日，每年 11 月前后国务院公布下一年节假日安排后：

1. 把落在工作日上的休市日写进 `packages/marketcalendar/internal/gencn/main.go` 的 `closures`，在 `packages/marketcalendar` 下运行 `go run ./internal/gencn -year <下一年>`（调休补班的周末照样休市）；它同时重写 `cn_stock.manifest.json` 的 `valid_through` 与 `sha256`，`go test ./packages/marketcalendar/` 校验。交易所 12 月公布休市安排后核对一遍，有出入就改 `closures` 用同一条命令重新生成；交易所日历已进 AkShare 时也可以用 `scripts/marketdata/update-calendar.sh` 整体替换；
2. 港股：联交所发出下一年的证券市场假期通告后（2026 年是 [CT/075/25](https://www.hkex.com.hk/Services/Circulars-and-Notices/Participant-and-Members-Circulars/SEHK/2025/SEHK_CT_075_2025?sc_lang=en)，2027 年是 [CT/077/26](https://www.hkex.com.hk/Services/Circulars-and-Notices/Participant-and-Members-Circulars/SEHK/2026/SEHK_CT_077_2026Jun01162846?sc_lang=en)），把落在工作日上的休市日和半日市（圣诞前夕、新年前夕、农历新年前夕，只开上午市）写进 `packages/marketcalendar/internal/genhk/main.go` 的 `closures`、`earlyCloses`，通告网址写进 `source`，在 `packages/marketcalendar` 下运行 `go run ./internal/genhk`（整份重新生成，只写港股的两个文件）。补登或改动半日市会让之后的交易序号整体平移，因子本地存储要重建；
3. `scripts/marketdata/validate-calendar.sh` 校验全部日历后，重新构建并发布 Collector、因子引擎、Strategy、Monitor、CLI，并重新上传 stockcn、stockhk 两个 SCF 代码包（`moox-cli collector function upload`，每个空间各传一次）。日历编在代码包里：漏传港股包，旧日历的覆盖期一过，港股函数每期都返回 `no_work`，不报错。

**必须在 12 月中旬前完成**：采集计划预检要求日历覆盖期至少还剩 14 天，否则拒绝新计划；到期前 60 天起每天告警。

## 6. 验收

1. 四台主机的单元都 ready：各主机 `for u in /data/moox/*/current; do (cd $u && ./status.sh); done`；因子机再看 `systemctl --user list-units 'moox*'`。
2. `moox-cli doctor alerts --file ./moox.toml`：新对象会先有“尚未上报 / 尚无成功运行”，等一个周期；1h 数据集要等第一个整点之后。长时间不消的告警按提示排查。
3. 一条链路至少跑通一期：`moox-cli collector status --space-id crypto`（各数据集 `level: ok`）→ 因子机 `/data/moox/factor-engine/logs/factor-engine.log` 里 `factor_period_done ... status=complete rows=<标的数>`
   （`status=degraded rows=0` 说明有问题，常见是 `factor_memory_budget_exceeded`，看内存配置）。
4. 管理台能打开：`curl --cacert <CA> --resolve <控制机>:9527:<控制机> https://<控制机>:9527/`，首页引用的 `static/js/*.js` 返回 200 且体积正常（占位页的首页也是 200，不能只看首页）。

## 7. 踩坑清单

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `moox.toml must be in repository root` | CLI 要求 moox.toml 在仓库根 | 在主仓库根目录运行，worktree 用 `--source-dir` |
| control 阶段失败，重试结果相同 | 软件制品绑定在 admin 的状态目录，重试不会带上新代码 | 修代码后换新状态目录，从 admin 重来 |
| control 阶段 `target_not_ready` 或 `target_process_exited`，Monitor 没起来 | Monitor 的定时器首次运行时依赖的服务还没起来，错误让进程退出 | 看候选发布目录 `/data/moox/prod/releases/bootstrap-*/<组件>/logs/trpc.log`；代码已改为跳过本轮，但同类问题仍可能出现在别的组件 |
| 管理台打开是空白/占位页 | web-host 内嵌了占位前端（`web/dist` 不完整） | 按第 2 节重新构建 web-host；`web/dist` 不完整时构建现在会直接失败 |
| `setup trust-browser` 报 `browser_trust_manual` | 没有终端或 sudo 失败 | 把错误里的命令交给用户在自己的终端运行 |
| `setup init` 报 `CreateFactorTask failed ... metadata row not found` | `moox.toml` 里有源数据集不存在的因子任务 | 见第 4 节“步骤 8 的已知限制” |
| 因子 `factor_memory_budget_exceeded`、`status=degraded rows=0` | 引擎 `compute.max_memory` 太小（旧脚本写死 1GiB，默认因子 EMA 一期约 1.1GB） | 脚本已改 4GiB 并在安装时自动 `tune --write`；已装的机器改 `engine.yaml` 后重启引擎 |
| 引擎重启后一段时间 `lease conflict`、`not_ready` | 旧租约还没过期 | 等几十秒，再等窗口载入（约 20 秒）和 warming |
| 告警“外部接入（overseas）连接被拒绝” | 海外机没装 Access | `deploy-unit --host overseas --profile access` |
| 清空后进程又被拉起来 | 定时看护（cron watchdog）还在 | 先删 crontab 里的 `moox-watchdog` 行再停 |
| 仓库里多出二进制文件 | `go build ./...` 交叉编译在单 main 模块里留了产物 | 立即删除；别 `git add -A` |

## 8. 收尾

- 提醒用户到腾讯云控制台删除旧的 `moox-fetch-plan-` 云函数（`setup wipe` 不清理）。
- 汇报：做了什么、验收结果、没做的事（第 5 节）、重建后丢失的手工配置、需要用户做的事（清理旧 CA 等）。
- 把发布过程中临时改过的代码或脚本提交并推送；`moox.toml` 的变更提醒用户 `moox-cli config backup`。
- 更新记忆（部署状态、踩坑）。
- 不要擅自合并 PR；合并要用户明确说。
