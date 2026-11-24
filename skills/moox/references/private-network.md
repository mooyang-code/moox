# 腾讯云分地域网络

采集云函数（SCF）写 Storage 走入口主机网关的外部入口（`11004`），不直连 Storage，也不连 EventBus，不创建云联网（CCN）。每个地域有两种访问方式：

- **vpc（默认）**：函数在地域的 VPC 里运行，经**同地域入口主机的私网地址**访问外部入口。
- **public**：用于没有 MooX 主机的地域。函数不绑 VPC，经**公网**连某台入口主机（一般是存储机）的公网地址加 `11004`。用它可以把东方财富的请求分散到更多地域的出口 IP 上。

## moox.toml 怎么写

- `[scf_plan.spaces.markets]` 列出每个市场使用的 SCF 地域。
- vpc 地域要有一台同地域的主机：`[hosts.<id>]` 配置了 `region` 与 `private_address`，并写了 `external_entry = true`（这台主机的网关开外部入口）。
- 入口主机必须是**普通云服务器（CVM）**，它所在的 VPC 就是 `scf_plan.regions.<region>` 里的 `vpc_id`。轻量应用服务器的内网在腾讯云内部单独的网络里，
  云函数绑不上（建函数报 `ResourceNotFound.Vpc`），要打通只能用云联网，而这里禁止云联网。2026-11-23 试过把广州的控制机（轻量服务器）开成入口，走不通。
  判断办法：按公网 IP 查 `DescribeInstances` 查得到就是 CVM，查不到多半是轻量服务器。
- vpc 地域：`[scf_plan.regions.<region>]` 写该地域的 `vpc_id`、`subnet_id`；同地域有多台带私网地址的入口主机时，用 `entry_host` 指定。
- public 地域：`[scf_plan.regions.<region>]` 写 `network = "public"` 和 `entry_host`（必填，任何地域写了 `external_entry = true`、有公网 `address` 的主机），不能写 `vpc_id`、`subnet_id`。入口主机的安全组要对公网放行 `11004`（`setup firewall` 不管这个端口）。
- `network` 按地域生效，各空间共用：同一地域不能一部分空间走 VPC、一部分走公网。
- 公网方式的代价：外部入口是 tRPC 明文加签名，签名带防重放，公网上能看到的只有写入的行情内容；跨地域公网偶发丢包时写入会重试，最坏是某一期晚到或缺几只，由补采补上。
- 没被市场使用的地域也可以留在 `scf_plan.regions` 里，照样渲染网络与入口。**撤掉一个地域**按这个顺序：
  1. 从市场（和 `backfill_region`）里去掉它，保留 `[scf_plan.regions.<region>]`，部署 Collector 配置；退役函数在计划推进时还要用这个地域的网络与入口，缺了新计划建不出来。
  2. 确认新计划已生效（其余地域补建了函数、每期按时到齐）。这个地域的函数此时只停了定时器，没有删。
  3. 到腾讯云控制台删掉这个地域的函数和命名空间（这时它已不在计划的地域里，`collector function delete --region` 会拒绝）。不能先删函数：还在计划里的函数删了会被立即重建。
  4. 删掉 `[scf_plan.regions.<region>]` 并部署。

`./bin/moox-cli setup validate --file ./moox.toml` 会检查以上约束：vpc 地域缺少同地域入口主机、缺私网地址、`entry_host` 不在该地域，public 地域缺 `entry_host`、写了 VPC、入口主机没开外部入口或没有公网地址时直接报错。

## 链路边界

| 链路 | 配置 | 规则 |
|---|---|---|
| SCF → 外部入口 | `MOOX_ENTRY_ADDRESS`、`MOOX_ENTRY_HOST`、`MOOX_CALLER_KEY_ID` / `MOOX_CALLER_KEY` | vpc 地域是同地域入口主机的私网地址加 `11004`，public 地域是 `entry_host` 的公网地址加 `11004`；入口主机 ID；`scf-collector` 的私钥在控制机上签发，Collector 写进函数环境 |
| SCF 函数网络 | `scf_plan.regions.<region>` 的 `network`、VPC/子网 | vpc 地域绑定该地域 VPC；public 地域不绑（原来绑着的会被清掉） |
| 上游交易所 / CLS | `public_net_status` | 需要访问公网时为 `ENABLE` |

## 禁止

- 不创建或复用 CCN，不调用 `ModifyInstancesVpcAttribute`。
- 不把 SCF 指向其他地域入口主机的私网地址；跨地域私网不通。
- 不修改管理台入口（Admin 的 9527）或 EventBus 目标来“配合”SCF 网络。
- SCF 网络与部署命令不修改 `moox.toml`；配置变更由 `config init/edit/set/restore` 等专用命令负责。日志和 JSON 不输出密码、SecretKey、签名密钥。

## 验收证据

- `setup inspect-scf --region <地域> --function <函数名>` 回读函数的 `vpc_id`、`subnet_id`、`public_net_status`、`entry_address`（public 地域 `vpc_id` 为空、`entry_address` 是入口主机公网地址）；`collector function list --drift` 不比较 VPC 和入口地址；
- 函数日志里认领与写入成功，Storage 能读回刚写入的数据。
