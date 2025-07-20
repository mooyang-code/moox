# DNS代理服务使用说明

## 功能概述

DNS代理服务提供了域名解析功能，支持：
- 使用多个DNS服务器解析域名
- 测量每个IP的网络延迟
- 按延迟从小到大排序返回结果
- 内存缓存机制提高响应速度
- 定时任务自动更新缓存

## API接口

### 1. DNS解析接口

**接口地址**: `/api/dnsproxy`

**请求方法**: GET 或 POST

**请求参数**:
- `domain`: 要解析的域名（必填）

**请求示例**:
```bash
# GET请求
curl "http://localhost:8080/api/dnsproxy?domain=www.baidu.com"

# POST请求
curl -X POST "http://localhost:8080/api/dnsproxy" \
     -H "Content-Type: application/x-www-form-urlencoded" \
     -d "domain=www.baidu.com"
```

**响应格式**:
```json
{
  "code": 200,
  "data": [
    {
      "domain": "www.baidu.com",
      "ip_list": [
        {
          "ip": "183.2.172.177",
          "latency": 10205625,
          "available": true,
          "last_ping": "2025-07-20T21:19:32.711Z"
        },
        {
          "ip": "103.235.46.102",
          "latency": 10619083,
          "available": true,
          "last_ping": "2025-07-20T21:19:32.711Z"
        }
      ],
      "resolve_at": "2025-07-20T21:19:32.699Z",
      "success": true,
      "message": "成功解析到 4 个IP地址"
    }
  ]
}
```

**字段说明**:
- `domain`: 解析的域名
- `ip_list`: IP地址列表（按延迟排序）
  - `ip`: IP地址
  - `latency`: 网络延迟（纳秒）
  - `available`: 是否可用
  - `last_ping`: 最后一次ping时间
- `resolve_at`: 解析时间
- `success`: 是否解析成功
- `message`: 消息说明

## 缓存机制

### 缓存策略
- 默认缓存时间：10分钟
- 缓存清理间隔：5分钟
- 优先从缓存获取结果，缓存未命中时实时解析
- 只有成功解析的结果才会被缓存

### 缓存管理函数
```go
// 获取缓存统计信息
stats := api.GetCacheStats()

// 清空缓存
api.ClearCache()

// 设置缓存配置
api.SetCacheConfig([]string{"www.example.com"}, 15*time.Minute)
```

## 配置文件

### 配置文件位置
DNS代理服务使用独立的配置文件：`server/config/dnsproxy.yaml`

### 配置文件结构
```yaml
dnsproxy:
  # 需要定时解析的域名列表
  domains:
    - www.baidu.com
    - www.google.com
    - www.github.com
    # ... 更多域名

  # 缓存配置
  cache:
    ttl_minutes: 10        # 缓存过期时间（分钟）
    cleanup_interval: 5    # 缓存清理间隔（分钟）

  # DNS服务器配置
  dns_servers:
    - name: "Google DNS Primary"
      address: "8.8.8.8:53"
      enabled: true
    # ... 更多DNS服务器

  # 超时配置
  timeouts:
    dns_query_seconds: 5      # DNS查询超时时间（秒）
    latency_test_seconds: 3   # 延迟测试超时时间（秒）
    concurrent_limit: 5       # 并发连接限制

  # 延迟检测配置
  latency:
    test_ports: ["80", "443", "22"]  # 测试端口列表
    retry_count: 2                   # 重试次数
    retry_delay: 100                 # 重试延迟（毫秒）
```

### 配置说明

#### 域名列表 (domains)
- 这些域名会被定时任务自动解析并缓存
- 建议添加常用的、访问频率高的域名
- 支持子域名和国际化域名

#### 缓存配置 (cache)
- `ttl_minutes`: 缓存过期时间，建议5-30分钟
- `cleanup_interval`: 清理间隔，0表示自动设置为TTL的一半

#### DNS服务器 (dns_servers)
- 支持多个DNS服务器，提高解析成功率
- `enabled: false` 可以临时禁用某个服务器
- 建议启用3-5个不同的DNS服务器

#### 超时配置 (timeouts)
- `dns_query_seconds`: DNS查询超时，建议3-10秒
- `latency_test_seconds`: 延迟测试超时，建议1-5秒
- `concurrent_limit`: 并发连接数，建议3-10个

#### 延迟检测 (latency)
- `test_ports`: 多个测试端口，会依次尝试
- `retry_count`: 失败重试次数，建议1-3次
- `retry_delay`: 重试间隔，建议50-500毫秒

## 定时任务

### 定时任务功能
- 定时解析配置文件中的域名列表
- 自动更新缓存中的解析结果
- 确保用户请求时能快速从缓存获取数据

### 定时任务入口
```go
// 定时任务入口函数
func DnsproxySchedule(ctx context.Context, params string) error
```

## 错误处理

### 常见错误码
- `400`: 缺少域名参数
- `500`: DNS解析失败

### 错误示例
```json
{
  "code": 400,
  "data": [
    {
      "success": false,
      "message": "缺少域名参数，请提供domain参数"
    }
  ]
}
```

## 性能特点

1. **多DNS服务器**: 使用多个DNS服务器提高解析成功率
2. **并发延迟测试**: 最多5个并发连接测试延迟
3. **智能排序**: 可用IP优先，按延迟从小到大排序
4. **缓存机制**: 减少重复解析，提高响应速度
5. **定时更新**: 自动更新热门域名的解析结果

## 日志级别

- `INFO`: 重要操作日志
- `DEBUG`: 详细调试信息
- `WARN`: 警告信息（如DNS服务器超时）
- `ERROR`: 错误信息

## 测试

运行测试命令：
```bash
cd server
go test ./test -v -run TestDNSProxy
```

测试覆盖：
- 基本DNS解析功能
- 缓存机制验证
- 定时任务执行
- 错误处理
