# 腾讯云分地域网络

采集云函数（SCF）在各地域的 VPC 里运行，只经**同地域 Access 主机的私网地址**访问 MooX：
认领、写 Storage 都走 Access（`11004`），不直连 Storage，也不连 EventBus。不创建云联网（CCN）。

## moox.toml 怎么写

- `[scf_plan.spaces.markets]` 列出每个市场使用的 SCF 地域。
- 用到的每个地域都要有一台主机：`[hosts.<id>]` 配置了 `region` 与 `private_address`，并在 `[placements]` 里带 `access` 组件。
- `[scf_plan.regions.<region>]` 写该地域的 `vpc_id`、`subnet_id`；同地域有多台带私网地址的 Access 主机时，用 `access_host` 指定入口主机。
- 用不到的地域不要写进 `scf_plan.regions`，校验会拒绝。

`./bin/moox-cli setup validate --file ./moox.toml` 会检查以上约束：缺少同地域 Access 主机、缺私网地址、`access_host` 不在该地域时直接报错。

## 链路边界

| 链路 | 配置 | 规则 |
|---|---|---|
| SCF → Access | `MOOX_ACCESS_ADDRESS`、`MOOX_ACCESS_ID`、`MOOX_CALLER_KEY_ID` / `MOOX_CALLER_KEY` | 同地域 Access 的私网地址；密钥由 Admin 分配 |
| SCF 函数网络 | `scf_plan.regions.<region>` 的 VPC/子网 | 函数绑定该地域 VPC |
| 上游交易所 / CLS | `public_net_status` | 需要访问公网时为 `ENABLE` |
| EventBus | `MOOX_EVENTBUS_NATS_URL` | 采集函数不连 EventBus；其他客户端按证书 SAN 用公网入口 |

## 禁止

- 不创建或复用 CCN，不调用 `ModifyInstancesVpcAttribute`。
- 不把 SCF 指向其他地域的 Access 私网地址；跨地域私网不通。
- 不修改控制台代理、`MOOX_PUBLIC_HOST` 或 EventBus 目标来“配合”SCF 网络。
- CLI 不修改 `moox.toml`，不在日志或 JSON 中输出密码、SecretKey、签名密钥。

## 验收证据

- `setup inspect-scf --region <地域> --function <函数名>` 回读函数的 `vpc_id`、`subnet_id`、`public_net_status`、`access_address`；
- 函数日志里认领与写入成功，Storage 能读回刚写入的数据。
