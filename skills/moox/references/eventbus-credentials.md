# EventBus 证书与口令同步

EventBus TLS CA、server 证书和每个 NATS role token 是**一份权威材料、多台机器上的多份副本**。
控制面 `export` 只更新 `~/.config/moox/eventbus/`（通常在 control 主机）。
**发布或轮换未完成，直到每一个仍连 `tls://<EventBus公网IP>:4222` 的程序都拿到同一份 CA 和自己的 role 文件并成功重连。**

违反字面规则就是违反精神：只换二进制、只重启 EventBus、只更新 control，都不算同步完成。

## 何时必须走完 fan-out

出现任一情况时先读本文，再发布或宣称完成：

- `eventbus-credentials ensure|export|rotate`
- `deploy-control` 且 EventBus TLS/`users.yaml` 被重写（含 `--reset-data` 后重新 ensure）
- EventBus 公网 IP / SAN 变化
- 日志出现 `Authorization Violation`、`certificate signature failure`、`certificate verify failed`
- EventBus `:4222` 对单一客户端 IP 堆积 `FIN-WAIT-2`，或 Host Agent `startAtOnce` 因 NATS 失败退出

## 权威材料

控制主机（默认 `/home/ubuntu/.config/moox/eventbus/`，mode `0600`/`0700`）：

- `ca.pem`、`server.pem`、`server-key.pem`、`users.yaml`
- 每个 role 一份 YAML：`hostagent-publisher.yaml`、`storage-eventbus.yaml`、`trade-eventbus.yaml`、`strategy-eventbus.yaml`、`factor-eventbus.yaml`、`monitor-observability.yaml`、`archive-eventbus.yaml`、`cloudnode-eventbus.yaml`、`cloudnode-worker.yaml`、`market-fetch-publisher.yaml`、`collector-market-fetch-consumer.yaml`、`metrics-publisher.yaml`、`internal-admin.yaml`

生成与轮换只通过 `skills/moox/scripts/eventbus-credentials.sh`（Admin CLI）。
不要把 token 或私钥打进 release archive、ZIP、命令行、聊天或 git。

`internal-admin.yaml` 的 `urls` 是控制机回环 `tls://127.0.0.1:4222`。在 Storage 上删 JetStream consumer 时必须 `--eventbus-url tls://<EventBus公网IP>:4222`，见 [`view-catchup.md`](view-catchup.md)。

`rotate --credential <role> --confirm` 会**立刻作废旧 token**。TLS CA 变化时所有客户端的 `ca.pem` 都必须换。

## 副本清单（漏一项即未完成）

| 程序 | 副本位置 | 同步方式 |
|---|---|---|
| EventBus 本进程 | 同上目录的 `users.yaml` + server 证书 | `deploy-control` / EventBus 重启 |
| Control 包内 Host Agent | `~/.config/moox/eventbus/hostagent-publisher.yaml` + `ca.pem` | `deploy-control` |
| **其他主机 user-systemd Host Agent** | `~/.config/moox/hostagent/eventbus.yaml` + `ca.pem` | **`hostagent-deploy.sh`（可 `--credentials-only`）**；**不被** `deploy-control` / `deploy-service` 覆盖 |
| Storage（常在独立主机） | `~/.config/moox/eventbus/storage-eventbus.yaml` + `ca.pem` | `setup deploy-storage`（从 control 拷贝） |
| Trade / Strategy / Factor / Monitor / Archive / CloudNode | 该主机 `~/.config/moox/eventbus/<role>.yaml` + `ca.pem` | 对应 `deploy-service` 或该主机的 control 包启动路径 |
| Collector SCF / CloudNode worker SCF | 函数环境 `MOOX_EVENTBUS_NATS_URL/USERNAME/PASSWORD` 与 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64` | **重新发布 SCF 包**；禁止 `MOOX_EVENTBUS_NATS_TLS_CA_FILE` |

Host Agent 在 compute 节点上是独立 rootless 安装。只发布 Storage/Trade 二进制或只 `export` control，**香港/其他 CVM 上的 Host Agent 会继续用旧 CA 和旧 token**。

## 发布时的强制顺序

1. 在 control 上 `ensure`/`export`（或 `rotate` 后立刻 `export`）。比较新 `ca.pem` 的 SHA-256，不要打印 PEM 或 token。
2. 若 `users.yaml` 或 server 证书变了：重启 EventBus，确认本机客户端（Monitor/Control Host Agent）先恢复。
3. `setup deploy-storage`（或等价地把 `storage-eventbus.yaml` + `ca.pem` 装到 Storage 主机），再重启 `storage-view` / `storage-primary`。
4. 对 **moox.toml 里每一台跑了 Host Agent 的主机** 执行：
   ```bash
   ./skills/moox/scripts/hostagent-deploy.sh user@host --credentials-only \
     --eventbus-file /path/hostagent-publisher.yaml \
     --ca-file /path/ca.pem
   ```
   二进制也要升级时用完整 archive 参数，不要省略 `--eventbus-file` / `--ca-file`。
5. 对每台仍连 EventBus 的业务主机发布或重启对应服务。
6. CA 或 `market-fetch-publisher` / `cloudnode-worker` token 变了：重发相关 SCF。
7. 验收全部通过后，才删除本机临时 `0600` 导出文件。

## 验收（禁止凭“进程在跑”收工）

- 每台客户端的 `ca.pem` SHA-256 与 control 的 `ca.pem` 相同。
- Host Agent：`systemctl --user is-active` 为 `active`，日志出现 `sample.timer launch success`，没有 `Authorization Violation` / `certificate signature failure`。
- EventBus `ss`：`FIN-WAIT-2` 不因某一客户端 IP 持续堆积。
- Storage View：不再刷 `delivery ack failed: nats: connection closed`。
- Monitor：Host snapshot 与 View 水位在轮换后继续前进。
- SCF：新请求使用新的 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64`。

## 常见借口

| 借口 | 现实 |
|---|---|
| 只换了二进制 | 旧 CA/token 仍在 `~/.config/moox/hostagent/` |
| control 已经 export | 那只是权威目录；副本还在别的机器 |
| 上次能连，证书不用动 | EventBus 重启后会按当前 server 证书做 TLS；旧 CA 会 `signature failure` |
| deploy-service 包内没有 secrets | 远端 `~/.config/moox/eventbus/` 仍是旧的，必须另行同步 |
| Host Agent 不是这次发布目标 | 它仍连同一 EventBus；漏同步会泄漏 TCP 并拖死 View |
| 代码里已经有 trackingDialer | 那只能减轻泄漏，不能代替正确的 CA 和 token |

## 禁止

- 把 token/私钥写进 argv、`SSHPASS` 以外的日志、git、skill 测试夹具以外的仓库文件。
- 为修连通性关闭 TLS 校验或改用 `nats://` 公网明文。
- 把 EventBus URL 改成内网 IP。证书 SAN 是公网 IP，TLS 校验会失败。
