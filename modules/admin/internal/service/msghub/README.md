# MsgHub - 持久化消息总线

MsgHub 是一个基于 NATS JetStream 的持久化消息总线，专为后台自动程序设计，支持发送端和消费端的钩子函数。

## 特性

- **持久化存储**: 基于 NATS JetStream 的文件存储，消息持久化到磁盘
- **发送端钩子**: 支持消息发送前的钩子函数（验证、加密、日志等）
- **消费端钩子**: 支持消息推送前的钩子函数（过滤、解密、验证等）
- **灵活的消息处理**: 支持手动 Ack/Nak，保证消息可靠处理
- **并发控制**: 支持设置最大并发处理数
- **自动重连**: 连接断开自动重连
- **多发送端/消费端**: 支持注册多个发送端和消费端

## 架构

```
msghub/
├── types/              # 类型定义和接口
│   ├── types.go       # Message、Options 等基础类型
│   ├── hooks.go       # 钩子函数定义和 HookChain
│   ├── publisher.go   # Publisher 接口
│   ├── consumer.go    # Consumer 接口
│   └── server.go      # MessageServer 接口
├── publisher/         # 发送端实现
│   ├── nats/          # NATS Publisher 实现
│   └── registry/      # Publisher 类型注册表
├── consumer/          # 消费端实现
│   ├── nats/          # NATS Consumer 实现
│   └── registry/      # Consumer 类型注册表
├── server/            # NATS Server 管理
│   ├── nats/          # NATS Server 实现
│   └── registry/      # Server 类型注册表
├── service.go         # 核心 Service 接口
├── impl.go            # Service 实现
├── registry.go        # Publisher/Consumer 实例注册管理
├── config.go          # 配置管理
├── utils.go           # 辅助工具函数
└── example/           # 示例代码
```

## 快速开始

### 1. 创建 MsgHub 服务

```go
import (
    "github.com/mooyang-code/moox/modules/admin/internal/service/msghub"
    "github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"
)

// 创建服务
svc, err := msghub.NewService(msghub.ServiceOptions{
    ServerType: msghub.NATSServerType,
    ServerOpts: types.ServerOptions{
        Host:     "127.0.0.1",
        Port:     4222,
        StoreDir: "/data/msghub",
        Timeout:  5 * time.Second,
    },
    AutoStart: true, // 自动启动服务器
})
```

### 2. 注册发送端（带钩子）

```go
err = svc.RegisterPublisher("my-publisher", msghub.NATSPublisherType, types.PublisherOptions{
    ServerURL:      "nats://127.0.0.1:4222",
    ConnectTimeout: 10 * time.Second,
    StreamName:     "my-stream",
    StreamSubjects: []string{"topic.created", "topic.updated"},
    PrePublishHook: func(msg *types.Message) error {
        // 发送前钩子：验证、加密、日志记录等
        log.Infof("发送消息: %s", msg.ID)
        msg.AddHeader("X-Source", "my-service")
        return nil
    },
})
```

### 3. 注册消费端（带钩子）

```go
err = svc.RegisterConsumer("my-consumer", msghub.NATSConsumerType, types.ConsumerOptions{
    ServerURL:      "nats://127.0.0.1:4222",
    ConnectTimeout: 10 * time.Second,
    StreamName:     "my-stream",
    Subject:        "topic.created",
    ConsumerName:   "my-processor",
    MaxInFlight:    100,
    AckWait:        30 * time.Second,
    PrePushHook: func(msg *types.Message) error {
        // 推送前钩子：过滤、解密、验证等
        log.Infof("接收消息: %s", msg.ID)
        if msg.GetHeader("X-Source") == "" {
            return fmt.Errorf("无效的消息来源")
        }
        return nil
    },
    Handler: func(msg *types.Message) error {
        // 业务逻辑处理
        log.Infof("处理消息: %s, 数据: %s", msg.ID, string(msg.Data))
        // 处理成功返回 nil，失败返回 error（会触发 Nak）
        return nil
    },
})
```

### 4. 启动消费端

```go
// 启动消费者开始接收消息
err = svc.StartConsumer("my-consumer")
```

### 5. 发送消息

```go
// 获取发送端
pub, err := svc.GetPublisher("my-publisher")

// 创建消息
msg := msghub.NewMessage("topic.created", []byte(`{"id": "123", "name": "test"}`))

// 发送消息
err = pub.PublishMsg(context.Background(), msg)
```

## 配置文件示例

```yaml
server:
  enable: true
  type: nats
  host: 127.0.0.1
  port: 4222
  store_dir: /data/msghub

publishers:
  - name: order-publisher
    type: nats
    server_url: nats://127.0.0.1:4222
    stream_name: orders
    stream_subjects:
      - order.created
      - order.updated

consumers:
  - name: order-consumer
    type: nats
    server_url: nats://127.0.0.1:4222
    stream_name: orders
    subject: order.created
    consumer_name: order-processor
    max_in_flight: 100
    ack_wait: 30
```

## API 文档

### Service 接口

```go
type Service interface {
    // Publisher 管理
    RegisterPublisher(name string, publisherType PublisherType, opts types.PublisherOptions) error
    GetPublisher(name string) (types.Publisher, error)
    UnregisterPublisher(name string) error
    ListPublishers() []string

    // Consumer 管理
    RegisterConsumer(name string, consumerType ConsumerType, opts types.ConsumerOptions) error
    GetConsumer(name string) (types.Consumer, error)
    StartConsumer(name string) error
    StopConsumer(name string) error
    UnregisterConsumer(name string) error
    ListConsumers() []string

    // 生命周期管理
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    IsRunning() bool
}
```

### Publisher 接口

```go
type Publisher interface {
    Connect(ctx context.Context) error
    Close() error
    Publish(ctx context.Context, subject string, data []byte) (string, error)
    PublishMsg(ctx context.Context, msg *Message) error
    IsConnected() bool
    GetOptions() PublisherOptions
}
```

### Consumer 接口

```go
type Consumer interface {
    Subscribe(ctx context.Context) error
    Start(ctx context.Context) error
    Stop() error
    IsRunning() bool
    GetOptions() ConsumerOptions
}
```

## 钩子函数

### 发送前钩子 (PrePublishHook)

在消息发送到 NATS 之前执行，可用于：
- 消息验证
- 数据加密
- 添加消息头
- 日志记录
- 消息过滤

```go
PrePublishHook: func(msg *types.Message) error {
    // 验证消息
    if len(msg.Data) == 0 {
        return fmt.Errorf("消息数据为空")
    }

    // 添加消息头
    msg.AddHeader("X-Timestamp", time.Now().Format(time.RFC3339))

    // 加密数据
    encrypted, err := encrypt(msg.Data)
    if err != nil {
        return err
    }
    msg.Data = encrypted

    return nil
}
```

### 推送前钩子 (PrePushHook)

在消息推送给业务处理器之前执行，可用于：
- 消息过滤
- 数据解密
- 消息验证
- 日志记录
- 权限检查

```go
PrePushHook: func(msg *types.Message) error {
    // 验证消息来源
    source := msg.GetHeader("X-Source")
    if source != "trusted-service" {
        return fmt.Errorf("不信任的消息来源: %s", source)
    }

    // 解密数据
    decrypted, err := decrypt(msg.Data)
    if err != nil {
        return err
    }
    msg.Data = decrypted

    return nil
}
```

## 错误处理

- **发送端钩子失败**: 消息不会被发送，返回错误
- **推送端钩子失败**: 消息会被 Nak，等待重试
- **业务处理失败**: 消息会被 Nak，等待重试
- **连接断开**: 自动重连（最多 5 次）

## 最佳实践

1. **使用唯一的 ConsumerName**: 确保消费者名称唯一，避免重复消费
2. **合理设置 MaxInFlight**: 根据业务处理能力设置并发数
3. **设置合适的 AckWait**: 根据消息处理时长设置确认等待时间
4. **钩子函数要轻量**: 避免在钩子中执行耗时操作
5. **优雅关闭**: 使用 `Stop()` 方法优雅关闭服务

## 运行示例

```bash
cd example
go run example.go
```

## 依赖

- github.com/nats-io/nats-server/v2
- github.com/nats-io/nats.go
- github.com/rs/xid

## License

MIT
