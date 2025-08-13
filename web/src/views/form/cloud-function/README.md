# 云函数管理页面 - 异步任务集成说明

## 功能概述

云函数管理页面已集成异步任务管理功能，支持批量新增、批量部署、批量删除等操作。所有批量操作都会通过异步任务系统执行，前端可以实时查看任务进度。

## 核心功能

### 1. 批量操作异步化
- **批量新增**：创建异步任务执行批量节点创建
- **批量部署**：（开发中）批量部署云函数代码
- **批量删除**：创建异步任务执行批量节点删除

### 2. 任务进度实时展示
- 任务执行时显示进度条
- 实时更新成功/失败数量
- 任务完成后自动刷新列表

### 3. 页面刷新状态保持
- 任务ID保存在URL参数中
- 刷新页面后自动恢复任务状态
- 只有处理中的任务会恢复轮询

## 实现细节

### 异步任务管理器 (`/src/utils/async-task.ts`)

提供了完整的异步任务管理功能：

```typescript
// 创建并执行任务
const taskId = await asyncTaskManager.createAndExecuteTask('BATCH_CREATE_NODE', {
  nodes: [...]
});

// 开始轮询任务状态
asyncTaskManager.startPolling(taskId, {
  onProgress: (data) => { /* 更新进度 */ },
  onSuccess: (data) => { /* 处理成功 */ },
  onFailed: (data) => { /* 处理失败 */ },
  onPartialSuccess: (data) => { /* 部分成功 */ }
});

// 页面刷新恢复
await asyncTaskManager.checkAndRestoreTask(handleTaskRestore);
```

### URL参数管理

- 任务创建时自动将 `task_id` 添加到URL
- 示例：`http://localhost:3000/cloud-function?task_id=550e8400-e29b-41d4-a716-446655440000`
- 任务完成后自动清除URL参数

### UI交互优化

1. **任务执行期间**：
   - 显示任务进度提示框
   - 禁用批量操作按钮
   - 隐藏行选择框

2. **任务完成后**：
   - 显示执行结果（3秒后自动关闭）
   - 自动刷新数据列表
   - 清空已选择项

## 使用示例

### 批量新增节点
```javascript
const executeBatchAdd = async () => {
  // 准备节点数据
  const nodes = [
    { cloud_account_id: 'xxx', node_type: 'scf', ... },
    // ...
  ];

  // 创建异步任务
  const taskId = await asyncTaskManager.createAndExecuteTask('BATCH_CREATE_NODE', {
    nodes
  });

  // 开始轮询
  asyncTaskManager.startPolling(taskId, {
    onProgress: (data) => {
      currentTaskStatus.value = data;
    },
    onSuccess: (data) => {
      handleTaskComplete(data);
    }
  });
};
```

### 批量删除节点
```javascript
const executeBatchDelete = async () => {
  const nodes = selectedKeys.value.map(nodeId => ({ node_id: nodeId }));

  const taskId = await asyncTaskManager.createAndExecuteTask('BATCH_DELETE_NODE', {
    nodes
  });

  // ... 轮询处理
};
```

## 注意事项

1. 任务执行期间不要关闭页面，否则无法获取最终结果
2. 如果页面意外关闭，重新打开页面会自动恢复任务状态
3. 一次只能执行一个批量任务
4. 任务完成后会自动刷新数据，无需手动刷新

## 后续优化

1. 支持任务取消功能
2. 添加任务历史记录查看
3. 支持批量操作的参数配置
4. 优化错误提示和重试机制