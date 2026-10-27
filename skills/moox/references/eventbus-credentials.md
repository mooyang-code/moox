# EventBus 证书与口令同步

EventBus TLS CA、server 证书和每个 NATS role token 是**一份权威材料、多台机器上的多份副本**。
权威材料由 Admin 保存；原生单元在安装时从 Admin 导出自己需要的角色，写进发布目录。
**轮换未完成，直到每一个仍连 `tls://<EventBus公网IP>:4222` 的程序都拿到同一份 CA 和自己的 role 文件并成功重连。**

违反字面规则就是违反精神：只换二进制、只重启 EventBus、只更新 control，都不算同步完成。

## 何时必须走完 fan-out

出现任一情况时先读本文，再发布或宣称完成：

- `eventbus-credentials ensure|export|export-clients|rotate|rotate-tls`
- EventBus 公网 IP / SAN 变化，或 EventBus 换了主机（例如从控制机搬到存储机）
- 日志出现 `Authorization Violation`、`certificate signature failure`、`certificate verify failed`
- EventBus `:4222` 对单一客户端 IP 堆积 `FIN-WAIT-2`，或 Host Agent 因 NATS 失败反复退出

## 权威材料

生成与轮换只通过 `skills/moox/scripts/eventbus-credentials.sh`（Admin CLI）。
不要把 token 或私钥写进命令行、聊天、git 或任何制品。

`internal-admin` 只在控制机使用：Storage 上删除 JetStream consumer、清除数据集事件等维护操作由 Admin 用数据库里的同一份令牌代为执行（Storage 命令经网关调用 Admin），见 [`view-catchup.md`](view-catchup.md)。

`rotate --credential <role> --confirm` 会**立刻作废旧 token**。TLS CA 变化时所有客户端的 CA 都必须换。

`rotate-tls --confirm` 按部署记录里 EventBus 当前所在主机的地址，重新生成 CA 与服务端证书（角色令牌不变）。EventBus 的 TLS 身份绑定它的地址（CA 记录里的 `nats_url`），
换主机之后 `ensure`、`export`、`export-clients` 都会拒绝旧身份，必须先 `rotate-tls`。CA 私钥签完即丢弃，所以只能连 CA 一起换，所有客户端与 EventBus 单元随之重新部署。
broker 的证书、私钥和用户表只随 `eventbus` 单元下发（`export-clients --server`）。

连接地址：客户端与 EventBus 同机连 `tls://127.0.0.1:4222`，否则连 EventBus 所在主机的对外地址；证书同时覆盖两者。

## 副本清单（漏一项即未完成）

| 程序 | 副本位置 | 同步方式 |
|---|---|---|
| EventBus 自己 | `eventbus` 单元发布目录的 `eventbus/secrets/eventbus/`（含服务端证书、私钥、用户表） | `setup deploy-unit --profile eventbus --release <版本>` |
| control 上的服务 | control 各组件发布目录的 `secrets/eventbus/` | `setup bootstrap --stage control --release <版本>` |
| 每台主机的 Host Agent | `host` 单元发布目录的 `host-agent/config/eventbus.yaml` | 每台主机 `setup deploy-host --release <版本>` |
| Storage / Trade 等业务单元 | 该单元发布目录的 `<组件>/secrets/eventbus/` | `setup deploy-unit --profile <单元> --release <版本>` |
| Collector SCF / CloudFunction worker SCF | 函数环境 `MOOX_EVENTBUS_NATS_URL/USERNAME/PASSWORD` 与 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64` | **重新发布 SCF 包**；禁止 `MOOX_EVENTBUS_NATS_TLS_CA_FILE` |
| 外置因子引擎 | 引擎配置里的 `factor-eventbus` 角色文件 | 重新导出并重启引擎 |

[`native-release.md`](native-release.md) 的二进制发布不更新配置和凭据：只发布二进制，各主机会继续用旧 CA 和旧 token。

## 新增角色（不轮换现有令牌）

给一个新的客户端（例如 `collector-eventbus`）开通事件总线权限，不需要轮换 CA 和现有令牌，在控制机上这样做；全程不打印令牌：

1. 备份：Admin 数据库（`sqlite3` 的 backup，不要直接 `cp` 带 WAL 的库）、EventBus 的 `users.yaml` 与 `app.yaml`、要改的客户端 `app.yaml`，放进 `/data/moox/prod/eventbus-backup-<标签>-<时间>/`。
2. 用新构建的 `moox-admin-cli`（先 `native-release.sh install ... --no-restart` 装到控制机）执行
   `eventbus-credentials ensure --db-path <admin.db> --encryption-key-file /data/moox/identity/admin-master.key --node-id control`：它只给缺少的角色生成令牌，已有令牌原样保留。
3. `export` 到一个 0700 的临时目录，**比较新旧 `users.yaml` 里每个旧角色的令牌与权限块**（只比对，不打印令牌）；旧角色应当全部不变。线上 `users.yaml` 以现有文件为准，只追加新角色的块，不要整份覆盖：导出的块是按当前代码生成的，可能比线上多删了仍在使用的旧权限。
4. 新角色的凭据文件放到使用方发布目录的 `secrets/eventbus/`（0600，`ca_file` 指向该目录里的 `ca.pem`，`urls` 用公网 `tls://<EventBus公网>:4222`）；使用方配置里引用它。
5. 给 EventBus 配置新增流（如有），安装与代码一致的 `moox-eventbus` 并重启；EventBus 重启会让所有客户端短暂重连。
6. 重启使用方，确认 JetStream 里出现它的持久消费者、日志没有 `Authorization Violation`，再删除临时导出目录。

## 轮换时的强制顺序

1. `rotate` 后立刻 `export`。比较新 CA 的 SHA-256，不要打印 PEM 或 token。
2. 用 `setup release build` 构建一个版本，先装 EventBus 单元（`setup deploy-unit --profile eventbus`），再装 control，确认 control 上的客户端恢复。
3. 对 **moox.toml 里每一台主机** 安装 `host` 单元，再安装各业务单元。
4. 重新发布 SCF，重启外置因子引擎。
5. 验收全部通过后，才删除本机临时 `0600` 导出文件。

## 验收（禁止凭“进程在跑”收工）

- 每台客户端的 CA SHA-256 与 Admin 导出的一致。
- Host Agent：Monitor 收到新样本，日志没有 `Authorization Violation` / `certificate signature failure`。
- EventBus `ss`：`FIN-WAIT-2` 不因某一客户端 IP 持续堆积。
- Storage View：不再刷 `delivery ack failed: nats: connection closed`。
- Monitor：Host snapshot 与 View 水位在轮换后继续前进。
- SCF：新请求使用新的 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64`。

## 常见借口

| 借口 | 现实 |
|---|---|
| 只换了二进制 | 旧 CA/token 仍在各单元的发布目录里 |
| control 已经 export | 那只是导出；副本还在别的机器 |
| 上次能连，证书不用动 | EventBus 重启后会按当前 server 证书做 TLS；旧 CA 会 `signature failure` |
| Host Agent 不是这次发布目标 | 它仍连同一 EventBus；漏同步会泄漏 TCP 并拖死 View |
| 代码里已经有 trackingDialer | 那只能减轻泄漏，不能代替正确的 CA 和 token |

## 禁止

- 把 token/私钥写进 argv、日志、git 或 skill 测试夹具以外的仓库文件。
- 为修连通性关闭 TLS 校验或改用 `nats://` 公网明文。
- 把 EventBus URL 改成内网 IP。证书 SAN 是公网 IP，TLS 校验会失败。SCF 必须继续用公网 `tls://<公网>:4222`，见 `private-network.md`。
