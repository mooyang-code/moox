# 腾讯云分地域网络

SCF 采集函数只经**外部接入**（`moox-access`，端口 `11004`）访问 MooX。按函数与外部接入所在的腾讯云地域选择路径：
同地域优先走私网，跨地域走公网。不创建云联网（CCN）。

外部接入部署在哪些主机，由 `moox.toml` 的 `[placements]` 决定（例如 `storage` 和 `compute-1`）；这些主机的
`private_address` 与 `region` 是选路的依据，CLI 会再向腾讯云查询它们真实的地域、VPC、子网和私网 IP。没有部署外部接入的
主机不需要填写这两项。

计划命令（只读）：

```bash
moox-cli setup scf-network-plan --file ./moox.toml
moox-cli setup scf-network-plan --file ./moox.toml --region ap-hongkong
```

## 路由规则

| 条件 | 外部接入地址（`MOOX_ACCESS_ADDRESS`） | SCF VPC |
|---|---|---|
| 与函数同地域的主机上有外部接入，且私网 IP/VPC/子网齐全 | `<私网 IP>:11004`，实例 ID `access@<主机>` | 绑定该主机所在的 VPC/子网 |
| 同地域没有外部接入，或私网条件不完整 | `access@storage` 的公网地址 `<公网 IP>:11004` | 不绑定 VPC |

`public_net_status` 只表示 SCF 是否需要访问交易所等公网服务，与访问外部接入的路径无关。同地域私网绑定默认要求外部接入
主机与 SCF 使用同一腾讯云账号，并且 VPC/子网对 SCF 可见；跨账号或权限不足时按公网回退，不为此创建 CCN。

## 链路边界

| 链路 | 配置 | 规则 |
|---|---|---|
| SCF → MooX 服务 | `MOOX_ACCESS_ADDRESS`、`MOOX_ACCESS_ID`、`MOOX_CALLER`、`MOOX_CALLER_KEY` | 只经外部接入；同地域私网或跨地域公网，由 Collector 经 CloudNode 写进函数环境，代码包里不带地址和凭据 |
| 消息总线 | `MOOX_EVENTBUS_NATS_URL` | 按消息总线证书 SAN 选择可验证的公网地址，保持 `tls://<公网>:4222` |
| 上游交易所、CLS | `public_net_status` | 需要访问公网时为 `ENABLE` |
| 国内连不上的交易所接口 | `[egress_proxy]` | Collector 自己的请求经香港的出口代理；SCF 的 DNS 快照由出口代理解析 |

## 禁止

- 不为跨地域的外部接入链路创建或复用 CCN。
- 不把外部接入的私网 IP 用于跨地域 SCF；私网地址只在同地域且 VPC/子网匹配时使用。
- 不修改控制台代理、`MOOX_PUBLIC_HOST` 或消息总线地址来“配合”私网。
- 不调用 `ModifyInstancesVpcAttribute`，不把主机 CVM 的 VPC 从 CCN detach。
- CLI 不修改 `moox.toml`，不在日志或 JSON 中输出密码、SecretKey、签名密钥。

## 初始化与发布

1. `setup init` 在 Admin 和元数据写入前生成 `scf_routes`；也可以单独运行 `setup scf-network-plan` 检查。
2. `collector function publish submit` 默认启用 `--same-region-first`，先发布 `access@storage` 同地域的 Invoke canary，
   并完整占满该地域配置的 `function_count`（含 Timer fleet 回读）后，才开始其他地域；同地域任一步骤失败都会停止后续地域。
   “占满”按 `moox.toml` 中该地域的 `function_count` 解释；发布器不会自动搬迁其他地域的配额，需要由用户在配置中调整。
3. 与外部接入同地域的函数，创建时必须同时带上外部接入的私网地址、`vpc_id` 和 `subnet_id`；其他地域的函数不带 VPC 配置。
   已经存在的函数可以用 `setup attach-scf-vpc --function <名称> --region <地域> --namespace <命名空间> --vpc-id <VPC> --subnet-id <子网>`
   绑定。
4. canary 必须验证 Provider HTTP 200、Storage 写入和 Storage 读回；仅有“函数已创建”不算发布成功。可用
   `setup inspect-scf` 回读云端实际配置。

## 验收证据

路由计划是只读决策，不代表云端已经绑定。验收必须保存：

- `scf-network-plan` 输出的每个地域的 `network`、`access_id`、`access_address`、`vpc_id`、`subnet_id` 和 `reason`；
- `setup inspect-scf` 的函数 `vpc_id`、`subnet_id`、`public_net_status` 和外部接入地址；
- Invoke canary 的 SCF 日志、Provider HTTP 200、Storage 写入和读回结果。

## setup private-network

```bash
./bin/moox-cli setup validate --file ./moox.toml
moox-cli setup private-network --file ./moox.toml --dry-run      # 只发现拓扑，不做端口探测
moox-cli setup private-network --file ./moox.toml                # 发现拓扑，并从每台主机探测其他主机的公网端口
moox-cli setup private-network --file ./moox.toml --skip-probe   # 跳过 SSH 端口探测
```

命令只读，不创建云联网，不修改主机或 SCF，也不会调用 `ModifyInstancesVpcAttribute`。可用 `--probe-regions` 额外探测其他地域。
