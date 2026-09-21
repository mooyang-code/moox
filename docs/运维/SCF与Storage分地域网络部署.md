# SCF 与 Storage 分地域网络部署

本文记录 MooX 新系统的默认部署规则。`moox.toml` 是部署输入，`storage_host` 是 Storage
位置的唯一来源；CLI 会向腾讯云查询该主机的真实地域、可用区、VPC、子网和私网地址。
CLI 不修改 `moox.toml`，也不会因为路由检查自动创建云联网（CCN）。

## 路由规则

| SCF 与 Storage | Storage 条件 | Storage 数据面地址 | SCF 网络配置 |
| --- | --- | --- | --- |
| 同一腾讯地域 | 有私网 IP、VPC ID、子网 ID | `ip://<private-ip>:11003` | 绑定 Storage 的 VPC/子网 |
| 不同地域 | 任意 | `ip://<public-ip>:11003` | 不绑定 Storage VPC |
| 同一地域但私网信息不完整 | 缺少任一私网条件 | `ip://<public-ip>:11003` | 不绑定 Storage VPC |

`public_net_status` 只控制 SCF 访问交易所等公网服务，和 Storage 数据面选私网还是公网
相互独立。需要公网出口的采集函数仍应设置为 `ENABLE`。可用区是否相同只能以腾讯云
实例查询结果为准；不能从地域名称推断可用区相同。

私网绑定默认假设 Storage 与 SCF 使用同一腾讯云账号，且该账号允许 SCF 使用 Storage
所在 VPC/子网。若账号不同、权限不足或跨账号 VPC 不可见，路由计划应按公网回退执行，
不要为了强行私网而引入 CCN；可通过显式公网 canary 先验证链路。

## 初始化与发布

1. 在 `moox.toml` 中配置 `storage_host`，并确保该主机引用的 HostCatalog 记录包含
   `provider = "tencent"`。每个 SCF Space 继续配置 `storage_gateway_host` 作为公网
   回退地址；有私网地址时可配置 `storage_private_gateway_host`，但不需要手工填写 VPC
   或子网 ID。
2. 部署 Storage 并确认 `11003` 数据面和 `11012` 健康检查可用。
3. 初始化时，`setup init` 在 Admin/Metadata 写入前执行只读路由发现，并在结果中返回
   `scf_routes` 和 `storage_region_configured`。若后者为 `false`，说明当前配置没有
   Storage 同地域的 SCF 区域，应先补充该区域或运行显式单节点 canary。单独查看路由计划：

   ```bash
   moox-cli setup scf-network-plan --file ./moox.toml
   ```

4. 发布 SCF 时，CLI 默认启用 `--same-region-first`：自动分配的函数先处理 Storage 同地域，
   直到该地域按 `region_limits.<region>.max_functions_per_namespace` 扣除 Invoke/快照辅助
   函数后的容量用尽，再把剩余函数分配到其他地域。显式 `function_count` 仍按运维配置执行。
   每个地域都会先
   创建/更新 Invoke canary，再放大 Timer fleet；同地域 canary 的 NodeCreateItem 同时携带
   `vpc_id`、`subnet_id` 和私网 Storage target。Storage 地域的任一 canary、批次或回读失败
   都会立即停止发布，不会继续消耗其他地域的公网流量。
   这里的“占满”指达到该地域命名空间可用容量，而不是达到一个固定的全球数量。CLI 不会
   自动迁移已有线上函数或删除旧 namespace；namespace 迁移必须先发布并验收新 namespace。

   ```bash
   moox-cli collector function publish submit \
     --file ./moox.toml --space-id crypto --same-region-first
   ```

   如果配置中没有 Storage 同地域，可用显式单地域 canary 验证（必须显式给出节点数）：

   ```bash
   moox-cli collector function publish submit \
     --file ./moox.toml --space-id crypto \
     --region <storage-region> --node-count 1 --same-region-first
   ```

   canary 成功标准是：SCF 日志出现 Storage 写入成功、Provider HTTP 200，且 Storage 读回
   新 K 线。canary 失败时停止该次 fleet 发布，不以“函数已创建”作为成功。

## 连接与鉴权边界

- 控制面 HTTP API 使用 Caddy HTTPS `:11001`；不要把原生 Storage tRPC `:11003` 当作
  HTTP API。
- SCF/Collector 访问 Storage 使用原生 tRPC `:11003`，并携带 Collector 服务凭据。
- 同地域私网只替换 Storage 数据面目标；EventBus、CLS 和交易所 HTTP 仍按各自配置选择
  公网或专用入口。
- 跨地域不引入 CCN。公网回退可能产生跨地域流量费用，发布结果中的 `storage_routes`
  会明确记录 `network=public` 和原因。

## 诊断与回滚

查看单个函数的实际网络配置：

```bash
moox-cli setup inspect-scf --file ./moox.toml \
  --region <scf-region> --namespace moox-crypto --function <function-name>
```

重点核对 `vpc_id`、`subnet_id`、`public_net_status` 和
`MOOX_STORAGE_RPC_GATEWAY_TARGET`。路由计划是只读计算，不代表云端已经绑定；云端回读和
canary 日志才是部署证据。

若同地域私网发生故障，临时发布可显式指定公网 `--storage-rpc-gateway-target
ip://<public-ip>:11003`，或按变更流程更新 Node 配置后重新发布。不要通过修改
`storage_gateway_host` 为私网地址来绕过地域判断。

## 常见误区

- 不要把 `DatasetRowsUpserted` 等数据事件当作 SCF 网络就绪信号；网络路由只由 Storage
  实例元数据和 SCF 地域决定。
- 不要仅因为 SCF 绑定了 VPC 就关闭公网出口；采集交易所通常仍需要公网或 NAT。
- 不要把一次 `setup scf-network-plan` 输出当作生产验收；必须同时保存函数配置回读、
  canary 日志、Storage 写入和读回结果。
