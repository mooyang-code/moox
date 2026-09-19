# Collector 采集任务与采集结果

## 用户模型

Collector 对用户只暴露“采集任务”和“采集结果”。一个任务自动独占一个结果，不允许选择或复用其他任务的结果。结果页按任务名称建立子 Tab，页面内部使用 Storage 的真实 View 查询数据，并保留筛选、排序、详情和 K 线弹窗。

任务生命周期如下：

1. 创建任务时由 Collector 根据 `space_id + "\\0" + task_id` 的 SHA-256 前 16 位生成结果身份。
2. Collector 创建并激活内部 Dataset，创建默认浏览 View，再写入任务表。
3. 任务执行器只写入任务对应的结果 Dataset；用户不需要知道 Dataset ID。
4. 删除任务时先删除任务运行记录，再由用户选择保留结果，或物理删除结果 View 和 Dataset。

## 接口与数据表

Collector RPC 使用 `GetTaskList`、`GetTaskDetail`、`CreateTask`、`UpdateTask`、`DisableTask`、`DeleteTask`。删除请求必须携带 `delete_result_data`，前端默认选择保留结果。

`t_collector_tasks` 的结果关联字段为：

- `c_result_dataset_id`：Collector 内部写入目标。
- `c_result_view_id`：结果页默认浏览 View。

`c_result_ownership` 已移除。结果是否物理删除只属于一次删除操作的用户选择，不再作为任务的持久状态。

执行实例使用 `c_task_id` 表示父任务，使用 `c_instance_id` 表示单个执行实例，避免两个含义不同的 Task ID 混用。Storage 内部仍使用 `dataset_id`、`t_datasets` 和 Metadata RPC，这是模块边界内的技术字段。

## 失败补偿

结果资源创建必须在任务行写入前完成。后续校验或任务写入失败时，Collector 调用结果管理器删除刚创建的 View/Dataset。重复创建使用确定性 ID，因此是幂等的。删除结果时按 View 后 Dataset 的顺序执行。

## 发布验证

发布后应验证：任务列表请求返回 `tasks`；创建任务可生成结果 Tab；结果页能查询真实数据；时序结果可以打开 K 线；删除任务的“保留/物理删除”两种分支分别符合选择。Collector 旧数据集管理入口不再注册路由或菜单。
