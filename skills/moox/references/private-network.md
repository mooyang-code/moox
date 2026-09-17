# 腾讯云分地域网络

Storage 数据面按 SCF 与 Storage 的腾讯地域选择路径：同地域优先私网，跨地域使用公网。
不创建云联网（CCN）。`storage_host` 是 Storage 位置的来源，CLI 会查询其真实地域、可用区、
VPC、子网和私网 IP；`storage_gateway_host` 始终保留为跨地域和故障回退地址。

计划命令：

```bash
moox-cli setup scf-network-plan --file ./moox.toml
```

## 路由规则

| 条件 | Storage target | SCF VPC |
|---|---|---|
| 同地域且私网 IP/VPC/子网齐全 | `ip://<private-ip>:11003` | 绑定 Storage VPC/子网 |
| 跨地域或私网条件不完整 | `ip://<public-ip>:11003` | 不绑定 Storage VPC |

`public_net_status` 仅表示 SCF 是否需要访问交易所等公网服务，与 Storage target 独立。
同地域私网绑定默认要求 Storage 与 SCF 使用同一腾讯云账号且 VPC/子网对 SCF 可见；
跨账号或权限不足时按公网回退，不为此创建 CCN。

## 链路边界

| 链路 | 配置 | 规则 |
|---|---|---|
| Storage 原生 tRPC | `MOOX_STORAGE_RPC_GATEWAY_TARGET` / `MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET` | 同地域私网或跨地域公网 `ip://<host>:11003` |
| EventBus | `MOOX_EVENTBUS_NATS_URL` | 按 EventBus 证书 SAN 选择可验证的公网/专用入口 |
| 上游交易所 / CLS | `public_net_status` | 需要访问公网时为 `ENABLE` |

## 禁止

- 不为跨地域 Storage 链路创建或复用 CCN。
- 不把 Storage 私网 IP 用于跨地域 SCF；私网地址只在同地域且 VPC/子网匹配时使用。
- 不修改 Caddy、`MOOX_PUBLIC_HOST` 或 EventBus 目标来“配合”Storage 私网。
- 不调用 `ModifyInstancesVpcAttribute`，不把主机 CVM VPC 从 CCN detach。
- CLI 不修改 `moox.toml`，不在日志或 JSON 中输出密码、SecretKey、签名密钥。

## 初始化与发布

1. `setup init` 在 Admin/Metadata 写入前生成 `scf_routes`；也可单独运行
   `setup scf-network-plan` 检查 Storage 实例信息。
2. `collector function publish submit` 默认启用 `--same-region-first`，先发布 Storage
   同地域 Invoke canary，并完整占满该地域配置的 `function_count`（含 Timer fleet 回读）后，
   才开始其他地域；同地域任一步骤失败都会停止后续地域。
   “占满”按 manifest 中该地域的 `function_count` 解释；发布器不会自动搬迁其他地域的
   配额，需由用户在配置中调整目标数量。
3. 同地域 NodeCreateItem 必须同时包含私网 Storage target、`vpc_id` 和 `subnet_id`；
   跨地域 NodeCreateItem 不包含 Storage VPC 配置。
4. canary 必须验证 Provider HTTP 200、Storage 写入和 Storage 读回；仅有“函数已创建”不算
   发布成功。可用 `setup inspect-scf` 回读云端实际配置。

## 存量迁移

`setup private-network --restore-scf-public` 仅用于清理旧部署留下的 VPC/私网绑定；它不是
新系统的默认路由。`--rewrite-runtime` 只在明确需要将常驻主机运行时切换到公网回退地址时
使用。新发布不要通过手工修改 `storage_gateway_host` 绕过地域判断。

## 配置示例

```toml
storage_gateway_host = "203.0.113.11"
storage_private_gateway_host = "10.0.0.5"
```

私网字段是提示信息，最终是否采用由腾讯实例发现结果决定；缺少 VPC/子网/私网任一条件时
自动回退公网。

## 验收证据

路由计划是只读决策，不代表云端已经绑定。验收必须保存：

- `scf-network-plan` 输出的 Storage region/zone/VPC/subnet 和 routes；
- `setup inspect-scf` 的函数 `vpc_id`、`subnet_id`、`public_net_status`、Storage target；
- Invoke canary 的 SCF 日志、Provider HTTP 200、Storage 写入和读回结果。
