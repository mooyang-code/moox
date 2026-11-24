# 主机登记与外部程序

Admin 的主机表（`t_hosts`）是主机的唯一登记处：地址、可达性、中文名和控制台终端用的 SSH 信息都在这里。
**机器的新增、部署、启停、移除只通过 `moox-cli`（本 skill）完成**；管理台的“主机工作台”“服务管理”页面全部只读，
页面上的提示就是指向这里。不要手改 Admin 数据库，也不要在管理台找新增或编辑入口。

## 命名

主机 ID 用英文短名、按角色命名，配固定中文名；同一角色有多台时才加序号（`overseas-2` → “海外机 2”）。
沟通和文档统一写“中文名（英文 ID）”。

| 主机 ID | 中文名 | 运行 |
| --- | --- | --- |
| `control` | 控制机 | Admin、Monitor、Collector（含云函数管理）、因子管理、归档管理、策略、控制台 |
| `storage` | 存储机 | Storage primary/view、EventBus；网关开外部入口（`external_entry = true`） |
| `overseas` | 海外机 | Trade、EgressProxy、Collector Stream、事件中心（callback-dispatcher）；网关开外部入口 |
| `factor` | 因子机 | 因子引擎、归档 Worker、内置对象存储（Linux，可以是内网机器） |

`moox.toml` 里 `[hosts.<ID>]` 写 `label`（不写时按 ID 生成）、`address`、`region`、`ssh = { port, username }`；
`[deployments]` 按主机 ID 列出业务组件，`host-gateway` 与 `host-agent` 自动补齐。控制台终端只对配了 SSH 私钥文件（`ssh.private_key_file`）、
且不是 `reachability = "outbound"` 的主机开放，其余主机在主机列表里置灰。

## 新增并部署一台机器

1. 由用户在明文的 `moox.toml` 里加上新的 `[hosts.<ID>]` 与 `[deployments]`（拓扑直接编辑这份文件；秘密在 `moox.secrets.toml`，Agent 不读取或代改）。
2. `moox-cli setup validate --file ./moox.toml`；`host_key_unknown` 时按 [`install.md`](install.md) 通过独立可信渠道核对指纹后 `setup trust-host`；`manual_steps` 里有 linger 时让用户先在那台机器上执行。
3. `moox-cli setup deploy --host <ID>`：把主机与它的部署登记到 Admin，装好主机网关、HostAgent 和 `[deployments]` 里登记在这台主机上的全部组件，并等它们就绪。
4. 之后调整这台主机的组件清单：改 `[deployments]` 后再 `setup deploy --host <ID>`（多出来的组件会被停掉并删掉服务，数据目录留着）。
   只想把登记同步给 Admin、不动目标机时用 `setup sync-deployments --host <ID>`（保留已有的启停状态）。

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
- `setup remove-host` 只删 Admin 里的登记（清空部署、路由，主机的网关身份和控制台 SSH 信息随之失效），不会停止或删除目标机上的进程与文件：执行前先用 `setup stop --host <ID> --component <组件> --yes` 逐个停掉这台主机的组件。
- 只是临时停一个组件、不改登记：`setup stop` / `setup start-component`（`systemctl --user disable --now` / `enable --now`）。被停用的组件在之后的部署里只更新文件、保持停用。

## 外部程序

`factor-engine` 与 `archive-worker` 是服务目录里的外部程序组件：和其他组件一样由 `setup deploy` 部署成 systemd **用户服务**（目标机需开启 linger），
它们是普通调用方：用所在主机的密钥、以自己的组件 ID 签名，从本机网关取目录，再直连存储机与控制机的 `11003`。健康端口（11417 / 11418）只听本机，Monitor 看上报，`moox-cli doctor` 经 SSH 在目标机上请求。
归档 Worker 另有只听 127.0.0.1 的数据端口 11428（管理台下载分区与看最新数据，只有 Admin 经 control 中转进入）；只支持装在 control 或只出站主机上，装在其他可直达主机上时这两个网页功能不可用。

```bash
./bin/moox-cli setup deploy --host factor                               # 整台
./bin/moox-cli setup deploy --host factor --component factor-engine     # 只部署一个
```

- 归档文件在 `<部署根>/archive`，导入包放在 `<部署根>/import`。
- `moox.toml` 有 `[archive_object_store]` 时，部署 archive-worker 会从 Admin 读出 `secret_id` 指向的密钥（提供方 `s3`、已启用）写成 `<部署根>/secrets/archive-object-store.env`；读密钥失败时部署停下，旧配置与旧进程不动。
  没有这一段时，这台主机会多一个内置对象存储组件 `seaweedfs`（服务 `moox-seaweedfs`，只听 127.0.0.1，数据在 `<部署根>/data/seaweedfs`，访问密钥在目标机上生成一次后一直沿用）。
  用外部 S3 时换机器不用拷归档目录（读回时从远端取回）；用内置对象存储时要把 `<部署根>/archive` 和 `<部署根>/data/seaweedfs` 一起拷。
- 退役一个外部程序：`setup deployment disable --component <组件> --yes`（先停服务再改登记），之后从 `[deployments]` 删除条目并 `setup deploy --host <ID>`。

## 全新部署

清空重来时，每一个破坏性步骤都先列清单、由用户确认再执行：

1. 运行 `setup wipe`：逐台停用并删除全部 `moox-*` 用户服务，杀掉部署根里的进程，清空部署根，并删掉这台电脑上的操作员身份。先看预览，再带 `--yes --expect <digest>` 执行。
2. 到腾讯云控制台清理由系统创建的 SCF 函数与触发器（`moox-fetch-plan-` 开头），或在 `setup wipe` 时带 `--clean-functions`（预览单列，要用户单独确认）。
3. 按 [`install.md`](install.md) 用 `setup install` 重新安装。

## 主机监控

主机监控只展示登记主机，每台一行：HostAgent 以登记的 `host_id` 上报，Monitor 按它记录，重装 HostAgent 沿用同一行。
主机从登记里删除后，它的上报不再进入主机列表，主机类检查也随之收尾。
