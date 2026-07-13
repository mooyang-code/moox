# tRPC 插件改造完成计划

## 目标与验收

| 优先级 | 能力 | 落地范围 | 验收证据 |
|---|---|---|---|
| P0 | Prometheus | 全部 11 个生产模块，私有监听，禁用 push | 配置矩阵脚本与端口检查 |
| P0 | recovery | 全部服务端 filter 链首，统一清洗 panic | panic 单测与配置矩阵 |
| P1 | OpenTelemetry | 第一阶段 Admin、Storage 双向链路 | body 抑制测试；配置采样 1%；端到端环境验证 |
| P1 | validation | 全部 protobuf RPC 模块；敏感请求不打印 | 每模块注册、代表性 `Validate`、配置测试 |
| P1 | CLS | 全模块可选 production writer；启动时幂等开通和建资源 | dry-run、SDK fake 测试、部署模板检查 |
| P2 | slime | 经审查的幂等只读 RPC；当前覆盖 Factor、Archive、Monitor 到 Storage 的 `ReadTimeSeriesRows` | 网络错误最多两次；业务错误一次；写 RPC 不挂载 |
| P2 | masking | Admin Secret 与 Trade API Key 响应 | 官方 masking filter 兼容测试 |
| P2 | transinfo-blocker | Factor 到 Storage 白名单边界 | trace/space allowlist 配置检查 |
| P3 | filterextensions | 暂缓 | 配置测试保证未误启用 |

## 发布顺序

1. 先发布 P0 与 validation，观察 panic、错误码和 Prometheus 指标。
2. 配置 OTLP Collector 后发布 Admin、Storage，验证一条跨服务 trace；未设置 endpoint 时不导出。
3. 在目标机建立 `secrets/cls.env`，用 `--enable-cls` 发布，确认自动创建/复用 Topic 与 warn/error 投递。
4. 发布 Factor、Archive、Monitor 的只读重试和 Factor metadata 白名单，注入网络故障确认写 RPC 不重试。Strategy 接入 Storage 查询时复用同一调用级策略。
5. 最后发布 Admin/Trade masking，并核对受控明文读取与客户端兼容性。

## 回滚

移除对应 filter 配置即可停用 OTel、validation、masking 和 transinfo；不带 `--enable-cls` 重新生成部署包即可移除 CLS writer。`slime` 回滚为删除对应只读 RPC 的调用级 `client.WithFilter`，不得改成代理级全局重试。Prometheus 与 recovery 是生产基线，不作为常规回滚项。
