# tRPC 插件运行基线

## 已启用基线

所有生产 tRPC 服务在 server filter 链首使用 `recovery`，将 RPC handler panic 转为受控系统错误；保留异步任务和 CloudRuntime 自身的恢复逻辑。支持 protobuf 校验的服务按顺序使用 `recovery`、`validation`、已有 `cors`/`spacectx`、`prometheus`。Prometheus 固定使用 `plugins.metrics.prometheus`，`enablepush: false`，不引入 Pushgateway 或 Prometheus Server。

## 日志与 CLS

| 环境 | Writer | 必填字段 | 禁止内容 |
|---|---|---|---|
| local/test | console | module、service、method、trace ID、code、duration | 凭证、原始 body、JWT、HMAC、API secret |
| production | console；warn/error 追加 CLS | 同上加 deployment version | 同上 |
| incident debug | 有时限、仅指定 method | trace ID 与批准的标识 | 未经脱敏测试的敏感字段 |

CLS-enabled releases run a predeploy check before release archive sync or
service shutdown. The check
uses the selected cloud account, or the first Tencent account when no ID is
supplied, and queries fixed `ap-guangzhou` resources: Logset `moox` and Topic
`moox-application`. It creates only missing resources and writes the verified
Topic ID into staged `trpc_go*.yaml` files. Writer credentials remain in the
target `secrets/cls.env` with mode `0600`; staged configs contain the verified
literal Topic ID and only `${MOOX_CLS_SECRET_ID}` and
`${MOOX_CLS_SECRET_KEY}` credential placeholders. A failed Admin/CloudNode service-auth check,
account lookup, credential reveal, or CLS API call stops the release before
release archive sync or service shutdown. On remote targets, the Skill may
upload a temporary architecture-matched `moox-cli` helper solely for this
check; it is removed immediately and is not release archive sync. The initial
remote level remains `warn`.

直接执行发布前初始化与预检：

```bash
skills/moox/scripts/cls-bootstrap.sh \
  --target user@host \
  --deploy-dir /home/user/moox \
  --stage-dir release/deploy-stage/moox \
  --admin-url http://127.0.0.1:11002
```

脚本通过带 service-auth 的 Admin/CloudNode 控制面调用
`moox-cli ops tencent cls prepare`；生产日志 writer 受上游
`trpc-log-cls v1.0.0` 限制，短期或轮换后的 `SecretId/SecretKey` 仅通过
目标机 `0600` 环境文件注入，不会写入仓库、stage、release archive 或日志。

所有启用 CLS 的 tRPC 进程在 `trpc.NewServer()` 完成插件初始化后，都会在入口处为默认 logger
追加固定的 `service_name` 属性；属性值使用部署模块名（如 `admin`、`factor`、`storage`）。
该属性随 console 和 CLS writer 一起输出，便于在固定 Topic 中按服务筛选；业务日志不能覆盖该字段。

## 链路、重试与元数据边界

- OpenTelemetry 第一阶段只覆盖 Admin 和 Storage 的 server/client filter。目标机可通过 `secrets/otel.env`（`0600`）设置 `MOOX_OTEL_ENDPOINT`、`MOOX_OTEL_INSECURE` 和可选采样率；未设置 endpoint 时使用 no-op provider。默认采样率 1%，不记录请求/响应 body，不重复导出 metrics 或 logs，部署脚本会为 Admin 和各 Storage 进程设置独立 `service.name`。由于官方 `oteltrpc v1.0.2` 与 Storage 使用的 OpenTelemetry 1.35 不兼容，MooX 使用同名本地兼容适配器；扩展其他模块前先完成 Admin 到 Storage 的端到端 trace 验证。
- `slime/retry` 使用共享的 `packages/trpcretry` 策略，仅挂在经审查的幂等只读 RPC 单次调用选项上，最多尝试两次，只重试客户端网络错误和超时。当前覆盖 Factor、Archive、Monitor 到 Storage 的 `ReadTimeSeriesRows`；Strategy 当前尚未实现 Storage RPC，后续接入 `ReadTimeSeriesRows` 等只读查询时应复用该策略。共享代理的写接口不会继承该 filter。
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
