# Storage View 重建门禁与构建日志设计

## 背景

Storage View 使用 A/B 物理索引控制文件增长，并在定义变化或索引异常时重建查询视图。容量整理重建会读取旧 Active 索引并写入新索引，与实时 View 消费共享 CPU、内存和磁盘资源。生产环境曾在消费积压时启动大型系统 View 重建，放大资源压力和业务 View 延迟。

当前 `t_view_index_builds` 只保存每个 View 的当前构建状态。激活成功后记录会被删除，因此用户无法回答以下问题：

- 为什么触发重建；
- 何时开始和结束；
- 是否成功、失败或因门禁跳过；
- 重建时的文件大小和消费积压；
- 连续跳过了多少次。

本次不改变数据采集、因子计算或 Dataset 事件处理边界。它只增强 Storage View 的重建启动条件、构建历史和页面展示。

## 目标

1. 容量整理重建只在 Storage View 负载较低时启动。
2. 必要重建不会被容量门禁阻止。
3. 同一 Storage View 进程同一时刻最多执行一个 A/B 重建。
4. 持久化每次构建的原因、时间、结果和关键运行指标。
5. 对重复的门禁跳过记录做聚合，避免按检查周期写入重复日志。
6. 在 View 浏览页通过一个文字按钮查看当前 View 的构建历史。

## 非目标

- 不合并 Collector 和 Factor 的写入。
- 不改变 Dataset 的现有独立处理与顺序语义。
- 不拆分或重建 JetStream stream、consumer 或 subject。
- 不允许前端启动、取消或重试构建。
- 不解析进程文本日志作为构建历史来源。

## 重建分类

### 必要重建

以下情况用于建立或恢复正确的查询索引：

- `initial_build`：View 尚无 Active 索引；
- `definition_change`：desired revision 高于 active revision；
- `active_missing`：Metadata 指向的 Active 物理索引不存在；
- `active_invalid`：Active 索引 revision、schema hash 或物理状态无效；
- `coverage_repair`：索引覆盖范围需要修复；
- `manual_repair`：运维命令显式触发修复。

必要重建不受消费积压门禁限制，否则损坏或缺失的 View 可能永久无法恢复。它仍受“单进程一次只运行一个构建”的资源串行限制。

### 容量整理重建

`size_limit` 表示有限保留窗口的 View 文件达到 `max_view_file_bytes`。Active 索引仍可读写，因此该重建属于低优先级容量整理。

容量整理重建只有在以下条件同时成立时才可声明 build：

1. Storage View consumer 已绑定；
2. Active View 恢复已完成；
3. `num_pending + num_ack_pending <= rebuild_max_pending`；
4. 上述条件连续满足 `rebuild_idle_checks` 次检查；
5. 当前没有其他 View 正在执行 A/B 重建；
6. 当前 View 不在容量重建冷却期；
7. 目标 inactive slot 不在旧 Active 的延迟删除期。

默认配置：

```yaml
storage:
  view:
    rebuild_max_pending: 32
    rebuild_idle_checks: 3
```

`rebuild_max_pending` 必须大于或等于 0，`rebuild_idle_checks` 必须大于 0。非法值阻止服务启动，不做静默修正。连续空闲计数保存在进程内；重启后从 0 开始，不需要持久化。

## 全局构建串行

Reconciler 在声明 build 前取得进程级构建许可。许可只覆盖一个实际 A/B 构建的生命周期，不覆盖普通实时写入、查询或门禁检查。

当另一个 View 已持有许可时，本轮检查不阻塞等待，而是记录或更新一条 `another_build_running` 跳过记录并返回。下一次 reconcile 再检查。这样不会让 View 遍历被一个长构建卡住。

## 构建历史模型

新增 `t_view_rebuild_logs`。`t_view_index_builds` 继续只保存当前构建状态和 CAS 所需字段，二者职责不混合。

每条历史记录包含：

| 字段 | 含义 |
| --- | --- |
| `log_id` | 稳定唯一 ID |
| `space_id`、`view_id` | 所属 View |
| `build_id` | 实际构建 ID；跳过记录可为空 |
| `index_id` | 目标 inactive 索引；尚未选择时可为空 |
| `trigger_reason` | 重建触发原因 |
| `result` | `running`、`succeeded`、`failed` 或 `skipped` |
| `block_reason` | 跳过原因；实际构建为空 |
| `target_view_revision` | 目标 View revision |
| `active_view_revision` | 检查时的 Active revision |
| `physical_bytes` | 检查时 Active 文件大小 |
| `num_pending`、`num_ack_pending` | 检查时 consumer 状态 |
| `entries_written` | 实际构建写入行数 |
| `started_at`、`finished_at` | 实际构建时间 |
| `first_checked_at`、`last_checked_at` | 跳过记录时间范围 |
| `skip_count` | 相同阻塞连续出现次数 |
| `error_summary` | 对用户安全的失败摘要 |
| `details_json` | 受控扩展信息，不写凭据或原始请求 |
| `created_at`、`updated_at` | 数据库记录时间 |

索引支持按 `space_id + view_id + created_at DESC` 分页，以及按 `build_id` 更新实际构建结果。

### 实际构建记录

成功声明 Metadata build 后插入 `running` 记录。状态变化继续由现有 A/B 状态机负责；历史记录只在关键边界更新：

1. build 声明成功：写入 `running` 和开始时间；
2. 激活并完成内存切换：更新为 `succeeded`，写入结束时间、耗时和行数；
3. build 被确认失败：更新为 `failed`，写入安全错误摘要；
4. 进程恢复时发现中断 build：将对应历史记录更新为 `failed`；找不到历史记录时补写一条失败记录。

构建历史写入失败不能改变 A/B 的正确性结果。服务记录结构化错误并继续执行原状态机，但 readiness 暴露最近一次历史写入失败，避免静默丢失审计记录。

### 跳过记录去重

跳过记录的聚合键为：

`space_id + view_id + trigger_reason + block_reason + result=skipped`

若该键最近一条记录仍处于同一连续阻塞区间，则更新 `last_checked_at`、`skip_count` 和最新指标，不新增行。条件恢复、触发原因变化或实际构建开始后，下一次同类跳过会创建新记录。

标准阻塞原因包括：

- `consumer_not_bound`；
- `restore_not_ready`；
- `consumer_backlog`；
- `another_build_running`；
- `cooldown_active`；
- `inactive_slot_retiring`；
- `consumer_state_unavailable`。

## Metadata API

新增只读 RPC：

```text
ListViewRebuildLogs(space_id, view_id, result?, page)
```

要求：

- `space_id` 和 `view_id` 必填；
- 使用现有 Metadata 读权限；
- 默认按 `created_at DESC`；
- 默认每页 50 条，遵循项目统一分页上限；
- 返回结构化枚举值和安全错误摘要；
- 不暴露 owner credential、认证字段或底层 SQL 错误。

当前 `GetView/ListViews` 的 `index_build` 投影保持不变，用于展示实时状态。历史弹窗单独按需调用新 RPC，避免每次 View 列表刷新都读取历史表。

## 前端交互

修改 View 浏览公共组件，使数据浏览页及嵌入该组件的因子结果页共享相同行为。

状态行展示为：

```text
已构建  构建完成 · 2026/8/12 12:48:54  日志
```

`日志` 使用小号文字按钮，放在构建时间右侧。用户点击后打开弹窗，标题为“构建日志：<view_id>”。弹窗首次打开时加载最近 50 条，关闭后不继续轮询。

表格列：

| 列 | 展示 |
| --- | --- |
| 时间 | 开始时间；跳过记录显示首次检查时间 |
| 触发原因 | 中文原因标签 |
| 结果 | 彩色状态标签 |
| 耗时 | 成功或失败构建的持续时间 |
| 写入行数 | 实际构建行数；跳过显示 `-` |
| 说明 | 失败摘要，或“队列积压，累计跳过 N 次” |

弹窗支持服务端分页。空列表显示“暂无构建日志”。请求失败使用页面统一错误提示，不能影响 View 查询结果。

## 一致性与失败处理

- 历史表不参与 A/B CAS，不作为恢复 Active 索引的依据。
- 只有 Metadata 激活成功且运行时已切换后，历史记录才标记成功。
- Activate 响应不确定时沿用现有 readback 判定，再决定成功或失败。
- 必要重建不会因日志表暂时不可写而被错误跳过。
- 容量整理门禁读取 consumer 状态失败时 fail closed，并聚合记录 `consumer_state_unavailable`。
- 跳过不是构建失败，不触发失败冷却。
- 所有时间由服务端以 UTC RFC3339Nano 保存，前端按现有时间格式展示。

## 测试与验收

### Storage Metadata

- schema migration 创建历史表和索引；
- running 记录可更新为 succeeded/failed；
- 跳过记录按聚合键更新次数，不产生重复行；
- 条件恢复后再次跳过会生成新记录；
- 按 View、结果和分页查询稳定排序；
- 删除 View 时级联删除对应历史记录。

### Storage View

- 容量超限但 backlog 超阈值时不声明 build，并写入跳过记录；
- 连续三次低于阈值后才启动容量重建；
- 空闲计数中途不满足条件时清零；
- 同时只有一个 View 获得构建许可；
- schema revision、Active 缺失等必要重建不受 backlog 门禁限制；
- 成功、失败、中断恢复和不确定激活均写入正确最终结果；
- 历史写入失败不改变 Active A 的可用性。

### Web

- `日志` 按钮紧邻构建时间；
- 点击后只查询当前 `space_id/view_id`；
- 原因、状态、耗时、行数和跳过次数显示正确；
- 空状态、分页和 API 错误有明确展示；
- 嵌入式因子结果页和普通 View 浏览页行为一致。

### 生产验收

1. 制造容量超限并保持 consumer backlog，确认没有新 build，日志显示聚合后的跳过次数。
2. 降低 backlog 并连续满足空闲检查，确认只启动一个 A/B build。
3. 验证 Active A 在重建期间持续查询和实时更新。
4. 等待切换完成，确认日志记录成功、耗时、写入行数和目标 revision。
5. 制造一次可控构建失败，确认 Active A 保持可用且日志显示安全错误摘要。
6. 在因子结果页点击“日志”，确认弹窗数据与 Metadata 表一致。

## 发布与兼容

Metadata schema 采用向前迁移，只新增表和 RPC，不修改现有 `t_view_index_builds` 语义。旧 View 不需要重建。发布顺序为 Storage Metadata/Primary、Storage View、Admin/Gateway、Web。前端在新 RPC 不可用时只让日志弹窗报错，不影响原有 View 查询。
