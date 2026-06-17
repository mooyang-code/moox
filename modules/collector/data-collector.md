

云函数数据采集器重构方案

一、架构设计原则

1. 清晰的职责分离：每个模块只负责一个核心功能
2. 依赖倒置：通过接口解耦具体实现
3. 事件驱动：使用事件总线实现组件间通信
4. 可测试性：便于单元测试和集成测试
5. 云原生设计：充分利用云函数特性

二、核心架构设计

┌─────────────────────────────────────────────────────────┐
│                   Cloud Function Handler                 │
│                  (统一入口，路由请求)                     │
└────────────────┬────────────────────────────────────────┘
│
┌────────────┴────────────┬─────────────┬─────────────┐
▼                         ▼             ▼             ▼
┌─────────┐          ┌──────────────┐ ┌─────────┐ ┌──────────┐
│Config   │          │Task Manager  │ │Heartbeat│ │Health    │
│Sync     │          │(任务管理器)   │ │Reporter │ │Monitor   │
│Handler  │          └──────┬───────┘ └────┬────┘ └──────────┘
└────┬────┘                 │              │
│                      ▼              │
▼              ┌───────────────┐      │
┌─────────┐         │Task Scheduler │      │
│Config   │         │(任务调度器)    │      │
│Store    │         └───────┬───────┘      │
│(持久化) │                 │              │
└─────────┘                 ▼              ▼
┌──────────────────────────────────────┐
│         Collector Registry          │
│      (数据采集器注册中心)             │
└──────────────┬───────────────────────┘
│
┌────────────────┼────────────────┐
▼                ▼                ▼
┌─────────┐     ┌─────────┐     ┌─────────┐
│Binance  │     │Huobi    │     │OKX      │
│Collector│     │Collector│     │Collector│
└─────────┘     └─────────┘     └─────────┘

三、模块详细设计

1. 入口层重构 (/internal/handler/)

// handler.go - 统一请求入口
type CloudFunctionHandler struct {
configHandler    *ConfigSyncHandler
heartbeatHandler *HeartbeatHandler
healthHandler    *HealthCheckHandler
taskManager      *TaskManager
}

// 路由不同类型的请求
func (h *CloudFunctionHandler) Handle(ctx context.Context, event Event) (interface{}, error) {
switch event.Action {
case "config_sync":
return h.configHandler.Handle(ctx, event)
case "heartbeat_probe":
return h.heartbeatHandler.Handle(ctx, event)
case "health_check":
return h.healthHandler.Handle(ctx, event)
default:
return h.handleDefault(ctx, event)
}
}

2. 配置管理重构 (/internal/config/)

// 配置同步处理器
type ConfigSyncHandler struct {
store ConfigStore
eventBus EventBus
}

// 配置存储接口
type ConfigStore interface {
Save(ctx context.Context, config *TaskConfig) error
Load(ctx context.Context) (*TaskConfig, error)
Watch(ctx context.Context) <-chan ConfigChangeEvent
}

// 任务配置结构
type TaskConfig struct {
Version   string
NodeID    string
Tasks     []Task
UpdatedAt time.Time
}

// 使用本地文件系统实现持久化
type FileConfigStore struct {
path string
mu   sync.RWMutex
}

3. 任务管理重构 (/internal/task/)

// 任务管理器
type TaskManager struct {
configStore ConfigStore
scheduler   Scheduler
registry    CollectorRegistry
metrics     MetricsCollector
}

// 任务调度器接口
type Scheduler interface {
Schedule(task Task) error
Cancel(taskID string) error
List() []ScheduledTask
}

// 基于cron的调度器实现
type CronScheduler struct {
cron     *cron.Cron
jobs     map[string]cron.EntryID
mu       sync.RWMutex
}

4. 心跳管理重构 (/internal/heartbeat/)

// 心跳上报器
type HeartbeatReporter struct {
nodeID      string
serverAddr  string  // 从心跳探测请求中获取
client      HeartbeatClient
taskManager *TaskManager
ticker      *time.Ticker
}

// 心跳客户端接口
type HeartbeatClient interface {
Report(ctx context.Context, req *HeartbeatRequest) error
}

// 心跳请求结构
type HeartbeatRequest struct {
NodeID    string
NodeType  string
Timestamp time.Time
Tasks     []TaskStatus
Metrics   map[string]interface{}
}

5. 数据采集器重构 (/internal/collector/)

// 采集器接口
type Collector interface {
ID() string
Type() string
Collect(ctx context.Context) error
Config() interface{}
}

// 采集器注册中心
type CollectorRegistry struct {
collectors map[string]CollectorFactory
mu         sync.RWMutex
}

// 采集器工厂
type CollectorFactory func(config interface{}) (Collector, error)

// 示例：K线数据采集器
type KlineCollector struct {
exchange string
symbol   string
interval string
storage  Storage
}

6. 存储层重构 (/internal/storage/)

// 统一存储接口
type Storage interface {
Save(ctx context.Context, data interface{}) error
Query(ctx context.Context, query Query) ([]interface{}, error)
Close() error
}

// 支持多种存储后端
type StorageFactory struct {
builders map[string]StorageBuilder
}

// S3存储实现（云函数环境）
type S3Storage struct {
bucket string
client S3Client
}

四、关键优化点

1. 生命周期管理

// 云函数生命周期管理
type LifecycleManager struct {
initOnce     sync.Once
shutdownOnce sync.Once
components   []Component
}

type Component interface {
Init(ctx context.Context) error
Shutdown(ctx context.Context) error
}

2. 错误处理与重试

// 统一错误处理
type ErrorHandler struct {
retryPolicy RetryPolicy
alerter     Alerter
}

// 重试策略
type RetryPolicy struct {
MaxRetries int
Backoff    BackoffStrategy
}

3. 监控与可观测性

// 指标收集器
type MetricsCollector struct {
counters map[string]*Counter
gauges   map[string]*Gauge
}

// 日志增强
type StructuredLogger struct {
nodeID string
fields map[string]interface{}
}

五、目录结构重组

data-collector/
├── cmd/
│   ├── serverless/          # 云函数入口
│   └── standalone/          # 独立运行模式
├── internal/
│   ├── handler/            # 请求处理器
│   │   ├── config.go       # 配置同步
│   │   ├── heartbeat.go    # 心跳处理
│   │   └── health.go       # 健康检查
│   ├── config/             # 配置管理
│   │   ├── store.go        # 配置存储
│   │   └── watcher.go      # 配置监听
│   ├── task/               # 任务管理
│   │   ├── manager.go      # 任务管理器
│   │   ├── scheduler.go    # 调度器
│   │   └── executor.go     # 执行器
│   ├── heartbeat/          # 心跳管理
│   │   ├── reporter.go     # 上报器
│   │   └── client.go       # 客户端
│   ├── collector/          # 数据采集
│   │   ├── registry.go     # 注册中心
│   │   ├── factory.go      # 工厂模式
│   │   └── types/          # 采集器类型
│   │       ├── kline.go
│   │       ├── ticker.go
│   │       └── orderbook.go
│   ├── storage/            # 存储层
│   │   ├── interface.go    # 存储接口
│   │   ├── s3/            # S3实现
│   │   └── local/         # 本地实现
│   ├── metrics/            # 监控指标
│   └── utils/              # 工具函数
├── pkg/                    # 可复用包
│   ├── retry/             # 重试机制
│   ├── cache/             # 缓存
│   └── logger/            # 日志
└── configs/               # 配置文件
├── serverless.yaml    # 云函数配置
└── collectors/        # 采集器配置

六、实施步骤

⏺ Update Todos
⎿  Error: Interrupted by user
⎿  Interrupted by user

⏺ 云函数数据采集器重构方案

基于对现有代码的分析，我提出以下全新的架构设计和重构方案：

一、整体架构设计

┌─────────────────────────────────────────────────────────────────┐
│                        服务端（Moox Server）                      │
│  ┌─────────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │ 任务调度中心    │  │ 心跳管理服务 │  │ 配置管理中心    │  │
│  └────────┬────────┘  └──────┬───────┘  └────────┬─────────┘  │
└───────────┼───────────────────┼──────────────────┼─────────────┘
│                   │                   │
▼                   ▼                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                     云函数节点（Data Collector）                  │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │                    统一入口（Handler）                    │   │
│  └─────────────┬───────────────────────────────────────────┘   │
│                │                                                 │
│  ┌─────────────▼────────────────────────────────────────────┐   │
│  │                   核心调度器（Scheduler）                 │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐│   │
│  │  │任务管理器│  │配置管理器│  │心跳管理器│  │状态管理器││   │
│  │  └──────────┘  └──────────┘  └──────────┘  └─────────┘│   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                   数据采集层（Collectors）               │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐│   │
│  │  │K线采集器 │  │行情采集器│  │深度采集器│  │其他采集器││   │
│  │  └──────────┘  └──────────┘  └──────────┘  └─────────┘│   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    基础设施层                             │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐│   │
│  │  │本地缓存  │  │事件总线  │  │监控日志  │  │ 错误处理││   │
│  │  └──────────┘  └──────────┘  └──────────┘  └─────────┘│   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘

二、核心模块重构方案

1. 统一入口处理器（Handler）

// internal/handler/handler.go
type CloudFunctionHandler struct {
scheduler  *Scheduler
config     *Config
lifecycle  *Lifecycle
}

// 处理所有云函数请求
func (h *CloudFunctionHandler) Handle(ctx context.Context, event CloudFunctionEvent) (*Response, error) {
switch event.Type {
case EventTypeInit:
return h.handleInit(ctx, event)
case EventTypeConfig:
return h.handleConfig(ctx, event)
case EventTypeHeartbeat:
return h.handleHeartbeat(ctx, event)
case EventTypeTask:
return h.handleTask(ctx, event)
case EventTypeHealth:
return h.handleHealth(ctx, event)
default:
return nil, ErrUnknownEventType
}
}

2. 核心调度器（Scheduler）

// internal/scheduler/scheduler.go
type Scheduler struct {
taskManager     *TaskManager
configManager   *ConfigManager
heartbeatManager *HeartbeatManager
stateManager    *StateManager
eventBus        *EventBus
}

// 负责协调各个管理器
func (s *Scheduler) Start(ctx context.Context) error {
// 1. 初始化配置
// 2. 启动心跳上报
// 3. 监听任务变更
// 4. 调度采集任务
}

3. 任务管理器（TaskManager）

// internal/task/manager.go
type TaskManager struct {
tasks      map[string]*Task
collectors map[string]Collector
store      TaskStore
mu         sync.RWMutex
}

type Task struct {
ID          string
Type        TaskType        // KLine, Ticker, OrderBook等
Symbol      string          // 交易对
Interval    string          // 时间间隔
Schedule    string          // cron表达式
Config      json.RawMessage // 任务特定配置
Status      TaskStatus
LastRun     time.Time
NextRun     time.Time
Statistics  *TaskStats
}

// 任务存储接口，支持本地文件或云存储
type TaskStore interface {
Load(ctx context.Context) ([]*Task, error)
Save(ctx context.Context, tasks []*Task) error
Watch(ctx context.Context, onChange func([]*Task)) error
}

4. 配置管理器（ConfigManager）

// internal/config/manager.go
type ConfigManager struct {
nodeConfig   *NodeConfig
serverConfig *ServerConfig
store        ConfigStore
notifier     *EventBus
}

type NodeConfig struct {
NodeID       string
NodeType     string
Region       string
Namespace    string
RunningTasks []string
Capabilities []string // 支持的采集类型
}

type ServerConfig struct {
ServerURL    string
ServerPort   int
AuthToken    string
HeartbeatInterval time.Duration
}

// 支持本地缓存和实时更新
type ConfigStore interface {
Get(key string) (interface{}, error)
Set(key string, value interface{}) error
Subscribe(key string, callback func(interface{})) error
}

5. 心跳管理器（HeartbeatManager）

// internal/heartbeat/manager.go
type HeartbeatManager struct {
client       HeartbeatClient
nodeInfo     *NodeInfo
taskManager  *TaskManager
stateManager *StateManager
ticker       *time.Ticker
}

type HeartbeatClient interface {
Report(ctx context.Context, payload *HeartbeatPayload) error
RegisterNode(ctx context.Context, node *NodeInfo) error
}

type HeartbeatPayload struct {
NodeID       string
NodeType     string
Timestamp    time.Time
RunningTasks []*TaskSummary
Metrics      *NodeMetrics
Metadata     map[string]interface{}
}

type NodeMetrics struct {
CPUUsage     float64
MemoryUsage  float64
TaskCount    int
SuccessRate  float64
ErrorCount   int
LastError    string
}

6. 数据采集器接口（Collector）

// internal/collector/interface.go
type Collector interface {
// 采集器元信息
Name() string
Type() CollectorType

      // 生命周期
      Init(config json.RawMessage) error
      Start(ctx context.Context) error
      Stop(ctx context.Context) error

      // 数据采集
      Collect(ctx context.Context, params CollectParams) (*CollectResult, error)

      // 健康检查
      HealthCheck(ctx context.Context) error
}

type CollectParams struct {
Symbol   string
Interval string
StartTime time.Time
EndTime   time.Time
Options   map[string]interface{}
}

type CollectResult struct {
Data      interface{}
Count     int
Timestamp time.Time
Metadata  map[string]interface{}
}

// 采集器注册器
type CollectorRegistry struct {
collectors map[string]func() Collector
}

func (r *CollectorRegistry) Register(name string, factory func() Collector) {
r.collectors[name] = factory
}

三、目录结构重构

data-collector/
├── cmd/
│   ├── standalone/      # 独立运行模式
│   └── serverless/      # 云函数模式
├── configs/
│   ├── default.yaml     # 默认配置
│   ├── serverless.yaml  # 云函数配置
│   └── sources/         # 数据源配置
├── internal/
│   ├── handler/         # 云函数入口处理
│   ├── scheduler/       # 核心调度器
│   ├── task/           # 任务管理
│   ├── config/         # 配置管理
│   ├── heartbeat/      # 心跳管理
│   ├── state/          # 状态管理
│   ├── collector/      # 采集器接口和注册
│   ├── sources/        # 具体数据源实现
│   │   ├── binance/
│   │   ├── okx/
│   │   └── ...
│   ├── storage/        # 存储层抽象
│   ├── event/          # 事件总线
│   ├── metrics/        # 监控指标
│   └── utils/          # 工具函数
├── pkg/                # 可导出的包
│   ├── model/          # 数据模型
│   ├── errors/         # 错误定义
│   └── logger/         # 日志工具
├── deploy/             # 部署相关
│   ├── serverless/     # 云函数部署配置
│   └── docker/         # Docker配置
└── tests/              # 测试代码

四、关键特性设计

1. 任务热更新机制

- 配置变更监听器实时监听任务配置变化
- 支持任务的动态加载、卸载和更新
- 任务版本管理，支持回滚

2. 容错和恢复机制

- 任务执行失败自动重试
- 断点续传支持
- 优雅关闭和状态持久化

3. 监控和告警

- 采集任务执行指标
- 错误率和延迟监控
- 自定义告警规则

4. 资源管理

- 并发控制和速率限制
- 内存使用优化
- 连接池管理

5. 安全机制

- API密钥加密存储
- 请求签名验证
- 访问控制和审计

五、实施步骤

1. 第一阶段：基础架构搭建
   - 实现核心调度器框架
   - 定义标准接口和数据模型
   - 搭建基础设施层（事件总线、日志、错误处理）
2. 第二阶段：核心功能实现
   - 实现任务管理器和配置管理器
   - 实现心跳管理器和状态管理器
   - 完成云函数入口处理器
3. 第三阶段：采集器迁移
   - 将现有采集器适配到新接口
   - 实现采集器注册和管理机制
   - 添加新的数据源支持
4. 第四阶段：高级特性
   - 实现任务热更新
   - 添加监控和告警
   - 优化性能和资源使用
5. 第五阶段：测试和部署
   - 完善单元测试和集成测试
   - 压力测试和性能优化
   - 编写部署文档和运维手册
