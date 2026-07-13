# tRPC 插件运行基线

## 已启用基线

所有生产 tRPC 服务在 server filter 链首使用 `recovery`，将 RPC handler panic 转为受控系统错误；保留异步任务和 CloudRuntime 自身的恢复逻辑。支持 protobuf 校验的服务按顺序使用 `recovery`、`validation`、已有 `cors`/`spacectx`、`prometheus`。Prometheus 固定使用 `plugins.metrics.prometheus`，`enablepush: false`，不引入 Pushgateway 或 Prometheus Server。

## 日志与 CLS

| 环境 | Writer | 必填字段 | 禁止内容 |
|---|---|---|---|
| local/test | console | module、service、method、trace ID、code、duration | 凭证、原始 body、JWT、HMAC、API secret |
| production | console；warn/error 追加 CLS | 同上加 deployment version | 同上 |
| incident debug | 有时限、仅指定 method | trace ID 与批准的标识 | 未经脱敏测试的敏感字段 |

CLS 仅由 `scripts/deploy-moox.sh --enable-cls` 写入 production 配置。仓库和 release tree 中只保留 `${MOOX_CLS_*}` 占位符；目标机的 `secrets/cls.env` 必须为 `0600`，至少包含 `MOOX_CLS_SECRET_ID` 和 `MOOX_CLS_SECRET_KEY`。`start.sh` 默认执行幂等初始化：检查/开通 CLS，创建或复用 Logset、Topic 和索引，然后把 Topic ID 注入本次服务进程。设置 `MOOX_CLS_AUTO_BOOTSTRAP=0` 时必须显式提供 `MOOX_CLS_TOPIC_ID`。初始远程级别为 `warn`。

直接执行初始化与预检：

```bash
moox-cli ops tencent cls bootstrap --region ap-guangzhou --dry-run
moox-cli ops tencent cls bootstrap --region ap-guangzhou
```

初始化命令使用腾讯云 SDK 默认凭据链，支持标准环境变量和 CVM 实例角色；生产日志 writer 受上游 `trpc-log-cls v1.0.0` 限制，仍需通过目标机环境文件注入短期或轮换后的 `SecretId/SecretKey`，不会把凭据写入仓库或日志。

## 链路、重试与元数据边界

- OpenTelemetry 第一阶段只覆盖 Admin 和 Storage 的 server/client filter。目标机可通过 `secrets/otel.env`（`0600`）设置 `MOOX_OTEL_ENDPOINT`、`MOOX_OTEL_INSECURE` 和可选采样率；未设置 endpoint 时使用 no-op provider。默认采样率 1%，不记录请求/响应 body，不重复导出 metrics 或 logs，部署脚本会为 Admin 和各 Storage 进程设置独立 `service.name`。由于官方 `oteltrpc v1.0.2` 与 Storage 使用的 OpenTelemetry 1.35 不兼容，MooX 使用同名本地兼容适配器；扩展其他模块前先完成 Admin 到 Storage 的端到端 trace 验证。
- `slime/retry` 仅挂在 Factor 的 `ReadTimeSeriesRows` 单次调用选项上，最多尝试两次，只重试客户端网络错误和超时。共享代理的写接口不会继承该 filter。
- `transinfo-blocker` 只在 Factor 到 Storage 的客户端边界启用白名单，允许 W3C trace 与 MooX trace/space 标识；JWT、HMAC、Authorization 等未列入白名单，禁止透传。
- `masking` 已用于 Admin Secret 列表/查询响应和 Trade API Key 响应；内部 `RevealSecret` 使用独立 protobuf 类型，避免脱敏破坏受控的明文读取流程。
- `filterextensions` 继续保持未引入。只有出现明确的方法级策略差异，并有顺序和回滚测试后才启用。

MooX JWT、request HMAC 和 service HMAC 网关过滤器保持权威。通用 JWT 插件不能替换其 claim 检查、元数据注入、签名和 no-auth 规则。

## 上线检查

```bash
bash scripts/test-trpc-plugin-config.sh
go test -count=1 ./packages/trpcplugintest
```

先按 admin、storage、trade、collector/factor/cloudnode、monitor/eventbus/hostagent/strategy/archive 的顺序发布。每批检查 panic 计数、tRPC 错误码、CLS 投递错误和指标端口均保持私有。
