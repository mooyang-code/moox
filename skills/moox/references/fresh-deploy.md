# 全新部署（清空生产后按默认配置重装）

适用：用户明确说“全新部署 / 清空重装 / 数据全部可以删、配置重建”，并要求用仓库默认配置初始化。
这是破坏性运维：**必须先得到用户的明确确认**，并复述要清空的主机和数据。升级已安装的单元不走这里，见 [`native-release.md`](native-release.md)。
本文是维护者在源码仓库里重装自己生产环境的记录（四台主机、从源码构建）；外部用户第一次安装按 [`install.md`](install.md)，用发布产物，不需要构建。

整体顺序：核对前提 → 备份 → 构建制品 → 停机清空 → 本机状态清理 → 安装（core → 主机 → 单元 → control）→ 默认初始化 → 验收。
耗时的命令都放后台运行，用日志文件加 Monitor 盯结果（一次完整安装约 20 分钟，另加 5 分钟构建）。

## 0. 前提与约定

- 命令在**主仓库根目录**执行：`moox.toml` 必须放在“仓库根”，CLI 不接受别的位置。代码在独立 worktree 时，仍在主仓库根目录运行，用 worktree 里构建的 CLI，
  并用 `--source-dir <worktree>` 指向源码；种子文件用绝对路径（`--seed`、`--config-dir`）。不要用旧的 `./bin/moox-cli`（种子格式可能已变）。
- 跑 CLI 前 `env -u HTTP_PROXY -u HTTPS_PROXY`（本机代理会挡住到主机的直连）。
- `moox.toml` 是加密的，Agent 只通过 CLI 读写，不直接读文件，也不打印密钥。改它之前先备份：`cp -p moox.toml moox.toml.bak-<说明>-<时间>`。
- 各主机地址在 `~/.moox-release.env`（`MOOX_RELEASE_HOST_*`、`MOOX_RELEASE_BUILDER`），SSH 只用公钥。
- 因子机（factor）同时是编译机时，不要碰它上面的构建目录（`~/moox-web` 等）。

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

- **前端必须是真实构建**：Mac 上 `vue-tsc`、`vite` 不可靠。在编译机（因子机，`~/moox-web`）构建：
  `rsync -a --delete --exclude node_modules --exclude dist --exclude .git web/ <编译机>:moox-web/`，再在编译机 `./node_modules/.bin/vite build --mode production`，
  把 `dist` rsync 回 `$WT/web/dist`，然后 `cd web-host && go run github.com/rakyll/statik@v0.1.7 -src=../web/dist -dest=./internal -f`，**提交 `web-host/internal/statik/statik.go`**。
  `web/dist` 缺少 `static/js` 时 `make statik` 会拒绝执行（否则会把占位页嵌进 web-host，管理台打不开）。
  之后 bootstrap 会重新生成 statik.go 造成无意义 diff，`git checkout` 掉即可。
- 纯 Go 组件由 bootstrap / deploy-unit 在本机交叉编译。想提前确认能编译：逐个 `go.work` 模块 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`，
  **跑完立刻检查并删除模块目录里生成的二进制**（单个 main 的模块会在目录里留文件，如 `modules/replay/replay`、`tools/moox-mcp/moox-mcp`；`git add -A` 会把它们提交）。
- 改动已提交并推送，CI 通过；线上二进制必须能对应到一个提交。

## 3. 停机并清空

对每台主机的操作顺序：因子机 → 海外机 → 控制机 → 存储机（因子引擎往 Storage 写，EventBus 最后停）。每台：

```bash
# 1) 先去掉定时看护，否则 watchdog 每分钟会把进程拉起来
crontab -l > ~/crontab.bak-<时间>; crontab -l | grep -v moox-watchdog | crontab - || crontab -r
# 2) 因子机：停 systemd 用户服务
systemctl --user stop moox-factor-engine.service moox-archive-worker.service
systemctl --user disable moox-factor-engine.service moox-archive-worker.service
# 3) 各单元用自带脚本停（不要自己 flock 维护锁，子进程会继承它）
for u in /data/moox/*/current; do (cd "$u" && ./stop.sh); done
# 4) 核对没有残留，有就杀；注意匹配锚定开头，避免杀到自己的 ssh 命令行
pgrep -f '^/data/moox/'
```

确认全部停止后，**保留 `/data/moox` 目录本身**（否则要 sudo 重建），只删里面的内容：

```bash
find /data/moox -mindepth 1 -maxdepth 1 -print0 | xargs -0 rm -rf
```

因子机先 `mkdir -p ~/moox-import-keep && mv /data/moox/import/* ~/moox-import-keep/`。

### 本机状态

- 旧的本机操作员身份目录移走并重建：`mv ~/.config/moox ~/.config/moox.bak-<时间> && mkdir -m 700 ~/.config/moox`，再把旧目录里的 `known_hosts`（SSH 指纹）拷回，免得重新核验指纹。
- 安装状态目录 `~/.local/state/moox/setup/<摘要>` 移走（`mv` 到 `setup.bak-<时间>`）。**这是必须的**：制品和请求哈希绑定在 core 阶段的状态里，
  control 阶段重试会复用 core 时构建的软件。想换制品（改了代码）只能换新状态目录，从 core 重来。

## 4. 安装顺序

首次安装的**所有命令都不带 `--release`**（`--release` 只用于升级）。以下命令都在主仓库根目录执行，`CLI=$WT/bin/moox-cli`，公共参数 `--file ./moox.toml --source-dir $WT`。
每一步成功的标志是输出里的 `*-ready` 与 `STEP-EXIT=0`；任何一步失败先看日志，不要盲目重试。

| 步 | 命令 | 备注 |
| --- | --- | --- |
| 1 | `$CLI setup bootstrap --stage core` | Admin、HostGateway、HostAgent、EventBus 凭据；输出 `core-ready`。约 3 分钟，含本机构建 |
| 2 | `$CLI setup deploy-host --host storage`，再 `overseas`、`factor` | 因子机要求 `[hosts.factor] reachability = "outbound"`，且已有 `/data/moox`（可写） |
| 3 | `$CLI setup deploy-unit --host storage --profile eventbus` | EventBus 装在 `[deployments]` 登记它的主机（默认存储机） |
| 4 | `$CLI setup deploy-unit --host storage --profile storage --binary-dir $WT/bin` | Storage 必须带第 2 节构建的 Linux CGO 制品 |
| 5 | 在存储机注册 DataNode（见下） | 节点 ID 固定 `storage-node-0` |
| 6 | `$CLI setup apply --file ./moox.toml` | 写入管理员与云凭据。**末尾的浏览器 CA 信任一步此时必然失败**（control 还没装、没有证书），以 `setup status` 返回 `"state":"completed"` 为准 |
| 7 | `$CLI setup metadata-import --storage-host storage --seed $WT/config/setup/metadata.yaml --spaces mooxsys`，再 `--spaces crypto,stockcn,stockhk,stockus` | 先系统空间，再业务空间 |
| 8 | 在存储机 `moox-storage-cli activate-datasets --metadata-target ip://127.0.0.1:20100` | 激活数据集；需要 `MOOX_STORAGE_NODE_AUTH_SECRET`（见下） |
| 9 | `$CLI setup deploy-unit --host storage --profile access`，再 `--host overseas --profile access`，再 `--host overseas --profile egress-proxy` | **存储机和海外机都要装 Access**，漏了海外机会出现“外接入（overseas）连接被拒绝”告警 |
| 10 | `$CLI setup bootstrap --stage control --create-proxy-ca` | internal TLS 的首次安装必须带 `--create-proxy-ca`；控制机磁盘小，失败重试前先清残留的发布目录 |
| 11 | 信任内部 CA（见下），然后 `$CLI setup init --config-dir $WT/config/setup --storage-host storage` | 创建业务空间、导入元数据与因子 |
| 12 | `$CLI setup deploy-unit --host overseas --profile collector-stream`，再 `--profile trade` | 必须在 control 就绪之后：collector-stream 启动后向 Collector 发心跳 |
| 13 | `$CLI setup sync-deployments --host factor`，再 `deploy-unit --host factor --profile factor-engine`，再 `--profile archive-worker` | 因子机外部程序，systemd 用户服务；引擎安装时自动按本机内存校准窗口与内存预算（`tune --write`） |

### 步骤 5、8：在存储机取节点密钥

`MOOX_STORAGE_NODE_AUTH_SECRET` 取自存储节点进程自己的环境，只放进环境变量、不打印不落盘：

```bash
./bin/moox-cli host exec --host <存储机主机 ID> -- sh -c 'cd /data/moox/storage/current
  pid=$(pgrep -f "^/data/moox/storage/.*moox-storage-node" | head -1)
  export MOOX_STORAGE_NODE_AUTH_SECRET=$(tr "\0" "\n" < /proc/$pid/environ | sed -n "s/^MOOX_STORAGE_NODE_AUTH_SECRET=//p")
  ./bin/moox-storage-cli register-node --metadata-target ip://127.0.0.1:20100 --node-id storage-node-0 --service-target ip://127.0.0.1:20107'
```

（`host exec` 经 `moox.toml` 里的 SSH 配置在存储机上执行，密钥只在远端 shell 的环境变量里，不会出现在输出里。）

步骤 8 同样取密钥，命令换成 `activate-datasets --metadata-target ip://127.0.0.1:20100`。

### 步骤 11 前：信任内部 CA

`setup init` 先检查内部 CA 是否已被本机信任，检查标准是**证书在系统钥匙串里**，没有就调 `scripts/deploy/install-caddy-ca.sh`（要 sudo 密码）。Agent 的 shell 没有终端，输不了密码：

1. 先核对本机缓存的证书就是新 CA：`openssl x509 -in ~/.moox/certs/moox-caddy-root-<控制机地址>.crt -noout -fingerprint -sha256`，与控制机 `/data/moox/prod/current/console-proxy/certs/caddy/root.crt` 的指纹一致。
2. 让用户在本机授权（弹窗由用户点、密码由用户输，Agent 看不到）：
   `security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db <证书>`（用户级信任，弹系统授权框）；
   再用 `osascript -e 'do shell script "security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain <证书>" with administrator privileges'` 把证书放进系统钥匙串（弹管理员密码框；系统信任设置可能因“无法交互”被拒，但证书已入系统钥匙串，检查即可通过）。
3. 用 `scripts/deploy/install-caddy-ca.sh --ca-file <证书> --check` 的退出码确认，再重跑 `setup init`。

旧的 CA 会留在钥匙串里，由用户自行清理。

### 步骤 11 的已知限制

`setup init` 会逐项导入 `moox.toml` 的 `[factors]` 任务；**任一任务的源数据集不存在就整体失败**，后面的任务不会建。默认只有两个 1m 因子任务（`ftask_spot_1m`、`ftask_swap_1m`，源数据集由 Collector 的默认采集任务创建）。
`moox.toml` 里指向自定义数据集的任务（例如手工建的 5m 采集任务的数据集）在全新系统里没有源数据，会让 init 在那里失败；先把它们从导入范围中拿掉，建好对应采集任务后再 `setup factors` 补导入。

## 5. 默认初始化的范围

“系统默认配置”取字面意思：仓库 `config/setup/*.yaml` 的种子（Space、元数据、默认采集任务、数据集健康策略）加上 `moox.toml` 里默认的因子任务。Collector 首次启动会把随发布安装的 `collector/config/collection-tasks.yaml` 写成默认采集任务
（只在任务表为空时写，任务 ID 固定，因子任务的源数据集依赖这些 ID；不足时 `moox-collector-cli init --seed-file` 补种）。默认只有 1m：币安现货、币安合约（crypto 空间，默认启用）和 A 股（stockcn，默认停用）；不再默认采集 1h，要 1h 数据时用 `moox-cli collector task create` 自己建。crypto 的采集方式默认就是 collector-stream（stream + local），不需要手工切换，用 `moox-cli collector ingest-mode get --space-id crypto` 确认。

### 默认展示策略（合约双均线）

给用户看的默认策略是 [`examples/strategies/dual_ma_swap_1m.yaml`](../../../examples/strategies/dual_ma_swap_1m.yaml)：基于币安合约 1m 因子任务（`ftask_swap_1m`）的 5/10 简单均线，
对任务里的**全部合约**逐个判断——快线 `ma__5` 在慢线 `ma__10` 之上的都持有（等权，单个最多 5%，较少时剩余留现金），落到慢线之下的下一期被剔除；只做多，
不挂账户（观察实例，只计算目标权重、不投递）。它依赖合约任务里有快、慢线两个成员 `{ factor = "ma", params = { window = 5 } }`（列 `ma__5`）与 `{ factor = "ma", params = { window = 10 } }`（列 `ma__10`），
`moox.toml.example` 的默认合约任务已包含；已有任务缺这两个成员时，往 `moox.toml` 的 `ftask_swap_1m` 里加上再 `setup factors`。

创建要等因子任务**进入运行**且结果视图覆盖够 `min_age_bars`（15 根，即任务开始出结果后约 15 分钟）；任务刚建、视图覆盖不足时 `strategy validate` 会提示还缺多少数据，启用也会被拒绝。依次执行（在主仓库根目录，用 `moox-cli gateway call`）：

```bash
S="$CLI strategy"
# 1) 试算：不落库，确认列都存在且能在最新一期求值（返回里的 trial 有内容才算通过）
$S validate --dsl-file $WT/examples/strategies/dual_ma_swap_1m.yaml --factor-task-id ftask_swap_1m
# 2) 创建定义（名称取自 DSL 的 name），记下返回的 strategy_id；之后改 DSL 要先停用实例，再 strategy update，再启用（启用中的实例引用定义时更新会被拒绝）
$S create --dsl-file $WT/examples/strategies/dual_ma_swap_1m.yaml
# 3) 创建观察实例（不挂账户）并启用，记下 instance_id
$S instance create --strategy-id <strategy_id> --factor-task-id ftask_swap_1m
$S instance enable --instance-id <instance_id> --yes   # 不带 --yes 只预览
```

启用后实例自动 init、start 并（无账户）自动 confirm 进入运行；`strategy result list --instance-id <instance_id>` 看每期结果，管理台的策略页能看到它。第一期是 `skipped(previous_version_unknown)`（用到 `bars[-1]` 需要上一根的版本证据），从第二期起正常求值；每期按条件筛选，满足的合约进入目标、不满足的记 `filtered`。注意 zsh 里把命令存进变量再 `$CALL` 调用不会分词，用函数 `call() { $CLI gateway call trpc.moox.strategy.StrategyMgr --space crypto "$@"; }`。一个因子任务只能被一个实例关联，且创建后不能改、不能删，所以这个任务留给默认策略，别的实例另建因子任务。

不在默认配置里、要另外做的事（汇报时列给用户，并附命令）：

- 长历史导入（例如永续 501 个标的的 5m）：见 [`initial-import.md`](initial-import.md)。导入前不要启用依赖长回看的因子任务；默认的 1m 因子任务在历史不足时按“降级/预热”运行，不影响实时链路。
- 迁移来的策略实例及其参数表：见 [`strategy-migration.md`](strategy-migration.md)，参数放 `local/`，不进仓库。
- 手工建的采集任务和标签、自定义因子任务。

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
| control 阶段失败，重试结果相同 | 软件制品绑定在 core 的状态目录，重试不会带上新代码 | 修代码后换新状态目录，从 core 重来 |
| control 阶段 `bootstrap observation failed`，Monitor 退出 | Monitor 的定时器首次运行时依赖的服务还没起来，错误让进程退出 | 看候选发布目录 `/data/moox/prod/releases/bootstrap-*/<组件>/logs/trpc.log`；代码已改为跳过本轮，但同类问题仍可能出现在别的组件 |
| 管理台打开是空白/占位页 | web-host 内嵌了占位前端（`web/dist` 不完整） | 按第 2 节在编译机构建前端并提交 statik.go；`make statik` 现在会拒绝不完整的 dist |
| `setup apply` 末尾报 `browser_ca_trust_failed` | control 还没装，取不到证书 | 预期现象，用 `setup status` 判断 |
| `setup init` 报 `browser_ca_trust_failed`、`sudo: a terminal is required` | 检查要求证书在系统钥匙串，安装需要 sudo | 见第 4 节“信任内部 CA”，由用户授权 |
| `setup init` 报 `CreateFactorTask failed ... metadata row not found` | `moox.toml` 里有源数据集不存在的因子任务 | 见第 4 节“步骤 11 的已知限制” |
| 因子 `factor_memory_budget_exceeded`、`status=degraded rows=0` | 引擎 `compute.max_memory` 太小（旧脚本写死 1GiB，默认因子 EMA 一期约 1.1GB） | 脚本已改 4GiB 并在安装时自动 `tune --write`；已装的机器改 `engine.yaml` 后重启引擎 |
| 引擎重启后一段时间 `lease conflict`、`not_ready` | 旧租约还没过期 | 等几十秒，再等窗口载入（约 20 秒）和 warming |
| 告警“外部接入（overseas）连接被拒绝” | 海外机没装 Access | `deploy-unit --host overseas --profile access` |
| 清空后进程又被拉起来 | 定时看护（cron watchdog）还在 | 先删 crontab 里的 `moox-watchdog` 行再停 |
| 仓库里多出二进制文件 | `go build ./...` 交叉编译在单 main 模块里留了产物 | 立即删除；别 `git add -A` |
| `statik.go` 出现无意义 diff | bootstrap 构建时重新生成 | `git checkout web-host/internal/statik/statik.go` |

## 8. 收尾

- 汇报：做了什么、验收结果、没做的事（第 5 节）、重建后丢失的手工配置、需要用户做的事（清理旧 CA 等）。
- 把发布过程中临时改过的代码或脚本提交并推送；`moox.toml` 的变更提醒用户 `moox-cli config backup`。
- 更新记忆（部署状态、踩坑）。
- 不要擅自合并 PR；合并要用户明确说。
