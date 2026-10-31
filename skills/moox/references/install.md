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

- **本机**：macOS、Linux 或 Windows，能 SSH 到上面的服务器（Windows 上部分辅助脚本需要 Git Bash 或 WSL）。
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
if [[ "$SRC" == cnb ]]; then BASE=https://cnb.cool/mooyang-code/moox/-/releases/download/${VER}; else BASE=https://github.com/mooyang-code/moox/releases/download/${VER}; fi
curl -fLO ${BASE}/moox-bundle_${VER}_linux_amd64.tar.gz
curl -fLO ${BASE}/moox-cli_${VER}_${OS}.tar.gz       # Windows 是 .zip
curl -fLO ${BASE}/SHA256SUMS
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

- Agent **不打开、不填写、不读取**这个页面和 `moox.toml`；页面地址没能自动打开时，把命令打印的地址原样转给用户。
- 之后要看或改配置用 `./bin/moox-cli config edit`（同样由用户在浏览器里操作）。
- 免密登录开通后，Agent 就能通过 `moox-cli` 登录这些服务器完成后续所有步骤。

## 3. 部署

每一步成功的标志是输出里的 `*-ready`；失败先看输出的原因，不要盲目重试，重试要用同样的参数。耗时较长的命令放后台运行。
`<主机ID>` 来自 `config init` 的输出：`control`（管理台）、`storage`、`overseas`、`factor`。

```bash
./bin/moox-cli setup validate                                   # 校验配置与 SSH 连通
./bin/moox-cli setup firewall                                   # 放行腾讯云防火墙端口
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

## 4. 初始化

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

## 5. 验收与交付

1. `./bin/moox-cli doctor alerts --format text`：新对象一开始会有“尚未上报 / 尚无成功运行”，等一两个周期再看；长时间不消的按 [`monitor-attention.md`](monitor-attention.md) 排查。
2. `./bin/moox-cli collector status --space-id crypto`：各数据集 `level: ok`。
3. 告诉用户管理台地址 `https://<管理台地址>:9527`、管理员用户名（密码在用户自己填的配置里），以及默认已经在跑的内容：
   币安现货与合约 1m K 线采集、两个 1m 因子任务。默认展示策略的创建见 [`strategy-dsl.md`](strategy-dsl.md)。
4. 列出还没做、需要用户决定的事：长历史导入（[`initial-import.md`](initial-import.md)）、自定义因子（[`factor-dsl.md`](factor-dsl.md)）、自定义策略。

## 升级到新版本

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
| `host_key_unknown` | 某台服务器的主机指纹没确认（例如后来新加的主机）：请用户经可信渠道（腾讯云控制台登录该机执行 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`）核对指纹后，执行 `setup trust-host --host <主机ID> --fingerprint <指纹>` |
| `ssh_auth_failed` | 免密登录没开通或公钥被删：让用户在终端执行 `ssh-copy-id -i ~/.ssh/moox_ed25519.pub -p <端口> <用户>@<地址>` |
| 腾讯云接口报权限不足 | 核对子用户的 6 个预设策略 |
| `setup init` 报某个因子任务的源数据集不存在 | 该任务指向还没创建的采集数据集，先从 `moox.toml` 去掉或先建采集任务 |
| 产物包只有 linux/amd64 | ARM 服务器暂不支持 |
