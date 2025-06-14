# Moox 网关服务使用说明

## 概述

Moox 网关服务已成功集成到主服务中，提供统一的API入口，支持路径转发、认证处理和请求追踪。

## 服务架构

```
前端应用
    ↓
网关服务 (localhost:18202)
    ↓
├── 存储服务 (127.0.0.1:18106)
├── 认证服务 (127.0.0.1:18201)
└── 元数据服务 (127.0.0.1:18105)
```

## 启动服务

### 方法一：使用启动脚本

```bash
# 进入服务器目录
cd src/github.com/mooyang-code/moox/server

# 运行启动脚本
./scripts/start_gateway.sh
```

### 方法二：直接启动

```bash
# 编译服务
go build -o bin/moox-server .

# 启动服务
./bin/moox-server
```

## 服务端口

- **网关服务**: `http://localhost:18202`
- **认证服务**: `http://localhost:18201`
- **trpc认证服务**: `tcp://localhost:18200`

## API 接口

### 1. 健康检查

```bash
GET http://localhost:18202/gateway/health
```

**响应示例：**
```json
{
  "status": "ok",
  "time": "2024-01-01 12:00:00",
  "services": ["storage", "metadata", "auth"]
}
```

### 2. 服务转发

**URL格式：**
```
POST http://localhost:18202/gateway/{service}/{method}
```

**必需头部：**
- `X-App-Id`: 应用ID
- `X-App-Key`: 应用密钥

**可选头部：**
- `X-Access-Token`: 访问令牌
- `X-Trace-Id`: 追踪ID
- `X-Client-Ip`: 客户端IP

### 3. 存储服务示例

```bash
curl -X POST http://localhost:18202/gateway/storage/ListProjects \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: trace-001" \
  -d '{
    "auth_info": {
      "app_id": "test123",
      "app_key": "test123"
    }
  }'
```

### 4. 认证服务示例

```bash
curl -X POST http://localhost:18202/gateway/auth/GetUserInfo \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: trace-002" \
  -d '{
    "app_info": {
      "app_id": "test123",
      "app_key": "test123"
    },
    "user_id": "test_user",
    "access_token": "access_token111111"
  }'
```

## 测试网关

运行测试脚本验证网关功能：

```bash
./scripts/test_gateway.sh
```

测试内容包括：
1. 健康检查接口
2. 存储服务转发
3. 认证服务转发
4. 错误处理

## 配置文件

### 网关配置 (config/gateway.yaml)

```yaml
gateway:
  port: 18202
  storage_addr: "127.0.0.1:18106"
  auth_addr: "127.0.0.1:18201"
  metadata_addr: "127.0.0.1:18105"
  timeout: 5000
  debug: true

rate_limit:
  requests_per_minute: 1000
  burst: 100
```

### trpc配置 (config/trpc_go.yaml)

网关服务配置：
```yaml
- name: trpc.moox.gateway.stdhttp
  port: 18202
  network: tcp
  protocol: http
  timeout: 5000
```

## 日志查看

服务启动后，日志文件位于：
- **应用日志**: `log/trpc.log`
- **控制台输出**: 实时显示服务状态

## 错误处理

### 常见错误码

- `400 Bad Request`: 请求参数错误
- `404 Not Found`: 服务处理器未找到
- `500 Internal Server Error`: 内部错误或底层服务调用失败

### 故障排查

1. **服务无法启动**
   - 检查配置文件是否存在
   - 检查端口是否被占用
   - 查看日志文件错误信息

2. **网关转发失败**
   - 检查底层服务是否启动
   - 验证服务地址配置
   - 检查网络连接

3. **认证失败**
   - 验证 X-App-Id 和 X-App-Key 头部
   - 检查认证服务状态

## 开发和扩展

### 添加新服务

1. 在 `config/gateway.yaml` 中添加服务配置
2. 在 `init.go` 中注册新服务处理器

```go
// 注册新服务
newServiceHandler := NewHTTPServiceHandler("newservice", cfg.GetNewServiceConfig())
gateway.Register(newServiceHandler)
```

### 自定义处理器

```go
type CustomHandler struct {
    serviceID string
}

func (c *CustomHandler) ServiceID() string {
    return c.serviceID
}

func (c *CustomHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
    // 自定义转发逻辑
    return []byte("custom response"), nil
}
```

## 监控和维护

### 性能监控

- 通过健康检查接口监控服务状态
- 查看日志文件了解请求处理情况
- 监控响应时间和错误率

### 日常维护

- 定期检查日志文件大小
- 监控服务内存和CPU使用情况
- 及时更新配置文件

## 安全注意事项

1. **认证头部**: 确保 X-App-Id 和 X-App-Key 的安全性
2. **网络安全**: 在生产环境中使用HTTPS
3. **访问控制**: 限制网关服务的访问来源
4. **日志安全**: 避免在日志中记录敏感信息

## 联系支持

如遇到问题，请：
1. 查看日志文件获取详细错误信息
2. 运行测试脚本验证功能
3. 检查配置文件是否正确 