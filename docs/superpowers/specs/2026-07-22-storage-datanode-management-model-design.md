# Storage DataNode 管理模型收敛设计

## 状态

本设计于 2026-07-22 确认。它补充并修订《Storage Dataset 单归属与字段级存储简化设计》中 DataNode 管理、Dataset 绑定和管理台展示部分。

MooX 尚未上线。本次变更不保留旧表迁移、旧 RPC、字段别名或前端兼容层。实现应以单一、清晰的最终模型为准。

## 目标

1. 让控制面与运行时保持一致：Dataset 通过 `data_node_id` 直接定位 DataNode。
2. 删除不再参与运行时决策的主存路由、权重和拓扑概念。
3. 让管理台在一个 DataNode 页面内展示节点及其全部 Dataset。
4. 明确部署流程与管理台的字段所有权，防止人工修改运行地址。
5. 允许尚未激活的 Dataset 安全更换 DataNode，但不建设数据迁移能力。

## 非目标

- 不提供 Dataset 分片、权重路由、自动均衡或故障转移。
- 不提供已有数据的在线或离线迁移。
- 不提供 Dataset 无归属状态。
- 不在本次变更中重新设计 ArchiveFile 的归档语义；Device 只移除已经失效的 DataNode 归属关系。
- 不保留 `PrimaryStoreNode`、`PrimaryStoreRoute` 或旧 URL 的专用兼容逻辑。

## 最终领域模型

### DataNode

DataNode 表示一个可被 `storage-primary` 直接调用的数据存储节点：

```text
DataNode
  node_id          部署生成的稳定标识
  name             管理台展示名称
  service_target   DataNode 内部 tRPC 地址
  status           active | disabled
  created_at
  updated_at
```

删除旧 `endpoint`、`weight`、`config_json` 和 `attributes.service_target`。`service_target` 是一等字段，不再从多个位置兜底读取。

字段所有权如下：

| 字段 | 写入方 | 管理台行为 |
| --- | --- | --- |
| `node_id` | 部署流程 | 只读 |
| `service_target` | 部署流程 | 只读 |
| `name` | 部署流程初始化，管理员维护 | 可编辑 |
| `status` | 部署流程初始化，管理员维护 | 可启停 |

部署注册已存在节点时，只更新 `service_target`，不得覆盖管理员设置的名称和停用状态。管理台不提供手工创建 DataNode 的入口。

### Dataset 绑定

Dataset 创建时必须指定有效的 `data_node_id`。Metadata 不保存额外的“数据集归属”或路由记录。

Dataset 增加持久化字段 `binding_locked`：

- 新建 Dataset 固定为 `status=disabled`、`binding_locked=false`。
- Dataset 通过激活自检后，服务在同一事务中设置 `status=active`、`binding_locked=true`。
- 一旦为 `true`，永不恢复为 `false`，即使 Dataset 后续被停用或数据被人工清理。
- 常规写路径不更新绑定状态，只读取 Metadata Snapshot。

这种激活门禁把放置决策移出写入热路径，也避免通过扫描 Pebble 判断“是否无数据”。

Dataset 同时增加从 `1` 开始的单调递增 `revision`。任何 Dataset Metadata 变更都在同一事务中递增 revision，激活和重新绑定使用它做乐观并发控制；时间戳不承担 CAS 语义。

管理员可在 Dataset 页面执行“更换数据节点”。服务端必须同时满足以下条件：

1. Dataset 状态为 `disabled`；
2. `binding_locked` 为 `false`；
3. 新 DataNode 存在且状态为 `active`；
4. 新旧 `data_node_id` 不相同。

操作直接把旧 `data_node_id` 原子更新为新值。系统不持久化空 `data_node_id`，因此界面上的“解绑”始终是一次“解绑并重新绑定”。

这条规则将原设计中“创建后永久不可修改”收窄为“首次激活后永久不可修改”。锁定后的 Dataset 不支持迁移；用户只能保留原节点或删除 Dataset。

## Schema

Metadata Schema 升级到 v5，并只接受 v5：

- `t_primary_store_nodes` 替换为 `t_data_nodes`；
- `t_data_nodes` 只保存最终 DataNode 字段；
- `t_datasets.c_data_node_id` 增加外键和查询索引；
- `t_datasets` 增加非空布尔字段 `c_binding_locked`，默认 `0`；
- `t_datasets` 增加正整数 `c_revision`，默认 `1`，每次 Dataset 变更原子递增；
- 删除 `t_primary_store_routes`；
- 删除 `t_dataset_topology_locks`；
- 删除 `t_storage_devices.c_node_id`；DataNode 的 Pebble 路径由部署配置管理，Device 不再表示 DataNode、DuckDB 或 Bleve 的子组件；
- 删除 Seed 中挂在 DataNode 下的 Pebble、DuckDB 和 Bleve Device；ArchiveFile 和 Device 的其余清理由后续 Archive 专项设计处理。

开发、测试和远端验证环境直接清理旧 Metadata SQLite 后重建，不编写 v4 到 v5 的数据迁移。

## Proto 与服务接口

### DataNode 接口

删除所有 `PrimaryStoreNode` 和 `PrimaryStoreRoute` 消息、请求、响应及 RPC，新增命名一致的 DataNode 接口：

```text
RegisterDataNode       部署流程注册或刷新 service_target
UpdateDataNode         管理员只更新 name/status
GetDataNode            获取单个节点
ListDataNodes          分页获取节点及每个节点的全部 Dataset 摘要
DeleteDataNode         删除已停用且无 Dataset 引用的节点
```

`RegisterDataNode` 只允许内部部署身份调用。管理台不暴露该入口。

`RegisterDataNodeReq` 只包含 `node_id`、`service_target` 和创建时使用的 `initial_name`。`UpdateDataNodeReq` 只包含 `node_id`、`name` 和 `status`。接口不接收完整 DataNode 对象，从协议层阻止调用方修改不属于自己的字段。

`ListDataNodes` 不返回单独的 `dataset_count`。每个列表项使用组合视图，避免把查询结果塞入核心实体：

```text
DataNodeListItem
  node: DataNode
  datasets: DatasetSummary[]

DatasetSummary
  space_id
  dataset_id
  name
  data_kind
  keep_duration
  status
```

节点列表可以分页，但每个返回节点都携带其全部 Dataset 摘要。Metadata 服务用一次节点查询和一次 Dataset 批量查询完成组装，再按 `data_node_id` 分组；禁止逐节点查询。

### Dataset 接口

- Dataset 创建和读取消息公开 `data_node_id`、`keep_duration`、`binding_locked` 和 `revision`。
- Dataset 列表请求增加可选 `data_node_id` 过滤条件。
- 新增 `RebindDatasetDataNode` 命令，参数包含 `space_id`、`dataset_id` 和新的 `data_node_id`。
- 新增只读 `CheckDatasetActivation`，返回逐项检查结果和当前 Dataset revision，不修改 Metadata。
- 新增幂等 `ActivateDataset`，内部复用同一个激活检查器，并携带客户端最近观察到的 Dataset revision。
- 通用 `UpdateDataset` 不允许修改 `data_node_id`、`binding_locked`，也不允许把状态改为 `active`；启用必须经过 `ActivateDataset`。
- `ActivateDataset` 对已锁定且已启用的 Dataset 返回幂等成功；对已锁定但停用的 Dataset 重新执行 readiness 检查后只恢复 `active`，不改变绑定。

激活检查器至少验证：

1. Dataset 状态与锁定组合合法：`disabled + unlocked` 可以首次激活，`disabled + locked` 可以重新启用，`active + locked` 视为幂等已激活，`active + unlocked` 必须失败；
2. Dataset Schema 和 `keep_duration` 合法；
3. 绑定的 DataNode 存在且为 `active`；
4. `service_target` 格式合法并可访问；
5. DataNode 的签名 readiness 响应成功，且返回的 `node_id` 与 Metadata 一致；
6. 提交激活时 Dataset revision 没有变化。

网络检查发生在事务外。检查通过后，服务打开短事务再次校验 Dataset revision、状态和 DataNode 引用，再提交状态转换。状态已变化时返回冲突，调用方重新执行自检，不持有数据库事务等待网络。

### 删除约束

DataNode 只有在 `status=disabled` 且没有任何 Dataset 引用时才能删除。服务端必须在同一 Metadata 写事务中检查引用并删除，不能只依赖前端判断。

失败响应应区分以下原因：节点仍启用、节点仍有关联 Dataset、目标节点不存在或已停用、Dataset 未停用、Dataset 绑定已锁定。管理台直接展示这些可操作的错误信息。

## 运行时数据流

### 常规路由

```text
Write/Read request
  -> storage-primary 从 Metadata Snapshot 读取 active Dataset
  -> Dataset.data_node_id
  -> 从同一 Snapshot 读取 active DataNode.service_target
  -> 直接调用 storage-node
```

运行时不得访问 Metadata SQLite，也不得读取路由表、权重、Subject Pattern、Hash Rule 或 Endpoint 兜底字段。Dataset 或 DataNode 非 active 时立即拒绝读写。

### Dataset 激活

```text
初始化或部署流程
  -> CheckDatasetActivation（只读）
  -> 检查结果全部通过
  -> ActivateDataset（内部复查 + revision CAS）
  -> 原子提交 status=active + binding_locked=true
  -> 刷新 Metadata Snapshot
```

激活封装属于 Storage 领域能力，初始化流程和自检模块都通过同一接口使用，不能各自复制检查规则。激活成功后普通写入只依赖缓存；激活失败时 Dataset 保持 disabled。

### 更换 DataNode

```text
管理员停用 Dataset
  -> 选择新的 active DataNode
  -> RebindDatasetDataNode
  -> 事务校验 disabled + binding_locked=false
  -> 原子替换 data_node_id
  -> 刷新 Metadata Snapshot
```

整个流程没有无归属中间态，也不复制 Pebble 数据。

## 管理台设计

### Storage 页面

`/#/ops/storage/nodes` 删除“主存路由”Tab。默认且唯一的节点视图命名为“数据节点”。未知 `tab` 参数统一回落到 `nodes`，不为 `routes` 编写专用别名。

DataNode 表格展示：

- 节点 ID；
- 名称；
- `service_target`；
- 状态；
- Dataset 标签列表；
- 更新时间；
- 查看、编辑、启停和删除操作。

Dataset 列直接展示节点下的全部 Dataset 名称，每个名称使用紧凑、可点击的 Tag。Tag 点击后跳转 Dataset 页面，Tooltip 显示 Space 和 Dataset ID；无 Dataset 时显示 `-`。Tag 可以自然换行，但必须限制列宽并固定操作列，不能挤压 `service_target` 或操作按钮。

“查看”操作打开节点详情抽屉。抽屉直接使用 `ListDataNodes` 已返回的数据，展示 Space、Dataset ID、名称、数据类型、保存时长和状态，并提供跳转 Dataset 页面的操作。抽屉内不编辑 Dataset。

### Dataset 页面

- 创建 Dataset 时必须选择 active DataNode，并填写 `keep_duration`。
- 编辑页只读展示当前 DataNode 和绑定锁定状态。
- 新建 Dataset 默认停用；“启用”操作调用 `CheckDatasetActivation` 展示结果，通过后再调用 `ActivateDataset`。
- `disabled` 且 `binding_locked=false` 时显示“更换数据节点”。
- 操作对话框必须选择新的 active DataNode 后才能提交。
- `binding_locked=true` 时隐藏更换节点操作，并在信息提示中说明已激活的 Dataset 不支持迁移。

### 信息提示

页面标题旁使用紧凑的 Info 图标。图标带 Tooltip，悬停或键盘聚焦时显示说明，不增加常驻说明卡片。

DataNode 详情提示以下信息：

- 节点由部署流程注册；
- `node_id` 和 `service_target` 不能在管理台修改；
- Dataset 通过 `data_node_id` 直接路由，不存在独立路由规则；
- 只有已停用且无 Dataset 的节点可以删除。

Dataset 详情提示以下信息：

- Dataset 始终必须绑定一个 DataNode；
- 第一次激活后绑定永久锁定；
- “更换数据节点”只适用于尚未激活的 Dataset；
- 系统不提供已有数据迁移。

Info 图标必须支持鼠标悬停、键盘聚焦和无障碍名称。Tooltip 不得遮挡标题、表格操作或对话框。

## Doctor 与自检边界

现有 `doctor bootstrap|diagnose` 默认保持只读，不因一次普通诊断命令修改 Dataset 状态。Doctor Runner 只调用 `CheckDatasetActivation` 并把结果记录为 Observation，不直接调用 `ActivateDataset`。

初始化或部署编排在 Doctor bootstrap 总结为 HEALTHY 后，显式调用 `ActivateDataset`。这一步必须在部署日志中记录 Dataset ID、检查摘要、激活结果和 Metadata revision。若用户只运行 Doctor 诊断，不会触发激活。

Storage readiness 检查从现有 deferred 状态升级为 active 时，应复用固定 Check ID 和有界超时，不允许任意脚本、动态检查 DSL 或自动修复动作。激活是用户启动的初始化流程中的显式状态转换，不属于 Doctor 自动修复。

## 清理范围

除本设计中的新旧对照说明外，以下旧概念必须在现行代码、Proto、生成代码、SQL、Seed、CLI、测试、前端和现行说明文档中零残留：

- `PrimaryStoreNode` / `primary_store_node`；
- `PrimaryStoreRoute` / `primary_store_route`；
- `weight`、`hash_rule`、`subject_pattern` 和路由 `priority`；
- `t_dataset_topology_locks` 及旧拓扑锁实现；
- `Device.node_id` 以及把 Pebble、DuckDB、Bleve 注册成节点子设备的 Seed；
- 从 `attributes` 或 `endpoint` 兜底解析 `service_target` 的逻辑；
- 管理台路由页及其 API、类型和菜单入口。

历史设计文档可以保留旧术语作为决策记录，但必须明确已被本设计取代。生成代码必须由更新后的 Proto 重新生成，不能手工修补。

## 测试与验收

### 后端

- DataNode 注册只能更新部署所有字段；管理员更新不能修改 `node_id` 或 `service_target`。
- `ListDataNodes` 正确批量组装全部 Dataset，空节点返回空数组。
- 节点启用或仍被 Dataset 引用时拒绝删除。
- Dataset 创建必须绑定 active DataNode。
- Dataset 创建后固定为 disabled 且未锁定。
- 激活检查只读、结果有界，并校验 DataNode 身份与 readiness。
- 激活使用 revision CAS 原子提交 active 和 binding_locked，冲突时不提交。
- 普通读写只使用 Metadata Snapshot，不访问 SQLite。
- 仅 `disabled + binding_locked=false` 的 Dataset 可以重新绑定。
- 路由解析只使用 `Dataset.data_node_id -> DataNode.service_target`。
- Schema v5、Seed、Cache Snapshot 和 CLI 摘要使用最终模型。

### 前端

- Storage 页面不再出现路由 Tab，未知 Tab 回落到节点视图。
- 节点表格把全部 Dataset 名称展示为可点击 Tag，详情抽屉不发起二次 Dataset 请求。
- 部署所有字段只读，名称和状态可编辑。
- 删除和重新绑定操作根据后端约束显示正确状态及错误提示。
- Info Tooltip 支持 hover、focus 和无障碍名称。
- Dataset 创建请求包含 `data_node_id` 和 `keep_duration`。
- Dataset 启用流程先显示激活检查结果，通过后再提交激活。

### 合同与 E2E

- 增加旧符号零残留合同测试；扫描实现、生成物、配置和现行说明文档，排除带有取代声明的历史设计记录。
- 重新生成 Storage Proto，并测试 Storage、Gateway 及所有 Metadata 消费方。
- E2E 覆盖节点注册、Dataset 创建、只读激活检查、revision 冲突、显式激活、缓存写路径、禁止重新绑定、未激活 Dataset 重新绑定和节点删除约束。
- Doctor E2E 证明普通 bootstrap 只输出激活 Observation，不修改 Dataset；部署 E2E 证明 HEALTHY 后显式激活。
- 管理台 E2E 覆盖节点 Dataset Tag、节点详情、Info Tooltip、Dataset 创建、激活和更换节点流程。

## 发布验证

实现完成后必须执行以下闭环：

1. 启动新的独立 Agent 审查完整差异；
2. 修复发现的问题，并对修复结果重新审查；
3. 运行后端测试、合同测试、前端检查和 E2E；
4. 构建 Linux amd64 发布物并记录 SHA256；
5. 根据 `custom.toml` 部署到 106 机器；
6. 在远端清理旧 Metadata 数据库并以 Schema v5 重建；
7. 验证签名健康检查、DataNode 注册、Dataset 路由和管理台 E2E；
8. 记录部署提交 SHA、二进制 SHA256 和远端验证结果。
