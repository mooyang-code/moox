# 安装、部署与初始化（基于发布产物）

外部用户第一次安装 MooX 时按本文执行：下载发布产物，不需要在本机编译。
维护者自己清空重装生产按 [`fresh-deploy.md`](fresh-deploy.md)，升级已安装的单元按 [`native-release.md`](native-release.md)。

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
- **服务器**（Linux x86_64，SSH 能登录，有 sudo 或 `/data` 可写；除因子机外，必须是上面腾讯云账号下的轻量应用服务器或 CVM，`config init` 按 IP 识别地域与规格）：

  | 角色 | 必需 | 建议配置 | 跑什么 |
  | --- | --- | --- | --- |
  | 存储机 | 是 | 4 核 16G、100G 磁盘 | Storage、EventBus、Access |
  | 管理台 | 是，可与存储机或海外机共用 | 2 核 4G | Admin、管理台、Monitor、Collector、因子管理、策略 |
  | 海外机 | 采集加密货币行情时需要（建议香港） | 2 核 4G | WS 行情推送、出口代理、交易执行 |
  | 因子机 | 可选，可以是家里或局域网的 Linux 机器 | 8 核 16G | 因子引擎、归档 Worker（systemd 用户服务） |

- **本机**：macOS 或 Linux，能 SSH 到上面的服务器。**Windows 现在只能使用只读命令，部署走不通**，用户用 Windows 时提前说明。
- **有没有旧部署**（在填配置之前问）：“目标机器上有旧部署吗？要保留数据吗？”
  - 要保留：不走 `config init`，按下面“保留数据”一节处理。
  - 不保留或不清楚：继续往下，第 3 节会实际检测。
- **告警渠道**：企业微信或飞书群机器人的 Webhook 地址（可选，没有可以部署后再在管理台添加）。

## 1. 下载发布产物

在一个新目录里下载两样东西：部署产物包（含全部服务器程序）和本机系统对应的 `moox-cli`。版本以发布页的最新版本为准（下面用 `VER`）。

| 文件 | 说明 |
| --- | --- |
| `moox-bundle_${VER}_linux_amd64.tar.gz` | 部署产物包：仓库文件快照 + 预编译的 linux/amd64 服务器程序 |
| `moox-cli_${VER}_darwin_arm64.tar.gz` / `darwin_amd64` / `linux_amd64` / `linux_arm64` / `windows_amd64.zip` | 本机命令行 |
| `SHA256SUMS` | 校验和 |

下载地址由上面测速选出的 `SRC` 决定：

- GitHub：`https://github.com/mooyang-code/moox/releases/download/${VER}/<文件名>`
- CNB：`https://cnb.cool/mooyang-code/moox/-/releases/download/${VER}/<文件名>`

```bash
VER=v0.4.1
OS=darwin_arm64        # 按本机选择：darwin_arm64 / darwin_amd64 / linux_amd64 / linux_arm64 / windows_amd64
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

之后**所有命令都在 `moox-${VER}` 目录里执行**：`moox.toml` 写在这里，`setup` 命令从这里读取预编译程序（`bin/linux-amd64`）和公共模板。
产物包里有 `moox-bundle.json`，CLI 据此识别产物包，不需要 git、Go、Docker 或 Node。

## 2. 生成配置（用户在自己的浏览器里填写）

```bash
./bin/moox-cli config init
```

命令在本机起一个一次性页面并打开系统浏览器，用户按页面分步填写：

1. 腾讯云密钥（先验证）；
2. 各台服务器的 IP、SSH 端口和用户名（地域、规格按 IP 自动识别）；
3. **免密登录**：页面在本机生成 MooX 专用密钥 `~/.ssh/moox_ed25519`，逐台确认主机指纹，用户输入一次 SSH 密码装上公钥（密码不保存），
   密码或用户名填错时，页面直接让用户重新填写用户名和密码；也可以按页面给出的 `ssh-copy-id` 命令自己在终端执行；因子机在免密开通后自动做环境检测；
4. 管理员账号与告警渠道（机器人 Webhook 可以先不填，部署完成后在管理台“监控 › 告警渠道”添加；添加之前出了问题不会通知到群里，要提醒用户补上）；
5. 确认后写出加密的 `moox.toml`（解密密钥在系统钥匙串）。

规则：

- 命令结束后立即告诉用户：配置已保存，页面可以关，之后不用重填。
- Agent **不打开、不填写、不读取**这个页面和 `moox.toml`；页面地址没能自动打开时，把命令打印的地址原样转给用户。
- 之后要看或改配置用 `./bin/moox-cli config edit`（同样由用户在浏览器里操作）。
- 免密登录开通后，Agent 就能通过 `moox-cli` 登录这些服务器完成后续所有步骤。

## 3. 判断走哪条路

```bash
./bin/moox-cli setup validate
```

输出的 `deployment.route` 说明目标机器的实际情况，后面按它走：

| route | 含义 | 怎么做 |
| --- | --- | --- |
| `fresh` | 全是空机 | 不用询问清空，确认部署计划后走第 4 节 |
| `resume` | 这台电脑上次的首次安装没做完 | 继续第 4 节，不询问清空 |
| `existing` | 已有部署，这台电脑没有匹配的状态（新电脑都是这种） | 询问是否彻底清空 |
| `upgrade` | 已有部署，这台电脑就是当初部署的电脑 | 询问是否彻底清空；不清空就按“升级到新版本” |
| `blocked` | 有阻塞项，`reasons` 写了原因 | 说明原因后停下，不要继续 |

`existing` 或 `upgrade` 时先取预览（不会改动任何东西）：

```bash
./bin/moox-cli setup wipe
```

再这样问用户，不能只问“要覆盖吗”。`<>` 里的内容从预览取：主机与版本来自 `plan.hosts`，配置之外的旧机器来自 `plan.other_hosts`（`plan.registry_unread` 为 true 时说明没能读取）：

> 检测到这些机器上已经部署了 MooX：`<主机、IP、版本>`。
> 要彻底清空吗？清空会删除这些机器上全部旧程序和旧数据（K 线、因子、策略、任务、归档、历史导入包、交易记录、管理台账号与操作记录、管理台里保存的交易所密钥等、配置备份），然后用你刚填的初始化设置重新发布 `<版本>`，并在腾讯云防火墙放行 `<端口摘要>`。删除后无法恢复，配置备份也会一起删除。
> `<配置之外还有这些旧机器：……，不会被清理，上面的旧程序会继续运行>`
> 如果旧部署启用过云函数采集，请你到腾讯云控制台删除 `moox-fetch-plan-` 开头的函数；不删会继续运行并产生费用，以后启用云函数时同名函数也会创建失败。
> 不清空就保留全部数据，这次不发布新程序，改走恢复配置。

- **用户同意**：执行下面的命令，清除和后面的部署都算在这次确认内，中途不再确认。

  ```bash
  ./bin/moox-cli setup wipe --yes --expect <预览里的 plan.digest>
  ```

  - 清除后 `route` 是 `fresh`，走第 4 节。
  - 预览里 `unknown_entries` 不为空时，把这些条目告诉用户：它们在部署目录里，也会被删除。
  - 中途失败后再运行一次会接着清：重新取预览，范围变小不用再问，范围变大要重新询问。
  - `plan.trade` 是 `live` 或 `unreadable` 时，命令会拒绝。这是唯一要重新询问的情况，用户明确同意后才加 `--accept-trade-risk`。
- **用户不同意**：把新的 `moox.toml` 改名（`config restore` 不覆盖已有文件），按“保留数据”处理。

## 4. 部署

每一步成功的标志是输出里的 `*-ready`；失败先看输出的原因，不要盲目重试，重试要用同样的参数。耗时较长的命令放后台运行。
`<主机ID>` 来自 `config init` 的输出：`control`（管理台）、`storage`、`overseas`、`factor`。

```bash
./bin/moox-cli setup firewall --yes                             # 放行腾讯云防火墙端口（内容在部署计划里给用户确认过）
./bin/moox-cli setup bootstrap --stage core                     # 管理台机：Admin、主机网关、主机代理 → core-ready
./bin/moox-cli setup deploy-host --host storage                 # 每台非管理台主机各一次：storage、overseas、factor → host-ready
./bin/moox-cli setup deploy-unit --host storage --profile eventbus
./bin/moox-cli setup deploy-unit --host storage --profile storage
./bin/moox-cli setup deploy-unit --host storage --profile access
./bin/moox-cli setup deploy-unit --host overseas --profile access          # 有海外机时
./bin/moox-cli setup deploy-unit --host overseas --profile egress-proxy    # 有海外机时
./bin/moox-cli setup bootstrap --stage control --create-proxy-ca           # 管理台全部服务 → control-ready
```

- `--create-proxy-ca` 只在管理台用内部证书（没有域名，`tls_mode` 为 internal 或 auto 判定为 internal）时的**首次**安装需要。
- 每个 `deploy-unit` 只装 `moox.toml` 的 `[deployments]` 里登记在该主机上的组件；管理台与哪台共用、有没有海外机，以 `config init` 的结果为准。

## 5. 初始化

```bash
./bin/moox-cli setup trust-browser                              # 内部证书时：让本机浏览器信任管理台证书
./bin/moox-cli setup init --config-dir ./config/setup --storage-host storage
./bin/moox-cli setup deploy-unit --host overseas --profile collector-stream   # 有海外机时；必须在 control 就绪之后
./bin/moox-cli setup deploy-unit --host overseas --profile trade              # 有海外机时
./bin/moox-cli setup sync-deployments --host factor                           # 有因子机时
./bin/moox-cli setup deploy-unit --host factor --profile factor-engine
./bin/moox-cli setup deploy-unit --host factor --profile archive-worker
./bin/moox-cli config backup                                    # 把加密配置备份到自己的管理台
```

启用 SCF 采集（`[scf_plan] enabled = true`）时，先按 `references/private-network.md` 为每个采集地域配置同地域的 Access 主机与 VPC。

`setup init` 依次完成：防火墙核对、浏览器证书检查、业务空间、云账户、日志服务、登记存储节点、导入元数据并激活数据集、导入 `moox.toml` 里的默认因子任务。

macOS 上用内部证书时，`trust-browser` 会请用户在系统弹窗里授权；不要替用户清理钥匙串里的这张证书。授权弹不出来时，把下面的命令给用户自己执行：

```bash
security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db ~/.moox/certs/moox-caddy-root-<管理台地址>.crt
```

## 6. 验收与交付

1. `./bin/moox-cli doctor alerts --format text`：新对象一开始会有“尚未上报 / 尚无成功运行”，等一两个周期再看；长时间不消的按 [`monitor-attention.md`](monitor-attention.md) 排查。
2. `./bin/moox-cli collector status --space-id crypto`：各数据集 `level: ok`。
3. 告诉用户管理台地址 `https://<管理台地址>:9527`、管理员用户名（密码在用户自己填的配置里），以及默认已经在跑的内容：
   币安现货与合约 1m K 线采集、两个 1m 因子任务。默认展示策略的创建见 [`strategy-dsl.md`](strategy-dsl.md)。
4. 列出还没做、需要用户决定的事：长历史导入（[`initial-import.md`](initial-import.md)）、自定义因子（[`factor-dsl.md`](factor-dsl.md)）、自定义策略。

## 保留数据

新电脑上要保留旧部署的数据时，走恢复配置。前提：旧管理台能打开，旧部署做过 `config backup`，并且知道**初始**管理员密码。

```bash
./bin/moox-cli config restore
```

如实告诉用户取回之后能做什么：

- 取回的只有配置。`setup validate`、`setup firewall`、`config backup` 这类只靠 SSH、云密钥或管理台登录的命令，有密钥就能用（SSH 密钥路径要重新核对）。
- `doctor`、`setup deploy-*` 依赖操作员身份和本机状态，**这台电脑上用不了**。
- 要升级时，优先回到当初部署的电脑操作。
- 原电脑已经不可用时，告诉用户现在不支持在新电脑上接管旧部署，后续版本会提供；或者选择彻底清空。不要用 `native-release.sh` 绕过，也不要手动恢复身份。

## 升级到新版本

这条路要求这台电脑就是当初部署的电脑（本机状态目录还在），新电脑上走“保留数据”一节。升级前比较两版 `catalog.yaml`，变了要整体发布。

下载新版本的产物包与 CLI，在新目录里放入原来的 `moox.toml`（或 `config restore` 从管理台取回），然后：

```bash
./bin/moox-cli setup release build --release <版本ID>          # 用产物包里的程序生成版本制品
./bin/moox-cli setup bootstrap --stage control --release <版本ID>
./bin/moox-cli setup deploy-host --host <主机ID> --release <版本ID>      # 其余主机与单元同理，按 native-release.md 的顺序
```

服务目录变化时必须整体发布，顺序与陷阱见 [`native-release.md`](native-release.md)。配置文件不随二进制更新。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `route=blocked` | 属主或权限不符、端口被占用，或有部署目录之外的旧进程 | 按 `reasons` 处理，不要继续安装 |
| `ssh_unreachable`，且是因子机 | 这台电脑和因子机不在同一网络 | 换到同一网络后重新运行 |
| `bootstrap observation failed` 等“retry”提示 | SSH 连接断开 | 用同样的参数重试一次 |
| `target_*`、`target_rejected` 开头的错误 | 目标机明确拒绝，比如机器不是空机 | 不要重试，也不要手动上机；回到 `setup validate` |
| `deployment_not_fresh` | 目标机不是空机，`bootstrap` 没有动手 | 回到第 3 节 |
| `unit deployment requires the original core state directory…`、`…operator bootstrap pin`、`full control requires…` | 这台电脑没有部署状态 | 回到 `setup validate`，不要手动恢复 |
| `wipe_scope_changed` | 清除范围和预览不一致，没有清除任何主机 | 重新预览并确认 |
| `wipe_failed`、`wipe_interrupted`、`wipe_incomplete` | 清除没做完 | 重新运行 `setup wipe` 接着清，清完才能安装 |
| `wipe_outside_processes` | 部署目录之外还有 MooX 进程，不会被杀 | 告诉用户进程路径，由用户决定 |
| `delete_target_rejected` | 部署目录是系统目录或家目录，或看不到 MooX 标记 | 核对 `moox.toml` 的 `[paths]`，不要手工删除 |
| `bundle_incomplete` | 产物包不完整，没有清除任何主机 | 重新下载产物包 |
| `private material directory ownership or permissions are invalid` | 本机私有目录权限不对（CLI 会自动收紧属于自己的目录） | 按报错里的路径和命令处理 |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | 系统 `known_hosts` 有旧记录，或机器确实换了 | 先跑 `setup validate`，通过说明 MooX 记录的指纹没变；不自己删记录，向用户说明 |
| `host_key_unknown` | 某台服务器的主机指纹没确认（例如后来新加的主机）：请用户经可信渠道（腾讯云控制台登录该机执行 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`）核对指纹后，执行 `setup trust-host --host <主机ID> --fingerprint <指纹>` |
| `ssh_auth_failed` | 免密登录没开通或公钥被删：让用户在终端执行 `ssh-copy-id -i ~/.ssh/moox_ed25519.pub -p <端口> <用户>@<地址>` |
| 腾讯云接口报权限不足 | 核对子用户的 6 个预设策略 |
| `setup init` 报某个因子任务的源数据集不存在 | 该任务指向还没创建的采集数据集，先从 `moox.toml` 去掉或先建采集任务 |
| 产物包只有 linux/amd64 | ARM 服务器暂不支持 |
