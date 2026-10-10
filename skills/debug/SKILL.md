---
name: debug
description: 排查 MooX 端到端故障时使用：涉及 Collector 的 SCF 构建/打包/发布、腾讯云 COS 或 SCF 创建、Timer 触发与批次领取、CLS 日志分析、远端 MooX 部署，或 Storage 写入验证。
---

# MooX 调试

## 概述

用于跨越本地代码、远端服务、腾讯云 SCF/COS/CLS 和 Storage 验证的、接近生产环境的 MooX 调试。以证据为准：先找出失败的边界，再改代码或重新部署。

## 开始之前

1. 说明具体症状、受影响的函数/节点/任务 ID、期望行为和当前的时间窗口。
2. 检查本地仓库状态，不要碰与本次无关的用户改动。
3. 确定被测试的路径：控制面、Storage、采集代码包、SCF 运行时、腾讯云账号、远端主机或前端代理。
4. 问题涉及 SCF 打包、发布、CLS 日志、远端部署或 K 线写入验证时，读 [SCF 端到端调试](references/scf-e2e-debug.md) 里的详细流程。

## 安全规则

- 不要在最终回复里打印腾讯云 SecretKey、服务签名密钥、SSH 密码或带签名的请求头。
- 优先用 `moox-cli` 命令和仓库自带的脚本，不要手工重复执行脆弱的腾讯云 API 调用。
- 破坏性操作之前，先确认目标资源名、地域、命名空间、账号和代码包版本。
- 旧的独立 Collector 仓库路径只是历史；当前的 Collector 代码和 SCF 打包逻辑都在 `modules/collector`。
- 前端的管理 API 必须走 `/api/admin`。服务之间的调用经主机网关（`moox-host-gateway`）；不在 MooX 主机上的调用方（SCF、因子引擎）只能经外部接入（`moox-access`）。
  没有 `/api/service`。

## 边界检查表

没有证据指向别处时，按这个顺序查：

1. 本地构建：SCF 包里有 `main`、`sources/`、EventBus CA，stockcn 还有市场资源。
2. 包上传：COS 对象存在，地域/桶/键与发布请求一致。
3. SCF 函数：名称、命名空间、地域、运行时、入口和环境变量与 CloudNode 的节点一致。
4. Timer：触发器已启用，cron 符合预期（`collector function timer-inventory`）。
5. 领取：函数经 `ClaimTimerBatch` 领取到它的 Timer 批次。
6. 执行：CLS 日志里有逐个标的的数据源结果。
7. Storage：`EnsureDatasetPeriod`/`CommitTimeSeriesBatch` 经外部接入（`moox-access`）成功，函数发布了 `MarketFetchBatchCompleted`。
8. 结果：这个空间、标的和频率的任务结果 View 里出现了数据行。

## 要保留的证据

- 修改前的 `git status --short`。
- 构建/打包命令和产出的代码包路径、版本。
- COS 桶、地域、对象键和代码包版本。
- SCF 函数名、命名空间、地域、运行时、入口、Timer cron 和环境变量的键（不要值）。
- CLS Topic ID、查询时间范围、request ID、batch ID 和关键日志行。
- Collector 在 `ClaimTimerBatch`、完成事件处理和重试前后的日志。
- Storage 的查询参数和结果数，不要完整的密钥或大的负载。

## 常见错误

- 因为函数存在就认为 Timer 触发了；要检查 Timer 状态和被领取的批次。
- 从旧的独立 checkout 重新构建 Collector 代码，而不是 `modules/collector`。
- 只看 Storage 的行，不检查周期状态（`complete`/`degraded`）和完成事件的消费者。
- 前端 404 时去查服务路径；前端必须用 `/api/admin`。
