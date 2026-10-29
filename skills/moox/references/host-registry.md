# 主机登记与外部程序

Admin 的主机表（`t_hosts`）是主机的唯一登记处：地址、可达性、中文名和控制台终端用的 SSH 信息都在这里。
**机器的新增、部署、启停、移除只通过 `moox-cli`（本 skill）完成**；管理台的主机列表、服务部署、数据节点页面全部只读，
页面上的提示就是指向这里。不要手改 Admin 数据库，也不要在管理台找新增或编辑入口。

## 命名

主机 ID 用英文短名、按角色命名，配固定中文名；同一角色有多台时才加序号（`overseas-2` → “海外机 2”）。
沟通和文档统一写“中文名（英文 ID）”。

| 主机 ID | 中文名 | 运行 |
| --- | --- | --- |
| `control` | 控制机 | Admin、Monitor、Collector、CloudFunction、因子管理、策略、控制台 |
| `storage` | 存储机 | Storage primary/node/view、EventBus、Access |
| `overseas` | 海外机 | Trade、Access、EgressProxy、Collector Stream |
| `factor` | 因子机 | 因子引擎、归档 Worker（外部程序，原生部署使用 Linux，可以是内网机器） |

`moox.toml` 里 `[hosts.<ID>]` 写 `label`（不写时按 ID 生成）、`address`、`region`、`ssh = { port, username }`；
`[deployments]` 按主机 ID 列出业务组件，`host-gateway` 与 `host-agent` 自动补齐。控制台终端只对配了 SSH 密码或私钥文件、
且不是 `reachability = "outbound"` 的主机开放，其余主机在主机列表里置灰。

## 新增并部署一台机器

1. 由用户准备含新 `[hosts.<ID>]` 与 `[deployments]` 的配置。当前 `config edit` 只能修改既有主机的 SSH 登录方式，地址、地域、用途只读，不能新增主机；明文配置可由用户自行编辑后用 `config encrypt` 加密，已有加密配置目前没有新增主机的表单入口。Agent 不读取或代改秘密配置。
2. `moox-cli setup validate --file ./moox.toml`；`host_key_unknown` 时按 [`install.md`](install.md) 通过独立可信渠道核对指纹后 `setup trust-host`。
3. `moox-cli setup deploy-host --host <ID>` 安装 Host Gateway 与 Host Agent，并把主机登记到 Admin。
4. 按该主机登记的业务组件逐个 `setup deploy-unit --host <ID> --profile <单元>`；命令只安装 `[deployments]` 里登记在该主机上的单元。
5. 需要调整已登记主机的组件清单时，`setup sync-deployments --host <ID>` 把 `moox.toml` 里的完整部署同步给 Admin（保留已有的启停状态）。

## 启用、停用、移除

所有写操作先预览，确认后加 `--yes`：

```bash
./bin/moox-cli setup host disable --host <ID>                         # 预览
./bin/moox-cli setup host disable --host <ID> --yes
./bin/moox-cli setup host enable --host <ID> --yes
./bin/moox-cli setup deployment disable --host <ID> --component <组件> --yes
./bin/moox-cli setup deployment enable --host <ID> --component <组件> --yes
./bin/moox-cli setup remove-host --host <ID> --yes                    # 主机已不在 moox.toml 里
```

- 普通组件只改登记状态，网关路由随之收敛。
- 外部程序（`factor-engine`、`archive-worker`）停用时先经 SSH 停掉并核对 systemd 服务已停止，再改登记；启用时先改登记再启动并等待就绪。
- 停用主机会先逐个停掉它上面启用的外部程序，全部成功后才改主机状态；恢复主机时只启动启用部署的外部程序，单独停用的保持停用。
- 停用主机上不能单独启用外部程序。
- `setup remove-host` 只删 Admin 里的登记（清空部署、路由，主机的网关身份和控制台 SSH 信息随之失效），不会停止或删除目标机上的进程与文件：执行前先在目标机停掉这台主机的全部组件。

## 外部程序

`factor-engine` 与 `archive-worker` 是服务目录里的外部程序组件：软件包随 release 交付，由 systemd **用户服务**托管（目标机需开启 linger），
健康检查经 control 网关中转到 11417 / 11418 的探测端口，Monitor 与 `moox-cli doctor` 按这个口径判断，调用方走 Access 的外部身份。

```bash
./bin/moox-cli setup release build --release <ID> --source-dir .   # release 里要有两个外部程序
./bin/moox-cli setup deploy-unit --host factor --profile factor-engine --release <ID>
./bin/moox-cli setup deploy-unit --host factor --profile archive-worker --release <ID>
```

- 主机或部署在 Admin 里已停用时，安装只落文件、不启动。
- 归档根目录取 `moox.toml` 的 `[paths] archive_root`。
- 退役一个外部程序：`setup deployment disable --component <组件> --yes`（先停服务再改登记），之后从 `[deployments]` 删除条目并 `setup sync-deployments`。

## 全新部署

项目未上线时允许清空重来（不做平滑迁移）。每一个破坏性步骤都先列清单、由用户确认再执行：

1. 停止各主机的守护与全部单元，删除 `/data/moox`；因子机上停止并删除两个外部程序的服务与安装目录。
2. 清理云上由系统创建的 SCF 函数与触发器；删除本机 CLI 的部署状态目录与操作员身份目录（或改用空目录）。
3. 按 [`install.md`](install.md) 的顺序重新初始化，外部程序放在 `setup init` 之后部署。

## 主机监控

主机监控只展示登记主机，每台主机取**最近上报**的采集器参与汇总；重装后换了 `agent_id` 的旧采集器静默退役，既不显示也不报失联。
没有登记主机 ID、或主机 ID 不在登记里的采集器不进入主机列表，页面只提示数量。采集器的清理同样通过本 skill 操作。
