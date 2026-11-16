# EventBus 证书与口令同步

每个 NATS role token 是**一份权威材料、多台机器上的多份副本**。云函数不连 EventBus，没有它的副本。
权威材料由 Admin 保存；每次 `setup deploy` 从 Admin 导出这台主机上各组件需要的角色，写到 `<部署根>/secrets/eventbus/`。
**单角色 token 轮换必须同步 broker 与所有使用该角色的客户端（包括同机使用 `tls://127.0.0.1:4222` 的服务），每一个都拿到自己的 role 文件并成功重连才算完成。**

EventBus 没有自己的证书和 CA：服务端证书就是它所在主机的主机证书（`<部署根>/secrets/host.crt`、`host.tls.key`，和主机网关用的是同一张），
客户端用 role 文件里 `ca_file` 指向的 MooX CA 根证书（`<部署根>/secrets/moox-ca.crt`）校验。没有 TLS 轮换命令。

违反字面规则就是违反精神：只换二进制、只重启 EventBus、只更新 control，都不算同步完成。

## 何时必须走完 fan-out

出现任一情况时先读本文，再发布或宣称完成：

- `eventbus-credentials ensure|export|export-clients|rotate`
- EventBus 所在主机改了地址，或 EventBus 换了主机（例如从控制机搬到存储机）：角色文件里的地址要跟着换
- 日志出现 `Authorization Violation`、`certificate signature failure`、`certificate verify failed`
- EventBus `:4222` 对单一客户端 IP 堆积 `FIN-WAIT-2`，或 Host Agent 因 NATS 失败反复退出

## 权威材料

生成与轮换只通过 `skills/moox/scripts/eventbus-credentials.sh`（Admin CLI）。
不要把 token 或私钥写进命令行、聊天、git 或任何制品。

`internal-admin.yaml` 是 EventBus 自身的内部管理凭据，只随 `eventbus` 组件装到 EventBus 所在主机，不分发给其他组件。Storage 不消费 JetStream（View 跟随 Primary 的变更日志），没有要代为维护的 consumer。

`rotate --credential <NATS用户名> --confirm` 立即替换 Admin 中该角色的 token，不保留双 token 过渡期；运行中的 broker 仍使用已加载的用户表，新 token 要在导出并安装新的 `users.yaml`、重启 EventBus 后才实际生效。因此必须同时安排 broker 与使用方更新。

参数名称不要混用：`rotate --credential` 使用 NATS 用户名（例如 `eventbus-internal-admin`、`monitor-observability-consumer`）；`export-clients --roles` 使用凭据文件去掉 `.yaml` 的名称（对应 `internal-admin`、`monitor-observability`；其余角色通常同名）。

broker 的用户表只随 `eventbus` 组件下发（`export-clients --server`）。

连接地址：客户端与 EventBus 同机连 `tls://127.0.0.1:4222`，否则连 EventBus 所在主机的对外地址；主机证书同时覆盖两者（SAN 是主机 ID、登记的对外地址与内网地址、回环地址）。

## 证书（主机证书）

- 换主机、改地址都不用轮换：改 `moox.toml` 后先 `setup deploy --host <EventBus 所在主机>`，再逐台部署其余主机，角色文件里的地址跟着换，角色令牌不变。
- 主机证书 5 年，Admin 每天巡检，到期前 90 天起告警。续签：`setup deploy --host <主机>`（每次部署都会重新签发并覆盖证书文件），
  再 `setup restart --host <主机> --component eventbus --yes` 和 `--component host-gateway --yes` 让进程换上新证书；根证书不变，别的主机不用动。
- 部署只覆盖证书文件、不会因为证书变了重启 EventBus：不重启就继续用进程启动时读到的那张。
- 配置里宣告的对外地址不在证书里时 EventBus 不启动（日志 `does not cover advertised host`）：先核对 `moox.toml` 里这台主机的地址，再重新部署。
- 客户端日志里的 `certificate signed by unknown authority`、`certificate signature failure`：这台主机的 `secrets/moox-ca.crt` 不是当前控制机的根证书（控制机重装过），重新部署这台主机。

## 副本清单（漏一项即未完成）

每台主机的副本都在 `<部署根>/secrets/eventbus/`：这台主机上各组件要的 `<角色>.yaml`；EventBus 所在主机另有 `users.yaml`。证书不在这个目录里（见上一节）。

| 程序 | 副本 | 同步方式 |
|---|---|---|
| EventBus 自己 | EventBus 所在主机的 `users.yaml`、`internal-admin.yaml`，以及这台主机的主机证书 | `setup deploy --host <EventBus 所在主机>` |
| 每台主机的 Host Agent | `secrets/eventbus/hostagent-publisher.yaml` | 每台主机 `setup deploy --host <主机>` |
| 控制机、存储机、海外机上的服务 | 各自的 `<角色>.yaml` 与人人都有的 `metrics-publisher.yaml` | 每台主机 `setup deploy --host <主机>` |
| 因子引擎 | 因子机的 `factor-eventbus.yaml` | `setup deploy --host <因子机>` |

`setup deploy` 每次都重新导出并写入凭据：凭据变了的组件会被重启，没变的不动。只部署个别组件（`--component`）时只更新这些组件的角色文件。

## 新增角色（不轮换现有令牌）

给一个新的客户端（例如 `collector-eventbus`、事件中心的 `callback-dispatcher-eventbus`）开通事件总线权限，不需要轮换现有令牌；全程不打印令牌：

1. 角色与权限写在代码里（`modules/admin/cmd/cli/eventbus_credentials.go`），组件要哪个角色登记在服务目录的 `credentials`。
2. 先部署控制机（`setup deploy --host <控制机>`）：它执行 `eventbus-credentials ensure`，只给缺少的角色生成令牌，已有令牌原样保留。
3. 再部署 EventBus 所在主机：新的用户表（含新角色和按当前代码重新生成的 ACL）随 `eventbus` 组件下发并重启它；EventBus 重启会让所有客户端短暂重连。
   ACL 可能随代码增加或删除权限，部署前逐项核对权限差异与本次发布的消费者契约。
4. 最后部署使用方所在的主机，确认 JetStream 里出现它的持久消费者、日志没有 `Authorization Violation`。

Collector 的发布权限也会随版本多一个主题（`moox.event.collector.instrument.delist_scheduled.v1.>`），所以 EventBus 所在主机要先于 Collector 所在主机部署。

## 轮换时的强制顺序

1. 在控制机上执行 `rotate --credential <NATS用户名> --confirm`（数据库 `<部署根>/data/admin/admin.db`，主密钥 `<部署根>/secrets/admin-master.key`）。不要打印 token。
2. 先部署 EventBus 所在主机（`setup deploy --host <主机>`），再部署控制机，确认控制机上的客户端恢复。
3. 对 **moox.toml 里每一台主机** 执行 `setup deploy --host <主机>`，包括只出站的因子机。这就是 fan-out：每台都做完才算完成。
4. 验收全部通过后才收工。

## 验收（禁止凭“进程在跑”收工）

- 每台主机的 `secrets/moox-ca.crt` 的 SHA-256 与控制机的根证书一致。
- Host Agent：Monitor 收到新样本，日志没有 `Authorization Violation` / `certificate signature failure`。
- EventBus `ss`：`FIN-WAIT-2` 不因某一客户端 IP 持续堆积。
- Storage View：日志里没有 `delivery ack failed: nats: connection closed`。
- Monitor：Host snapshot 与 View 水位在轮换后继续前进。

## 常见借口

| 借口 | 现实 |
|---|---|
| 只换了二进制 | 手工换二进制不会带上新的 token，要 `setup deploy` |
| control 已经 export | 那只是导出；副本还在别的机器 |
| 上次能连，证书不用动 | EventBus 重启后用磁盘上当前的主机证书；客户端手里的根证书不是签它的那张（控制机重装过）就会 `signature failure` |
| Host Agent 不是这次发布目标 | 它仍连同一 EventBus；漏同步会泄漏 TCP 并拖死 View |
| 代码里已经有 trackingDialer | 那只能减轻泄漏，不能代替正确的根证书和 token |

## 禁止

- 把 token/私钥写进 argv、日志、git 或 skill 测试夹具以外的仓库文件。
- 为修连通性关闭 TLS 校验或改用 `nats://` 公网明文。
- 把跨主机 EventBus URL 擅自改成未签入证书的内网地址。主机证书里只有登记的对外地址、内网地址与回环地址；同机应使用回环，跨机使用登记的对外地址。外部 SCF 的入口（主机网关的外部入口）与此独立，见 `private-network.md`。
