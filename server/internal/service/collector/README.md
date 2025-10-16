# Collector API 文档

本模块实现了对云函数采集器节点和配置的管理功能。

## API 端点

所有API通过统一的api接口访问：
- 基础URL: `/api/{interfaceid}`

### 1. 采集器节点管理 (t_cloud_nodes)

接口ID: `t_cloud_nodes`

#### 获取节点列表
```
GET /api/t_cloud_nodes
```

响应示例：
```json
{
    "code": 200,
    "data": [
        {
            "id": 1,
            "node_id": "node-001",
            "node_name": "采集节点1",
            "ip_address": "192.168.1.100",
            "hostname": "collector-01",
            "version": "1.0.0",
            "status": 1,
            "last_heartbeat": "2024-03-20T10:30:00Z",
            "metadata": "{}",
            "create_time": "2024-03-20T09:00:00Z",
            "modify_time": "2024-03-20T10:30:00Z"
        }
    ]
}
```

#### 获取单个节点
```
GET /api/t_cloud_nodes?node_id=node-001
```

#### 注册新节点
```
POST /api/t_cloud_nodes
Content-Type: application/json

{
    "_action": "register",
    "node_id": "node-002",
    "node_name": "采集节点2",
    "ip_address": "192.168.1.101",
    "hostname": "collector-02",
    "version": "1.0.0",
    "metadata": "{\"region\":\"cn-north-1\"}"
}
```

#### 更新节点信息
```
POST /api/t_cloud_nodes
Content-Type: application/json

{
    "_action": "update",
    "node_id": "node-001",
    "node_name": "采集节点1-更新",
    "ip_address": "192.168.1.100",
    "hostname": "collector-01",
    "version": "1.0.1",
    "status": "1",
    "metadata": "{\"region\":\"cn-north-1\",\"zone\":\"az1\"}"
}
```

#### 删除节点
```
POST /api/t_cloud_nodes
Content-Type: application/json

{
    "_action": "delete",
    "node_id": "node-001"
}
```

#### 更新心跳
```
POST /api/t_cloud_nodes
Content-Type: application/json

{
    "_action": "heartbeat",
    "node_id": "node-001"
}
```

### 2. 节点采集器配置管理 (t_node_collectors_conf)

接口ID: `t_node_collectors_conf`

#### 获取节点配置列表
```
GET /api/t_node_collectors_conf?node_id=node-001
```

#### 获取启用的配置
```
GET /api/t_node_collectors_conf?node_id=node-001&enabled=true
```

#### 获取单个配置
```
GET /api/t_node_collectors_conf?id=1
```

#### 创建配置
```
POST /api/t_node_collectors_conf
Content-Type: application/json

{
    "_action": "create",
    "node_id": "node-001",
    "source_name": "binance",
    "collector_type": "kline",
    "data_type": "spot",
    "cron_expression": "0 */5 * * * ?",
    "symbols": "[\"BTCUSDT\",\"ETHUSDT\"]",
    "intervals": "[\"1m\",\"5m\",\"1h\"]",
    "config": "{\"limit\":1000,\"timeout\":30}",
    "enabled": "1",
    "priority": "10"
}
```

#### 更新配置
```
POST /api/t_node_collectors_conf
Content-Type: application/json

{
    "_action": "update",
    "id": "1",
    "node_id": "node-001",
    "source_name": "binance",
    "collector_type": "kline",
    "data_type": "spot",
    "cron_expression": "0 */10 * * * ?",
    "symbols": "[\"BTCUSDT\",\"ETHUSDT\",\"BNBUSDT\"]",
    "intervals": "[\"1m\",\"5m\",\"15m\",\"1h\"]",
    "config": "{\"limit\":2000,\"timeout\":60}",
    "enabled": "1",
    "priority": "20"
}
```

#### 删除配置
```
POST /api/t_node_collectors_conf
Content-Type: application/json

{
    "_action": "delete",
    "id": "1"
}
```

## 数据模型

### CollectorNode（采集器节点）
- `node_id`: 节点唯一标识
- `node_name`: 节点名称
- `ip_address`: IP地址
- `hostname`: 主机名
- `version`: 采集器版本
- `status`: 节点状态（0=离线，1=在线，2=维护中）
- `last_heartbeat`: 最后心跳时间
- `metadata`: 节点额外信息（JSON格式）

### NodeCollectorConf（节点采集器配置）
- `node_id`: 关联的节点ID
- `source_name`: 数据源名称（binance/okx等）
- `collector_type`: 采集器类型（kline/ticker/orderbook/trade）
- `data_type`: 数据类型
- `cron_expression`: 定时器表达式
- `symbols`: 交易对列表（JSON数组）
- `intervals`: K线时间间隔（JSON数组）
- `config`: 采集器详细配置（JSON对象）
- `enabled`: 是否启用（-1=否，1=是）
- `priority`: 优先级（用于负载均衡）

## 错误处理

所有错误响应格式：
```json
{
    "code": 500,
    "data": ["错误信息"]
}
```

常见错误码：
- 200: 成功
- 400: 请求参数错误
- 404: 资源未找到
- 500: 服务器内部错误

## 注意事项

1. 节点心跳超时时间为5分钟，超时后节点状态会自动变为离线
2. 删除节点时会同时软删除该节点的所有配置
3. JSON格式字段（symbols、intervals、config、metadata）必须是有效的JSON
4. 所有删除操作都是软删除（设置c_invalid=1）