# 云函数调用API使用示例

## API端点
`POST /api/v1/cloud-function/invoke`

## 功能说明
该接口用于触发云函数执行，支持同步和异步调用模式。

## 请求格式

### 请求头
```
Content-Type: application/json
```

### 请求体参数
| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| node_id | string | 否* | 节点ID，与function_name二选一 |
| function_name | string | 否* | 函数名称，与node_id二选一 |
| namespace | string | 否 | 命名空间，不填则使用节点默认命名空间 |
| event_data | object/any | 否 | 传递给云函数的事件数据 |
| invoke_type | string | 否 | 调用类型：sync(同步)/async(异步)，默认sync |
| qualifier | string | 否 | 版本号或别名 |

## 使用示例

### 1. 通过节点ID调用云函数
```bash
curl -X POST http://localhost:8080/api/v1/cloud-function/invoke \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "scf-collector-node-001",
    "event_data": {
      "action": "test",
      "message": "Hello from API"
    }
  }'
```

### 2. 直接指定函数名调用
```bash
curl -X POST http://localhost:8080/api/v1/cloud-function/invoke \
  -H "Content-Type: application/json" \
  -d '{
    "function_name": "data-collector-function",
    "namespace": "production",
    "event_data": {
      "action": "collect",
      "targets": ["BTC/USDT", "ETH/USDT"]
    },
    "invoke_type": "sync"
  }'
```

### 3. 异步调用示例
```bash
curl -X POST http://localhost:8080/api/v1/cloud-function/invoke \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "scf-collector-node-002",
    "event_data": {
      "action": "batch_process",
      "task_ids": ["task-001", "task-002", "task-003"]
    },
    "invoke_type": "async"
  }'
```

### 4. 触发任务配置导入
```bash
curl -X POST http://localhost:8080/api/v1/cloud-function/invoke \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "scf-collector-node-001",
    "event_data": {
      "action": "task_config_import",
      "task_config": {
        "task_id": "task-btc-kline-1m",
        "project_id": "crypto-project",
        "dataset_id": "binance-spot",
        "task_type": "data_collect",
        "collector_type": "kline",
        "source_name": "binance",
        "target_objects": ["BTCUSDT"],
        "collect_params": {
          "intervals": ["1m", "5m"],
          "limit": 1000
        }
      }
    }
  }'
```

## 响应格式

### 成功响应
```json
{
  "code": 0,
  "message": "Success",
  "request_id": "req-123456789",
  "result": {
    "status": "ok",
    "data": "Function executed successfully"
  },
  "duration": 150,
  "bill_duration": 200,
  "memory_usage": 67108864
}
```

### 错误响应
```json
{
  "code": 500,
  "message": "Function execution failed: timeout",
  "request_id": "req-123456789"
}
```

## 错误码说明
| 错误码 | 说明 |
|--------|------|
| 200 | 成功 |
| 400 | 请求参数错误 |
| 404 | 节点或函数不存在 |
| 405 | 请求方法不支持 |
| 500 | 服务器内部错误 |

## 注意事项
1. 调用前需要确保云提供商已正确配置
2. 同步调用有超时限制，建议长时间任务使用异步调用
3. 事件数据会被自动转换为JSON格式传递给云函数
4. 返回的result字段会自动尝试解析为JSON，如果不是JSON格式则返回原始字符串