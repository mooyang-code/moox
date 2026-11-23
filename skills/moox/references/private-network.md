# 腾讯云分地域网络

采集云函数（SCF）在各地域的 VPC 里运行，只经**同地域入口主机的私网地址**访问 MooX：
写 Storage 走入口主机网关的外部入口（`11004`），不直连 Storage，也不连 EventBus。不创建云联网（CCN）。

## moox.toml 怎么写

- `[scf_plan.spaces.markets]` 列出每个市场使用的 SCF 地域。
- 用到的每个地域都要有一台主机：`[hosts.<id>]` 配置了 `region` 与 `private_address`，并写了 `external_entry = true`（这台主机的网关开外部入口）。
- 入口主机必须是**普通云服务器（CVM）**，它所在的 VPC 就是 `scf_plan.regions.<region>` 里的 `vpc_id`。轻量应用服务器的内网在腾讯云内部单独的网络里，
  云函数绑不上（建函数报 `ResourceNotFound.Vpc`），要打通只能用云联网，而这里禁止云联网。2026-11-23 试过把广州的控制机（轻量服务器）开成入口，走不通。
  判断办法：按公网 IP 查 `DescribeInstances` 查得到就是 CVM，查不到多半是轻量服务器。
- `[scf_plan.regions.<region>]` 写该地域的 `vpc_id`、`subnet_id`；同地域有多台带私网地址的入口主机时，用 `entry_host` 指定。
- 用不到的地域不要写进 `scf_plan.regions`，校验会拒绝。

`./bin/moox-cli setup validate --file ./moox.toml` 会检查以上约束：缺少同地域入口主机、缺私网地址、`entry_host` 不在该地域时直接报错。

## 链路边界

| 链路 | 配置 | 规则 |
|---|---|---|
| SCF → 外部入口 | `MOOX_ENTRY_ADDRESS`、`MOOX_ENTRY_HOST`、`MOOX_CALLER_KEY_ID` / `MOOX_CALLER_KEY` | 同地域入口主机的私网地址加 `11004`、入口主机 ID；`scf-collector` 的私钥在控制机上签发，Collector 写进函数环境 |
| SCF 函数网络 | `scf_plan.regions.<region>` 的 VPC/子网 | 函数绑定该地域 VPC |
| 上游交易所 / CLS | `public_net_status` | 需要访问公网时为 `ENABLE` |

## 禁止

- 不创建或复用 CCN，不调用 `ModifyInstancesVpcAttribute`。
- 不把 SCF 指向其他地域入口主机的私网地址；跨地域私网不通。
- 不修改管理台入口（Admin 的 9527）、`MOOX_PUBLIC_HOST` 或 EventBus 目标来“配合”SCF 网络。
- SCF 网络与部署命令不修改 `moox.toml`；配置变更由 `config init/edit/set/restore` 等专用命令负责。日志和 JSON 不输出密码、SecretKey、签名密钥。

## 验收证据

- `setup inspect-scf --region <地域> --function <函数名>` 回读函数的 `vpc_id`、`subnet_id`、`public_net_status`、`entry_address`；
- 函数日志里认领与写入成功，Storage 能读回刚写入的数据。
