# 腾讯云公网通信

Control / Storage / Compute / 因子引擎 / SCF 之间一律走**公网 IP**。
不再创建云联网，不要用内网 IP 改写运行时。

命令入口仍是 `moox-cli setup private-network`（别名 `moox-cli ops tencent private-network`），
用途只剩：把存量 SCF 解绑 VPC、把主机 `runtime.env` 改回公网 Storage RPC。

先读 `moox.toml.example` 里的 `storage_gateway_host` 注释。`storage_private_gateway_host`
即使写在用户 `moox.toml` 里也会被忽略，不要再填。

## 主机与 SCF 通信一律走公网

**SCF、主机 Collector、因子引擎与 MooX 服务、上游交易所、CLS 之间的全部通信走公网，禁止云联网 CCN / 函数 VPC / 主机内网 IP。**

| 链路 | 函数环境 / 配置 | 必须是 |
|---|---|---|
| Storage 原生 tRPC | `MOOX_STORAGE_RPC_GATEWAY_TARGET` / `MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET` | `ip://<Storage 公网 IP>:11003`，即 `storage_gateway_host` |
| EventBus | `MOOX_EVENTBUS_NATS_URL` | `tls://<EventBus 公网 IP>:4222`；证书 SAN 是公网 IP |
| 其它 MooX 服务（如 Trade DNS） | 对应 `ip://` 目标 | 该主机公网 IP，不要填内网 |
| 上游交易所 / CLS | `public_net_status` | `ENABLE`；函数不绑 VPC |

### 禁止

- 创建或复用 CCN、给主机打内网、给 SCF 建专用 VPC。
- 按国内/海外给 SCF 选 Storage 内网 IP，或把 `storage_private_gateway_host` 发布进函数。
- 把主机 Collector / 因子引擎 `runtime.env` 改成内网 Storage IP。
- 把 EventBus / Caddy / `MOOX_PUBLIC_HOST` 改成内网 IP。EventBus 改内网会 TLS 失败，见 `eventbus-credentials.md`。
- 解绑函数 VPC 时，把主机 CVM VPC 从 CCN detach。

存量函数若仍绑着 VPC，用 `--restore-scf-public`（旧名 `--update-scf-gateway` 等价于它）：
改回公网网关、`PublicNet=ENABLE`、清空函数 VPC；只 detach **专用 SCF VPC**。

存量主机若 `runtime.env` 仍指向内网 Storage，用 `--rewrite-runtime` 改回公网 IP 并重启 Collector。

## 何时使用 private-network

- 已有 SCF 仍走 CCN 内网，需要改回公网（`--restore-scf-public`）
- 主机 Collector / 因子引擎仍指向内网 Storage IP，需要改回公网（`--rewrite-runtime`）
- 用户提到 CCN 费用、SCF 公网、SCF VPC、内网组网：告诉他们已经改为公网，不要再组网

不要用它做：改 EventBus 证书、Caddy/`MOOX_PUBLIC_HOST`、新建云联网、把 SCF 再绑回 CCN。

## 红线

- 不要调用 `ModifyInstancesVpcAttribute`（会停机）。
- SSH 和控制台入口继续用公网 IP。
- `storage_gateway_host` 保持 **公网** Storage IP。SCF、主机 Collector、因子引擎只读这个公网地址。
- 不要再填写 `storage_private_gateway_host`；CLI 会忽略该字段。
- 不要改 EventBus `tls://<公网>:4222`、Caddy、`MOOX_PUBLIC_HOST`、compute-1 的公网入口。
- CLI **不改** `moox.toml`。Agent 也不得 `cat`/`source` 整个文件，或把密码、`secret_id`、`secret_key` 打进对话。
- 解绑 SCF VPC 时，不得把主机 CVM VPC 从 CCN 卸下。

## 固定步骤

在仓库根目录执行。先 dry-run，确认 JSON 后再写云。

```bash
./bin/moox-cli setup private-network --file ./moox.toml --dry-run
./bin/moox-cli setup private-network --file ./moox.toml --restore-scf-public --dry-run
./bin/moox-cli setup private-network --file ./moox.toml --restore-scf-public
./bin/moox-cli setup private-network --file ./moox.toml --rewrite-runtime --skip-probe
./bin/moox-cli setup private-network --file ./moox.toml --probe-only
```

`--restore-scf-public` 把各地域函数的 `MOOX_STORAGE_RPC_GATEWAY_TARGET` 改回 Storage 公网 IP，开启公网出口，并解除函数 VPC；专用 SCF VPC 会从 CCN detach。`--update-scf-gateway` 已废弃，等价于 `--restore-scf-public`。

常用开关：`--skip-scf`（默认 true）、`--skip-hosts`（默认 true）、`--skip-probe`、`--rewrite-runtime`、`--probe-regions`。
`--rewrite-runtime` 只把 Control 上 `runtime.env` 的 Storage RPC 改回公网并重启 Collector，不会改 EventBus，更不会改 SCF。

命令不会创建云联网，也不会放通内网 CIDR 防火墙。`--probe-only` 探测的是公网端口。

## 填写 moox.toml

不要写 `storage_private_gateway_host`。只确认每个 `[[scf_fetcher.spaces]]` 的 `storage_gateway_host` 是 Storage 公网 IP：

```toml
storage_gateway_host = "203.0.113.11"
```

```bash
./bin/moox-cli setup validate --file ./moox.toml
```

校验成功后只回显每个 space 的 `space_id`、`storage_gateway_host`。不要打印其它字段。
后续 `collector function publish` 与因子引擎 runtime 都写成 `storage_gateway_host` 公网地址。

## 验收

`--probe-only` 的 `probes` 里，`expect=open` 的项必须是 `open`。探测目标是公网 IP。

业务侧：

- Control / Compute / 因子引擎 → Storage 公网 `11003` 应可达；主机 Collector/Factor 的 Storage gateway 必须是 `ip://<storage-public-ip>:11003`。
- 全部 SCF `MOOX_STORAGE_RPC_GATEWAY_TARGET` 应是公网 Storage IP；`MOOX_EVENTBUS_NATS_URL` 应是公网 EventBus；函数不应再绑 VPC。

`runtime_config_hits` 里出现 EventBus、Caddy、`MOOX_PUBLIC_HOST`、trade gateway 的公网 IP 是预期残留，不要据此改成内网。
