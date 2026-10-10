# EventBus 证书与口令同步

EventBus TLS CA、server 证书和每个 NATS role token 是**一份权威材料、多台机器上的多份副本**。
权威材料在 control 的 `<部署根目录>/secrets/eventbus/`（默认 `/data/moox/prod/secrets/eventbus/`），其他主机上的
副本由部署命令从那里复制。
**发布或轮换未完成，直到每一个仍连 `tls://<EventBus公网IP>:4222` 的程序都拿到同一份 CA 和自己的 role 文件并成功重连。**

违反字面规则就是违反精神：只换二进制、只重启 EventBus、只更新 control，都不算同步完成。

## 何时必须走完 fan-out

出现任一情况时先读本文，再发布或宣称完成：

- `eventbus-credentials ensure|export|rotate`
- `eventbus` 组件重启后 TLS 或 `users.yaml` 被重写（含重置数据后重新 ensure）
- EventBus 公网 IP / SAN 变化
- 日志出现 `Authorization Violation`、`certificate signature failure`、`certificate verify failed`
- EventBus `:4222` 对单一客户端 IP 堆积 `FIN-WAIT-2`，或主机采集器 `startAtOnce` 因 NATS 失败退出

## 权威材料

control 上的 `secrets/eventbus/`（目录 mode `0700`，文件 `0600`）：

- `ca.pem`、`server.pem`、`server-key.pem`、`users.yaml`
- 每个 role 一份 YAML，role 名（`rotate --credential` 用它）与文件名的对应关系：

| role | 文件 |
| --- | --- |
| `eventbus-internal-admin` | `internal-admin.yaml` |
| `hostagent-publisher` | `hostagent-publisher.yaml` |
| `metrics-publisher` | `metrics-publisher.yaml` |
| `monitor-observability-consumer` | `monitor-observability.yaml` |
| `storage-eventbus` | `storage-eventbus.yaml` |
| `archive-eventbus` | `archive-eventbus.yaml` |
| `market-fetch-publisher` | `market-fetch-publisher.yaml` |
| `collector-market-fetch-consumer` | `collector-market-fetch-consumer.yaml` |
| `factor-eventbus` | `factor-eventbus.yaml` |
| `strategy-eventbus` | `strategy-eventbus.yaml` |
| `trade-eventbus` | `trade-eventbus.yaml` |

生成与轮换只通过 Admin CLI（在 control 的部署根目录下执行，路径都是相对它的）：

```bash
cd /data/moox/prod
current/bin/moox-admin-cli eventbus-credentials rotate \
  --db-path data/admin/admin.db --encryption-key-file secrets/admin-encryption-key \
  --credential <role> --confirm
current/bin/moox-admin-cli eventbus-credentials export \
  --db-path data/admin/admin.db --encryption-key-file secrets/admin-encryption-key \
  --nats-url tls://<control 公网地址>:4222 --output-dir secrets/eventbus
```

`<role>` 用上表第一列的 role 名。`eventbus` 组件每次启动前也会自动执行 `ensure` 和 `export`，所以重启它就能让
新材料落到 `secrets/eventbus/`。不要把 token 或私钥打进发布包、命令行、聊天或 git。

`internal-admin.yaml` 的 `urls` 是控制机回环 `tls://127.0.0.1:4222`。在 Storage 上删 JetStream consumer 时必须
`--eventbus-url tls://<EventBus公网IP>:4222`，见 [`view-catchup.md`](view-catchup.md)。

`rotate --credential <role> --confirm` 会**立刻作废旧 token**。TLS CA 变化时所有客户端的 `ca.pem` 都必须换。

## 副本清单（漏一项即未完成）

| 程序 | 副本位置 | 同步方式 |
|---|---|---|
| EventBus 本进程 | control 的 `secrets/eventbus/` 里的 `users.yaml` + server 证书 | 重启 `eventbus` 组件 |
| control 上的其他组件（Admin、Monitor、Collector、Strategy 等） | 同一目录里各自的 role YAML + `ca.pem` | `moox-cli setup service restart --host control --components <组件>` |
| **其他主机上的组件**（Storage、Trade、各主机的主机采集器、各组件的指标发布） | 该主机 `secrets/eventbus/<role>.yaml` + `ca.pem` | **`moox-cli setup deploy-host --host <主机> --reuse-binaries`**：从 control 复制该主机上各组件需要的文件，并重启受影响的组件；**`deploy-service` 只处理一个组件，不会更新主机上的其他组件** |
| 操作员机器上的 Factor Engine | 安装目录 `secrets/factor-eventbus.yaml` + 同目录的 CA 文件 | 从 control 复制 `factor-eventbus.yaml` 和 `ca.pem`，再用原来的参数重新执行 `scripts/deploy/deploy-factor-engine.sh`（可加 `--skip-build`） |
| Collector SCF / CloudNode worker SCF | 函数环境 `MOOX_EVENTBUS_NATS_URL/USERNAME/PASSWORD` 与 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64` | **重新发布 SCF 包**；禁止 `MOOX_EVENTBUS_NATS_TLS_CA_FILE` |

主机采集器是每台主机自动部署的主机组件，用 `hostagent-publisher.yaml` 发布主机快照。只发布 Storage 或 Trade 的二进制，或只在
control 上 `export`，**其他主机上的主机采集器会继续用旧 CA 和旧 token**，所以必须对部署表里的每一台主机都执行
`deploy-host`。

## 发布时的强制顺序

1. 在 control 上 `rotate`（如有）、`export` 或重启 `eventbus`。比较新 `ca.pem` 的 SHA-256，不要打印 PEM 或 token。
2. 若 `users.yaml` 或 server 证书变了：重启 `eventbus`，确认 control 上的客户端（Monitor、主机采集器）先恢复。
3. 对 **部署表里每一台其他主机** 执行 `moox-cli setup deploy-host --host <主机> --reuse-binaries`（Storage 主机也一样，
   `storage-view` 和 `storage-primary` 会被重启）；control 上的组件用 `setup service restart` 重启。
4. 操作员机器上的 Factor Engine 同步 `factor-eventbus.yaml` 和 `ca.pem` 并重启。
5. CA 或 `market-fetch-publisher` token 变了：重新发布相关 SCF。
6. 验收全部通过后，才删除本机临时 `0600` 导出文件。

## 验收（禁止凭“进程在跑”收工）

- 每台主机 `secrets/eventbus/ca.pem` 的 SHA-256 与 control 的相同。
- `moox-cli setup service status --host <主机>` 里全部组件都是「运行中 … 就绪」；`logs/<组件>/stdout.log` 里没有
  `Authorization Violation` / `certificate signature failure`；主机采集器日志出现 `sample.timer launch success`。
- EventBus `ss`：`FIN-WAIT-2` 不因某一客户端 IP 持续堆积。
- Storage View：不再刷 `delivery ack failed: nats: connection closed`。
- Monitor：Host snapshot 与 View 水位在轮换后继续前进。
- SCF：新请求使用新的 `MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64`。

## 常见借口

| 借口 | 现实 |
|---|---|
| 只换了二进制 | 旧 CA/token 仍在各主机的 `secrets/eventbus/` |
| control 已经 export | 那只是权威目录；副本还在别的机器 |
| 上次能连，证书不用动 | EventBus 重启后会按当前 server 证书做 TLS；旧 CA 会 `signature failure` |
| 只 `deploy-service` 了一个组件 | 同一台主机上其他组件的 role 文件不会更新，必须 `deploy-host` |
| 主机采集器不是这次发布目标 | 它仍连同一 EventBus；漏同步会泄漏 TCP 并拖死 View |
| 代码里已经有 trackingDialer | 那只能减轻泄漏，不能代替正确的 CA 和 token |

## 禁止

- 把 token/私钥写进 argv、日志、git，或测试夹具以外的仓库文件。
- 为修连通性关闭 TLS 校验或改用 `nats://` 公网明文。
- 把 EventBus URL 改成内网 IP。证书 SAN 是公网 IP，TLS 校验会失败。SCF 必须继续用公网 `tls://<公网>:4222`，见
  [`private-network.md`](private-network.md)。
