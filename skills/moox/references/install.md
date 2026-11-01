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
VER=v0.4.3
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
| `resume` | 这台电脑上次的首次安装没做完，配置和版本都没变 | 继续第 4 节，不询问清空 |
| `existing` | 已有部署，这台电脑没有匹配的状态（新电脑都是这种）；或者上次没装完的状态是另一份配置、另一个版本生成的，`reasons` 写了原配置的位置 | 询问是否彻底清空；要接着装就换回原配置和原版本 |
| `upgrade` | 已有部署，这台电脑就是当初部署的电脑 | 询问是否彻底清空；不清空就按“升级到新版本”。如果是本次安装在 `bootstrap --stage control` 之后中断，不用询问，从中断的步骤继续 |
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
> `<existing 时：不清空就保留全部数据，这次不发布新程序，改走恢复配置；upgrade 时：不清空就保留全部数据，只升级程序>`

- **用户同意**：执行下面的命令，清除和后面的部署都算在这次确认内，中途不再确认。

  ```bash
  ./bin/moox-cli setup wipe --yes --expect <预览里的 plan.digest>
  ```

  - 清除后 `route` 是 `fresh`，走第 4 节。
  - 预览里 `unknown_entries` 不为空时，把这些条目告诉用户：它们在部署目录里，也会被删除。
  - 预览里 `skipped_dirs` 不为空时也告诉用户：这些目录看不到 MooX 的文件，不会清除。
  - 中途失败后再运行一次会接着清：重新取预览，范围变小不用再问，范围变大要重新询问。
  - `plan.trade` 是 `live` 或 `unreadable` 时，命令会拒绝，要重新询问用户，明确同意后才加 `--accept-trade-risk`。
- **用户不同意**：`existing` 时把新的 `moox.toml` 改名（`config restore` 不覆盖已有文件），按“保留数据”处理；`upgrade` 时按“升级到新版本”处理。

## 4. 部署

**顺序由 CLI 给出，不要自己推算。** `setup validate` 输出的 `deployment.next_steps` 是按当前拓扑和已完成的步骤算出的命令列表（有没有海外机、因子机，管理台用什么证书，上次做到哪一步都算在内），把整份列表放进部署计划给用户确认一次，然后从上到下逐条运行。

- 每条成功的标志是输出里的 `*-ready` 或 `"status":"ready"`。耗时较长的命令放后台运行。
- 中断或换一次会话后，重新运行 `setup validate`，从新的 `next_steps` 继续；已做完的步骤不会再列出来。`resume` 时不要从头开始。
- `next_steps` 为空、`reasons` 也为空且 `route` 是 `upgrade`，说明安装已完成。如果 `config backup` 没跑过，补跑一次。`reasons` 不为空时把缺的步骤告诉用户：安装中途换了配置或版本的，换回原配置和原版本后补上；装好后有意新增主机或组件的，按 [`host-registry.md`](host-registry.md) 部署，不要清空。
- 输出里有 `warnings` 时先告诉用户。`disk_low` 不拦 `setup wipe`，但部署命令会拒绝，要先腾出空间或换大磁盘。

大致顺序，以 `next_steps` 为准：防火墙 → admin → 其他主机的主机网关 → EventBus、Storage → `setup init --stage storage`（业务空间和元数据）→ Access、出口代理 → 管理台 → 信任证书（内部证书时）→ `setup init` → 海外采集和交易 → 因子机 → 配置备份。

元数据必须在管理台之前激活：Monitor 要等系统数据集就绪才算启动成功，`bootstrap --stage control` 没有这一步会直接拒绝。

## 5. 初始化

启用 SCF 采集（`[scf_plan] enabled = true`）时，先按 `references/private-network.md` 为每个采集地域配置同地域的 Access 主机与 VPC。

`setup init --stage storage` 只写业务空间、登记存储节点、导入并激活元数据。不带 `--stage` 的 `setup init` 在管理台装好后运行，依次完成防火墙核对、浏览器证书检查、业务空间、云账户、日志服务、元数据核对、导入 `moox.toml` 里的默认因子任务，已经做过的不会重复写入。

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

| 现象 | 含义 | 处理 |
| --- | --- | --- |
| `route=blocked` | 属主或权限不符、端口被占用、有部署目录之外的旧进程，或上一次部署操作还在运行 | 按 `reasons` 处理，不要继续安装 |
| `ssh_unreachable`，且是因子机 | 这台电脑和因子机不在同一网络 | 换到同一网络后重新运行 |
| `ssh_disconnected` | SSH 连接断开，没有拿到结果 | 只有这个错误可以重跑：先 `setup validate` 确认没有“上一次部署操作还在运行”，再用同样的参数重跑 |
| `target_not_ready` | 组件在时限内没就绪，已被停掉；括号里的 `not_ready` 列出没就绪的项或缺少的服务 | 按缺的项找前置步骤，用 `setup logs` 看日志。什么都没变就不要重试 |
| `target_process_exited` | 组件启动后自己退出 | 用 `setup logs` 看日志，把事实告诉用户 |
| `target_port_in_use` | 端口被其他程序占用，括号里有端口和程序名 | 问用户能否停掉那个程序，不要手动杀进程。部署目录里的残留进程 CLI 会自动清理，不会报这个错 |
| `target_subset_installed`、`target_identity_differs`、`target_binding_missing`、`target_partial_install`、`target_directory_invalid` | 目标机的状态和这次操作不符，比如机器不是空机 | 不要重试，也不要手动上机；回到 `setup validate` 按 `route` 处理 |
| `target_rejected` | 目标机失败，没有已知原因 | 不要重试；用 `setup logs` 看相关组件的日志。还看不出来时，征得用户同意后单次设置 `MOOX_SETUP_DEBUG_HELPER=1` 重跑一次看原因（输出可能含路径，不要转发密钥） |
| `deployment_incomplete` | 配置缺组件，部署到一半会卡住 | 按报错处理：能补的运行 `config migrate --add-deployment`；要重新生成的先把旧 `moox.toml` 改名，再 `config init`。然后 `setup validate` |
| `disk_low` | 目标机磁盘不够（失败尝试的残留已算入总量，下一次部署会自动清掉；但上传安装包至少要 1 GB 真实可用空间） | 告诉用户腾出空间或换大磁盘 |
| `admin_not_ready`、`host_not_ready` | admin 或这台主机的主机服务还没完成，或这台电脑没有部署状态 | 回到 `setup validate`，按 `next_steps` 走 |
| `logs_unavailable` | 读不到目标机的日志 | 检查 SSH 连接；读不到就向用户说明，不要上机 |
| `init_storage_required` | 系统元数据还没激活 | 先运行 `setup init --stage storage` |
| `deployment_not_fresh` | 目标机不是空机，`bootstrap` 没有动手 | 回到第 3 节 |
| `…operator bootstrap pin`、`full control requires…` | 这台电脑没有部署状态 | 回到 `setup validate`，不要手动恢复 |
| `wipe_scope_changed` | 清除范围和预览不一致，没有清除任何主机 | 重新预览并确认 |
| `wipe_failed`、`wipe_interrupted`、`wipe_incomplete` | 清除没做完 | 重新运行 `setup wipe` 接着清，清完才能安装 |
| `wipe_outside_processes` | 部署目录之外还有 MooX 进程，不会被杀 | 告诉用户进程路径，由用户决定 |
| `delete_target_rejected` | 部署目录是系统目录或家目录，或看不到 MooX 标记 | 核对 `moox.toml` 的 `[paths]`，不要手工删除 |
| `bundle_incomplete` | 产物包不完整，没有清除任何主机 | 重新下载产物包 |
| `private material directory ownership or permissions are invalid` | 本机私有目录权限不对（CLI 会自动收紧属于自己的目录） | 按报错里的路径和命令处理 |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | 系统 `known_hosts` 有旧记录，或机器确实换了 | 先跑 `setup validate`，通过说明 MooX 记录的指纹没变；不自己删记录，向用户说明 |
| `host_key_unknown` | 某台服务器的主机指纹没确认（例如后来新加的主机） | 请用户经可信渠道（腾讯云控制台登录该机执行 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`）核对指纹后，执行 `setup trust-host --host <主机ID> --fingerprint <指纹>` |
| `ssh_auth_failed` | 免密登录没开通或公钥被删 | 让用户在终端执行 `ssh-copy-id -i ~/.ssh/moox_ed25519.pub -p <端口> <用户>@<地址>` |
| 腾讯云接口报权限不足 | 子用户缺策略 | 核对子用户的 6 个预设策略 |
| `setup init` 报某个因子任务的源数据集不存在 | 该任务指向还没创建的采集数据集 | 先从 `moox.toml` 去掉或先建采集任务 |
| 产物包只有 linux/amd64 | ARM 服务器暂不支持 | 换 x86_64 服务器 |
