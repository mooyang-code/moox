# 全新部署（清空生产后按默认配置重装）

适用：用户明确说“全新部署 / 清空重装 / 数据全部可以删、配置重建”，并要求用仓库默认配置初始化。
这是破坏性运维：**必须先得到用户的明确确认**，并复述要清空的主机和数据。升级已安装的环境不走这里，见 [`source-deploy.md`](source-deploy.md)。
本文是维护者在源码仓库里重装自己生产环境的记录（四台主机、从源码构建）；外部用户第一次安装按 [`install.md`](install.md)，用发布产物，不需要构建。

整体顺序：核对前提 → 备份 → 构建二进制 → 清空 → 安装（一条 `setup install`）→ 验收。
耗时的命令都放后台运行，用日志文件加 Monitor 盯结果。

## 0. 前提与约定

- 两份配置：明文的 `moox.toml`（主机、部署、部署参数）和同目录加密的 `moox.secrets.toml`（管理员、云密钥、交易所密钥、告警渠道）。可以放在任意目录，命令用 `--file <moox.toml 路径>` 指过去。
  Agent 不直接读秘密文件、不打印密钥，要看用 `moox-cli config get`（默认打码）。改之前先备份两份文件。
- 代码在独立 worktree 时，用 worktree 里构建的 CLI，并用 `--source-dir <worktree>` 指向源码：配置模板、systemd 单元模板、元数据种子（`config/setup`）、云函数代码包（`modules/collector`）、默认因子任务和默认策略都从这里取，不看运行命令时的当前目录。
  `setup install` 把同一个目录传给它里面的每一步；单独跑 `setup deploy`、`setup init`、`setup start` 时各自带上同一个 `--source-dir`，否则按当前目录取（在主仓库根运行就会把主仓库那个分支的云函数代码传上去）。种子不在 `<源码目录>/config/setup` 时才写 `--config-dir`。
  产物包（没有源码）安装也是这个参数：指到解压后的产物包目录，云函数程序按清单下载预编译的，不在本机编译。
- 跑 CLI 前 `env -u HTTP_PROXY -u HTTPS_PROXY`（本机代理会挡住到主机的直连）。
- 本机就是编译机：所有二进制都在本机构建（纯 Go 交叉编译，Storage 与前端进本机 Docker），不登录任何主机去编译。
- 每台主机一个部署根（默认 `/data/moox`），组件是 systemd 用户服务 `moox-<组件>`。布局见 [`docs/部署与运维.md`](../../../docs/部署与运维.md)。
- 每台主机的部署用户都要开 linger（`sudo loginctl enable-linger <用户>`，要用户自己执行）；`setup validate` 的 `manual_steps` 会列出没开的主机。

## 1. 清空前核对

1. Trade 只有纸面账户：`moox-cli trade account list`、`trade order list --only-open` 看有没有实盘账户、持仓和未完成订单；有就停下来问用户。
2. 四台主机（control、storage、overseas、factor）`setup validate` 都通过；控制机磁盘小（约 39G），装完应还有 ≥8G。
3. 记下要保留的东西：因子机 `/data/moox/import`（历史导入包，重新下载很贵，清空前挪到 `~/moox-import-keep`）。其余数据按用户说法全部删除。
4. 管理台里后来改过或新加的密钥（交易所密钥、告警渠道、云账号）只在线上有，秘密文件里只是初始值：清空前让用户在管理台导出或记下来。
5. 手工调过的组件配置应该已经写在 `moox.toml` 的 `[overrides]` 里（重装自动带上）；没写进去的手工改动清空后会丢，汇报时一并说明。保留期以 `[storage_retention]` 为准。

## 2. 构建二进制（清空之前做完）

构建失败发生在清空之后，生产会长时间空着，所以先把二进制都构建并验证。

```bash
WT=<worktree 绝对路径>          # 代码所在目录
# Mac 版 CLI（来自要发布的提交）
cd $WT && env -u HTTP_PROXY -u HTTPS_PROXY ./scripts/build/build.sh cli
# 全部组件的 Linux 二进制（纯 Go 交叉编译；Storage 与前端在本机 Docker 里，macOS 先 colima start），产物在 $WT/bin-linux
cd $WT && env -u HTTP_PROXY -u HTTPS_PROXY bash scripts/build/build-linux-local.sh --out $WT/bin-linux \
  admin host-gateway hostagent eventbus egress-proxy collector collector-stream factor-mgr factor-engine \
  archive-mgr archive-worker strategy strategy-cli trade callback-dispatcher monitor storage-cli storage
```

- 目标清单和 `scripts/release/build-release.sh` 里的 `server_targets` 一致（去掉 `collector-scf`）；`archive-worker` 这个目标会带上内置对象存储的 `weed`。
  部署时缺哪个组件的哪个文件，`setup deploy` 会直接报出来（`binary_missing`）。
- **前端随 admin 现场构建**：`build.sh admin` 编译时按当前源码构建前端并内嵌；`web/dist` 缺少 `static/js` 时构建直接失败。
  想先跑前端测试和代码检查：`cd $WT && bash scripts/build/build-web-docker.sh --check`。
- 不要在模块目录里 `go build ./cmd/...`（不带 `-o` 会把二进制留在目录里，`git add -A` 会把它们提交）。
- 改动已提交并推送，CI 通过；线上二进制必须能对应到一个提交。

## 3. 清空

清空一律用 `setup wipe`，不要手工 `rm`。它逐台停用并删除全部 `moox-*` 用户服务，杀掉可执行文件在部署根里的进程，清空部署根下的内容（保留目录本身，否则要 sudo 重建），再删掉这台电脑上的操作员身份（`moox-cli` 的签名私钥、控制机根证书与它的指纹）。

因子机的 `/data/moox/import`、归档文件 `/data/moox/archive` 和内置对象存储的数据 `/data/moox/data/seaweedfs` 都在部署根下，会一起清掉；预览里单独列出后两项的大小。导入包要留下先挪走：`mkdir -p ~/moox-import-keep && mv /data/moox/import/* ~/moox-import-keep/`。

```bash
$CLI setup wipe --file ./moox.toml                                   # 预览：主机、条目、单元、归档与对象存储的大小、digest
$CLI setup wipe --file ./moox.toml --yes --expect <预览里的 plan.digest>
$CLI setup validate --file ./moox.toml                               # deployment.route 应为 fresh
```

中途失败后再运行一次会接着清。CLI 默认不清理云函数：重装后 Collector 按函数名接管同名函数，计划外的旧函数被停掉定时器、等人工删除，见第 6 节“全新部署后的验收”第 4 步。

**清云函数的顺序**：Collector 还在运行时用 `collector function delete` 删计划内的函数，下一轮对账（几分钟内）会把它们重新建出来，清空前删等于白删。正确做法是先清空、重装，新 Collector 把旧函数列为计划外（`collector function list` 的 `unplanned`，定时器已被停用），这时再按第 6 节“全新部署后的验收”第 4 步删除，不会重建。

## 4. 安装

```bash
$CLI setup install --file ./moox.toml --source-dir $WT --binary-dir $WT/bin-linux --space-id crypto         # 预检
$CLI setup install --file ./moox.toml --source-dir $WT --binary-dir $WT/bin-linux --space-id crypto --yes
```

预检输出安装步骤（`steps`），每一步带可以单独运行的命令。带 `--yes` 后依次：

| 步 | 做什么 | 备注 |
| --- | --- | --- |
| 0 | `setup operator` | 这台电脑没有 `~/.config/moox/moox-cli.key` 就生成一把签名私钥，有就复用（重跑不换） |
| 1 | 防火墙 | 放行各主机对外的端口 |
| 2 | `setup deploy --host control --component admin,host-gateway,host-agent` | 先在控制机上执行 `moox-admin-cli bootstrap`（建库、CA、主密钥、管理员、登记全部主机与部署、登记这台电脑的 `moox-cli` 公钥、签发控制机的签名密钥，只建不改），再装这三个组件；控制机的根证书同时记到这台电脑 |
| 3 | `setup deploy --host storage` | Storage、EventBus，存储机的网关同时开外部入口（11004）；Storage 主库启动前自动初始化元数据库 |
| 4 | `setup init --stage storage` | 业务空间、初始秘密、元数据。Collector 启动时要核对内置任务的元数据，所以排在控制机其余组件之前 |
| 5 | `setup deploy --host control` | 控制机其余组件（Monitor、Collector、因子管理、归档管理、策略） |
| 6 | `setup deploy --host overseas`，再 `--host factor` | 海外机（Trade、出口代理、collector-stream、事件中心，网关开外部入口）；因子机（因子引擎、归档 worker、内置对象存储）排最后 |
| 7 | 等全部组件就绪 | 各台的本机 `/readyz` |
| 8 | `setup init` | 告警渠道、云账户、日志服务、采集计划、登录校验（这些要等 Collector、Monitor 起来） |
| 9 | 备份配置、`setup start --space-id crypto`、`setup verify --space-id crypto` | 默认因子任务与示例策略在 `setup start` 里导入 |

- `setup verify` 不把“有限保留期的数据集还没有归档任务”算作没装好（全新安装一定没有归档任务，这一项在 `doctor alerts` 里照常显示）：装完后按需要用 `moox-cli archive tasks create` 或管理台“数据归档 › 归档任务”建任务。
- 任何一步失败先看原因和日志（`setup logs --host <主机> --component <组件> --journal`），修好后重跑同一条 `setup install`：每一步都可以重做，没有变化的组件不重启。
- 改了代码要重新构建二进制再重跑；`setup deploy` 按二进制摘要判断哪些组件有变化。
- 另一个空间装完后再启动：`$CLI setup start --source-dir $WT --space-id stockcn`（`setup start` 没有预览，不带 `--yes`）、`$CLI setup verify --space-id stockcn`。
- 交易所密钥：`setup init` 只写入、不检测。Trade 装好后逐把检测 `readonly = true` 以外的密钥：
  `$CLI trade inspect-credential --exchange EXCHANGE_BINANCE --environment ACCOUNT_ENVIRONMENT_PRODUCTION --credential-secret-id exchange-binance-production-main`
  （固定 ID 是 `exchange-<交易所>-<环境>-<名称>`，OKX 用 `EXCHANGE_OKX`，测试网用 `ACCOUNT_ENVIRONMENT_TESTNET`），看 `blockers` 有无阻断项；`检测没有完成` 只是交易所接口暂时不可用，稍后重试。
- 因子引擎的窗口与内存：模板默认回看 50000 根、计算内存 4GiB、进程预算不限。要调就在因子机上运行 `/data/moox/bin/moox-factor-engine tune --config /data/moox/config/factor-engine/engine.yaml` 看建议值，
  写进 `moox.toml` 的 `[overrides."factor-engine"."engine.yaml"]`，再 `setup deploy --host factor --component factor-engine`。不要直接改目标机上的配置文件，下次部署会被覆盖。

### 信任 MooX CA（用户自己做）

安装不碰信任库，也不等这一步。信任要这台电脑的管理员授权，Agent 的 shell 没有终端，做不了。安装结束时转告用户：

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ~/.moox/certs/moox-ca-<控制机地址>.crt
```

用户也可以在自己的终端运行 `setup trust-browser`。不做时管理台能打开，但浏览器有安全提示；其他设备从登录页下载根证书。重装后 CA 是新的，所有设备要重新装一次根证书；上一套的根证书留在钥匙串里，由用户自行清理。

### `setup start` 的已知限制

`setup start` 会逐项导入 `config/setup/factor-tasks.yaml` 里这个空间的因子任务；**任一任务的源数据集不存在就整体失败**，后面的任务不会建。默认有三个因子任务：crypto 的 `ftask_spot_1m`、`ftask_swap_1m` 与 A 股的 `ftask_cn_5m`（源数据集都由 Collector 的默认采集任务创建）；没有因子机时跳过因子；crypto 要海外机，没有海外机时 `setup start --space-id crypto` 本来就不能运行。
导入中断（成员加进去了、还没启用）后重跑 `setup start` 会接着启用。已知差别：它分不清“导入中断”和“人工停用”，在管理台手工停用的默认因子成员，重跑 `setup start` 后会被重新启用，要停用的重跑后再停一次；手动的 `factor task apply --file` 不会替你启用线上停用的成员，仍按差异报出来。
自己的因子任务文件里指向自定义数据集的任务（例如手工建的 5m 采集任务的数据集）在全新系统里没有源数据；建好对应采集任务后再 `factor task apply --file` 导入。

## 5. 默认初始化的范围

“系统默认配置”取字面意思：仓库 `config/setup/*.yaml` 的种子（Space、元数据、默认采集任务、数据集健康策略、默认因子任务 `factor-tasks.yaml`）。Collector 首次启动会把随部署放到 `<部署根>/config/collector/collection-tasks.yaml` 的种子写成默认采集任务
（只在任务表为空时写，任务 ID 固定，因子任务的源数据集依赖这些 ID；不足时 `moox-collector-cli init --seed-file` 补种）。默认有币安现货 1m、币安合约 1m（crypto 空间）、A 股 5m 和两条 A 股日线（stockcn，沪深 300 成分股；一条纯日线，一条带复权、申万行业、财务的附加字段，见 [`ashare-daily.md`](ashare-daily.md)），都是停用状态，`setup start --space-id <空间>` 才启用（会启用这个空间里停用的 K 线任务；两条日线任务标了 `start: manual`，`setup start` 跳过并在输出里提示，要用 `collector task update --enabled=true` 显式启用），安装开始时问用户选哪个空间；不再默认采集 1h，要 1h 数据时用 `moox-cli collector task create` 自己建。内置任务只在 Collector 启动时检查，运行中删空任务不会重新写入。crypto 的内置任务采集方式是 `stream`（由 collector-stream 推送采集并补采，市场默认值），A 股与港美股是 `scf`，不需要手工切换；crypto 不依赖云函数，首份计划要等 collector-stream 在线，用 `moox-cli collector status` 和 `setup verify` 确认。`ingest-mode get` 建好任务就能查到。

### 默认展示策略（合约双均线）

`setup start --space-id crypto` 自动创建：[`examples/strategies/dual_ma_swap_1m.yaml`](../../../examples/strategies/dual_ma_swap_1m.yaml)，绑定合约 1m 因子任务 `ftask_swap_1m`，固定 ID `default_dual_ma_swap_1m`，观察实例，不挂账户、只计算不投递。重复运行不会重复创建。

快线 `ma__5` 在慢线 `ma__10` 之上的合约都持有，等权，单个最多 5%。前几根 `ma__10` 为空时记 `too_many_missing`，第一个 ok 之前按积累处理，上限 30 分钟或 20 个周期，所以约 15 分钟内看到第一个结果。示例不写 `factor_params`，不独占关联因子任务：用户自己的实例也引用 `ftask_swap_1m` 时照常创建，两个实例共用这个任务。

### A 股空间（stockcn）

`config init` 第一步验证密钥时同时取到账号 AppId，识别服务器时拿到它们的 VPC、子网与私网 IP，据此自动生成 stockcn 的 `[scf_plan]`：函数入口优先选存储服务器所在地域（函数与 Storage 同地域，写入全程走内网），存储服务器不可用时退而选其他开了外部入口（`external_entry = true`）且有可用 VPC 的主机（如海外服务器）（云账号引用 Admin 里的 `tencent-default`，代码包桶 `moox-scf-<管理台地域>-<AppId>` 首次上传时自动创建）；没有任何可用入口主机时不生成、要手工补（入口主机要有可绑定的 VPC、所在地域支持云函数、同地域的其他主机在同一个 VPC），或者子用户没有 CAM 读权限取不到 AppId（页面会提示）。只有生成计划时才给入口主机写 `private_address`。`setup init` 据此登记云账号。

`setup start --space-id stockcn` 的前提是 `[scf_plan]` 打开并配了 stockcn（见 `moox.toml.example`：`[[scf_plan.spaces]] space_id = "stockcn"`，`markets.equity` 写入口主机所在地域，优先存储机所在地域，该地域要有写了 `external_entry = true`、配了 `private_address` 的主机）。它依次：

1. 启用任务时，任务绑定的标签还没有成员（全新部署时自动标签要等 Collector 下一轮标签巡检才首次同步）就先立即同步再启用：沪深 300 成分标签 `cn_hs300`（属性 `sync.index = "000300"`，东方财富成分接口，之后每天 08:40 自动同步，接口失败时保留旧成员）；
2. 启用“A 股 K 线 5m”任务并触发对账，Collector 建出 SCF 计划（300 只，函数个数按各源权重、限速和标的数估算，约 4 个，每 5 分钟触发一次、收盘后 1 秒同时取数，午休、收盘后、非交易日函数直接返回 `no_work`）；
3. 导入因子任务 `ftask_cn_5m`（只有 `ma__5`、`ma__10`），创建观察实例 `default_dual_ma_cn_5m`（[`examples/strategies/dual_ma_cn_5m.yaml`](../../../examples/strategies/dual_ma_cn_5m.yaml)，不挂账户）。

A 股按交易时段推进：每个交易日 48 期（09:30～11:25、13:00～14:55），11:25 那根之后是 13:00 那根；午休、隔夜、周末 Monitor 不报停滞。
数据是不复权 5m（通达信、东方财富按权重 4:2 对股票固定分配，每家最多试 3 次，失败时依次换下一家，新浪、腾讯只做备源——它们收盘后头几秒给未定稿值，理由见 `docs/模块/采集.md`；腾讯成交额由收盘价 × 成交量估算），除权日前后跨越除权的均线会有一次跳变。A 股实例可以挂 A 股模拟账户（交易模块新建账户时选“A 股”）；A 股实盘的 QMT 执行端还没接入。

### 交易日历的年度更新

交易日历内嵌在 `packages/marketcalendar/data/`（采集、因子、策略、Monitor、CLI 共用），每份日历两个文件、各自更新：A 股是 `cn_stock.json` 与 `cn_stock.manifest.json`，港股是 `hk_stock.json` 与 `hk_stock.manifest.json`，美股是 `us_stock.json` 与 `us_stock.manifest.json`。A 股覆盖期到当年 12 月 31 日，每年 11 月前后国务院公布下一年节假日安排后：

1. 把落在工作日上的休市日写进 `packages/marketcalendar/internal/gencn/main.go` 的 `closures`，在 `packages/marketcalendar` 下运行 `go run ./internal/gencn -year <下一年>`（调休补班的周末照样休市）；它同时重写 `cn_stock.manifest.json` 的 `valid_through` 与 `sha256`，`go test ./packages/marketcalendar/` 校验。交易所 12 月公布休市安排后核对一遍，有出入就改 `closures` 用同一条命令重新生成；交易所日历已进 AkShare 时也可以用 `scripts/marketdata/update-calendar.sh` 整体替换（这个脚本只管 A 股 `cn_stock`；港股、美股没有对应脚本，只用下面第 2、3 项的 `genhk`、`genus`，更新时分别执行）；
2. 港股：联交所发出下一年的证券市场假期通告后（2026 年是 [CT/075/25](https://www.hkex.com.hk/Services/Circulars-and-Notices/Participant-and-Members-Circulars/SEHK/2025/SEHK_CT_075_2025?sc_lang=en)，2027 年是 [CT/077/26](https://www.hkex.com.hk/Services/Circulars-and-Notices/Participant-and-Members-Circulars/SEHK/2026/SEHK_CT_077_2026Jun01162846?sc_lang=en)），把落在工作日上的休市日和半日市（圣诞前夕、新年前夕、农历新年前夕，只开上午市）写进 `packages/marketcalendar/internal/genhk/main.go` 的 `closures`、`earlyCloses`，通告网址写进 `source`，在 `packages/marketcalendar` 下运行 `go run ./internal/genhk`（整份重新生成，只写港股的两个文件）。补登或改动半日市会让之后的交易序号整体平移，因子本地存储要重建；
3. 美股：纽交所公布下一年的假期表后（[假期与提前收市日](https://www.nyse.com/trade/hours-calendars)），把落在工作日上的休市日和半日市（感恩节次日、平安夜等，13:00 收市）写进 `packages/marketcalendar/internal/genus/main.go` 的 `closures`、`earlyCloses`，网址写进 `source`，在 `packages/marketcalendar` 下运行 `go run ./internal/genus`（整份重新生成，只写美股的两个文件）。不套“节假日逢周六提前一天补休”之类的规则，一律照假期页逐日登记；夏令时不用管，`barclock` 按纽约时区换算。补登或改动半日市会让之后的交易序号整体平移，因子本地存储要重建；
4. `scripts/marketdata/validate-calendar.sh` 校验全部日历后，重新构建并发布 Collector、因子引擎、Strategy、Monitor、CLI，并重新上传 stockcn、stockhk、stockus 三个 SCF 代码包（`moox-cli collector function upload`，每个空间各传一次）。日历编在代码包里：漏传港股或美股包，旧日历的覆盖期一过，对应函数每期都返回 `no_work`，不报错。

**必须在 12 月中旬前完成**：采集计划预检要求日历覆盖期至少还剩 14 天，否则拒绝新计划；到期前 60 天起每天告警。

## 6. 验收

1. 四台主机的组件都在运行：`$CLI setup status --file ./moox.toml` 里每台 `installed: true`、各组件 `state: active`；再逐台 `$CLI doctor diagnose --file ./moox.toml --node <主机>`（`control`、`storage`、`overseas`、`factor`）看各组件的健康检查与上报
   （刚装完时结论是 `INCONCLUSIVE`、退出码 3，唯一没过的是 `host.disk_forecast`“磁盘预测历史不足”，属于正常，其余检查都应是 PASS 或 SKIPPED）；`doctor bootstrap` 只能查运行它的那台机器，在操作电脑上验收目标机不用它。没开 linger 的主机在 `setup install` 预检的 `manual_steps` 里就会列出。
2. `moox-cli doctor alerts --file ./moox.toml`：新对象会先有“尚未上报 / 尚无成功运行”，等一个周期；1h 数据集要等第一个整点之后。长时间不消的告警按提示排查。
3. 一条链路至少跑通一期：`moox-cli collector status --space-id crypto`（各数据集 `level: ok`）→ 因子引擎的输出（`$CLI setup logs --host factor --component factor-engine --journal --grep factor_period_done`）里 `factor_period_done ... status=complete rows=<标的数>`
   （`status=degraded rows=0` 说明有问题，常见是 `factor_memory_budget_exceeded`，看内存配置）。
4. 管理台能打开：`curl --cacert <CA> --resolve <控制机>:9527:<控制机> https://<控制机>:9527/`，首页返回 200 且引用的 `static/js/*.js` 也返回 200、体积正常；返回 503 和“前端没有打包进这个程序”说明 admin 编译时没带上前端。

### 全新部署后的验收

在上面四项之外逐项核对：第 1～13 步是采集与云函数，第 14～23 步是因子、策略与回放，第 24～33 步是控制面与部署。
`CLI` 同第 4 节，`STOCKCN_DATASET`、`CRYPTO_1H_DATASET` 与 `<…>` 换成实际的 ID，时间用 RFC3339。写操作照常先预览、给用户确认后再加 `--yes`；
第 8、9 步要在腾讯云控制台动手，第 21 步会真实下单、只在测试网或模拟盘账户上做，第 29、30 步会停组件、触发告警，这几步都先问用户。

1. **配置**：`$CLI setup validate --file ./moox.toml` 通过。
2. **进程与库**：`$CLI setup status --file ./moox.toml`、`$CLI doctor diagnose --file ./moox.toml --node control` 的组件清单与 `[deployments]` 一致。
   主机装了 `sqlite3` 时只读看表：Collector 库（`/data/moox/data/collector/moox_collector.db`）有 `t_collector_series_timeline`、`t_collector_cloud_accounts`、`t_collector_function_packages`。命令形如 `$CLI host exec --host control -- sqlite3 -readonly <库文件> .tables`。
3. **云账号与代码包**：`$CLI collector account list` 有一条云账号；`$CLI collector package list --space-id stockcn` 有可用（`available`）的代码包，每个配置了云函数的空间各查一次。
4. **自动接管与计划外函数**（验收前先不清腾讯云上已有的函数）：
   ```bash
   $CLI setup start --file ./moox.toml --source-dir $WT --space-id stockcn
   $CLI collector fetch-plan --space-id stockcn
   $CLI collector function list --space-id stockcn
   ```
   计划在 10 分钟内变成已生效，Collector 日志里没有 `already exists`；函数列表里每个函数存在、定时器一致、代码包最新。上一次安装留下、不在计划里的函数列在 `unplanned`，第一次读回之后定时器都是停用（第二片及以后的命名空间里的也是）。
   按各行给出的删除命令（`collector function delete --space-id stockcn --name <函数名> --namespace <命名空间> --region <地域>`，先不带 `--yes` 看预览）清掉之后，列表里不再出现。
   全新库里 crypto 的内置任务采集方式是 `stream`，Collector 启动没有因为写内置任务失败而退出。
5. **采集方式**：`$CLI collector ingest-mode get --space-id crypto` 全是 `stream`，`--space-id stockcn` 全是 `scf`。再选一个 crypto 的 1h 数据集做一次往返切换（分钟第 20～40 秒执行）：
   ```bash
   $CLI collector ingest-mode set --space-id crypto --dataset-id CRYPTO_1H_DATASET --ingest scf --yes
   $CLI collector status --space-id crypto
   $CLI collector ingest-mode set --space-id crypto --dataset-id CRYPTO_1H_DATASET --ingest stream --yes
   ```
   两次都生成交接计划并生效，切换前后的周期在 Storage 里都完成，随后 `collector kline check` 覆盖切换前后的区间没有缺口。
6. **实时采集与时间线**：开盘后 `$CLI collector status --space-id stockcn` 里第一个 5 分钟周期已完成；`$CLI collector kline check --space-id stockcn --dataset-id STOCKCN_DATASET --from START --to END` 没有缺口。
   Collector 库的 `t_collector_series_timeline` 每个采集数据集每个频率有一行，合成目标数据集不在里面。
7. **补采**：
   ```bash
   $CLI collector kline backfill --space-id stockcn --dataset-id STOCKCN_DATASET --from START --to END --force
   $CLI collector function list --space-id stockcn
   ```
   补采函数被建出来（列表里出现 `backfill` 一项），作业完成；再提交一次不会重建函数。
8. **读回修复**：在腾讯云控制台手工删掉一个采集函数、停掉另一个的定时器，`$CLI collector function list --space-id stockcn --drift` 先列出这两个；下一次读回（最长 1 小时，重启 Collector 会立即做首轮读回）之后两个都恢复，`--drift` 为空。
9. **暂停与恢复**：
   ```bash
   $CLI collector fetch-plan pause --space-id stockcn --yes
   $CLI collector function list --space-id stockcn
   $CLI collector fetch-plan resume --space-id stockcn
   $CLI collector function list --space-id stockcn
   ```
   暂停命令返回完成时全部定时器已停用，5 分钟后复查仍是停用（没有后台操作把它重新打开）；恢复后一轮内全部启用。在计划还没生效、函数还在创建时再做一遍，结果相同。
10. **代码包升级**：`$CLI collector function upload --space-id stockcn --cloud-account-id <云账号 ID> --version <新版本> --collector-root $WT/modules/collector` 之后几分钟内，`collector function list` 里全部函数的代码包变成新版本，期间采集不中断（有缺口由自动补采补上）。
11. **重启续做**：在第 4 步计划还没生效时重启 Collector，计划仍然走完，腾讯云上没有重复或残缺的函数。
12. **监控**：在管理台“设置 › 密钥”停用云账号密钥，Monitor 报“调用腾讯云失败”，恢复密钥后告警消除（这一步要用户在管理台操作）。
13. **依赖边界**（在仓库里）：
    ```bash
    cd $WT/modules/collector && go list -deps ./cmd/scf/market_data ./cmd/stream | grep -E 'sqlite|gorm|internal/store|internal/fetchplan|moox/modules/cloud|scfinvoker'; echo "exit=$?"
    ```
    没有输出，`exit=1`。不要直接 grep `cloudfunction`：云函数程序引的第三方包 `scf-go-lib/cloudfunction` 是正常依赖。
14. **策略定义**：`examples/strategies/` 的 5 个示例各用头部注释里说的因子任务试算一次（默认部署只有 `ftask_spot_1m`、`ftask_swap_1m`、`ftask_cn_5m`，其余按注释先建任务；`ma_var_5m` 写了 `factor_params`，试算只校验 DSL）：
    ```bash
    $CLI strategy validate --dsl-file $WT/examples/strategies/dual_ma_swap_1m.yaml --factor-task swap=ftask_swap_1m
    ```
    再写一份带 `type: signal` 的规则、一份带 `tranche:` 字段的规则各试算一次，前者报 type“只能是 rank”，后者报未知字段；把 dual_ma 的规则改成 `holding: {bars: 24, offsets: all}` 能通过。
15. **固定标的择时**：按 `docs/策略DSL手册.md` 第 6 节的 BTC 规则（列名换成任务里有的，例如 `ma__5`、`ma__10`）建一个定义和观察实例，跑过至少一次入场和一次出场后，
    `$CLI strategy result show --result-id <结果>` 的明细里有 `hold_entered`、`exit_condition`。
16. **共用任务与删除**：
    - 两个不调参的实例选同一个任务都能启用：`$CLI strategy instance create --strategy-id default_dual_ma_swap_1m --factor-task swap=ftask_swap_1m --enable`，与默认示例 `default_dual_ma_swap_1m` 同时出结果（`strategy result list`）。
      停用并删除新建的那个（`strategy instance disable`、`strategy instance delete`，预览里 `ftask_swap_1m` 在 `keep_factor_tasks`），之后 `$CLI factor task show --task-id ftask_swap_1m` 仍在，默认示例照常出结果。
    - 建一个 crypto 5m 的因子任务，加可变成员 `$CLI factor member add --task-id <5m 任务> --factor-id ma --kind var`，用 `ma_var_5m` 建观察实例并启用；`factor task show` 里 `bound_instance_id` 是这个实例。
      再用同一份定义建第二个实例选这个任务，启用被拒（“关联的是实例 X，不是本实例”）；再建一个不调参的实例（例如 `dual_ma` 的规则换成 `ma__var` 列）引用它，能启用。
    - 这时删除 `ma_var_5m` 的实例被拒，提示里列出那个不调参的实例；先删引用方，再删 `ma_var_5m` 的实例，预览里这个任务在 `delete_factor_tasks`，删完 `factor task show` 返回不存在、结果数据集一起删掉。
17. **`__params`**：默认合约任务只有固定成员，结果行的 `__params` 为空：
    ```bash
    $CLI data view query --space-id crypto --view-id view_factor_swap_1m --column __params --limit 3 \
      --selectors '[{"space_id":"crypto","dataset_id":"dataset_factor_swap_1m","subject_id":"BTC-USDT","freq":"1m"}]'
    ```
    第 16 步那个调参任务的 `__params` 只有 `ma__var` 一个键，`$CLI factor task params --task-id <5m 任务>` 有 `ma__var` 最近一次提交（`t_factor_member_params` 的一行）。
    对固定成员发起一次补算（`factor recalc submit --task-id ftask_swap_1m --member ma__5 --from START --to END`），完成后同一区间的 `ma__5` 与补算前逐位相同，`__params` 仍为空。
18. **序列全集**：事件校验要求 `universe_series` 必填、覆盖 `universe_subject_ids`、tag 非空，不合格的事件发不出去，所以看下游：crypto 现货、合约与 A 股的因子任务都出结果
    （`$CLI factor status`，因子引擎的输出里 `factor_period_done ... status=complete`）；对 `ftask_cn_5m` 发起一段补算能算出结果。有统一数据集时它的任务同样出结果。
19. **方式 A 仍可用**：现货、永续各一个任务（`--factor-task spot=ftask_spot_1m --factor-task swap=ftask_swap_1m`）的 `market: {select: both, prefer: spot}` 观察实例能启用并出结果；用同样的 `--factor-task` 做一次 View 来源的回放能跑完。
20. **回放**：
    ```bash
    $CLI strategy replay start --strategy-id default_dual_ma_swap_1m --factor-task swap=ftask_swap_1m --from START --to END --swap-fee-bps 5
    $CLI strategy replay start --strategy-id default_dual_ma_swap_1m --factor-task swap=ftask_swap_1m --from START --to END --swap-fee-bps 5 --slippage-bps 20
    $CLI strategy replay show --replay-id <回放>
    ```
    两次都完成，带滑点的累计收益更低。`ma_var_5m` 不加 `--input` 时提交被拒（只能用“因子现算”），加 `--input compute` 能跑完，`strategy replay bars` 每期的 `params_json` 里有 `ma__var`。
    选一个起点早于 View 保留根数的区间：View 来源提交即拒绝，`--input compute` 能跑（因子机本地数据要覆盖这段，必要时先做初始数据导入）。
21. **归属（测试网或模拟盘账户，先问用户）**：
    - 启用挂了这个组合账户的实例：确认之前 `$CLI trade shadow list --instance-id <实例>` 里有影子目标、没有订单；`$CLI strategy instance confirm --instance-id <实例>` 预览拿到会话号，再 `$CLI strategy instance confirm --instance-id <实例> --session-id <会话> --yes`（或管理台实例列表的“确认实盘”）之后，`$CLI trade logical target --logical-account-id <组合>` 里是这个实例的会话，Trade 下单。
    - 停用：几秒内这份目标变为过期、目标挂单被撤（挑一份已收敛并带挂单的目标验证，`trade order list --logical-account-id <组合> --owner-type TARGET --only-open` 变空），持仓不动。
    - 停用后把同一会话更晚的一份目标事件重放进 `MOOX_TRADE`：Trade 日志（`$CLI setup logs --host overseas --component trade --lines 2000`）里这条投递的 `error_code` 是 `superseded`，没有新订单。
      仓库里没有重放单个事件的命令，做不了就在汇报里写明没有线上验证，这一条由 Trade 的单测与本机 e2e（`scripts/test/e2e/test-strategy-trade-event-e2e.sh`）覆盖。
    - 再启用：新会话的第一份目标被接受。
    - 停掉 Trade（`$CLI setup stop --host overseas --component trade --yes`）后停用：实例显示已停用，并提示“Trade 未确认”；恢复 Trade（`$CLI setup start-component --host overseas --component trade --yes`）后再执行一次 `strategy instance disable`，目标变为过期。
    - 停用后把实例改到另一个组合账户（`strategy instance update`），再执行一次 `disable`，关闭仍落在原组合上（原组合的目标过期，新组合没有变化）。
    - 管理台的组合账户详情页能看到关联的实例；`$CLI trade logical show --logical-account-id <组合>` 的输出里没有归属相关的字段（归属只记在 Strategy）。
22. **因子侧**：新建因子任务后几分钟内 `$CLI factor task status --task-id <任务>` 显示计算中（检查全部通过）；第 17 步已看过 `t_factor_member_params`；
    带自定义算子的补算作业续跑时用的是首次领取时的算子版本：主机装了 `sqlite3` 时只读看 Factor 库，
    `$CLI host exec --host control -- sqlite3 -readonly /data/moox/data/factor-mgr/factor.db "SELECT c_job_id, c_udf_bindings FROM t_factor_recalc_jobs"`，
    领取过的作业这一列有值（空对象表示没引用算子），激活新版本后再续跑这一列不变。
23. **EngineControl 与模板**：`$CLI host routes --host-id factor` 里有 `trpc.moox.factor.EngineControl` 的三条路由，调用方是 strategy；因子机上 `ss -ltn | grep ':11423 '` 只监听 `127.0.0.1`。
24. **逐台核对部署**（`$CLI host exec --host <主机> -- <命令>`；因子机只出站，这一步在局域网里做）：
    ```bash
    systemctl --user list-units 'moox-*' --no-legend     # 只有这台主机该有的组件，全部 active
    cat /data/moox/version.json                          # 版本与提交是这次的，四台一致
    ls /data/moox                                        # bin config secrets data logs version.json（因子机另有 archive import）
    ss -ltn | grep -v 127.0.0.1                          # control: 9527 11003；storage: 11003 11004 4222；overseas: 11003 11004；factor: 只有 11003
    ls /data/moox/secrets                                # storage、factor 上只有本机密钥、主机证书、根证书、EventBus 角色文件（及本机的对象存储凭据）
    ```
25. **管理台登录**（用户在浏览器里做）：装根证书后打开 `https://<控制机>:9527` 没有证书警告；登录响应带 `Set-Cookie`，有 HttpOnly、Secure、SameSite=Strict；接口响应带 gzip。
    打开控制机终端、下载一个归档分区都成功。重启 Admin 之后要重新登录（会话只在进程内存里）。
26. **跨主机与出站主机**：`$CLI trade account list`、`$CLI data kline get …` 成功（经控制机到海外机、存储机）；
    `$CLI host agent status --host factor`、`$CLI lifecycle status --module factor-engine` 成功（经控制机中转）。
27. **外部入口**：`setup export-skill-config` 导出后用 skill 的 `scripts/data-kline.sh` 读到数据（`moox-skill` 身份走 11004）；`setup start --space-id stockcn` 之后等一期，A 股函数写入成功（第 6 步）。
28. **隔离**：仓库里没有在真机上发伪造请求的命令，这一条由 `make test-e2e-gates` 的主机网关联调覆盖（场景 `external-entry-local-and-cross-host-forward-acl-and-origin-caller-rules`）。
    要在真机上做时，在因子机上用本机主机密钥对海外机 `11003` 发 Trade 下单请求，四次都应被拒：以 `factor-engine` 身份，被调用名单拒绝；
    以 `host-gateway` 身份带 `X-Moox-Origin-Caller: moox-cli` 或 `scf-collector`，原因都是 `origin_host`（先判签名主机，因子机不是入口主机）；
    在存储机上以 `host-gateway` 身份带 `X-Moox-Origin-Caller: moox-cli`，原因是 `origin_identity`。原因看目标主机网关 `/metrics` 的 `moox_gateway_external_denials_total{reason=…}`。
29. **系统暂停与恢复**（先问用户）：`$CLI system pause --yes`，等过一个 1m 周期，`$CLI system resume`：采集、因子、策略依次追平后放行，期间没有下单；首页依次显示全部暂停、恢复追赶、正常。
30. **健康上报与直推**（在测试窗口内做，先问用户；每一项做完恢复再做下一项）：
    - 停掉海外机的 `egress-proxy` 两分钟（`setup stop` / `setup start-component`）：Monitor 报“停止上报”，恢复后自动消除。
    - 停掉存储机的 `eventbus` 五分钟：只收到一条“观测输入中断”，没有逐个组件的“停止上报”。
    - 停掉控制机的 `host-gateway` 五分钟：其他三台各推一条“控制机或 Monitor 不可达”，恢复后各推一条恢复。
    - 十分钟内重启 `monitor` 三次（`setup restart`）：其他三台各推一条“Monitor 反复重启”。
31. **因子引擎重启与本地写入**：`$CLI setup restart --host factor --component factor-engine --yes` 后 `$CLI factor task status`，载入期间显示未通过的检查项，之后自动开始计算；
    `$CLI import --pack <导入包> --target factor --dry-run` 通过，再实际导入一个小包（经引擎的入站服务）。`$CLI storage repair-view …` 的预览里停启 storage-view 用的是 systemd。
32. **升级与重跑**：
    - 改一行日志后重新构建，`$CLI setup deploy --host overseas --component trade`：只重启 Trade，`version.json` 更新，数据目录没动。
    - 给某个服务加一个方法后只部署该组件：这台主机的 `host-gateway` 被一起升级并重启。
    - 再跑一次 `$CLI setup install --space-id crypto --yes`：不报错，没有组件被重启，操作电脑的 `~/.config/moox/moox-cli.key` 没有变。
    - 在控制机第一次装完、存储机装到一半时中断安装，重跑同一条命令能接着装完。
33. **换电脑与清空预览**：另一台电脑上放两份配置文件、SSH 私钥、解密密钥，`$CLI setup operator --register` 再 `$CLI setup deploy --host control`、`$CLI setup status` 成功，原电脑的调用被拒（先问用户，做完要在原电脑上重新登记）。
    `$CLI setup wipe` 的预览单独列出归档文件与 SeaweedFS 数据的大小，并提醒先导出管理台里的密钥。

## 7. 踩坑清单

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `linger_required` | 目标机的部署用户没开 linger | 让用户在那台机器上 `sudo loginctl enable-linger <用户>` |
| `binary_missing` | `--binary-dir` 里缺某个组件的二进制或配套命令行 | 按第 2 节的目标清单补编 |
| `not_ready`，某个组件一直 `readyz 503` 或健康端口连不上 | 组件启动失败或依赖没起来 | `setup logs --host <主机> --component <组件> --journal` 看启动输出，再看文件日志 |
| 控制机第一次部署时 `control_admin_failed` | `moox-admin-cli bootstrap` 失败 | 看报错最后一行；多半是配置里的主机地址、组件不满足服务目录的要求 |
| `operator_trust_failed` | 这台电脑记着上一套部署的控制机根证书（上次没用 `setup wipe` 清空） | 按报错删掉 `~/.config/moox` 下列出的两个文件再重跑 |
| 管理台打开是“前端没有打包进这个程序” | moox-admin 编译时没有带上前端 | 按第 2 节重新构建 admin 后 `setup deploy --host control --component admin` |
| `setup trust-browser` 报 `browser_trust_manual` | 没有终端或 sudo 失败 | 把错误里的命令交给用户在自己的终端运行 |
| `setup start` 报 `CreateFactorTask failed ... metadata row not found` | 种子里有源数据集不存在的因子任务 | 见第 4 节“`setup start` 的已知限制” |
| 因子 `factor_memory_budget_exceeded`、`status=degraded rows=0` | 引擎 `compute.max_memory` 装不下一期计算 | 按第 4 节用 `tune` 的建议值写 `[overrides]` 后重新部署因子引擎 |
| 引擎重启后一段时间 `lease conflict`、`not_ready` | 上一个进程的租约还没过期 | 等几十秒，再等窗口载入（约 20 秒）和 warming |
| 仓库里多出二进制文件 | `go build ./...` 交叉编译在单 main 模块里留了产物 | 立即删除；别 `git add -A` |

## 8. 收尾

- `setup wipe` 默认不清理腾讯云上的函数：同名的由 Collector 接管，计划外的按第 6 节第 4 步用 `collector function delete` 清掉（先预览、给用户确认）；换过云账号的，旧账号下的函数用 `setup inspect-scf` 查出来，提醒用户到腾讯云控制台删除。
- 汇报：做了什么、验收结果、没做的事（第 5 节）、重建后丢失的手工配置、需要用户做的事（清理旧 CA 等）。
- 把发布过程中临时改过的代码或脚本提交并推送；配置有变更时提醒用户 `moox-cli config backup`（两份文件一起备份）。
- 更新记忆（部署状态、踩坑）。
- 不要擅自合并 PR；合并要用户明确说。
