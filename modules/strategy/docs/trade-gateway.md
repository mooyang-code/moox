# Strategy 到 Trade 的账户授权接线

Trade 控制服务使用 HTTP，Strategy 通过节点 HTTPS 服务网关访问，不直连 Trade 端口，也不使用只转发 tRPC 的 native Gateway。

```yaml
trade:
  gateway_url: https://trade.example:11001
  target_node: trade-node
  ca_file: ../certs/gateway/peers.pem
  timeout: 3s
```

`target_node` 是接收请求的 Gateway 注册节点 ID，不是服务名，仅使用小写字母、数字、连字符或下划线。跨主机必须使用 HTTPS；HTTP 仅允许 loopback。URL 只填写 origin，不带 `/api/service` 路径。URL 和 node 都空表示尚未接入 Trade，控制面可创建禁用实例，但依赖 Trade 的校验/启用不会成功；只配置其中一项、非法 node 或负 timeout 在配置加载阶段拒绝，不延迟到第一次 Claim。

进程凭据沿用 `MOOX_GATEWAY_SERVICE_KEY_ID`、`MOOX_GATEWAY_CALLER=strategy`、`MOOX_GATEWAY_SERVICE_SECRET_KEY`；由部署脚本从 Strategy 专用 key 文件注入，禁止写入仓库。CA 未在 YAML 配置时读取 `MOOX_GATEWAY_CA_FILE`。

`MOOX_TRADE_GATEWAY_URL` 和 `MOOX_TRADE_GATEWAY_NODE_ID` 可覆盖此依赖。部署打包时两项应一起提供；共同部署 Trade 时默认本机 HTTP Gateway 和本节点 ID，Strategy 独立部署时必须显式指向 Trade 节点，或保留未接线状态。渲染值保存在部署的 `strategy/config/app.yaml`，重启不依赖最初打包进程。Factor/Storage 使用的 `MOOX_SERVICE_GATEWAY_TARGET/MOOX_GATEWAY_TARGET_NODE` 不覆盖 Trade。

`trade_owner` 是同一 Trade HTTP 服务的窄权限网关路由，不是新的交易服务。它只允许 caller `strategy` 调用 `GetLogicalAccount`、`ClaimLogicalAccountOwner`、`ReleaseLogicalAccountOwner`、`RebindLogicalAccountOwner`，透传 `X-Space-Id`。`RebindLogicalAccountOwner` 只接受带完整 instance/session CAS 身份的现代请求；旧 runner/rebind_key 请求被冲突拒绝。普通下单、人工接管、Paper 创建和资金操作仍走原有管理入口；不能为了连通授权调用而公开全部 Trade 方法。

节点注册有两条入口：`setup deploy-service trade` 在实际 Trade 节点同步服务和 ownership 路由；完整部署脚本将专用 URL/node 持久化为 `config/trade-gateway.json`，Admin 启动时仅向该节点导入 `trade_owner`，不依赖 DNS resolver。control-only 包也需要显式传入相同 URL/node；只在 Strategy 包设置地址，并不会自动修改另一台 control 机器的配置。没有显式 placement 的组件 overlay 不覆盖已安装的 placement 文件。

现有节点升级时仍须通过管理配置核验对应节点的 applied route snapshot、Strategy key 和证书信任，不能将本地 seed 变更或注册请求成功当作线上已经生效。旧授权入口仍需按执行计划 T10 原子清理后再发布。

推荐先创建 disabled 实例，再显式启用。创建部分成功时，错误响应保留实例身份；读取状态也失败时只返回身份，不声称当前 enabled/session。相同 ID 同配置重试返回当前状态，不自动执行 Claim；需要恢复启用时使用 `SetStrategyInstanceEnabled`。未知远端结果不能通过换 ID 重建来回避。

本地接线验收：

```bash
env GOFLAGS=-race bash scripts/test/e2e/test-strategy-trade-gateway-e2e.sh
```

此脚本使用临时 SQLite、测试证书、随机 loopback 端口，运行三个真实客户端/服务进程。它验证 Paper 账户授权和拒绝路径，不代表订单成交或正式环境验收；成交链另由 Strategy/Outbox/NATS/Paper E2E 验证。
