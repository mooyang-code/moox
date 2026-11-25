---
name: debug
description: 用于调试、排查 MooX 端到端（e2e）故障：collector 的 SCF 代码包构建、打包与发布，腾讯云 COS 或 SCF 函数创建，Timer 计划与周期提交（period commit），CLS 日志分析，MooX 远程部署，以及 Storage 写入核对。
---

# MooX 调试

## 概述

这个 skill 用于接近生产环境的 MooX 调试：问题跨本地代码、远程服务、腾讯云 SCF/COS/CLS 和 Storage 核对。排查要以证据为依据：先确定是哪一段出了问题，再改代码或重新部署。

## 开始之前

1. 说清具体现象、受影响的函数 / 节点 / 任务 ID、预期行为和当前的时间窗口。
2. 查看本地仓库状态，不要碰用户无关的改动。
3. 确定被测的是哪一段：控制面、Storage、collector 代码包、SCF 运行时、腾讯云账号、远程主机，还是前端代理。
4. 问题涉及 SCF 打包、发布、CLS 日志、远程部署或 K 线写入核对时，阅读 [SCF 端到端调试](references/scf-e2e-debug.md) 里的详细流程。

## 安全规则

- 最终回答里不要打印腾讯云 SecretKey、服务访问密钥、SSH 密码或签名请求头。
- 优先用 `moox-cli` 命令和 MooX 自带脚本，不要手工重复容易出错的腾讯云 API 调用。
- 做破坏性操作前，先确认目标资源名、地域、命名空间、账号和代码包版本。
- Collector 代码和 SCF 代码包的构建逻辑在 `modules/collector` 下。
- 前端请求的管理接口必须走 `/api/admin`；服务之间的调用是经本机主机网关的签名 tRPC（`packages/gatewayclient`，回环 `11002`，跨主机 TLS `11003`）；外部调用方（SCF、Skill）以各自的签名身份从入口主机网关的外部入口（`11004`）进入；因子引擎和归档 Worker 是普通组件，用因子机密钥签名，走 `11003`。

## 逐段检查清单

没有证据指向别处时，按这个顺序查：

1. 本地构建：SCF 代码包里有 `main` 和 `sources/`（市场文件和交易日历编译进了二进制）。
2. 代码包上传：COS 对象存在，地域 / 桶 / key 与发布请求一致。
3. SCF 函数：存在于预期的地域和命名空间，代码包是最新的，环境里的分配哈希与计划一致（`collector function list --drift`）。
4. Timer：触发器已启用，cron 与预期一致（同一条命令；计划外的函数单独列出，它们的定时器已停）。
5. 计划：确认数据集走 SCF（stream / local 另有执行器）；函数环境里带着当前的采集计划（`collector fetch-plan`）。
6. 执行：CLS 日志里有每个标的的数据源请求结果。
7. Storage：`CommitTimeSeriesBatch` 经网关外部入口写入成功。
8. 结果：任务的结果 View 里出现对应空间、标的和频率的行。

## 要保留的证据

- 改动前的 `git status --short`。
- 构建 / 打包命令，以及产出的代码包路径和版本。
- COS 桶、地域、对象 key 和代码包版本。
- SCF 函数名、命名空间、地域、运行时、handler、Timer cron 和环境变量的键（不要值）。
- CLS topic ID、查询时间范围、request ID、batch ID 和关键日志行。
- 计划对账和补采前后的 Collector 日志。
- Storage 查询参数和结果条数，不要保留完整密钥或大段数据。

## 常见错误

- 函数存在就认定 Timer 触发过；要核对 Timer 读回、生效的采集计划和调用日志。
- 只看 Storage 里的行，不看周期状态（`complete` / `degraded`）和下游的周期事件。
- 前端 404 去查服务路径；前端必须走 `/api/admin`。
