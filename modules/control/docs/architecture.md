# MooX Server 架构文档

## 1. 系统概述

MooX Server 是一个基于 Go 语言和 TRPC 框架构建的云函数管理平台，主要用于管理多云厂商（腾讯云、阿里云等）的云函数节点，提供统一的接口进行节点创建、部署、监控和管理。

### 1.1 核心功能

- **多云管理**：支持多个云厂商的云函数管理（腾讯云 SCF、阿里云 FC 等）
- **云账户管理**：管理多个云厂商账户，支持密钥加密存储
- **节点管理**：创建、部署、删除、监控云函数节点
- **代码包管理**：上传、存储、分发云函数代码包
- **异步任务**：支持长时间运行的异步任务（节点创建、部署等）
- **心跳监控**：实时监控节点健康状态
- **数据采集**：定时采集云函数运行数据
- **WebSSH**：提供 Web 终端访问功能
- **认证鉴权**：基于 JWT 的用户认证和鉴权

### 1.2 技术栈

- **框架**：TRPC-Go（腾讯开源 RPC 框架）
- **数据库**：SQLite / MySQL / PostgreSQL（基于 GORM）
- **对象存储**：腾讯云 COS（支持扩展）
- **加密**：AES-256-GCM（云账户密钥加密）
- **日志**：TRPC-Go 日志系统
- **配置**：YAML + 环境变量

## 2. 系统架构

### 2.1 分层架构

```
┌─────────────────────────────────────────────────────────────┐
│                      API Layer (TRPC)                       │
│  ┌─────────┬─────────┬───────────┬──────────┬──────────┐   │
│  │ Auth    │CloudNode│ Collector │PackageMgr│Heartbeat │   │
│  │   API   │   API   │    API    │   API    │   API    │   │
│  └─────────┴─────────┴───────────┴──────────┴──────────┘   │
└─────────────────────────────────────────────────────────────┘
                             │
┌─────────────────────────────────────────────────────────────┐
│                      Service Layer                          │
│  ┌──────────┬──────────┬──────────┬──────────┬─────────┐   │
│  │Auth      │CloudNode │Collector │PackageMgr│Heartbeat│   │
│  │Service   │Manager   │Manager   │Service   │Manager  │   │
│  └──────────┴──────────┴──────────┴──────────┴─────────┘   │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │           AsyncTask Service (通用异步任务)          │   │
│  │  - Job/Task 模型                                    │   │
│  │  - Worker 池                                        │   │
│  │  - 任务队列                                         │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                             │
┌─────────────────────────────────────────────────────────────┐
│                    Data Access Layer                        │
│  ┌──────────┬──────────┬──────────┬──────────┬─────────┐   │
│  │Auth      │CloudNode │Collector │PackageMgr│AsyncTask│   │
│  │  DAO     │   DAO    │   DAO    │   DAO    │   DAO   │   │
│  └──────────┴──────────┴──────────┴──────────┴─────────┘   │
└─────────────────────────────────────────────────────────────┘
                             │
┌─────────────────────────────────────────────────────────────┐
│                  Infrastructure Layer                       │
│  ┌─────────┬──────────┬───────────┬──────────┬─────────┐   │
│  │Database │Provider  │  Storage  │  Crypto  │  Config │   │
│  │Manager  │(多云SDK) │  (COS)    │ (AES256) │ Manager │   │
│  └─────────┴──────────┴───────────┴──────────┴─────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 模块依赖关系

```
┌────────────────────────────────────────────────────────────┐
│                     Bootstrap (启动层)                     │
│  - 配置加载                                                │
│  - 服务初始化                                              │
│  - TRPC 服务器启动                                         │
└────────────────────────────────────────────────────────────┘
                         │
        ┌────────────────┼────────────────┐
        ▼                ▼                ▼
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│   Auth      │  │  CloudNode  │  │  Collector  │
│   Module    │  │   Module    │  │   Module    │
│             │  │             │  │             │
│  - Login    │  │ - Account   │  │ - Task Cfg  │
│  - JWT      │  │ - SCF Node  │  │ - Instance  │
│  - Password │  │ - Heartbeat │  │ - Executor  │
└─────────────┘  └─────┬───────┘  └─────────────┘
                       │
        ┌──────────────┼──────────────┐
        ▼              ▼              ▼
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│ PackageMgr  │  │  AsyncTask  │  │  Heartbeat  │
│   Module    │  │   Module    │  │   Module    │
│             │  │             │  │             │
│ - Upload    │  │ - Job/Task  │  │ - Monitor   │
│ - Download  │  │ - Worker    │  │ - Check     │
│ - Storage   │  │ - Registry  │  │ - Update    │
└─────────────┘  └─────────────┘  └─────────────┘
        │              ▲              │
        └──────────────┴──────────────┘
                       │
        ┌──────────────┴──────────────┐
        ▼                             ▼
┌─────────────┐              ┌─────────────┐
│  Database   │              │  Provider   │
│   Module    │              │   Module    │
│             │              │             │
│ - Manager   │              │ - Tencent   │
│ - GORM      │              │ - Aliyun    │
│ - SQLite    │              │ - Abstract  │
└─────────────┘              └─────────────┘
```

### 2.3 模块职责

| 模块 | 职责 | 依赖模块 |
|------|------|----------|
| **auth** | 用户认证、JWT 管理、密码加密 | database |
| **cloudnode** | 云账户管理、云节点管理、心跳处理 | database, provider, packagemgr, asynctask |
| **collector** | 数据采集任务配置、实例管理、定时执行 | database, cloudnode, provider |
| **packagemgr** | 代码包上传、下载、存储管理 | database, asynctask, storage(COS) |
| **asynctask** | 异步任务编排、Worker 执行、状态管理 | database |
| **heartbeat** | 节点心跳监控、健康检查、状态更新 | database, cloudnode, provider |
| **database** | 数据库连接管理、事务处理 | - |
| **provider** | 多云厂商 SDK 封装、统一接口 | - |
| **dnsproxy** | DNS 代理服务 | - |
| **fileserver** | 文件下载服务 | - |
| **ssh** | WebSSH 服务 | - |

## 3. 核心设计模式

### 3.1 服务接口模式

每个模块都定义了顶层接口（`interface.go`），通过接口实现模块间的解耦：

```go
// 模块对外暴露接口
package packagemgr

type Service interface {
    GetPackageList(ctx context.Context, req *PackageListRequest) (*PackageListResponse, error)
    GetPackageDetail(ctx context.Context, id int64) (*PackageDetail, error)
    DeletePackage(ctx context.Context, id int64) error
    // ...
}

// 工厂函数创建实例
func NewService(dbManager *database.Manager, asyncTask asynctask.Service) Service {
    // 内部创建 DAO
    db := dbManager.GetDB()
    packageDAO := dao.NewFunctionPackageDAO(db)
    return impl.NewFunctionPackageService(packageDAO, asyncTask)
}
```

**优势**：
- 模块间只依赖接口，不依赖具体实现
- DAO 封装在模块内部，外部无法直接访问
- 便于单元测试和 Mock

### 3.2 DAO 封装模式

每个模块的 DAO 层完全封装在模块内部：

```
packagemgr/
├── interface.go      # 对外接口
├── impl/             # 业务逻辑实现
├── dao/              # 数据访问层（内部）
├── model/            # 数据模型
└── executors/        # 异步任务处理器
```

**原则**：
- 跨模块交互通过 Service 接口
- 不允许直接访问其他模块的 DAO
- 保持模块边界清晰

### 3.3 异步任务模式（Job-Task 模型）

```
Job (作业)
├── Task 1 (任务)
├── Task 2 (任务)
└── Task 3 (任务)
```

**特点**：
- 一个 Job 包含多个 Task
- Task 之间可以有依赖关系
- 支持任务重试和失败处理
- Worker 池并发执行任务

**使用场景**：
- 创建云节点（多步骤）
- 部署云节点（上传代码 + 更新配置）
- 上传代码包（文件上传 + 存储）
- 删除云节点（云端删除 + 本地清理）

### 3.4 Provider 抽象模式

统一不同云厂商的 API 接口：

```go
type Client interface {
    // 云函数操作
    CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*CreateFunctionResponse, error)
    UpdateFunction(ctx context.Context, req *UpdateFunctionRequest) error
    DeleteFunction(ctx context.Context, req *DeleteFunctionRequest) error
    InvokeFunction(ctx context.Context, req *InvokeFunctionRequest) (*InvokeFunctionResponse, error)
    GetFunctionLogs(ctx context.Context, req *GetFunctionLogsRequest) (*GetFunctionLogsResponse, error)
}
```

**实现**：
- `provider/tencent/` - 腾讯云 SCF
- `provider/aliyun/` - 阿里云 FC（待实现）

### 3.5 加密存储模式

使用 GORM 钩子实现透明加密/解密：

```go
// BeforeCreate - 创建前加密
func (c *CloudAccount) BeforeCreate(tx *gorm.DB) error {
    return c.encryptSecrets()
}

// AfterFind - 查询后解密
func (c *CloudAccount) AfterFind(tx *gorm.DB) error {
    return c.decryptSecrets()
}
```

**优势**：
- 业务层无需关心加密逻辑
- 数据库存储密文，提高安全性
- 使用 AES-256-GCM 认证加密

## 4. 数据库设计

### 4.1 核心表结构

| 表名 | 说明 | 关键字段 |
|------|------|----------|
| `t_users` | 用户表 | user_id, username, password_hash |
| `t_cloud_accounts` | 云账户表 | account_id, provider, secret_id(加密), secret_key(加密) |
| `t_cloud_nodes` | 云节点表 | node_id, account_id, status, package_id |
| `t_heartbeats` | 心跳记录表 | node_id, last_heartbeat_time, status |
| `t_function_packages` | 代码包表 | id, name, version, storage_path |
| `t_async_jobs` | 异步作业表 | job_id, status, progress |
| `t_async_job_tasks` | 异步任务表 | task_id, job_id, task_type, status |
| `t_collector_task_configs` | 采集任务配置表 | id, name, cron_expr, target_type |
| `t_collector_task_instances` | 采集任务实例表 | id, config_id, status, result |

### 4.2 数据模型关系

```
t_users (用户)
    │
    └── 管理多个 ──> t_cloud_accounts (云账户)
                         │
                         ├── 创建多个 ──> t_cloud_nodes (云节点)
                         │                     │
                         │                     ├── 关联 ──> t_function_packages (代码包)
                         │                     │
                         │                     └── 产生 ──> t_heartbeats (心跳)
                         │
                         └── 触发 ──> t_async_jobs (异步作业)
                                         │
                                         └── 包含 ──> t_async_job_tasks (任务)

t_collector_task_configs (采集配置)
    │
    └── 产生 ──> t_collector_task_instances (采集实例)
```

## 5. 启动流程

### 5.1 初始化顺序

```
1. main.go
   └── bootstrap.Initialize()
       ├── LoadConfigs()                    # 加载配置
       │   ├── 加载 app.yaml
       │   ├── 加载 trpc_go.yaml
       │   └── 环境变量覆盖
       │
       ├── config.SetGlobalConfig()         # 设置全局配置
       │
       ├── StartBackgroundServices()        # 启动后台服务
       │   ├── database.NewManager()        # 初始化数据库
       │   │   └── 自动迁移表结构
       │   │
       │   ├── asynctask.NewService()       # 创建异步任务服务
       │   ├── packagemgr.NewService()      # 创建包管理服务
       │   ├── cloudnode 相关服务            # 创建云节点服务（含心跳内存存储）
       │   │
       │   ├── 注册异步任务处理器
       │   │   ├── cloudnode executors
       │   │   └── packagemgr executors
       │   │
       │   ├── asyncTaskService.StartWorker() # 启动 Worker
       │   ├── fileserver.StartFileDownloadService() # 文件下载服务
       │   └── sshapp.StartWebSSHService()  # WebSSH 服务
       │
       └── RegisterTRPCServices()           # 注册 TRPC 服务
           ├── Auth API
           ├── CloudNode API
           ├── Collector API
           ├── PackageMgr API
           └── Heartbeat API

2. trpc.NewServer()                         # 创建 TRPC 服务器
   └── 启动 HTTP/RPC 监听
```

### 5.2 配置优先级

```
环境变量 > 配置文件 > 默认值
```

示例：
- 加密密钥：`MOOX_ENCRYPTION_KEY` > `config.security.encryption_key` > `"moox-cloud-secret-key-32bytes"`
- 数据库路径：`DB_PATH` > `config.database.path` > `"./data/moox.db"`

## 6. 关键流程

### 6.1 创建云节点流程

```
1. API: POST /api/cloudnode/create
   └── CloudNodeHandler.CreateNode()

2. Service: 创建异步 Job
   └── asyncTaskService.AsyncJobCreate([
       {TaskType: "CREATE_NODE", Params: {...}}
   ])

3. AsyncTask: Worker 池消费任务
   └── Worker.processTask()
       └── 根据 TaskType 查找处理器
           └── cloudnode.CreateNodeExecutor

4. Executor: 执行创建逻辑
   ├── 获取云账户
   ├── 获取 Provider Client
   ├── 调用云厂商 API 创建函数
   ├── 保存节点到数据库
   └── 更新任务状态

5. 返回: 客户端轮询 Job 状态
   └── GET /api/asynctask/query?job_id=xxx
```

### 6.2 心跳监控流程

```
1. 节点启动后定时发送心跳
   └── POST /api/heartbeat/report
       {node_id, load, timestamp}

2. HeartbeatService 处理心跳
   ├── 更新内存心跳存储
   ├── 更新节点采集器/版本缓存（如有变更）
   └── 返回配置更新

3. 在线状态判断（按需）
   └── 通过最后心跳时间 + 超时阈值计算在线/离线

4. 初始化新节点协程
   └── initializeNewNodes()
       ├── 查询已部署但未初始化的节点
       ├── 调用云函数触发初始化
       └── 更新节点状态
```

### 6.3 代码包上传流程

```
1. API: POST /api/package/upload
   └── PackageHandler.UploadPackage()

2. Service: 创建异步 Job
   └── asyncTaskService.AsyncJobCreate([
       {TaskType: "UPLOAD_FILE", Params: {file_data, ...}}
   ])

3. AsyncTask: Worker 执行上传
   └── packagemgr.UploadFileExecutor
       ├── 保存到本地临时目录
       ├── 上传到 COS（如果配置）
       ├── 保存记录到数据库
       └── 清理临时文件

4. 返回: 代码包 ID
   └── 可用于创建/部署云节点
```

## 7. 安全设计

### 7.1 认证鉴权

- **JWT Token**：基于 JWT 的无状态认证
- **密码加密**：Bcrypt 哈希存储
- **登录保护**：失败次数限制、账户锁定
- **中间件**：统一的认证拦截器

### 7.2 数据加密

- **云账户密钥**：AES-256-GCM 加密存储
- **加密密钥管理**：环境变量或配置文件
- **透明加解密**：GORM 钩子自动处理

### 7.3 数据脱敏

- **API 返回**：敏感字段自动脱敏
- **日志输出**：不记录敏感信息
- **示例**：`secret_key: "sec********789"`

## 8. 扩展性设计

### 8.1 多云厂商支持

通过 Provider 接口抽象，轻松扩展新的云厂商：

```go
// 1. 实现 Provider.Client 接口
type AliyunClient struct {
    // ...
}

// 2. 注册到工厂
factory.RegisterProvider("aliyun", NewAliyunClient)
```

### 8.2 异步任务扩展

新增任务类型只需：

```go
// 1. 定义任务处理器
func MyTaskExecutor(ctx context.Context, params string) error {
    // 业务逻辑
}

// 2. 注册处理器
asyncTaskService.RegisterHandler("MY_TASK", MyTaskExecutor, "我的任务")
```

### 8.3 存储扩展

支持多种存储后端：
- 本地文件系统
- 腾讯云 COS
- 阿里云 OSS（待扩展）
- AWS S3（待扩展）

## 9. 性能优化

### 9.1 数据库优化

- **连接池管理**：MaxOpenConns、MaxIdleConns
- **索引优化**：关键字段建立索引
- **预编译语句**：GORM 自动处理
- **事务控制**：批量操作使用事务

### 9.2 并发处理

- **Worker 池**：可配置的异步任务 Worker 数量
- **协程并发**：心跳监控、文件服务使用协程
- **并发安全**：sync.Map 管理节点状态

### 9.3 缓存策略

- **内存缓存**：节点状态、Provider 客户端
- **文件缓存**：代码包本地缓存（可配置大小和过期时间）

## 10. 监控和日志

### 10.1 日志级别

- `DEBUG`：详细调试信息
- `INFO`：一般运行信息
- `WARN`：警告信息
- `ERROR`：错误信息

### 10.2 关键日志点

- 服务启动/关闭
- 异步任务执行
- 云 API 调用
- 心跳超时
- 数据库错误
- 加密/解密失败

### 10.3 指标监控

- 异步任务队列长度
- Worker 执行时间
- API 响应时间
- 节点在线率
- 心跳延迟

## 11. 部署架构

### 11.1 单机部署

```
┌─────────────────────────────┐
│      MooX Server (单进程)   │
│  ┌───────────────────────┐  │
│  │   TRPC Server         │  │
│  ├───────────────────────┤  │
│  │   Service Layer       │  │
│  ├───────────────────────┤  │
│  │   Worker Pool         │  │
│  ├───────────────────────┤  │
│  │   SQLite              │  │
│  └───────────────────────┘  │
└─────────────────────────────┘
```

### 11.2 分布式部署（未来）

```
┌──────────────┐   ┌──────────────┐   ┌──────────────┐
│  API Server  │   │  API Server  │   │  API Server  │
└──────┬───────┘   └──────┬───────┘   └──────┬───────┘
       │                  │                  │
       └──────────────────┼──────────────────┘
                          │
                  ┌───────▼────────┐
                  │  Load Balancer │
                  └───────┬────────┘
                          │
       ┌──────────────────┼──────────────────┐
       │                  │                  │
┌──────▼───────┐   ┌──────▼───────┐   ┌──────▼───────┐
│   Worker 1   │   │   Worker 2   │   │   Worker 3   │
└──────────────┘   └──────────────┘   └──────────────┘
       │                  │                  │
       └──────────────────┼──────────────────┘
                          │
                  ┌───────▼────────┐
                  │  MySQL/Postgres│
                  └────────────────┘
```

## 12. 开发规范

### 12.1 代码组织

```
internal/service/[module]/
├── interface.go       # 对外接口定义
├── impl/              # 业务逻辑实现
│   └── service_impl.go
├── dao/               # 数据访问层
│   └── xxx_dao.go
├── model/             # 数据模型
│   └── xxx.go
├── api/               # HTTP API 处理器
│   ├── handler.go
│   └── router.go
├── executors/         # 异步任务执行器（如有）
│   └── xxx_executor.go
└── README.md          # 模块说明文档
```

### 12.2 命名规范

- **包名**：小写，单数形式（`packagemgr`, `asynctask`）
- **接口**：名词，首字母大写（`Service`, `DAO`）
- **实现**：接口名 + `Impl` 后缀（`ServiceImpl`）
- **函数**：驼峰命名，动词开头（`CreateNode`, `GetPackageList`）
- **常量**：大写下划线分隔（`MAX_RETRY_COUNT`）

### 12.3 错误处理

```go
// 返回错误，携带上下文
if err != nil {
    return fmt.Errorf("failed to create node: %w", err)
}

// 记录错误日志
log.ErrorContextf(ctx, "Failed to create node: %v", err)
```

### 12.4 接口设计原则

- **依赖倒置**：高层模块依赖接口，不依赖具体实现
- **单一职责**：每个接口只负责一个功能领域
- **最小暴露**：只暴露必要的方法
- **避免循环依赖**：通过接口和依赖注入解决

## 13. 未来规划

### 13.1 功能增强

- [ ] 支持更多云厂商（阿里云、华为云、AWS）
- [ ] WebUI 管理界面
- [ ] 节点负载均衡
- [ ] 自动扩缩容
- [ ] 成本分析报表
- [ ] 告警通知（邮件、短信、Webhook）

### 13.2 性能优化

- [ ] 分布式任务队列（Redis、RabbitMQ）
- [ ] 数据库读写分离
- [ ] 缓存优化（Redis）
- [ ] API 限流和熔断

### 13.3 运维增强

- [ ] Prometheus 指标导出
- [ ] 链路追踪（Jaeger）
- [ ] 配置中心集成（Apollo、Nacos）
- [ ] 容器化部署（Docker、K8s）

## 14. 相关文档

- [AsyncTask 模块文档](./asynctask.md)
- [Auth 模块文档](./auth.md)
- [CloudNode 模块文档](./cloudnode.md)
- [Collector 模块文档](./collector.md)
- [PackageMgr 模块文档](./packagemgr.md)
- [Database 模块文档](./database.md)
- [云账户密钥加密说明](./cloud_account_encryption.md)
- [API 接口文档](./api.md)
