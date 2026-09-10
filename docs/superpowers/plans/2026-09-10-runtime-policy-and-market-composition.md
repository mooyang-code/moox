# 运行配置与行情装配调整

> 按已确认讨论实施；两个独立模块并行修改，主 Agent 集成，codeCR 独立审查。

**目标：** 部署后重启仍使用明确的 Observability 投递策略；行情调度层不再装配具体数据源，永续来源标识准确。

**架构：** setup 配置经发布脚本写入目标机运行环境，Monitor 配置层校验并向 consumer 传递策略。具体 provider 在 runtime composition 层装配，复用 marketdata.Registry 注入通用采集流程。

**技术栈：** Go、Cobra、TOML/YAML、Bash、NATS JetStream。

## 1. Monitor 配置

- [x] 新增 all/new 配置、环境覆盖及非法值测试，先确认失败。
- [x] 在 config 层加载与校验 deliver_policy；默认 all，consumer 不再读取进程环境。
- [x] bootstrap 显式映射并传入 NATS 策略；策略冲突保持报错，不自动重建 durable。
- [x] 执行 `go test ./internal/config ./internal/observability/eventconsumer ./internal/bootstrap`。

## 2. 初始化与发布

- [x] 增加 setup observability.deliver_policy 与部署选项，非法值提前拒绝。
- [x] 将策略持久化并注入 Monitor；覆盖发布未显式指定时保留已有值。
- [x] 测试配置默认、显式覆盖、发布归档、目标启动脚本与覆盖发布。

## 3. 行情装配

- [x] 增加分层约束、现货/永续来源与接口选择测试，确认旧实现失败。
- [x] 迁移具体 provider 工厂至薄装配层，serverless/bootstrap 注入通用工厂。
- [x] Binance 源层区分 spot_http/swap_http，更新关联规则与默认路由。
- [x] 保持其他市场能力；测试 timer/invoke、未知来源及持久化来源标识。

## 4. 验证与交付

- [x] 集成测试、Linux 构建、脚本语法和 diff 校验。
- [x] 独立 codeCR 审查并修复问题。
- 交付要求：提交并推送本次相关改动；保留无关工作区改动。

验证记录：Monitor、Collector 全模块测试通过，CLI setup config/deploy 与命令映射测试通过，部署 policy contract 通过；现货/永续、1m/1H、Invoke/Timer 共 8 组本地 TLS HTTP 集成测试通过，核心包 race 测试通过。Linux Monitor、CLI、Collector、Collector-SCF 构建通过。codeCR 终审无剩余 P1/P2。

既有失败未纳入修复：CLI command 全包的 metadata active/disabled 断言与现有工作区配置不一致；旧 preserve-disabled contract 在读取 trade-gateway.json 时失败，已在 b814ad69 基线复现。以上不等同于生产验收。

## 边界

本轮不操作线上部署、不读取私密 moox.toml、不删除 durable 或数据。
`all` 与 `new` 切换可能与已有 durable 不兼容；运维必须先核验策略并明确迁移，不以清空消费进度作为自动修复。
