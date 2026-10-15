# 原生部署的发布流程（Agent 用）

适用于线上这套**原生部署**（`/data/moox/<单元>/releases/<发布ID>` + `current`，由 `moox-runtime` 管理进程）：
在构建机上构建 Linux 二进制，经本机中转，逐台替换并重启，核对 sha 与就绪。旧的 `deploy-moox.sh`、`setup deploy-service` ZIP 包流程
（[`service-release.md`](service-release.md)、[`release.md`](release.md)）属于切换前的部署方式，不要用来发布当前线上。

固化的步骤在 `skills/moox/scripts/native-release.sh`（契约测试 `test-native-release.sh`）；这里写流程、顺序和陷阱。

## 先确认再动手

- 用户明确说“发布/部署/上线某某”才发布，并且只发布他点名的组件；不要顺手带上别的组件。发布前向用户复述：哪些组件、哪些主机、是否涉及服务目录。
- 只用 SSH 公钥（脚本固定 `BatchMode=yes`、禁用密码登录）。不读取、不使用 `moox.toml` 里的账号密码；不处理 EventBus 等凭据。
- 破坏性运维（重置 View、清数据、清事件）不是发布，单独向用户确认，见 [`cli-operations.md`](cli-operations.md)。
- 前提：改动已提交并推送、测试通过。脚本会拒绝“工作区有未提交改动”和“提交还没推送”，因为线上二进制必须能对应到一个提交。

## 一次性配置

主机地址不入库，写在 `~/.moox-release.env`（模板见 [`release-hosts.env.example`](release-hosts.env.example)）：

```bash
MOOX_RELEASE_BUILDER=<构建机用户>@<构建机地址>
MOOX_RELEASE_HOST_CONTROL=ubuntu@<控制机>
MOOX_RELEASE_HOST_STORAGE=ubuntu@<Storage 主机>
MOOX_RELEASE_HOST_COMPUTE_1=ubuntu@<compute-1>
```

实际地址见 `docs/计划/网关与服务部署重构设计.md` 的主机表；构建机是内网的编译机，也运行因子引擎。配置好后先只读验证：

```bash
S=skills/moox/scripts/native-release.sh
$S status --host control      # 各单元组件的状态、就绪与二进制 sha
$S ledger --host control      # 发布台账：谁在什么时候装了哪个提交
```

## 单元与组件速查

每台主机的部署根都是 `/data/moox`；**单元**是其下带 `current/` 的目录，`--unit` 用目录名。组件 id 以 `status.sh` 的输出为准。

| 主机 | 单元 | 组件 id（二进制名 `moox-<id>`） |
| --- | --- | --- |
| 控制机 | `prod` | `admin` `eventbus` `monitor` `collector` `cloudnode` `factor-mgr` `strategy` `web-host` `console-proxy` |
| Storage 主机 | `storage` | `storage-primary` `storage-node` `storage-view`（**同一份二进制，三个文件名**） |
| 每台主机 | `host` | `host-gateway` `host-agent` |
| Storage 主机、compute-1 | `access` | `access` |
| compute-1 | `trade`、`egress-proxy` | `trade`、`egress-proxy` |
| 构建机 | systemd 用户单元 | `moox-factor-engine`（不在 `/data/moox` 里，见下） |

构建目标是 `scripts/build/build.sh` 的参数，和组件 id 基本一致（`host-gateway` 目标同时产出 server、cli 和 `moox-runtime`；`admin`、`monitor`、`collector`、
`factor-mgr`、`strategy`、`trade`、`cloudnode` 目标同时产出对应的 `-cli`；`storage-cli`、`factor-engine`、`hostagent` 是独立目标）。
CGO 只有 Storage 需要（DuckDB），脚本对 `storage*` 目标自动开启，其余一律关闭；**不要在 macOS 上交叉编译 Storage**。

## 标准流程（单个或几个组件）

```bash
S=skills/moox/scripts/native-release.sh

# 1. 构建：同步到构建机、构建、取回到 $TMPDIR/moox-release/<提交>/，并逐个校验 sha
$S build collector

# 2. 逐台逐组件安装并重启；输出里的 BACKUP 要记下来，回滚用
$S install --host control --unit prod --file <取回的 moox-collector> --restart collector

# 3. 核对
$S status --host control
```

`install` 做的事：上传并核对 sha → 备份旧二进制到 `<单元根>/binary-backup-<标签>-<时间>/`（每次都是新目录）→ 先写 `.new` 再 `mv` 原子替换 →
写发布台账 → `restart.sh --components <id>` → 核对返回的 `binary_sha256` 等于刚装的 → 等组件就绪（默认最多 60 秒）。
任何一步失败都会非零退出；**失败时线上可能已经是新二进制**（替换在重启之前），按输出的 `BACKUP:` 回滚：

```bash
$S rollback --host control --unit prod --backup /data/moox/prod/binary-backup-collector-<时间> --restart collector
```

规则：

- 一次只动一个组件、一台主机；每步看到 `RESULT:...ready=True` 和 `READY:` 再继续。
- 装 cli 之类没有对应组件的二进制用 `--no-restart`；`--restart` 的组件必须是真正使用这个二进制的那个，否则 sha 核对会失败（这是有意的）。
- Storage 三个名字：构建 `storage-primary`，再用 `--name moox-storage-primary|moox-storage-node|moox-storage-view` 各装一次保持一致；
  只重启需要的那个（例如只改 View 逻辑就只给 `storage-view` 加 `--restart storage-view`，前两个用 `--no-restart`）。
  重启 `storage-primary`、`storage-node` 会让这一刻的周期提交失败，监控可能出一条几十秒的行情探针告警并自动恢复。
- 发布 Storage 前确认没有正在跑的 `reset-view-consumers` 或 View 重建。

## 服务目录变化：必须整体发布

`packages/servicecatalog/catalog.yaml`（新增或删除 RPC 路由、改 ACL、改方法超时都算）内嵌在 **Admin、Monitor、每台主机的 host-gateway** 里。
Monitor 要求自己内嵌的目录与 Admin 提供的逐字节一致，不一致就停掉放置同步，三台主机的“网关心跳”告警同时出现；host-gateway 用内嵌目录校验路由快照。

```bash
$S catalog-changed --since <上次整体发布目录的提交>   # 退出码 10 = 目录变了，必须整体发布
$S catalog-changed --host control                    # 用台账里最近一次 moox-admin 的提交；台账还没有记录时用 --since
```

台账只记录用本脚本装的二进制；在此之前手工装的没有记录。最近一次整体发布目录对应的提交是 `defc05c7`（2026-10-15）。

顺序（缺一不可）：

1. 构建 `host-gateway admin monitor`（同一个提交）。
2. 三台主机的 `host` 单元：**`moox-host-gateway` 和 `moox-host-gateway-cli` 都装**（旧 cli 会拒绝新快照），重启 `host-gateway`；一台一台来，间隔约 15 秒，控制机最后。
3. Admin 与 Monitor 连着装（先 Admin 后 Monitor），间隔不超过 1～2 分钟：两者之间 Monitor 与 Admin 的目录不一致，放置同步会停摆，窗口越短越好。
   **控制机的 host-gateway 装完后可能起不来**：它启动时从还是旧版的 Admin 取初始路由快照，新目录校验不过就退出
   （日志 `initial host snapshot unavailable: invalid host gateway snapshot: route differs from catalog or directory`，`status` 里 `host-gateway stopped`，
   脚本不打印 `RESULT/READY`）。此时控制机上的组件跨主机调用全部中断，要立刻装 Admin，Admin 就绪后在控制机执行
   `/data/moox/host/current/bin/moox-runtime start --plan /data/moox/host/current/runtime.json --components host-gateway` 拉起网关（2026-10-15 实测中断约 50 秒）。
   Storage 主机和 compute-1 的网关不受影响。
4. 核对：Admin 数据库的 `t_host_gateway_status` 里三台主机 `c_expected_hash == c_applied_hash` 且 `c_last_error` 为空；Monitor 日志每分钟有 `placement sync updated N checks`、没有 `catalog differs`。

只想发布别的改动、而目录改动还不想发布时，在构建树里去掉目录改动再构建（不要把半截目录发出去）。

## 配置文件不随二进制更新

发布目录里的 `<组件>/config/*.yaml` 是首次安装时放下的，只换二进制不会带上仓库里的配置改动。改了服务超时、端口之类配置时：

1. 线上先备份再改：`cp -p $REL/<组件>/config/trpc_go.yaml <备份目录>/`，用 `python3` 做精确替换并断言只命中一处，不要 `sed` 盲改。
2. 重启该组件，再验证。
3. 把这次手工改动写进文档（例如 Admin 的 `trpc.moox.ops.SysDeploy` 服务端 `timeout` 已手工改为 300000），否则下次重建发布目录会丢。

目录里的方法超时（`method_timeouts_ms`）要和服务端 `trpc_go.yaml` 的 `timeout` 配套：服务端不小于目录。

## 不走 `native-release.sh` 的组件

**因子引擎**（构建机上的 systemd 用户单元 `moox-factor-engine.service`，安装在 `~/moox/factor-engine/`）：构建目标 `factor-engine`，就在构建机上操作，不需要 scp。

```bash
D=~/moox/factor-engine; TS=$(date +%Y%m%d%H%M%S)
mkdir -p $D/bin-backup-$TS && cp -p $D/bin/moox-factor-engine $D/bin-backup-$TS/
install -m 0755 ~/moox-build/bin/moox-factor-engine $D/bin/.moox-factor-engine.new && mv -f $D/bin/.moox-factor-engine.new $D/bin/moox-factor-engine
systemctl --user restart moox-factor-engine.service
```

- **避开整点前后重启**：重启会丢一个整点周期的计算（`1h` 因子集）。只落盘不重启，等合适时机再重启也可以。
- 管理端租约 TTL 45 秒：新进程会先报 `lease conflict`，约 45 秒后接管，期间因子不出结果；重启后各因子集要重新预热几分钟，这期间数据集显示“数据积累中”但不告警。
- 引擎配置在 `~/moox/factor-engine/config/engine.yaml`，改前备份；账本重置用 `moox-factor-engine reset-gap-ledger`（引擎必须已停止）。

**前端 `web-host`**（本机构建，发布到控制机 `prod` 单元）：

1. `web/` 下 `NODE_OPTIONS=--experimental-require-module pnpm run build:prod`。
2. `web-host/` 下 `make statik && make build-linux`（PATH 里要有 Go 1.26.9 toolchain），产物 `bin/moox-web-host`。
3. 构建会改写 `web-host/internal/statik/statik.go` 与 `web/src/components.d.ts`：发布后 `git checkout --` 还原，**不要提交**。
4. 用 `native-release.sh install --host control --unit prod --file web-host/bin/moox-web-host --restart web-host` 装。
5. 线上验证：curl 懒加载分包检索新文案；页面需登录，由用户自己在浏览器验证。

## 发布后验证（只读）

1. `status`：组件都 `ready=True`，sha 变成新构建的 sha。
2. 告警：发布期间 Monitor 会短暂出现“运行指标上报”“网关心跳”“行情探针”告警，**几分钟内自动恢复就是正常**；超过 5 分钟仍未恢复才查。在控制机上读：

```bash
REL=$(readlink -f /data/moox/prod/current)
python3 - "$REL/monitor/data/monitor/monitor.db" <<'EOF'
import sqlite3, sys
c = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
for r in c.execute("select c_rule_id, c_status, c_triggered_at from t_monitor_alert_states where c_status not in ('ok','resolved','OK')"): print(r)
EOF
```

3. 日志：`/data/moox/run/logs/<组件>.log` 里看新进程启动之后有没有 error/panic；Storage 的日志可能含非 UTF-8 字节，用 `grep -a`。
4. 功能验证：用只读或无副作用的调用验证新行为（例如 `--dry-run`、对不存在的对象做一次空操作），不要用真实数据做试验。

## 陷阱清单

- **`.gotmp` 丢失，旧二进制被原样重新部署**：rsync `--delete` 会删掉构建机上的 `.gotmp`，`go` 创建不了工作目录就失败，上一次的产物还留在 `bin/`。`build` 已经排除 `.gotmp` 并只认本次新产出的文件；手工构建时每次先 `mkdir -p bin .gotmp`，并核对返回的 `binary_sha256` 确实变了。
- **构建的是工作区，不是提交**：未提交的改动会被同步过去，线上二进制就对不上任何提交。`build` 要求干净且已推送。
- **构建机 Go 版本**：用 `.go-version` 指定的 toolchain（`$HOME/.local/go<版本>/bin`），并设 `GOTOOLCHAIN=local`，不要联网下载 toolchain。`-buildvcs=false`。
- **`restart.sh` 输出多行日志，最后一行才是 JSON**；`status.sh` 同理。
- **host 单元每台主机都有**，控制机上也有；改了网关就三台都要装，而且 cli 也要装。
- **服务目录只发布一半**：见上节，会让三台主机同时出“网关心跳”告警。
- **配置改动不会随二进制上线**：见上节。
- **不要用 `moox-cli` 做发布**：它会用 `moox.toml` 里的 SSH 密码；本流程只用 SSH 密钥。
- **不要把中转文件、备份目录留在 `/tmp`**：脚本会清理上传的临时文件；备份目录保留在单元根下，隔几天清理旧的，别删最近一次。
- **同一秒内连续安装**：备份目录名带时间戳，脚本保证每次都新建，不会覆盖前一次的备份。
- **重启代价**：Storage 的 primary/node 重启会让周期提交失败片刻；因子引擎重启丢一个整点周期并要预热；`factor-mgr` 重启时如果引擎正在上报补算进度，这次上报会失败（任务租约 15 分钟后重领，从已上报的进度续跑）。
- **发布不是迁移**：数据库结构变化要先确认旧二进制不认新表时的影响，必要时先执行对应的 `init`/迁移再换二进制；不确定就问用户。

## 回滚

```bash
$S rollback --host <别名> --unit <单元> --backup <install 输出的 BACKUP> --restart <组件 id>
```

只接受该单元下的 `binary-backup-*` 目录，把里面的 `moox-*` 装回去并重启，写台账（提交列显示 `rollback:<备份目录名>`，此时 `catalog-changed --host` 无法判断，需要用 `--since`）。
配置文件的回滚用发布前备份的那份手工还原。服务目录整体发布失败时，也按“三台网关 → Admin → Monitor”的顺序整体回滚，不要只回滚一部分。
