# 异步任务 API 使用指南

## 概述

异步任务管理系统用于处理批量操作（如批量创建/更新/删除云函数节点），前端通过异步任务ID跟踪操作进度。

## API 接口

### 1. 执行异步任务

**请求地址**: `POST /moox-api/async_task/execute`

**请求示例**:
```json
{
    "task_id": "550e8400-e29b-41d4-a716-446655440000",  // 前端生成的UUID
    "task_type": "BATCH_CREATE_NODE",                   // 任务类型
    "request_params": {
        "nodes": [
            {
                "cloud_account_id": "account_001",
                "node_type": "SCF",
                "region": "ap-beijing",
                "ip_address": "10.0.0.1",
                "version": "1.0.0",
                "supported_collectors": "metrics,logs",
                "capacity": "100",
                "metadata": "{\"env\":\"prod\"}"
            },
            {
                "cloud_account_id": "account_001",
                "node_type": "SCF",
                "region": "ap-shanghai",
                "ip_address": "10.0.0.2",
                "version": "1.0.0",
                "supported_collectors": "metrics,logs",
                "capacity": "100",
                "metadata": "{\"env\":\"prod\"}"
            }
        ]
    }
}
```

**响应示例**:
```json
{
    "code": 200,
    "message": "Task created and executing"
}
```

### 2. 查询任务状态

**请求地址**: `GET /moox-api/async_task/query?task_id=550e8400-e29b-41d4-a716-446655440000`

**响应示例**:
```json
{
    "code": 200,
    "data": {
        "task_id": "550e8400-e29b-41d4-a716-446655440000",
        "task_type": "BATCH_CREATE_NODE",
        "task_status": 1,       // 1-处理中 2-成功 3-失败 4-部分成功
        "total_count": 2,
        "success_count": 1,
        "failed_count": 0,
        "progress": 50,         // 进度百分比
        "error_message": "",
        "created_at": "2024-01-01 10:00:00",
        "completed_time": ""
    }
}
```

## 任务类型

- `BATCH_CREATE_NODE`: 批量创建节点
- `BATCH_UPDATE_NODE`: 批量更新节点
- `BATCH_DELETE_NODE`: 批量删除节点

## 任务状态

- `1`: 处理中
- `2`: 成功
- `3`: 失败
- `4`: 部分成功

## 前端集成流程

1. **生成任务ID**: 前端生成UUID作为任务ID
2. **发起批量操作**: 调用执行接口，携带任务ID和请求参数
3. **轮询任务状态**: 使用任务ID定期查询任务进度
4. **显示执行结果**: 根据任务状态显示成功/失败提示

## 示例代码（前端）

```javascript
// 生成任务ID
const taskId = generateUUID();

// 将任务ID添加到URL参数
const url = new URL(window.location);
url.searchParams.set('task_id', taskId);
window.history.pushState({}, '', url);

// 执行批量操作
const response = await fetch('/moox-api/async_task/execute', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
        task_id: taskId,
        task_type: 'BATCH_CREATE_NODE',
        request_params: { nodes: [...] }
    })
});

// 轮询任务状态
const pollTaskStatus = async () => {
    const status = await fetch(`/moox-api/async_task/query?task_id=${taskId}`);
    const data = await status.json();
    
    if (data.data.task_status === 1) {
        // 继续轮询
        setTimeout(pollTaskStatus, 2000);
    } else {
        // 显示结果
        if (data.data.task_status === 2) {
            showSuccess('批量操作成功');
        } else {
            showError(`批量操作失败: ${data.data.error_message}`);
        }
    }
};

pollTaskStatus();
```