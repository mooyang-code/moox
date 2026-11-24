# 从源码目录部署与升级（Agent 用）

适用于线上这套部署：每台主机一个部署根（默认 `/data/moox`），组件是 systemd 用户服务 `moox-<组件>`，由 `moox-cli setup deploy` 部署。
维护者在本机构建 Linux 二进制（含 CGO 的 Storage 在 Docker 里构建），再逐台部署。构建环境见 [`docs/构建手册.md`](../../../docs/构建手册.md)。
外部用户用发布产物升级，按 [`install.md`](install.md) 的“升级到新版本”；清空重装按 [`fresh-deploy.md`](fresh-deploy.md)。

## 先确认再动手

- 用户明确说“发布/部署/上线某某”才发布，并且只发布他点名的组件；不要顺手带上别的组件。发布前向用户复述：哪些组件、哪些主机、是否涉及服务目录、哪些组件会重启。
- 破坏性运维（重置 View、清数据、清事件）不是发布，单独向用户确认，见 [`cli-operations.md`](cli-operations.md)。
- 前提：改动已提交并推送、测试通过，线上二进制必须能对应到一个提交。不要从带未提交改动的工作区构建。
- 从 main 构建前先看 main 上有没有别的会话合进来、还没到发布条件的改动；有就列出来写进确认清单。

## 主机与组件

主机命名统一为控制机（control）、存储机（storage）、海外机（overseas）、因子机（factor），以 `moox.toml` 的 `[hosts]` 与 `[deployments]` 为准，登记与启停见 [`host-registry.md`](host-registry.md)。

| 主机 | 组件（`--component` 的取值，二进制一般是 `moox-<组件>`） |
| --- | --- |
| 每台主机 | `host-gateway` `host-agent`（自动带上，不写在 `[deployments]` 里） |
| 控制机 | `admin` `monitor` `collector` `factor-mgr` `archive-mgr` `strategy` |
| 存储机 | `storage-primary` `storage-view`（各一份二进制）`eventbus` |
| 海外机 | `trade` `egress-proxy` `collector-stream` `callback-dispatcher` |
| 因子机 | `factor-engine` `archive-worker`，没配外部对象存储时还有内置的 `seaweedfs`（二进制 `weed`） |

组件要哪些二进制与配套命令行、启动参数、配置模板、启动顺序都登记在服务目录（`packages/servicecatalog/catalog.yaml` 的 `binary`、`tools`、`args`、`config_files`、`after`）。

## 标准流程

```bash
WT=<代码所在目录>; CLI=$WT/bin/moox-cli
# 1. 构建：本机 CLI 和要发布的组件的 Linux 二进制（目标名见 scripts/build/build.sh；Storage 与 admin 的前端在 Docker 里，macOS 先 colima start）
cd $WT && ./scripts/build/build.sh cli
cd $WT && bash scripts/build/build-linux-local.sh --out $WT/bin-linux <目标>...
# 2. 看现状
$CLI setup status --file <moox.toml>
# 3. 部署：只发布点名的组件；整台主机时不写 --component
$CLI setup deploy --file <moox.toml> --source-dir $WT --binary-dir $WT/bin-linux --host <主机> --component <组件>[,<组件>]
# 4. 核对
$CLI setup status --file <moox.toml> --host <主机>
```

- `--binary-dir` 里要有这次部署的每个组件的二进制和配套命令行（例如 `admin` 要 `moox-admin` 与 `moox-admin-cli`），缺了会报 `binary_missing` 并指出是哪个文件。
  构建目标和组件基本同名：`host-gateway`、`admin`、`collector`、`factor-mgr`、`strategy` 目标同时产出对应的 `-cli`，`hostagent` 目标产出 `moox-host-agent`，
  `storage` 目标产出两个 Storage 二进制，`storage-cli` 是独立目标，`archive-worker` 目标带上 `weed`。
- 部署按二进制摘要和渲染出的配置判断变化：**只有变了的组件会重启**，没变的只核对在运行。结果里每个组件是 `started`、`restarted`、`unchanged` 或 `paused`（被 `setup stop` 停用的，只更新文件）。
- 部署最后等这次装的组件就绪（本机 `/readyz`），到时限没就绪会列出来并返回失败：先看 `setup logs --host <主机> --component <组件> --journal`。
- 控制机每次部署都会先执行 `moox-admin-cli bootstrap`（CA、主密钥、管理员、已登记的公钥只建不改；主机与部署按 `moox.toml` 同步，启停状态保留）；其余主机每次先把登记同步到 Admin，再向控制机要身份材料。

## 服务目录变化

服务目录（方法、调用权限、端口）编在每个网关、客户端和 Admin 里，路由由各网关按 Admin 下发的主机与部署自己算。只部署个别组件时，`setup deploy` 发现目标机 `version.json` 记的服务目录摘要和这次不一样，会把这台主机的主机网关一起升级并重启。
目录变化涉及多台主机时，逐台部署受影响的主机，控制机先，再是其余主机；全部做完前新方法可能在还没升级的网关上被拒绝，这是预期的短暂状态。

## 配置随部署渲染

每次部署都从仓库里的模板重新渲染这个组件的配置（`<部署根>/config/<组件>/`）、环境文件（`<部署根>/secrets/env/<组件>.env`）和单元文件，目标机上的手工改动会被覆盖。

- 部署参数（保留期、出口白名单、CLS 地址等）改 `moox.toml` 后重新部署对应主机生效。
- 个别组件要调的值写在 `moox.toml` 的 `[overrides."<组件>"."<配置文件名>"]`，每行 `"键路径" = 值`，渲染之后逐项覆盖；键路径不存在会报错。
- 目录里的方法超时（方法上的 `timeout_ms`）要和服务端 `trpc_go.yaml` 的 `timeout` 配套：服务端不小于目录。改模板或写 `[overrides]`，不要上机改。

## 重启的代价

- Storage 主库重启会让周期提交失败片刻；View 重启后低频视图有空窗。
- 因子引擎**避开整点前后重启**：重启会丢一个整点周期的计算（`1h` 因子任务）；同一个 `engine_id` 的新进程第一次心跳就顶替旧进程（只有另一个 `engine_id` 在当前引擎心跳还新鲜的 45 秒内来登记才报 `lease conflict`），之后各因子任务要重新预热，耗时随标的数与回看根数而定。
- `factor-mgr` 重启时如果引擎正在上报补算进度，这次上报会失败（任务租约 15 分钟后重领，从已上报的进度续跑）。
- EventBus 重启会让所有客户端短暂重连，只在需要时做。
- 发布不是迁移：数据库结构变化要先确认旧二进制不认新表时的影响；加索引的大表要先手工建索引再换二进制。不确定就问用户。

## 发布后验证（只读）

1. `setup status`：组件都是 `active`，`version.json` 的版本与提交是这次的。
2. 告警：发布期间 Monitor 会短暂出现“运行指标上报”“行情探针”告警，**几分钟内自动恢复就是正常**；超过 5 分钟仍未恢复才查：`moox-cli doctor alerts --format text`。
3. 日志：用 `moox-cli monitor logs --component <组件> --from <发布时刻>` 看新进程启动之后的记录。**先确认里面有这次启动的 `process_started`**：它同时证明新二进制已经在跑、EventBus 的发布权限没问题、Monitor 入库正常。
   看不到它时不能当成“没有错误”。崩溃信息（panic）在 journald 里：`setup logs --host <主机> --component <组件> --journal`；完整的文件日志在 `<部署根>/logs/<组件>/trpc.log`。
4. 前端（内嵌在 `moox-admin` 里）：`curl -sk https://<控制机地址>:9527/ | grep -o 'static/js/index-[^"]*\.js'` 应等于构建输出里“moox-admin 内嵌前端”那一行的入口文件。页面需登录，由用户自己在浏览器验证。
5. 功能验证：用只读或无副作用的调用验证新行为（例如预览、对不存在的对象做一次空操作），不要用真实数据做试验。

## 陷阱清单

- **旧二进制被原样重新部署**：核对构建产物的时间；部署结果里该组件是 `unchanged` 说明二进制和配置都没变。
- **构建的是工作区，不是提交**：未提交的改动会被编进二进制，线上二进制就对不上任何提交。
- **Go 版本**：用 `.go-version` 指定的 toolchain（`$HOME/.local/go<版本>/bin`，没有就运行 `bash scripts/build/install-go.sh`），并设 `GOTOOLCHAIN=local`，不要联网下载 toolchain。
- **本机的 `moox-cli` 也要是这次的提交**：服务目录、配置渲染和安装脚本都编在 CLI 里。
- **不要上机手工换二进制或改配置**：`version.json` 记的摘要会对不上，下次部署把它当成有变化重启；手工改的配置会被覆盖。
- **海外机带宽小**：整台部署要上传几百 MB 二进制，避开行情密集的时段；只改了一个组件就用 `--component`。

## 回滚

用上一个提交构建的二进制和 CLI 再部署一遍同样的组件。数据目录不受部署影响；数据库结构只增不减的改动可以这样回退。
服务目录变化的回滚同样逐台做完，控制机先。
