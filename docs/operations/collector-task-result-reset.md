# Collector 任务结果模型切换 Runbook

本 Runbook 用于未上线环境从旧 Collector 规则/数据集模型切换到采集任务结果模型。

## 发布前

1. 备份远端 Collector SQLite 数据库、Storage metadata manifest 和当前 `moox-collector`、`moox-web-host` 二进制，并记录 SHA-256。
2. 确认当前 Collector 已停止写入，确认空间、任务、实例、批次、重试记录数量。
3. 仅清理 `owner_module=collector` 的 View/Dataset；Factor、Strategy 和手工 Storage 数据不在清理范围。
4. 新版本使用 `t_collector_tasks`，不会读取旧 `t_collector_task_rules`。发现旧表时先按备份流程处理，不要直接启动新 Collector。

## 启动与检查

1. 初始化 Collector 新 Schema 和内置任务种子。
2. 启动 Storage、Collector、Monitor 和网关，确认健康检查通过。
3. 从管理台创建一个带任务名称的采集任务，确认结果 View 自动创建。
4. 打开“采集结果”，确认按任务名称出现子 Tab，能看到真实数据，并可打开 K 线弹窗。
5. 禁用任务后确认结果仍可读；删除任务并选择保留结果，确认 View/Dataset 仍存在但结果列表不再展示。
6. 在测试任务上选择“同时物理删除结果数据和数据视图”，确认任务、实例、View 和 Dataset 均删除。

## 回滚

若新 Collector 或前端不可用，先停止新版本服务，再同时恢复旧 Collector 二进制、旧数据库和旧前端静态资源。结果模型切换不应删除 Storage 中 Factor/Strategy 数据。结果创建补偿失败时，依据发布前 manifest 清理带 `owner_module=collector` 的孤儿资源后重试。
