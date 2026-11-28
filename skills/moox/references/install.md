# 安装、部署与初始化（基于发布产物）

外部用户第一次安装 MooX 时按本文执行：下载发布产物，不需要在本机编译。
维护者自己清空重装生产按 [`fresh-deploy.md`](fresh-deploy.md)，从源码目录部署和升级按 [`source-deploy.md`](source-deploy.md)。

## 先测网络，选下载源

安装一开始就在用户的电脑上测一下到 GitHub 和 CNB（cnb.cool，国内）的延迟，后面的下载都用更快的那个。**差异不大就用 GitHub**；只有 GitHub 不通、或 CNB 的延迟低于它的 70% 才用 CNB。
先 `ping`，被禁 ICMP 时自动改测 HTTPS 建连耗时。把结果告诉用户（例如“github.com 90 ms，cnb.cool 9 ms，从 CNB 下载”）。两个都不通时先让用户检查网络或代理，不要往下执行。

```bash
# 平均往返延迟（毫秒）；ping 不通（含被禁 ICMP）时改测 HTTPS 建连耗时，都不通则输出空
rtt() {
  local ms=""
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) ms=$(ping -n 3 "$1" 2>/dev/null | grep -Eo '(平均|Average) = [0-9]+' | grep -Eo '[0-9]+$' || true) ;;
    *) ms=$(ping -c 3 -i 0.5 "$1" 2>/dev/null | awk -F/ '/^(rtt|round-trip)/ {print $5}' || true) ;;
  esac
  [[ -n "$ms" ]] || ms=$(curl -s -o /dev/null --max-time 5 -w '%{time_connect}' "https://$1" 2>/dev/null | awk '$1 > 0 {printf "%.1f", $1 * 1000}')
  echo "$ms"
}
G=$(rtt github.com); C=$(rtt cnb.cool)
echo "github.com: ${G:-不通} ms，cnb.cool: ${C:-不通} ms"
SRC=github   # 差异不大或都测不出来就用 GitHub；只有 GitHub 不通、或 CNB 的延迟低于它的 70% 才用 CNB
if [[ -z "$G" && -n "$C" ]] || { [[ -n "$G" && -n "$C" ]] && awk "BEGIN{exit !($C < $G * 0.7)}"; }; then SRC=cnb; fi
echo "下载源: $SRC"
[[ -n "$G$C" ]] || echo "github.com 和 cnb.cool 都连不上：请先检查网络或代理"
```

## 0. 先跟用户确认

- **云厂商（最先确认）**：**当前所有服务器必须部署在腾讯云上**（轻量应用服务器或 CVM），只有因子机例外（可以是家里或局域网的 Linux 机器）。
  - 用户用腾讯云：往下继续。
  - 用户用其他主流云厂商：如实告诉用户其他云厂商正在支持中，现在不能用 `config init` 自动初始化，不要硬跑。
  - 用户用小众云厂商，或不想用腾讯云：告诉用户可以手动部署，参考 GitHub 源码（<https://github.com/mooyang-code/moox>）里的 `docs/部署与运维.md` 和 `moox.toml.example`，自行初始化各模块配置；不要代为猜测配置。

- **云账号**：需要调用云厂商接口的操作（识别服务器、防火墙、采集云函数、日志服务）目前对接腾讯云 API，其他云厂商后续接入。需要一个腾讯云子用户的 API 密钥，授权 6 个预设策略：
  `QcloudSCFFullAccess`、`QcloudCOSFullAccess`、`QcloudCLSFullAccess`、`QcloudLighthouseFullAccess`、`QcloudCVMFullAccess`、`QcloudVPCFullAccess`。
- **服务器**（Linux x86_64，SSH 能登录，部署目录要让 SSH 用户可写，不可写时报错里有要运行的 `sudo` 命令；除因子机外，必须是上面腾讯云账号下的轻量应用服务器或 CVM，`config init` 按 IP 识别地域与规格）：

  | 角色 | 必需 | 建议配置 | 跑什么 |
  | --- | --- | --- | --- |
  | 存储机 | 是 | 4 核 16G、100G 磁盘 | Storage、EventBus，网关开外部入口 |
  | 管理台 | 是，可与存储机或海外机共用 | 2 核 4G | Admin、管理台、Monitor、Collector、因子管理、归档管理、策略 |
  | 海外机 | 采集加密货币行情时需要（建议香港） | 2 核 4G | WS 行情推送、出口代理、交易执行 |
  | 因子机 | 可选，可以是家里或局域网的 Linux 机器 | 8 核 16G | 因子引擎、归档 Worker（systemd 用户服务） |

  **轻量应用服务器还是云服务器（CVM）**：区别只在云函数能不能接进来。股票行情（A 股、港股、美股）由云函数采集，
  云函数写数据要经**同地域**一台开了外部入口的主机的内网地址进来，这台主机必须是 CVM；轻量应用服务器的内网在腾讯云内部单独的网络里，
  云函数绑不上（建函数报 `ResourceNotFound.Vpc`），要打通只能用云联网，而部署规则不用云联网。按要采的市场选：

  | 角色 | 只采加密货币 | 还要采股票（云函数） |
  | --- | --- | --- |
  | 存储机 | 轻量或 CVM 都行 | **CVM**，放在云函数要用的地域（A 股、美股一般放南京）。它是默认的云函数入口，写入全程走内网 |
  | 管理台 | 轻量或 CVM 都行（不做入口） | 轻量或 CVM 都行。用轻量时它不能做入口，云函数地域里要另有 CVM |
  | 海外机 | 轻量或 CVM 都行 | 云函数也要放香港时（港股、加密货币云函数）用 **CVM**；只跑 WS 推送、出口代理、交易执行时轻量就够 |
  | 因子机 | 局域网机器 | 局域网机器 |

  - 云函数用到的**每个地域都要有一台 CVM 入口主机**，同地域的其他主机要和它在同一个 VPC。想把补采函数（`backfill_region`）单独放一个地域，
    那个地域也要有 CVM 入口主机；只有轻量服务器的地域放不了。
  - 轻量服务器套餐自带流量包、单价低，适合管理台；CVM 按配置与带宽计费，存储机要 16G 内存，选 CVM 也不会贵太多。
  - 海外机带宽通常很小，不要从它往回拉大文件。
  - 判断一台机器是哪种：`config init` 识别服务器时会显示“轻量应用服务器”或“云服务器”。
  - 当前作者的部署：管理台是广州的轻量服务器（4 核 4G）；存储机是南京的 CVM（4 核 16G，A 股、美股云函数与港股补采函数的入口）；
    海外机是香港的 CVM（2 核 2G，港股定时函数的入口，也跑加密货币 WS 推送与交易执行）；因子机在局域网。

- **本机**：macOS 或 Linux，能 SSH 到上面的服务器。**Windows 现在只能使用只读命令，部署走不通**，用户用 Windows 时提前说明。
- **有没有装过**：不用先问，填完配置后第 3 节自动检测每台机器上的每个组件，再按检测结果决定新安装、保留数据续装/更新（`--keep`）或全部覆盖重装（`--wipe`）；彻底卸载用 `setup wipe`。
  只有用户说机器已经装好、但这台电脑上没有原来的两份配置（换了电脑）时，先按“保留数据与换电脑”取回配置，不要重新 `config init`。
- **启动哪个空间**：加密货币用 `crypto`（需要海外机），A 股用 `stockcn`（需要配 `[scf_plan]`）。采集任务默认停用，安装时只启动选定的空间。
- **告警渠道**：企业微信、飞书、钉钉群机器人的 Webhook 地址，或一个接收告警的公网 HTTPS 地址（不在配置文件里填，安装完成后直接到管理台「资源与运维 › 监控告警」的“消息通知设置”添加；飞书机器人开了“签名校验”、钉钉机器人启用了“加签”时还要它的密钥）。

## 1. 下载发布产物

在一个新目录里下载两样东西：部署产物包（仓库文件快照和附件清单，不含服务器程序，各组件的包由目标机按清单下载）和本机系统对应的 `moox-cli`。版本以发布页的最新版本为准（下面用 `VER`）。

| 文件 | 说明 |
| --- | --- |
| `moox-bundle_${VER}_linux_amd64.tar.gz` | 部署产物包：仓库文件快照和附件清单，约 12MB，不含服务器程序 |
| `moox-<组件>_${VER}_linux_amd64.tar.gz` | 每个组件一个包，目标机自己并行下载，不用手工下载 |
| `moox-cli_${VER}_darwin_arm64.tar.gz` / `darwin_amd64` / `linux_amd64` / `linux_arm64` / `windows_amd64.zip` | 本机命令行 |
| `SHA256SUMS` | 校验和 |

下载地址由上面测速选出的 `SRC` 决定：

- GitHub：`https://github.com/mooyang-code/moox/releases/download/${VER}/<文件名>`
- CNB：`https://cnb.cool/mooyang-code/moox/-/releases/download/${VER}/<文件名>`

```bash
VER=v0.0.4
OS=darwin_arm64        # 按本机选择：darwin_arm64 / darwin_amd64 / linux_amd64 / linux_arm64
GITHUB=https://github.com/mooyang-code/moox/releases/download/${VER}
CNB=https://cnb.cool/mooyang-code/moox/-/releases/download/${VER}
if [[ "$SRC" == cnb ]]; then BASES=("$CNB" "$GITHUB"); else BASES=("$GITHUB" "$CNB"); fi
# 选中的源失败时自动换另一个；连接超时、重试和断点续传都由 curl 负责
fetch() {
  local base
  for base in "${BASES[@]}"; do
    curl -fL --connect-timeout 10 --retry 2 --retry-delay 2 -C - -O "$base/$1" && return 0
    echo "从 $base 下载 $1 失败，换另一个源" >&2
  done
  return 1
}
fetch moox-bundle_${VER}_linux_amd64.tar.gz
fetch moox-cli_${VER}_${OS}.tar.gz
fetch SHA256SUMS
shasum -a 256 -c SHA256SUMS --ignore-missing          # Linux 用 sha256sum -c
tar -xzf moox-bundle_${VER}_linux_amd64.tar.gz
tar -xzf moox-cli_${VER}_${OS}.tar.gz -C moox-${VER}/bin
cd moox-${VER}
./bin/moox-cli --help
```

之后**所有命令都在 `moox-${VER}` 目录里执行**：`moox.toml` 与 `moox.secrets.toml` 写在这里，目标机按产物包清单自己下载组件包（每个组件一个包），测速选源，每台最多 4 条连接，校验 SHA256；都下载不了时由这台电脑下载再推送。
产物包里有 `moox-bundle.json`，CLI 据此识别产物包，不需要 git、Go、Docker 或 Node。

## 2. 生成配置（用户在自己的浏览器里填写）

```bash
./bin/moox-cli config init
```

命令在本机起一个一次性页面并打开系统浏览器。页面先显示欢迎屏，用户点「开始设置」后按页面分步填写：

1. 腾讯云密钥（先验证）；
2. 各台服务器的 IP、SSH 端口和用户名（地域、规格按 IP 自动识别）；
3. **免密登录**：页面在本机生成 MooX 专用密钥 `~/.ssh/moox_ed25519`，逐台确认主机指纹，用户输入一次 SSH 密码装上公钥（密码不保存），
   密码或用户名填错时，页面直接让用户重新填写用户名和密码；也可以按页面给出的 `ssh-copy-id` 命令自己在终端执行；因子机在免密开通后自动做环境检测；
4. 管理员账号（管理台登录用）；
5. 确认后写出两份文件：明文的 `moox.toml`（主机、部署和部署参数，没有秘密）与加密的 `moox.secrets.toml`（仅含管理员、腾讯云密钥与 CLS 写入密钥；解密密钥在系统钥匙串）。

规则：

- 命令结束后立即告诉用户：配置已保存，页面可以关，之后不用重填。
- Agent **不打开、不填写、不读取**这个页面和 `moox.secrets.toml`；页面地址没能自动打开时，把命令打印的地址原样转给用户。`moox.toml` 是明文拓扑，可以直接看和改。
- 之后要看或改配置用 `./bin/moox-cli config edit`（同样由用户在浏览器里操作）。
- 交易所密钥与告警通知渠道都不在配置文件里填写，安装完成后把 `setup install` / `setup init` 输出的 `console` 入口地址交给用户在管理台页面录入：
  - **交易所密钥**（`https://<管理台地址>:9527/#/settings/secrets`）：在「系统设置 › 密钥管理」新增，类别选“交易所”，填交易所（`binance` / `okx`）、环境（生产 / 测试网）、是否只读与 OKX 口令。录入后可在页面点「测试连接」或运行 `moox-cli trade inspect-credential --exchange <交易所> --environment <环境> --credential-secret-id <密钥 ID>` 检测（只读密钥不检测）；输出里有阻断项就告诉用户原因，`检测没有完成` 是交易所接口暂时不可用，稍后重试。
  - **币安只读 Key**（可选）：Collector 的下架检测用它读币安现货下架计划，按属性自动匹配任一把启用的币安生产环境只读密钥（类别“交易所”、厂商 `binance`、环境“生产”、勾选“只读”，不依赖固定 ID）。不配也能装完：Monitor 只报一条 warning“下架检测缺少币安只读 Key”，不挡 `setup verify`；缺 Key 期间现货下架只能手工登记（`moox-cli collector delist register`，默认只预览、`--yes` 才执行，或管理台“交易管理 › 下架名单”页的“手工登记下架”；登记后到下架前 24 小时自动卖出实盘持仓，`--yes` 前先向用户确认标的和下架时间；演练用 `--paper-only`）。之后在管理台「系统设置 › 密钥管理」补建或启用，下一轮轮询即自动恢复。
  - **告警通知渠道**（`https://<管理台地址>:9527/#/ops/monitor`）：在「资源与运维 › 监控告警」的“消息通知设置”添加。`setup install` / `setup init` 会只读检查 Monitor 渠道数，若尚未配置（`notification.warning`）会在结果里提醒；交付时务必把这条提醒和监控页地址给用户，未配置前出了故障不会通知到群里。
- 不配海外机（没有出口代理）时 Collector 连不上币安、OKX，安装渲染会把 Collector 的 `delist.auto_detect` 设为 false，不自动检测下架，手工登记照常可用；配了海外机时为 true。
- 免密登录开通后，Agent 就能通过 `moox-cli` 登录这些服务器完成后续所有步骤。
- MooX 密钥不是 OpenSSH 的默认身份文件。手工验证要写 `ssh -i ~/.ssh/moox_ed25519 <用户>@<IP>`，普通 `ssh` 询问密码不代表公钥没装上。

## 3. 检测已有部署，选择保留数据还是全部覆盖

`config init` 一结束（用户填完各台机器）就运行，不用等用户开口：

```bash
./bin/moox-cli setup validate
```

它校验两份配置和到各台服务器的连接，并只读检测每台机器（部署根、`version.json`、控制机完成标记 `install-complete.json`、各组件数据格式 `data-format`、`moox-*` 用户服务、部署根里的进程），不改任何东西。输出的 `deployment` 里：

| 字段 | 含义 | 怎么做 |
| --- | --- | --- |
| `route` = `fresh`（`modes` = `["install"]`） | 每台机器都没有 `version.json` 与完成标记，没装过 | 直接走**新安装**（第 4 节），不用询问保留或覆盖 |
| `route` = `incomplete` | 至少一台机器有 `version.json`，但控制机没有完成标记 `install-complete.json`（上次安装没装完） | 跑 `setup install` 预检，把结果给用户看，问清**保留数据续装（`--keep`）**还是**全部覆盖（`--wipe`）** |
| `route` = `installed` | 控制机已有完成标记 `install-complete.json`（已装好） | 跑 `setup install` 预检，把结果给用户看，问清**保留数据更新（`--keep`）**还是**全部覆盖（`--wipe`）** |
| `data_format` | 本版本与目标机上各组件的数据格式比对（`can_keep` 为 `false` 时，`incompatible` 列出格式变化或未知的组件） | `can_keep` 为 `false` 时 `--keep` 会被拒绝，必须走 `--wipe` 全部覆盖 |
| `components` | 每台主机：`version`（这台装的版本）、`components`（`moox.toml` 要求的每个组件：`installed` 为 `version.json` 里有记录，`state` 为 `active` / `inactive` / `missing`；`extra: true` 是已不属于这台主机的组件） | 一个组件 `installed` 为 true 或 `state` 不是 `missing` 就算已部署 |
| `notes` | 部分主机没装过、装过的主机缺组件（上次安装没做完，或 `moox.toml` 新加的主机、组件）；主机上有东西但没有 `version.json`（失败的部署尝试或别的文件） | 原样告诉用户 |
| `manual_steps` | 要用户在目标机上自己做的事（需要 sudo），最常见的是开 linger | 把命令原样交给用户，做完再继续；没做完时安装和更新都会停下 |
| `warnings` | journald 没有持久化、磁盘余量小 | 告诉用户，不拦安装 |

- **linger**：组件都是 systemd 用户服务，部署用户没开 linger 时登出后服务会被停掉。`manual_steps` 里是 `sudo loginctl enable-linger <用户>`，要用户在那台机器上执行。
- **journald**：组件的标准输出与标准错误（崩溃信息、启动失败的原因）在 journald 里。没有持久化（`/var/log/journal` 不存在）时重启后看不到之前的输出，建议用户执行 `warnings` 里的命令。

### 装过时：先跑预检，再问用户保留还是覆盖

**CLI 不做交互式提问。** 检测到目标机装过（`route` 为 `incomplete` 或 `installed`）时，先运行一次不带 `--yes` 的只读预检：

```bash
./bin/moox-cli setup install                                 # 已装好时可不带 --space-id；没装完或要覆盖时也可先带上 --space-id <空间>
```

先确认配置来源：这台电脑上的 `moox.toml`、`moox.secrets.toml` 是不是当初装这套系统用的那两份（换电脑时从原电脑拷过来的也算）。
刚在这台电脑上用 `config init` 重新生成的（例如换了电脑）就停下，按“保留数据与换电脑”取回原来的配置再回到这里：用新生成的配置保留更新会登记新的操作身份、按新配置重新渲染全部主机。

把预检输出的部署状态（`route`）、各主机组件、`data_format` 格式比对结果，以及并排列出的 `options.keep` 与 `options.wipe`（含将自动备份的密钥与告警渠道条数 `backup`、本地归档目录大小 `archive_kb`）完整给用户看，再问清选择（对整套部署统一选，不逐台选）：

> 检测到目标机已经装过 MooX（状态：`<没装完 / 已装好>`，涉及 `<主机、IP、版本、组件>`）。数据格式比对结果：`<全部兼容 / 以下组件格式已变化或未知：...>`。请确认选择哪种方式：
> 1. **保留数据（`--keep`）**：`<若为已装好>`只换新版本的二进制与配置，K 线、因子、策略、交易记录、管理台账号等数据全部保留，不重新初始化，也不重新启用手工停掉的任务（需更新/新装/删掉的组件与换主机列表见预检 `options.keep.hosts` / `moves`）；`<若为没装完>`保留已有数据，接着跑完剩余安装步骤并验收；`<若 data_format.can_keep 为 false>`**本次不可选**，因格式已变化或未知。
> 2. **全部覆盖（`--wipe`）**：先自动将全部非系统密钥与告警渠道加密备份到本机 `moox.backup`（预计备份 `<secret_count>` 把密钥、`<channel_count>` 个告警渠道；**交易账户与组合账户不纳入备份**），再清空全部主机的部署根——**归档文件（`archive/`，当前 `<大小>`）会一起清掉，它是唯一可能补不回来的数据**（外部 `[archive_object_store]` 远端桶不删）——然后全新安装，并在全部组件就绪后自动覆盖导入备份。

- 用户选**保留数据**：带 `--keep --yes` 执行（`installed` 不要求 `--space-id`；`incomplete` 带 `--space-id <空间>`）。
- 用户选**全部覆盖**：带 `--space-id <空间> --wipe <预检里的 wipe.digest> --yes` 执行；若清空前自动备份失败，命令会立即停下不清空任何主机，修好后重试，或经用户明确同意加 `--no-backup` 跳过在线备份（本机已有 `moox.backup` 时会自动沿用并打上待导入标记）。
- 用户只想**单独卸载**（不马上重装）：走下面的 `setup wipe`。
- 带 `--yes` 但既没给 `--keep` 也没给 `--wipe` 时，CLI 会直接报错（`install_choice_required`），不动任何主机。

### 卸载（单独卸载）

先取预览（不会改动任何东西）：

```bash
./bin/moox-cli setup wipe
```

预览会输出清空范围（`plan.hosts`，含每台主机的 `archive_kb` 归档目录大小与 `plan.digest`）、不会动的东西（`left_behind`），以及清空前将自动备份的密钥与告警渠道条数（`backup`）。按以下内容向用户确认，不能只问“要覆盖吗”：

> 卸载会先自动把全部非系统密钥与告警渠道加密备份到本机 `moox.backup`（预计备份 `<secret_count>` 把密钥、`<channel_count>` 个告警渠道；交易账户与组合账户不纳入备份；若在线备份失败会停下不清，除非带 `--no-backup`）。
> 随后停掉并删除这些机器上全部 MooX 服务：`<主机、IP>`，删除部署目录里的全部内容：程序、配置、K 线、因子、策略、任务、交易记录、管理台账号与操作记录，
> 以及本地归档文件（`<大小>`；**它是唯一可能补不回来的数据**；配了 `[archive_object_store]` 外部对象存储时，远端桶里的归档不删）。删除后无法恢复。
> 如果启用过云函数采集，请你到腾讯云控制台删除 `moox-fetch-plan-` 开头的函数（或者让我带 `--clean-functions` 一起删）；不删会继续运行并产生费用，以后启用云函数时同名函数也会创建失败。
> 卸载不会动这些：`<left_behind>`，需要时你自己处理。

- **用户同意卸载**：`./bin/moox-cli setup wipe --yes --expect <预览里的 plan.digest>`。卸载后马上重装的，也可以直接用 `setup install --space-id <空间> --wipe <预检里的 wipe.digest> --yes`，卸载和重装合并在一次确认内。
  - 卸载开始清空前会给 `moox.backup` 打上“卸载后待导入”标记（直到重装自动导入成功后才清掉）：标记在的时候，任何自动或手动备份都不会覆盖该文件，防止中途失败再卸载时用半成品盖掉完好备份。带 `--no-backup` 卸载时，本机已有的 `moox.backup` 同样会被打上该标记，下次重装自动导入的就是它。
  - 卸载会同时删掉这台电脑上的操作员身份（`~/.config/moox` 下 `moox-cli` 的签名私钥、控制机根证书与它的指纹）：重装的控制机会生成新的 CA，`moox-cli` 的密钥也在下一次安装的第一步重新生成。
  - 中途失败后再运行一次会接着清：重新取预览，范围变了要重新询问。
  - 云上的采集计划函数默认不删：同一云账号可能有其他环境。要清理时加 `--clean-functions` 预览，用户确认后再带 `--clean-functions --expect-functions <functions_digest>`。
- 只拆掉一台机器（其余保留）不用卸载，按 [`host-registry.md`](host-registry.md) 移除主机。

## 4. 部署（统一入口 `setup install`）

**所有整套部署（新安装、没装完续装、已装好保留数据更新、清空全部覆盖）统一用 `setup install`，用户确认后后台运行：**

```bash
# 1. 只读预检（把路线、数据格式比对与可选操作给用户看）
./bin/moox-cli setup install --space-id <空间>
./bin/moox-cli setup install                                                      # 已装好系统保留更新可不写 --space-id

# 2. 按状态与用户选择执行
./bin/moox-cli setup install --space-id <空间> --yes                              # 没装过（fresh）：全新安装
./bin/moox-cli setup install --space-id <空间> --keep --yes                       # 没装完（incomplete）：保留数据续装
./bin/moox-cli setup install --keep --yes                                         # 已装好（installed）：保留数据更新（不要求 --space-id）
./bin/moox-cli setup install --space-id <空间> --wipe <wipe.digest> --yes         # 装过：自动备份 → 清空全部主机 → 全新安装 → 自动覆盖导入备份
```

### 全新安装 / 没装完续装 / `--wipe` 全部覆盖

带 `--yes` 后按顺序执行，进度逐行输出到标准错误：

0. （仅 `--wipe`）先自动备份全部非系统密钥与告警渠道到 `moox.backup` 并打上“卸载后待导入”标记（若已有待导入备份则保护不覆盖），再清空全部主机的部署根与本机操作员身份；
1. 准备这台电脑的操作身份（`setup operator`）：没有 `~/.config/moox/moox-cli.key` 就生成一把签名私钥，有就复用；它的公钥在下一步部署控制机时登记；
2. 放行防火墙端口；
3. 控制机先装 Admin、主机网关和 HostAgent（其余主机要向 Admin 要身份材料）；
4. 带 EventBus、Storage 的主机（默认是存储机）；
5. `setup init --stage storage`：业务空间、默认云账号密钥、元数据（控制机上的 Collector 启动时要核对元数据）；
6. 控制机其余组件；
7. 其余主机，因子机排最后；
8. 统一等一次全部组件就绪；
9. **自动导入待恢复备份**（在全部组件就绪之后、完整初始化之前）：若本机有带“卸载后待导入”标记的 `moox.backup`，按覆盖导入密钥（`SecretMgr.ImportSecrets`，保留原密钥 ID）与告警渠道（`MonitorMgr.ImportNotificationChannels`，按地址幂等覆盖），成功后清掉待导入标记；没有备份则提示去管理台录入；
10. `setup init`：写入默认云账号密钥（只建不改）、只读检查告警渠道（为空时提醒）、登记云账号、日志服务、采集计划，并输出管理台及密钥/监控入口地址；
11. `setup start` 启动空间，先跑安装后自检（`doctor bootstrap`，逐台核对本机/SSH 事实与 Monitor 总览），再跑逐空间验收（`setup verify`）；自检与验收通过后在控制机部署根下单独写出完成标记 `install-complete.json`。

- 每台主机的部署做的事：把登记同步到 Admin，预检本机组件的数据格式，目标机按产物包清单下载组件包（下载不了时由这台电脑下载再推送），向控制机要身份材料，
  渲染配置、环境文件和单元文件，装好后按启动顺序重启有变化的组件，写 `version.json`，再等这次装的组件就绪。
  每台主机有一把签名私钥（`<部署根>/secrets/host.key`，本机组件共用）：例行部署不换它，只重新签发主机证书；私钥丢了或和控制机登记的对不上时会自动签发新的。
- **中途失败修好后继续**：重新预检后带 `--keep --yes`（保留已有数据续装），或带 `--wipe <新 digest> --yes`（全部覆盖，不会覆盖之前已写好的待导入备份）。

### 已装好系统的保留数据更新（`setup install --keep`）

- 预检的 `options.keep.hosts` 按主机列出组件：`update` 是已部署、这次重新部署的；`install` 是 `moox.toml` 里新加的组件或新主机上的组件；`remove` 是 `moox.toml` 里已经去掉、这次会停掉并删除的。
  `options.keep.moves` 是换了主机的组件：旧主机上停掉删除，新主机上从空数据开始，**数据不迁移**。把它们、`data_format` 结果和 `steps`、`note` 一起给用户确认；`remove`、`moves` 不为空时单独说明。
- 只有 `data_format.can_keep` 为 `true` 时才能选 `--keep`；数据格式变化或未知时会拒绝并列出不兼容组件，改走 `--wipe`。
- 执行步骤与安装顺序相同，但**不做初始化、不导入备份、不启动空间、不验收**：手工停用的任务保持停用，不重建云函数，数据目录（`data/`）与控制机完成标记 `install-complete.json` 都不碰；每台主机只重启二进制或配置有变化的组件，组件按 `moox.toml` 的声明启停（`setup stop` 临时停掉的会被重新拉起）。
- 新版本带来的业务空间、数据集、内置采集任务和云函数代码包不会随保留更新到位；`moox.toml` 里新开了云函数采集（`[scf_plan]`）、日志服务或换了云账号也不会生效。需要时再运行一次 `setup init`（只建不改）。
- 更新后对用户启动的空间跑一次 `setup verify --space-id <空间>` 验收。

### 随时手动备份（`setup backup`）

```bash
./bin/moox-cli setup backup                    # 导出全部非系统密钥与告警渠道到 moox.backup（不设待导入标记）
./bin/moox-cli setup backup --verify           # 写完后读回校验渠道解密并以 overwrite=false 验收密钥导入
```

平时随时可以手动跑一次，防止 Admin 故障起不来时无备份可用；它不设“卸载后待导入”标记，不影响之后卸载时的自动备份。若当前已有未导入的待导入备份，手动备份会被拒绝覆盖（`backup_pending_import`）。

**分步命令用于修复和日常运维：**

| 命令 | 作用 |
| --- | --- |
| `setup deploy --host <主机> [--component a,b]` | 部署一台主机，或只部署其中几个组件 |
| `setup status [--host <主机>]` | 只读：各主机的版本（`version.json`）和每个组件的服务状态 |
| `setup logs --host <主机> [--component <组件>] [--journal] [--grep <文字>]` | 只读：先看文件日志，没有再看 journald；`--journal` 只看标准输出与标准错误 |
| `setup restart / stop / start-component --host <主机> --component <组件> --yes` | 重启；停掉并不再开机自启；恢复。不带 `--yes` 只预览 |

- `setup stop` 只是临时停：之后的部署按 `moox.toml` 的声明把它重新拉起。要长期停用写进 `[deployments_disabled]`，部署结果里是 `disabled`。
- 只部署个别组件时，如果服务目录变了，这台主机的主机网关会一起升级并重启（路由是网关按内嵌的服务目录算的）。
- 手工调过的组件配置写在 `moox.toml` 的 `[overrides."<组件>"."<配置文件名>"]` 里，每行是 `"键路径" = 值`，部署渲染之后逐项覆盖，重新部署不会丢；
  键路径不存在会报错。例如因子引擎的回看根数与内存上限（`moox-factor-engine tune` 给出建议值）：

  ```toml
  [overrides."factor-engine"."engine.yaml"]
  "window.lookback" = 50000
  "compute.max_memory" = "8GiB"
  ```

## 5. 初始化

启用 SCF 采集（`[scf_plan] enabled = true`）时，先按 `references/private-network.md` 为每个采集地域配置同地域的入口主机（`external_entry = true`）与 VPC。

`setup install` 已经包含初始化；单独运行时：`setup init --stage storage` 写入默认云账号密钥（`tencent-default`，只建不改）并把业务空间与元数据导入 Storage（数据集建好即为 active，不需要另外激活）；
不带 `--stage` 的 `setup init` 在全部组件装好后运行，依次完成防火墙核对、默认云账号密钥写入、告警渠道只读检查（为空时提醒）、云账户登记、日志服务、采集计划、登录与元数据核对，并输出管理台入口地址（`console.url`、`console.secrets_url`、`console.monitor_url`），已经做过的不会重复写入。`setup init` 会用秘密文件里的管理员密码核对登录，在管理台改过密码后要同步修改 `moox.secrets.toml`。
因子任务和默认策略在 `setup start` 里按空间导入。

管理台用内部证书。信任要这台电脑的管理员授权，Agent 做不了，也不阻塞安装：安装结束时把下面的命令交给用户在自己的终端运行，不要替用户清理钥匙串里的这张证书。

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ~/.moox/certs/moox-ca-<管理台地址>.crt
```

不做时管理台能打开，但浏览器有安全提示；其他设备从登录页下载根证书。

采集或计划有问题时，不要删任务、不要用 `host exec` 改数据库、不要用维护者脚本。先看 `collector status --space-id <空间>` 和 `setup verify` 给出的原因，走不通就停下来汇报。重启单个组件用 `setup restart`，不用重新部署整台主机。

## 6. 验收与交付

1. `./bin/moox-cli doctor alerts --format text`：新对象一开始会有“尚未上报 / 尚无成功运行”，等一两个周期再看；长时间不消的按 [`monitor-attention.md`](monitor-attention.md) 排查。
2. `./bin/moox-cli collector status --space-id crypto`：各数据集 `level: ok`。
   新数据集第一次接入时 Collector 自动补历史（`collector status` 的“历史补齐”显示进度与预计剩余时间）：补齐期间因子任务显示“历史补齐中”、不计算，策略不下单，监控显示“待生效”，都不是故障，补齐结束自动放行；
   `setup verify` 把补历史中的因子任务算通过，因子检查的 `detail` 写“因子任务 … 补历史中，下游待生效”，交付时如实告诉用户这些任务要等补齐结束才开始计算；
   不想等用 `moox-cli collector history-seed skip --space-id <空间>`。美股、港股几十分钟，A 股、crypto 要几小时。要加快 crypto：在 `moox.toml` 给 crypto 配几个海外 public 地域（见 `moox.toml.example`）。
3. 把 `setup install` / `setup init` 输出的管理台入口地址交给用户：
   - 管理台首页：`https://<管理台地址>:9527`（告知管理员用户名，密码在用户自己填的配置里）；
   - 交易所密钥录入：`https://<管理台地址>:9527/#/settings/secrets`（在「系统设置 › 密钥管理」新增交易与币安只读密钥；装完实盘/测试网密钥后可用「测试连接」或 `moox-cli trade inspect-credential` 检测）；
   - 告警通知渠道录入：`https://<管理台地址>:9527/#/ops/monitor`（在「资源与运维 › 监控告警」的“消息通知设置”添加；如果输出里带了未配置告警渠道的提醒，明确告知用户在补上之前故障不会推到群里）；
   - 同时说明默认已经在跑的内容：用户选的空间（crypto 是币安现货与合约 1m K 线采集；有因子机时还有因子任务和默认示例策略，合约 1m 双均线，观察实例，不投递）。以 `setup verify` 的结果为准，没验收通过不要说已经在跑。
4. 列出还没做、需要用户决定的事：长历史导入（[`initial-import.md`](initial-import.md)）、自定义因子（[`factor-dsl.md`](factor-dsl.md)）、自定义策略。

## 保留数据与换电脑

要保留线上数据时不卸载，用 `setup install --keep`（先不带 `--yes` 看预检与数据格式比对结果）。

换了电脑时，从原电脑拷过来三样东西：`moox.toml`、`moox.secrets.toml`，以及解密秘密文件的密钥（在原电脑的系统钥匙串里，没有钥匙串的机器在 `~/.config/moox/config.key`）。
系统不再把配置备份到管理台；原电脑已经找不回这两份文件时，只能用 `config init` 重新生成，再按“装过时：先跑预检，再问用户保留还是覆盖”处理。如实告诉用户：

- `setup validate`、`setup status`、`setup logs`、`setup firewall` 这类只靠 SSH、云密钥的命令，拷过来就能用（SSH 密钥路径要重新核对）。
- 经网关调用的命令（`doctor`、`collector`、`factor`、`trade` 等）和其余主机的 `setup deploy` 要这台电脑有操作员身份。新电脑上按顺序做两步（都要先问用户）：
  1. `./bin/moox-cli setup operator --register`：在这台电脑生成 `moox-cli` 的签名私钥，并经 SSH 把公钥登记到控制机。它**替换**原来登记的公钥，原来那台电脑随即不能再用，两台电脑不能同时用。约 10 秒后各主机网关生效。
  2. `./bin/moox-cli setup deploy --host control`：取回控制机的根证书（之后部署别的主机、连因子机都要用）；它重新执行 Admin 的初始化（只建不改），没有变化的组件不重启。
  这两步还没有在真机上验证过，做之前告诉用户。
- 这两步和 `setup status` 都要先解开 `moox.secrets.toml`：没带解密密钥会报“这台电脑上没有解密秘密文件的密钥”，把原电脑的密钥带过来即可，不要重新生成密钥覆盖。
- 身份目录（默认 `~/.config/moox`）的路径里不能有符号链接：报 `private material directory must be physical` 时按提示用 `--operator-dir` 指到实际路径。
- 不要手动拷贝或恢复 `~/.config/moox` 下的身份文件。

## 升级到新版本

下载新版本的产物包与 CLI（同一版本），把原来的 `moox.toml` 和 `moox.secrets.toml` 放进新目录，然后运行 `setup install` 预检并按数据格式比对结果决定保留数据（`--keep`）还是全部覆盖（`--wipe`）：

```bash
./bin/moox-cli setup validate                     # 看各主机现在的版本、组件与数据格式
./bin/moox-cli setup install                      # 预检：把数据格式比对、--keep 与 --wipe 两个选项给用户确认
./bin/moox-cli setup install --keep --yes         # 格式兼容、用户选择保留数据时执行（不要求 --space-id）
./bin/moox-cli setup verify --space-id <空间>
```

- 数据格式变化或未知时，`--keep` 会被拒绝并列出具体组件；让用户确认后改用 `./bin/moox-cli setup install --space-id <空间> --wipe <预检里的 wipe.digest> --yes`（清空前自动备份密钥与告警渠道，装完自动覆盖导入）。
- 只想更新一台主机时用 `setup deploy --host <主机>`（控制机先；始终按保留数据预检，格式变化会拒绝并提示走全部覆盖）。
- 每台主机只重启二进制或配置有变化的组件；整台部署总是带着主机网关，服务目录变化不需要另外处理。
- 保留更新只动程序和配置，数据目录（`data/`）不碰。配置每次部署都从这个版本的模板重新渲染，手工调过的值要写在 `[overrides]` 里才会保留。
- 回滚：用旧版本的产物包目录和 CLI 再部署一遍。数据格式未变的版本可以这样回退；格式变化的版本不行。
- 维护者从源码目录部署见 [`source-deploy.md`](source-deploy.md)。

## 常见问题

| 现象 | 含义 | 处理 |
| --- | --- | --- |
| `linger_required`、`manual_steps_required` | 目标机的部署用户没开 linger | 把报错里的 `sudo loginctl enable-linger <用户>` 交给用户在那台机器上执行，再重跑 |
| `target_prepare_failed` | 目标机上建不了部署目录（上级目录不可写、部署根不属于 SSH 用户或是符号链接） | 把报错里的 `sudo install -d -o <用户> <部署根>` 交给用户执行 |
| `ssh_unreachable`，且是因子机 | 这台电脑和因子机不在同一网络 | 换到同一网络后重新运行 |
| `install_failed` | 目标机上的安装脚本失败，后面是最后一行原因（启动某个服务失败、Storage 元数据库初始化失败等） | 按原因用 `setup logs --host <主机> --component <组件> --journal` 看启动输出；修好后重跑 |
| `not_ready` | 这次装的组件到时限还没就绪，括号里是原因（`readyz 503`、健康端口连不上、服务状态） | 用 `setup logs` 看日志。多半是它依赖的组件还没装（按安装顺序补上）或配置有误；什么都没变就不要反复重试 |
| `control_admin_failed` | 控制机上的 `moox-admin-cli` 执行失败（初始化 Admin、导出身份材料） | 看报错的最后一行；控制机还没装过时先 `setup deploy --host <控制机>` |
| `control_not_ready` | 这台电脑没有记下控制机的根证书指纹 | 先 `setup deploy --host <控制机>`；换了电脑见“保留数据与换电脑” |
| `operator_key_missing` | 这台电脑没有 `moox-cli` 的签名私钥 | 全新安装：`setup operator`（`setup install` 会自己做）；已经装好的系统换了电脑：`setup operator --register` |
| `operator_key_mismatch` | 控制机上登记的 `moox-cli` 公钥不是这台电脑的 | 问用户是不是换了电脑；是的话 `setup operator --register`（原电脑随即不能用） |
| `rotate_unsupported` | 对控制机用了 `--rotate-host-key` | 控制机的签名密钥不单独换，按报错的做法 |
| `operator_trust_failed` | 这台电脑上记着另一套部署的控制机根证书（线上清空重装过，CA 变了） | 确认线上确实是重装的之后，按报错删掉 `~/.config/moox` 下列出的两个文件再重跑 |
| `identity_invalid` | 从控制机取回的身份材料校验没通过（主机地址、组件和 Admin 里登记的不一致，或 CA 对不上） | 核对 `moox.toml` 里这台主机的地址和 `[deployments]` 后重跑 `setup deploy --host <主机>`（部署开头会先把 `moox.toml` 的登记写进 Admin） |
| `component_not_on_host` | `--component` 写的组件不在这台主机上 | 按报错列出的组件改 |
| `binary_dir_required`、`binary_missing` | 在源码目录里部署，没给或缺编译好的 Linux 二进制 | 用户安装应在产物包目录里运行；维护者见 [`source-deploy.md`](source-deploy.md) |
| `version_invalid` | 目标机的 `version.json` 损坏 | 告诉用户；确认后删掉它重跑（会把全部组件当成有变化重启一遍） |
| `config_invalid: [overrides…]` | `[overrides]` 写错：组件没有这个配置文件，或键路径不存在 | 按报错改 `moox.toml`；键路径以渲染出的配置为准（目标机 `<部署根>/config/<组件>/`） |
| `install_step_failed` | `setup install` 的某一步失败，后面是那一步的原因和单独重跑它的命令 | 按原因处理后重跑对应的 `setup install`（装过时带 `--keep --yes` 或 `--wipe <摘要> --yes`） |
| `install_choice_required`、`install_choice_conflict` | 目标机装过（`incomplete` 或 `installed`）时带 `--yes` 却未明确选 `--keep` / `--wipe`，或两者同时指定 | 先不带 `--yes` 跑 `setup install` 看预检，把结果给用户确认后明确二选一 |
| `data_format_incompatible` | 已有数据组件的格式与新版本不兼容或缺少 `data-format`（格式未知），拒绝保留数据 | 把不兼容组件列表给用户看，确认后走 `setup install --space-id <空间> --wipe <摘要> --yes` |
| `backup_failed` | 清空前自动备份密钥与告警渠道失败，已中止且未清空任何主机 | 先排查 Admin/Monitor 连通性后重试；若旧服务已无法恢复，经用户确认后加 `--no-backup`（本机已有 `moox.backup` 会被沿用并标记待导入） |
| `backup_pending_import` | 本机 `moox.backup` 带有“卸载后待导入”标记，拒绝被新备份覆盖 | 先完成安装导入该备份，防止半成品状态盖掉完好备份 |
| `update_core_missing` | 已装好系统出现控制机或存储机缺少 `version.json`，拒绝只做保留更新 | 排查该主机或改走 `--wipe` 全部覆盖 |
| `keep_not_needed` | 目标机没装过却带了 `--keep` | 去掉 `--keep` 直接运行 `setup install --space-id <空间> --yes` |
| `start_prerequisite_missing` | crypto 缺 collector-stream 或出口代理，其他空间没配 `[scf_plan]` | 告诉用户缺什么，按 `setup validate` 处理 |
| `no_collection_tasks` | 这个空间没有任何 K 线任务，`--space-id` 写错或种子没写入（任务都停用时验收跳过，不报错） | 核对空间，看 `collector task list`；不要手工改数据库 |
| `verify_read_failed` | 到超时还读不到验收数据，组件没起来或连接不通 | 看错误里的组件，用 `setup logs` 查 |
| `browser_trust_manual` | 信任要管理员授权，没有终端或安装失败 | 把错误里的命令交给用户在自己的终端运行，不影响继续安装 |
| `browser_ca_trust_failed` | 从管理台服务器取不回 MooX CA 根证书 | 检查 SSH 和控制机是否已装好（根证书在 `<部署根>/secrets/moox-ca.crt`），用 `setup logs --host <控制机> --component admin` 查 |
| `component_operation_failed` | `setup restart`、`stop`、`start-component` 失败 | 看错误原因；用 `setup logs --journal` 查 |
| `collection_not_started` | 还没运行 `setup start`（任务默认停用）；或到超时还没有采集计划、collector-stream 不在线 | 看 `collector status` 给出的原因，不要删任务、不要手工改数据 |
| `monitor_unhealthy` | 到超时 Monitor 还有 critical 问题，报错里列出了主机和组件 | 看 `doctor alerts`，再用 `doctor bootstrap --node <主机>` 经 SSH 看那台主机上组件的本机 `/readyz` 与 Monitor 上报状态（健康端口只听本机，防火墙不放行它）。组件都正常但告警不解除时，检查告警通知渠道：恢复通知发不出去时告警会保持触发 |
| `monitor_unreadable` | 到超时还读不到 Monitor | 用 `setup logs --host <控制机> --component monitor` 查 Monitor |
| `kline_not_arrived` | 到超时还没有 K 线，或缺失超过 5% | 看 `collector status` 和 `setup logs --host <海外机> --component collector-stream` |
| `factor_not_advancing` | 启用的 1m 因子任务没有前进，或最近一期失败（源数据集补历史中的任务不算，它们按通过记、说明里写“补历史中，下游待生效”） | 看 `factor status` 和 `setup logs --host <因子机> --component factor-engine` |
| `strategy_not_running` | 启用的策略实例没有运行会话 | 看 `strategy instance show` 和 `setup logs --host <控制机> --component strategy` |
| `admin_not_ready` | 这台电脑没有 `moox-cli` 的签名私钥 | 同 `operator_key_missing` |
| `logs_failed` | 读不到目标机的日志 | 检查 SSH 连接；读不到就向用户说明，不要上机 |
| `wipe_scope_changed` | 清空范围和预览不一致，没有清空任何主机 | 重新预览并确认 |
| `wipe_incomplete`、`deployment_wipe_failed` | 清空没做完 | 重新运行 `setup wipe` 接着清，清完才能安装 |
| `wipe_not_needed` | 目标机没装过却带了 `--wipe` | 去掉 `--wipe` 再运行 |
| `functions_unread`、`functions_delete_failed` | `--clean-functions` 读不了某个地域，或没有删完 | 检查云账号权限后重跑 |
| `bundle_incomplete` | 产物包清单里没有某个组件的包 | 重新下载产物包 |
| `bundle_version_mismatch` | 产物包和 moox-cli 不是同一版本 | 下载同一版本的 CLI 和产物包 |
| `target_arch_unsupported` | 发布只有 linux/amd64，目标机架构不同 | 换 amd64 主机 |
| `no_download_source`、`download_failed` | 目标机和这台电脑都下载不了组件包 | 检查网络后重跑同一条命令，已下载的不会重下 |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | 系统 `known_hosts` 有旧记录，或机器确实换了 | 先跑 `setup validate`，通过说明 MooX 记录的指纹没变；不自己删记录，向用户说明 |
| `host_key_unknown` | 某台服务器的主机指纹没确认（例如后来新加的主机） | 请用户经可信渠道（腾讯云控制台登录该机执行 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`）核对指纹后，执行 `setup trust-host --host <主机ID> --fingerprint <指纹>` |
| `ssh_auth_failed` | 免密登录没开通或公钥被删 | 让用户在终端执行 `ssh-copy-id -i ~/.ssh/moox_ed25519.pub -p <端口> <用户>@<地址>` |
| 腾讯云接口报权限不足 | 子用户缺策略 | 核对子用户的 6 个预设策略 |
| `setup start` 报某个因子任务的源数据集不存在 | 该任务指向还没创建的采集数据集 | 先建采集任务；默认任务在 `config/setup/factor-tasks.yaml`，自己的任务用 `factor task apply --file` |
| 产物包只有 linux/amd64 | ARM 服务器暂不支持 | 换 x86_64 服务器 |
