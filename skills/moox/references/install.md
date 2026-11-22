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
  | 管理台 | 是，可与存储机或海外机共用 | 2 核 4G | Admin、管理台、Monitor、Collector、因子管理、策略 |
  | 海外机 | 采集加密货币行情时需要（建议香港） | 2 核 4G | WS 行情推送、出口代理、交易执行 |
  | 因子机 | 可选，可以是家里或局域网的 Linux 机器 | 8 核 16G | 因子引擎、归档 Worker（systemd 用户服务） |

- **本机**：macOS 或 Linux，能 SSH 到上面的服务器。**Windows 现在只能使用只读命令，部署走不通**，用户用 Windows 时提前说明。
- **有没有装过**：不用先问，填完配置后第 3 节自动检测每台机器上的每个组件，再决定新安装、覆盖更新还是卸载。
  只有用户说机器已经装好、但这台电脑上没有原来的两份配置（换了电脑）时，先按“保留数据与换电脑”取回配置，不要重新 `config init`。
- **启动哪个空间**：加密货币用 `crypto`（需要海外机），A 股用 `stockcn`（需要配 `[scf_plan]`）。采集任务默认停用，安装时只启动选定的空间。
- **告警渠道**：企业微信、飞书、钉钉群机器人的 Webhook 地址，或一个接收告警的公网 HTTPS 地址（可选，没有可以部署后再在管理台添加）。飞书机器人开了“签名校验”、钉钉机器人启用了“加签”时还要它的密钥。

## 1. 下载发布产物

在一个新目录里下载两样东西：部署产物包（含全部服务器程序）和本机系统对应的 `moox-cli`。版本以发布页的最新版本为准（下面用 `VER`）。

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
VER=v0.0.2
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
4. 管理员账号与告警渠道（机器人 Webhook 可以先不填，部署完成后在管理台“监控 › 告警渠道”添加；添加之前出了问题不会通知到群里，要提醒用户补上）；
5. 交易所密钥（可选，只有配了海外服务器才有这一步）：币安或 OKX 的 API 密钥写成 `[[exchange_keys]]`，只读密钥勾“只读”；页面不在本机检测（密钥通常绑定了海外服务器的 IP），装完后在海外机上检测；
6. 确认后写出两份文件：明文的 `moox.toml`（主机、部署和部署参数，没有秘密）与加密的 `moox.secrets.toml`（管理员、云密钥、交易所密钥、告警渠道；解密密钥在系统钥匙串）。

规则：

- 命令结束后立即告诉用户：配置已保存，页面可以关，之后不用重填。
- Agent **不打开、不填写、不读取**这个页面和 `moox.secrets.toml`；页面地址没能自动打开时，把命令打印的地址原样转给用户。`moox.toml` 是明文拓扑，可以直接看和改。
- 之后要看或改配置用 `./bin/moox-cli config edit`（同样由用户在浏览器里操作）。
- 填了交易所密钥的：`setup install` 把它们写进密钥管理（只建不改，管理台里已有或已删除的不动），安装结束后逐把检测：`moox-cli trade inspect-credential --exchange EXCHANGE_BINANCE --environment ACCOUNT_ENVIRONMENT_PRODUCTION --credential-secret-id exchange-binance-production-<名称>`（`readonly = true` 的不检测）。输出里有阻断项就告诉用户原因，由用户在交易所或管理台处理；`检测没有完成` 是交易所接口暂时不可用，稍后重试。
- 币安只读 Key（可选）：Collector 的下架检测用它读币安现货下架计划，固定读名称为 `readonly` 的币安生产环境只读密钥（`exchange = "binance"`、`environment = "production"`、`name = "readonly"`、`readonly = true`，ID `exchange-binance-production-readonly`）。
  不配也能装完：Monitor 只报一条 warning“下架检测缺少币安只读 Key”，不挡 `setup verify`；缺这把 Key 时现货下架只能手工登记：`moox-cli collector delist register`（默认只预览，`--yes` 才执行），或管理台「事件中心」“事件”页签里“标的即将下架”一行的“手工登记”。
  登记后到下架前 24 小时（T−24h，已不足 24 小时的立即）会自动卖出策略持有的实盘仓位；`--yes` 前先向用户确认标的和下架时间；演练用 `--paper-only`（只平模拟盘组合，但同样全局禁止买入这个标的，直到演练记录结束）。
  要补就让用户 `config edit` 加上，再 `setup init` 写入，下一轮检测自动恢复。
  注意 `setup init`（以及 `setup install` 里写告警渠道的那一步）对告警渠道的规则是“Monitor 一个渠道都没有就写入秘密文件里的 `[notification]`”：
  用户在管理台把渠道全部删掉之后再跑，会把秘密文件里的旧渠道写回去。不想要它就先让用户在 `config edit` 里把 Webhook 地址清空。
- 不配海外机（没有出口代理）时 Collector 连不上币安、OKX，安装渲染会把 Collector 的 `delist.auto_detect` 设为 false，不自动检测下架，手工登记照常可用；配了海外机时为 true。
- 免密登录开通后，Agent 就能通过 `moox-cli` 登录这些服务器完成后续所有步骤。
- MooX 密钥不是 OpenSSH 的默认身份文件。手工验证要写 `ssh -i ~/.ssh/moox_ed25519 <用户>@<IP>`，普通 `ssh` 询问密码不代表公钥没装上。

## 3. 检测已有部署，选择方式：新安装、覆盖更新、卸载

`config init` 一结束（用户填完各台机器）就运行，不用等用户开口：

```bash
./bin/moox-cli setup validate
```

它校验两份配置和到各台服务器的连接，并只读检测每台机器（部署根、`version.json`、`moox-*` 用户服务、部署根里的进程），不改任何东西。输出的 `deployment` 里：

| 字段 | 含义 | 怎么做 |
| --- | --- | --- |
| `modes` = `["install"]` | 每台机器的部署根（默认 `/data/moox`）下都没有 `version.json`，没装过（`route` = `fresh`） | 走**新安装**（第 4 节），不用询问清空 |
| `modes` = `["update", "uninstall"]` | 至少一台机器装过（`route` = `installed`） | 把检测结果列给用户，让用户选**覆盖更新**或**卸载** |
| `components` | 每台主机：`version`（这台装的版本）、`components`（`moox.toml` 要求的每个组件：`installed` 为 `version.json` 里有记录，`state` 为 `active` / `inactive` / `missing`；`extra: true` 是已不属于这台主机的组件） | 一个组件 `installed` 为 true 或 `state` 不是 `missing` 就算已部署 |
| `notes` | 部分主机没装过（上次安装没做完，或新加的主机）；主机上有东西但没有 `version.json`（失败的部署尝试或别的文件） | 原样告诉用户 |
| `manual_steps` | 要用户在目标机上自己做的事（需要 sudo），最常见的是开 linger | 把命令原样交给用户，做完再继续；没做完时安装和覆盖更新都会停下 |
| `warnings` | journald 没有持久化、磁盘余量小 | 告诉用户，不拦安装 |

- **linger**：组件都是 systemd 用户服务，部署用户没开 linger 时登出后服务会被停掉。`manual_steps` 里是 `sudo loginctl enable-linger <用户>`，要用户在那台机器上执行。
- **journald**：组件的标准输出与标准错误（崩溃信息、启动失败的原因）在 journald 里。没有持久化（`/var/log/journal` 不存在）时重启后看不到之前的输出，建议用户执行 `warnings` 里的命令。

### 装过时这样问用户

先按 `components` 列一张表（主机、IP、版本、已部署的组件、没部署的组件），再问。选择对整套部署统一做，不逐台选：

> 检测到这些机器上已经部署了 MooX：`<主机、IP、版本、已部署的组件>`。`<没部署的组件或主机>` 还没装。请选一种方式：
> 1. **覆盖更新**：把这个版本的程序和配置按顺序重新部署到全部机器，K 线、因子、策略、交易记录、管理台账号等数据都保留；没装的组件和新加的主机会装上，`moox.toml` 里去掉的组件会停掉删除。
> 2. **卸载**：停掉并删除这些机器上全部 MooX 服务和全部数据，删除后无法恢复（范围见下面的预览）。卸载完可以接着全新安装。

- 用户选**覆盖更新**：走第 4 节的“覆盖更新”。
- 用户选**卸载**：先取预览，按下面的话术确认后执行；执行完问用户要不要接着新安装，要的话回到第 4 节“新安装”（或者一开始就用 `setup install --wipe` 把卸载和新安装合成一次确认）。
- `notes` 说部分主机没装过时，先问用户是不是**上次 `setup install` 没做完**：是的话重跑同一条 `setup install --space-id <空间> --yes`（见第 4 节），不走覆盖更新——覆盖更新不做初始化和启动空间，会留下一套没装完的系统。
- 这个选择只在开始时问一次；同一次安装中途失败后照旧重跑同一条 `setup install`，不重新检测、不再问。

### 卸载

先取预览（不会改动任何东西）：

```bash
./bin/moox-cli setup wipe
```

再这样问用户，不能只问“要覆盖吗”。主机来自预览的 `plan.hosts`，其中 `archive_kb`、`seaweedfs_kb` 是归档文件与内置对象存储数据的大小（-1 表示没有）；不会动的东西来自 `left_behind`：

> 卸载会停掉并删除这些机器上全部 MooX 服务：`<主机、IP>`，删除部署目录里的全部内容：程序、配置、K 线、因子、策略、任务、交易记录、管理台账号与操作记录、配置备份，
> 以及归档文件（`<大小>`）和内置对象存储里的归档副本（`<大小>`）。删除后无法恢复。
> 管理台里后来改过或新加的密钥（交易所密钥、告警渠道、云账号）只在线上有，卸载前请先在管理台里导出或记下来。
> 如果启用过云函数采集，请你到腾讯云控制台删除 `moox-fetch-plan-` 开头的函数（或者让我带 `--clean-functions` 一起删）；不删会继续运行并产生费用，以后启用云函数时同名函数也会创建失败。
> 卸载不会动这些：`<left_behind>`，需要时你自己处理。

- **用户同意卸载**：`./bin/moox-cli setup wipe --yes --expect <预览里的 plan.digest>`。卸载后马上重装的，也可以直接 `setup install` 带 `--wipe <预检里的 wipe.digest>`（见第 4 节），卸载和后面的安装都算在这次确认内。
  - 卸载会同时删掉这台电脑上的操作员身份（`~/.config/moox` 下 `moox-cli` 的签名私钥、控制机根证书与它的指纹）：重装的控制机会生成新的 CA，`moox-cli` 的密钥也在下一次安装的第一步重新生成。
  - 中途失败后再运行一次会接着清：重新取预览，范围变了要重新询问。
  - 云上的采集计划函数默认不删：同一云账号可能有其他环境。要清理时加 `--clean-functions` 预览，用户确认后再带 `--clean-functions --expect-functions <functions_digest>`。
- 只拆掉一台机器（其余保留）不用卸载，按 [`host-registry.md`](host-registry.md) 移除主机。

## 4. 部署

### 新安装

**主线是一条命令，用户同意后后台运行：**

```bash
./bin/moox-cli setup install --space-id <空间>              # 只读预检，把结果告诉用户
./bin/moox-cli setup install --space-id <空间> --yes        # 执行
./bin/moox-cli setup install --space-id <空间> --yes --wipe <预检里的 wipe.digest>   # 装过、用户确认卸载后重装
```

- 预检输出路线、安装步骤（`steps`，每一步带可以单独运行的命令）、`manual_steps`，装过时还有清空范围（`wipe`）。
- 带 `--yes` 后按顺序执行，进度逐行输出到标准错误：
  0. 准备这台电脑的操作身份（`setup operator`）：没有 `~/.config/moox/moox-cli.key` 就生成一把签名私钥，有就复用；它的公钥在下一步部署控制机时登记；
  1. 放行防火墙端口；
  2. 控制机先装 Admin、主机网关和 HostAgent（其余主机要向 Admin 要身份材料）；
  3. 带 EventBus、Storage 的主机（默认是存储机）；
  4. `setup init --stage storage`：业务空间、初始秘密、元数据（控制机上的 Collector 启动时要核对元数据）；
  5. 控制机其余组件；
  6. 其余主机，因子机排最后；
  7. 统一等一次全部组件就绪；
  8. `setup init`：云账号、日志服务、采集计划、告警渠道（这些要等 Collector、Monitor 起来）；
  9. 备份配置、`setup start` 启动空间、`setup verify` 验收。
- 每台主机的部署做的事：把登记同步到 Admin，目标机按产物包清单下载组件包（下载不了时由这台电脑下载再推送），向控制机要身份材料，
  渲染配置、环境文件和单元文件，装好后按启动顺序重启有变化的组件，写 `version.json`，再等这次装的组件就绪。
  每台主机有一把签名私钥（`<部署根>/secrets/host.key`，本机组件共用）：例行部署不换它，只重新签发主机证书；私钥丢了或和控制机登记的对不上时会自动签发新的。
- **中途失败修好后重跑同一条命令**：每一步都可以重做，没有变化的组件不会重启。报错里有“单独重跑这一步”的命令，也可以只重跑那一步再继续。
- 装过的机器不带 `--wipe` 重跑就是接着装完，数据保留。但重跑会再执行一次 `setup start`：用户手工停用的内置采集任务与默认因子任务成员会被重新启用，
  走云函数的空间会重建云函数。**已经装好的系统要换版本或改配置用覆盖更新**（`setup update`），不要重跑 `setup install`。

### 覆盖更新

```bash
./bin/moox-cli setup update          # 只读预览：每台主机要更新（update）、新装（install）、删掉（remove）的组件和执行步骤
./bin/moox-cli setup update --yes    # 用户确认后执行，后台运行
```

- 预览的 `hosts` 按主机列出组件：`update` 是已部署、这次重新部署的；`install` 是 `moox.toml` 里新加的组件或新主机上的组件；`remove` 是 `moox.toml` 里已经去掉、这次会停掉并删除的。
  把它和 `steps`、`note` 一起作为更新计划给用户确认；`remove` 不为空时单独说明这些组件会被删除。
- 步骤和新安装的部署顺序相同：准备操作身份、放行防火墙、控制机先装 Admin、主机网关与 HostAgent，再装带 EventBus、Storage 的主机，然后控制机其余组件和其余主机（因子机最后），统一等一次全部就绪，最后备份配置。
- 不做初始化（不写业务空间、元数据、初始秘密和告警渠道），不启动空间（手工停用的任务保持停用，不重建云函数），数据目录（`data/`）不碰；每台主机只重启二进制或配置有变化的组件，`setup stop` 停掉的保持停用。
- 中途失败（`update_step_failed`）修好后重跑同一条 `setup update --yes`，报错里也有单独重跑那一步的命令；目标机都没装过时报 `update_not_installed`，改走新安装。

**分步命令用于修复和日常运维：**

| 命令 | 作用 |
| --- | --- |
| `setup deploy --host <主机> [--component a,b]` | 部署一台主机，或只部署其中几个组件 |
| `setup status [--host <主机>]` | 只读：各主机的版本（`version.json`）和每个组件的服务状态 |
| `setup logs --host <主机> [--component <组件>] [--journal] [--grep <文字>]` | 只读：先看文件日志，没有再看 journald；`--journal` 只看标准输出与标准错误 |
| `setup restart / stop / start-component --host <主机> --component <组件> --yes` | 重启；停掉并不再开机自启；恢复。不带 `--yes` 只预览 |

- `setup stop` 停掉的组件，之后的部署只更新它的文件、保持停用（结果里是 `paused`），要 `setup start-component` 才恢复。
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

`setup install` 已经包含初始化；单独运行时：`setup init --stage storage` 只写业务空间、初始秘密（管理员、腾讯云密钥、交易所密钥，只建不改）并导入元数据（数据集建好即为 active，不需要另外激活）；
不带 `--stage` 的 `setup init` 在全部组件装好后运行，依次完成防火墙核对、业务空间与初始秘密、告警渠道、云账户、日志服务、采集计划、元数据核对，已经做过的不会重复写入。
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
3. 告诉用户管理台地址 `https://<管理台地址>:9527`、管理员用户名（密码在用户自己填的配置里），以及默认已经在跑的内容：
   用户选的空间。crypto 是币安现货与合约 1m K 线采集；有因子机时还有因子任务和默认示例策略（合约 1m 双均线，观察实例，不投递）。以 `setup verify` 的结果为准，没验收通过不要说已经在跑。
4. 列出还没做、需要用户决定的事：长历史导入（[`initial-import.md`](initial-import.md)）、自定义因子（[`factor-dsl.md`](factor-dsl.md)）、自定义策略。

## 保留数据与换电脑

要保留线上数据时不卸载，用覆盖更新（`setup update`）。

换了电脑（没有原来的两份配置）时，先从管理台取回配置。前提：管理台能打开，线上做过 `config backup`，并且知道**初始**管理员密码。

```bash
./bin/moox-cli config restore
```

取回的是 `moox.toml` 与 `moox.secrets.toml` 两份。如实告诉用户：

- `setup validate`、`setup status`、`setup logs`、`setup firewall`、`config backup` 这类只靠 SSH、云密钥或管理台登录的命令，取回配置后就能用（SSH 密钥路径要重新核对）。
- 经网关调用的命令（`doctor`、`collector`、`factor`、`trade` 等）和其余主机的 `setup deploy` 要这台电脑有操作员身份。新电脑上按顺序做两步（都要先问用户）：
  1. `./bin/moox-cli setup operator --register`：在这台电脑生成 `moox-cli` 的签名私钥，并经 SSH 把公钥登记到控制机。它**替换**原来登记的公钥，原来那台电脑随即不能再用，两台电脑不能同时用。约 10 秒后各主机网关生效。
  2. `./bin/moox-cli setup deploy --host control`：取回控制机的根证书（之后部署别的主机、连因子机都要用）；它重新执行 Admin 的初始化（只建不改），没有变化的组件不重启。
  这两步还没有在真机上验证过，做之前告诉用户。
- 这两步和 `setup status` 都要先解开 `moox.secrets.toml`：用 `config restore` 取回的配置已经按这台电脑的密钥加密，直接可用；两份配置是从原电脑直接拷过来的，还要把解密密钥带过来（在原电脑的系统钥匙串里，没有钥匙串的机器在 `~/.config/moox/config.key`），否则报“这台电脑上没有解密秘密文件的密钥”，改用 `config restore`。
- 身份目录（默认 `~/.config/moox`）的路径里不能有符号链接：报 `private material directory must be physical` 时按提示用 `--operator-dir` 指到实际路径。
- 不要手动拷贝或恢复 `~/.config/moox` 下的身份文件。

## 升级到新版本

下载新版本的产物包与 CLI（同一版本），把原来的 `moox.toml` 和 `moox.secrets.toml` 放进新目录（或 `config restore` 取回），然后覆盖更新：

```bash
./bin/moox-cli setup validate                     # 看各主机现在的版本与组件
./bin/moox-cli setup update                       # 预览，给用户确认
./bin/moox-cli setup update --yes
./bin/moox-cli setup status
```

- 只想更新一台主机时用 `setup deploy --host <主机>`（控制机先）。
- 每台主机只重启二进制或配置有变化的组件；整台部署总是带着主机网关，服务目录变化不需要另外处理。
- 升级只动程序和配置，数据目录（`data/`）不碰。配置每次部署都从这个版本的模板重新渲染，手工调过的值要写在 `[overrides]` 里才会保留。
- 回滚：用旧版本的产物包目录和 CLI 再部署一遍。数据库结构只增不减的版本可以这样回退；发布说明里写了“不可回退”的版本不行。
- 维护者从源码目录部署见 [`source-deploy.md`](source-deploy.md)。

## 常见问题

| 现象 | 含义 | 处理 |
| --- | --- | --- |
| `linger_required`、`manual_steps_required` | 目标机的部署用户没开 linger | 把报错里的 `sudo loginctl enable-linger <用户>` 交给用户在那台机器上执行，再重跑 |
| `target_prepare_failed` | 目标机上建不了部署目录（上级目录不可写、部署根不属于 SSH 用户或是符号链接） | 把报错里的 `sudo install -d -o <用户> <部署根>` 交给用户执行 |
| `ssh_unreachable`，且是因子机 | 这台电脑和因子机不在同一网络 | 换到同一网络后重新运行 |
| `install_failed` | 目标机上的安装脚本失败，后面是最后一行原因（启动某个服务失败、Storage 元数据库初始化失败、内置对象存储没有就绪等） | 按原因用 `setup logs --host <主机> --component <组件> --journal` 看启动输出；修好后重跑 |
| `not_ready` | 这次装的组件到时限还没就绪，括号里是原因（`readyz 503`、健康端口连不上、服务状态） | 用 `setup logs` 看日志。多半是它依赖的组件还没装（按安装顺序补上）或配置有误；什么都没变就不要反复重试 |
| `control_admin_failed` | 控制机上的 `moox-admin-cli` 执行失败（初始化 Admin、导出身份材料） | 看报错的最后一行；控制机还没装过时先 `setup deploy --host <控制机>` |
| `control_not_ready` | 这台电脑没有记下控制机的根证书指纹 | 先 `setup deploy --host <控制机>`；换了电脑见“保留数据与换电脑” |
| `operator_key_missing` | 这台电脑没有 `moox-cli` 的签名私钥 | 全新安装：`setup operator`（`setup install` 会自己做）；已经装好的系统换了电脑：`setup operator --register` |
| `operator_key_mismatch` | 控制机上登记的 `moox-cli` 公钥不是这台电脑的 | 问用户是不是换了电脑；是的话 `setup operator --register`（原电脑随即不能用） |
| `rotate_unsupported` | 对控制机用了 `--rotate-host-key` | 控制机的签名密钥不单独换，按报错的做法 |
| `operator_trust_failed` | 这台电脑上记着另一套部署的控制机根证书（线上清空重装过，CA 变了） | 确认线上确实是重装的之后，按报错删掉 `~/.config/moox` 下列出的两个文件再重跑 |
| `identity_invalid` | 从控制机取回的身份材料校验没通过（主机地址、组件和 Admin 里登记的不一致，或 CA 对不上） | 核对 `moox.toml` 里这台主机的地址和 `[deployments]`，`setup sync-deployments --host <主机> --yes` 后重跑 |
| `component_not_on_host` | `--component` 写的组件不在这台主机上 | 按报错列出的组件改 |
| `binary_dir_required`、`binary_missing` | 在源码目录里部署，没给或缺编译好的 Linux 二进制 | 用户安装应在产物包目录里运行；维护者见 [`source-deploy.md`](source-deploy.md) |
| `version_invalid` | 目标机的 `version.json` 损坏 | 告诉用户；确认后删掉它重跑（会把全部组件当成有变化重启一遍） |
| `config_invalid: [overrides…]` | `[overrides]` 写错：组件没有这个配置文件，或键路径不存在 | 按报错改 `moox.toml`；键路径以渲染出的配置为准（目标机 `<部署根>/config/<组件>/`） |
| `install_step_failed` | `setup install` 的某一步失败，后面是那一步的原因和单独重跑它的命令 | 按原因处理后重跑同一条 `setup install` |
| `update_step_failed` | `setup update` 的某一步失败，后面是那一步的原因和单独重跑它的命令 | 按原因处理后重跑同一条 `setup update --yes` |
| `update_not_installed` | 目标机都没装过，没有可以覆盖更新的部署 | 走新安装 `setup install` |
| `start_prerequisite_missing` | crypto 缺 collector-stream 或出口代理，其他空间没配 `[scf_plan]` | 告诉用户缺什么，按 `setup validate` 处理 |
| `no_collection_tasks` | 这个空间没有任何 K 线任务，`--space-id` 写错或种子没写入（任务都停用时验收跳过，不报错） | 核对空间，看 `collector task list`；不要手工改数据库 |
| `verify_read_failed` | 到超时还读不到验收数据，组件没起来或连接不通 | 看错误里的组件，用 `setup logs` 查 |
| `browser_trust_manual` | 信任要管理员授权，没有终端或安装失败 | 把错误里的命令交给用户在自己的终端运行，不影响继续安装 |
| `browser_ca_trust_failed` | 从管理台服务器取不回 MooX CA 根证书 | 检查 SSH 和控制机是否已装好（根证书在 `<部署根>/secrets/moox-ca.crt`），用 `setup logs --host <控制机> --component admin` 查 |
| `component_operation_failed` | `setup restart`、`stop`、`start-component` 失败 | 看错误原因；用 `setup logs --journal` 查 |
| `collection_not_started` | 还没运行 `setup start`（任务默认停用）；或到超时还没有采集计划、collector-stream 不在线 | 看 `collector status` 给出的原因，不要删任务、不要手工改数据 |
| `monitor_unhealthy` | 到超时 Monitor 还有 critical 问题，报错里列出了主机和组件 | 看 `doctor alerts`，再用 `doctor diagnose --node <主机>` 经 SSH 看那台主机上组件的本机 `/readyz`（健康端口只听本机，防火墙不放行它）。组件都正常但告警不解除时，检查告警通知渠道：恢复通知发不出去时告警会保持触发 |
| `monitor_unreadable` | 到超时还读不到 Monitor | 用 `setup logs --host <控制机> --component monitor` 查 Monitor |
| `kline_not_arrived` | 到超时还没有 K 线，或缺失超过 5% | 看 `collector status` 和 `setup logs --host <海外机> --component collector-stream` |
| `factor_not_advancing` | 启用的 1m 因子任务没有前进，或最近一期失败 | 看 `factor status` 和 `setup logs --host <因子机> --component factor-engine` |
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
