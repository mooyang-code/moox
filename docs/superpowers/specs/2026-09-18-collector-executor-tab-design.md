# 采集任务执行器子 Tab 设计

## 背景

当前“采集任务”页面已经承载“采集规则”和“任务实例”两个子 tab；“云节点”虽然是采集任务的实际执行器，却仍以独立菜单和独立路由存在，导致采集任务相关能力被拆散。

## 目标

- 将云节点管理并入“采集任务”工作台，作为“执行器”子 tab。
- 保留云节点现有的查询、批量新增、部署、删除、同步、云账户和代码包管理能力。
- 移除侧边栏中的独立“云节点”入口。
- 兼容历史 `/collector/cloudnodes` 链接，自动跳转到 `/collector/rules?tab=executors`。
- 将首页云节点指标卡跳转到新的执行器子 tab。

## 非目标

- 不修改云节点 API、批量变更逻辑、云账户逻辑或代码包逻辑。
- 不改变“采集规则”和“任务实例”的数据行为。
- 不迁移或重命名云节点内部组件目录。

## 交互与路由

“采集任务”子 tab 顺序固定为：

1. 采集规则
2. 任务实例
3. 执行器

执行器复用现有 `cloud-node.vue` 页面组件。`/collector/rules?tab=executors` 显示执行器 tab；缺省或未知 tab 仍回到采集规则。旧 `/collector/cloudnodes` 路由改为重定向，保留历史书签与首页旧链接的兼容性。

## 菜单与首页

- 静态菜单删除独立 `collector-cloudnodes` 项。
- `collector-cloudnodes` 中文/英文翻译可以保留，供兼容路由元信息使用。
- 首页云节点指标卡跳转到 `/collector/rules?tab=executors`。

## 验收标准

- 采集任务页面可见并按顺序展示三个子 tab。
- 点击“执行器”后仍能看到“云节点”标题和原有操作栏。
- 访问 `/collector/cloudnodes` 后最终 URL 为 `/collector/rules?tab=executors`，且执行器 tab 被选中。
- 侧边栏不再出现独立云节点菜单。
- 相关 Vitest、Playwright、生产构建通过。

