# AsyncTask 模块文档

## 1. 模块概述

AsyncTask 是 MooX Server 的核心异步任务编排模块，采用 **Job-Task 模型**实现长时间运行任务的异步执行、状态管理和进度追踪。

### 1.1 核心功能

- **任务编排**：支持一个 Job 包含多个 Task
- **异步执行**：基于 Worker 池的并发任务执行
- **状态管理**：完整的任务状态追踪（待处理、处理中、成功、失败）
- **进度查询**：实时查询任务执行进度
- **插件化扩展**：动态注册任务处理器
- **错误处理**：任务失败记录详细错误信息

### 1.2 适用场景

- 云节点创建（多步骤操作）
- 云节点部署（上传代码 + 更新配置）
- 代码包上传（文件处理 + 存储）
- 批量操作（批量创建、批量删除）
- 其他耗时操作

## 2. 架构设计

### 2.1 Job-Task 模型

```
┌─────────────────────────────────────────────────┐
│                    Job (作业)                   │
│  - job_id: "uuid"                               │
│  - total_task_cnt: 3                            │
│  - success_task_cnt: 2                          │
│  - failed_task_cnt: 1                           │
│  - is_started: true                             │
├─────────────────────────────────────────────────┤
│                   Tasks (任务)                  │
│  ┌──────────────────────────────────────────┐  │
│  │ Task 1: CREATE_NODE (成功)               │  │
│  ├──────────────────────────────────────────┤  │
│  │ Task 2: CREATE_NODE (成功)               │  │
│  ├──────────────────────────────────────────┤  │
│  │ Task 3: CREATE_NODE (失败)               │  │
│  └──────────────────────────────────────────┘  │
└─────────────────────────────────────────────────┘
```

**设计特点**：
- **一个 Job 包含多个 Task**：批量操作通过一个 Job 提交
- **Task 独立执行**：每个 Task 独立执行，互不影响
- **状态聚合**：Job 状态由所有 Task 状态聚合计算
- **进度追踪**：根据完成的 Task 数量计算进度百分比

### 2.2 模块结构

```
asynctask/
├── interface.go                # 对外接口定义
├── impl/                       # 业务逻辑实现
│   └── service_impl.go         # Service 实现
├── dao/                        # 数据访问层
│   ├── async_job.go            # Job DAO
│   └── async_job_task.go       # Task DAO
├── model/                      # 数据模型
│   ├── async_job.go            # Job 实体
│   └── async_job_task.go       # Task 实体
├── queue/                      # 任务队列
│   └── task_queue.go           # 内存队列实现
├── registry/                   # 处理器注册表
│   └── handler.go              # TaskHandler 注册管理
├── types/                      # 共享类型定义
│   └── dto.go                  # DTO 类型
├── api/                        # HTTP API
│   ├── handler.go              # HTTP 处理器
│   ├── router.go               # 路由注册
│   └── types.go                # API 请求/响应类型
└── README.md                   # 模块说明
```

### 2.3 核心组件

| 组件 | 职责 | 说明 |
|------|------|------|
| **Service** | 服务接口 | 对外暴露的核心接口 |
| **ServiceImpl** | 服务实现 | 实现异步任务的核心逻辑 |
| **TaskQueue** | 任务队列 | 存储待处理的 Task，Worker 从中消费 |
| **Worker** | 任务消费者 | 从队列中取出 Task 并执行 |
| **TaskHandler** | 任务处理器 | 具体的业务逻辑处理函数 |
| **Registry** | 注册表 | 管理 TaskType 到 TaskHandler 的映射 |
| **DAO** | 数据访问 | 管理 Job 和 Task 的数据库操作 |

## 3. 核心接口

### 3.1 Service 接口

```go
package asynctask

// Service 异步任务服务接口
type Service interface {
    // AsyncJobCreate 创建异步Job（可包含N个Task）
    // tasks: 任务列表，每个任务包含 taskType 和 requestParams
    // 返回: 自动生成的jobID 和 error
    AsyncJobCreate(ctx context.Context, tasks []TaskRequest) (string, error)

    // AsyncJobQuery 查询Job状态
    // jobID: Job唯一标识
    // 返回: JobQueryResult 包含Job状态和Task详情
    AsyncJobQuery(ctx context.Context, jobID string) (*JobQueryResult, error)

    // RegisterHandler 注册任务处理器和显示文本
    // taskType: 任务类型（如 CREATE_NODE, DELETE_NODE 等）
    // handler: 任务处理函数
    // displayText: 任务类型的显示文本（如 "创建节点", "删除节点" 等）
    RegisterHandler(taskType string, handler registry.TaskHandler, displayText string)

    // GetRegistry 获取任务处理器注册表（用于高级操作）
    GetRegistry() *registry.TaskHandlerRegistry

    // StartWorker 启动任务消费者（Worker）
    // workerCount: Worker数量
    StartWorker(ctx context.Context, workerCount int) error

    // StopWorker 停止任务消费者
    StopWorker() error
}
```

### 3.2 TaskHandler 接口

```go
package registry

// TaskHandler 任务处理函数签名
// ctx: 上下文
// taskID: 任务唯一标识
// requestParams: 请求参数（JSON字符串）
// 返回: resultData（JSON字符串）和 error
type TaskHandler func(ctx context.Context, taskID string, requestParams string) (resultData string, err error)
```

## 4. 数据模型

### 4.1 AsyncJob（作业）

```go
type AsyncJob struct {
    JobID          string    `gorm:"column:c_job_id;primaryKey"`
    TotalTaskCnt   int       `gorm:"column:c_total_task_cnt"`
    SuccessTaskCnt int       `gorm:"column:c_success_task_cnt"`
    FailedTaskCnt  int       `gorm:"column:c_failed_task_cnt"`
    IsStarted      bool      `gorm:"column:c_is_started"`
    CreateTime     time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime     time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

**字段说明**：
- `JobID`：Job 唯一标识（UUID）
- `TotalTaskCnt`：总任务数
- `SuccessTaskCnt`：成功任务数
- `FailedTaskCnt`：失败任务数
- `IsStarted`：是否已启动（标记是否已将 Task 加入队列）

### 4.2 AsyncJobTask（任务）

```go
type AsyncJobTask struct {
    TaskID         string    `gorm:"column:c_task_id;primaryKey"`
    JobID          string    `gorm:"column:c_job_id;index"`
    TaskType       string    `gorm:"column:c_task_type"`
    TaskStatus     int       `gorm:"column:c_task_status"`
    RequestParams  string    `gorm:"column:c_request_params;type:text"`
    ResultData     string    `gorm:"column:c_result_data;type:text"`
    ErrorMessage   string    `gorm:"column:c_error_message;type:text"`
    CreateTime     time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime     time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

**字段说明**：
- `TaskID`：Task 唯一标识（UUID）
- `JobID`：所属 Job ID
- `TaskType`：任务类型（如 `CREATE_NODE`）
- `TaskStatus`：任务状态（0-待处理、1-处理中、2-成功、3-失败）
- `RequestParams`：请求参数（JSON 字符串）
- `ResultData`：执行结果（JSON 字符串）
- `ErrorMessage`：错误信息（失败时记录）

### 4.3 任务状态枚举

```go
const (
    TaskStatusPending    = 0 // 待处理
    TaskStatusProcessing = 1 // 处理中
    TaskStatusSuccess    = 2 // 成功
    TaskStatusFailed     = 3 // 失败
)
```

## 5. 工作流程

### 5.1 完整生命周期

```
┌─────────────┐
│ 1. 创建Job  │
│  - 生成jobID│
│  - 保存Job  │
│  - 创建Tasks│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 2. 入队列   │
│  - Task → Q │
│  - 标记启动 │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 3. Worker   │
│    消费     │
│  - 取Task   │
│  - 标记处理中│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 4. 执行     │
│    Handler  │
│  - 业务逻辑 │
│  - 返回结果 │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 5. 更新状态 │
│  - Task状态 │
│  - Job计数  │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 6. 完成     │
│  - 记录结果 │
│  - 或错误   │
└─────────────┘
```

### 5.2 时序图

```
Client          Service         Queue          Worker         Handler
  │                │              │              │              │
  │──Create Job──>│              │              │              │
  │               │──Save Job──>DB             │              │
  │               │              │              │              │
  │               │──Enqueue──> │              │              │
  │               │              │              │              │
  │<──Return jobID│              │              │              │
  │               │              │              │              │
  │               │              │<──Dequeue───│              │
  │               │              │              │              │
  │               │              │              │──Execute────>│
  │               │              │              │              │
  │               │              │              │<──Result─────│
  │               │              │              │              │
  │               │<──Update Task Status───────│              │
  │               │              │              │              │
  │──Query Job──>│              │              │              │
  │               │──Query DB──>DB             │              │
  │<──Result─────│              │              │              │
```

## 6. 使用指南

### 6.1 创建 Service

```go
import (
    "github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"
    "github.com/mooyang-code/moox/modules/admin/internal/service/database"
)

// 方式1：使用 database.Manager（推荐）
dbManager := database.NewManager()
asyncService := asynctask.NewService(dbManager)

// 方式2：直接使用 gorm.DB
asyncService := asynctask.NewServiceWithDB(db)
```

### 6.2 注册 TaskHandler

```go
import (
    "context"
    "encoding/json"
    "fmt"
)

// 定义请求参数结构
type CreateNodeRequest struct {
    CloudAccountID string `json:"cloud_account_id"`
    Region         string `json:"region"`
    FunctionName   string `json:"function_name"`
}

// 定义结果结构
type CreateNodeResult struct {
    NodeID string `json:"node_id"`
    Status string `json:"status"`
}

// 实现 TaskHandler
func CreateNodeHandler(ctx context.Context, taskID string, requestParams string) (string, error) {
    // 1. 解析请求参数
    var req CreateNodeRequest
    if err := json.Unmarshal([]byte(requestParams), &req); err != nil {
        return "", fmt.Errorf("failed to parse params: %w", err)
    }

    // 2. 执行业务逻辑
    nodeID, err := createCloudFunction(ctx, req)
    if err != nil {
        return "", fmt.Errorf("failed to create node: %w", err)
    }

    // 3. 构造返回结果
    result := CreateNodeResult{
        NodeID: nodeID,
        Status: "created",
    }

    resultJSON, _ := json.Marshal(result)
    return string(resultJSON), nil
}

// 注册处理器
asyncService.RegisterHandler("CREATE_NODE", CreateNodeHandler, "创建节点")
```

### 6.3 启动 Worker

```go
// 启动 3 个 Worker
workerCount := 3
if err := asyncService.StartWorker(ctx, workerCount); err != nil {
    log.Fatalf("Failed to start workers: %v", err)
}

// 确保程序退出时停止 Worker
defer asyncService.StopWorker()
```

### 6.4 创建 Job

```go
import "github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"

// 构造任务列表
tasks := []asynctask.TaskRequest{
    {
        TaskType:      "CREATE_NODE",
        RequestParams: `{"cloud_account_id":"acc1","region":"ap-shanghai","function_name":"node1"}`,
    },
    {
        TaskType:      "CREATE_NODE",
        RequestParams: `{"cloud_account_id":"acc2","region":"ap-beijing","function_name":"node2"}`,
    },
}

// 提交 Job
jobID, err := asyncService.AsyncJobCreate(ctx, tasks)
if err != nil {
    log.Fatalf("Failed to create job: %v", err)
}

fmt.Printf("Job created: %s\n", jobID)
```

### 6.5 查询 Job 状态

```go
// 查询 Job
result, err := asyncService.AsyncJobQuery(ctx, jobID)
if err != nil {
    log.Fatalf("Failed to query job: %v", err)
}

// 打印 Job 状态
fmt.Printf("Job Status: %s\n", result.JobStatusText)
fmt.Printf("Progress: %d%%\n", result.Progress)
fmt.Printf("Total: %d, Success: %d, Failed: %d\n",
    result.TotalTaskCnt, result.SuccessTaskCnt, result.FailedTaskCnt)

// 遍历 Task 详情
for _, task := range result.Tasks {
    fmt.Printf("Task %s [%s]: %s\n",
        task.TaskID, task.TaskTypeDisplayText, task.TaskStatusText)

    if task.TaskStatus == 3 { // 失败
        fmt.Printf("  Error: %s\n", task.ErrorMessage)
    }
}
```

## 7. HTTP API

### 7.1 创建 Job

**请求**：
```http
POST /api/asynctask/create
Content-Type: application/json

{
  "tasks": [
    {
      "task_type": "CREATE_NODE",
      "request_params": "{\"cloud_account_id\":\"acc1\",\"region\":\"ap-shanghai\"}"
    }
  ]
}
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "job_id": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

### 7.2 查询 Job

**请求**：
```http
GET /api/asynctask/query?job_id=550e8400-e29b-41d4-a716-446655440000
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "job_id": "550e8400-e29b-41d4-a716-446655440000",
    "job_status": 2,
    "job_status_text": "完成",
    "total_task_cnt": 2,
    "success_task_cnt": 2,
    "failed_task_cnt": 0,
    "progress": 100,
    "tasks": [
      {
        "task_id": "task-1",
        "task_type": "CREATE_NODE",
        "task_type_display_text": "创建节点",
        "task_status": 2,
        "task_status_text": "成功",
        "request_params": "{\"cloud_account_id\":\"acc1\"}",
        "result_data": "{\"node_id\":\"node-123\"}",
        "error_message": ""
      }
    ]
  }
}
```

## 8. 依赖关系

### 8.1 内部依赖

```
asynctask (package level)
├── interface.go → impl/
├── impl/ → dao/, queue/, registry/
├── dao/ → model/
├── api/ → interface.go (Service)
└── executors/ → interface.go (Service)
```

### 8.2 外部依赖

```
asynctask
├── database.Manager        # 数据库管理器
├── gorm.DB                 # GORM 数据库连接
└── context.Context         # 上下文传递
```

### 8.3 被依赖关系

```
asynctask.Service (被以下模块依赖)
├── cloudnode/executors     # 云节点异步任务
├── packagemgr/executors    # 代码包异步任务
└── collector/impl          # 采集器异步任务（可选）
```

## 9. 配置说明

### 9.1 Worker 配置

在 `config/app.yaml` 中配置：

```yaml
worker:
  async_task_worker_count: 3      # AsyncTask Worker 数量
```

### 9.2 队列配置

队列大小在创建时指定：

```go
taskQueue := queue.NewMemoryTaskQueue(5000)  // 队列容量 5000
```

## 10. 最佳实践

### 10.1 任务设计原则

1. **幂等性**：TaskHandler 应该是幂等的，多次执行相同参数应产生相同结果
2. **无状态**：Handler 不应依赖外部状态，所有必要信息应通过 requestParams 传递
3. **错误处理**：明确的错误信息，便于问题定位
4. **超时控制**：长时间运行的任务应检查 context.Done()

### 10.2 参数序列化

推荐使用结构化的 JSON 参数：

```go
// 好的实践
type Params struct {
    Field1 string `json:"field1"`
    Field2 int    `json:"field2"`
}
params, _ := json.Marshal(Params{...})

// 避免
params := "field1=value1&field2=value2"  // 不推荐
```

### 10.3 错误返回

明确的错误信息：

```go
// 好的实践
return "", fmt.Errorf("failed to create node: account %s not found", accountID)

// 避免
return "", errors.New("error")  // 信息不明确
```

### 10.4 Worker 数量

根据任务类型和系统资源配置 Worker 数量：

- **CPU 密集型**：Worker 数 ≈ CPU 核心数
- **IO 密集型**：Worker 数 > CPU 核心数
- **混合型**：根据实际测试调整

## 11. 监控和调试

### 11.1 日志关键点

```go
// ServiceImpl 中的关键日志
log.InfoContextf(ctx, "[AsyncTask] Job created: %s, tasks: %d", jobID, len(tasks))
log.InfoContextf(ctx, "[AsyncTask] Task %s started, type: %s", taskID, taskType)
log.InfoContextf(ctx, "[AsyncTask] Task %s completed, type: %s", taskID, taskType)
log.ErrorContextf(ctx, "[AsyncTask] Task %s failed: %v", taskID, err)
```

### 11.2 指标监控

建议监控的指标：

- 队列长度（当前待处理 Task 数量）
- Worker 执行时间（平均、P95、P99）
- 任务成功率（按 TaskType 统计）
- 任务失败率和失败原因分布

### 11.3 常见问题

| 问题 | 可能原因 | 解决方法 |
|------|----------|----------|
| 任务一直 Pending | Worker 未启动 | 检查 StartWorker() 是否调用 |
| 任务执行缓慢 | Worker 数量不足 | 增加 Worker 数量 |
| 内存占用高 | 队列积压 | 增加 Worker 或优化 Handler |
| Handler 未找到 | 未注册 Handler | 检查 RegisterHandler() 调用 |

## 12. 性能优化

### 12.1 批量操作

使用事务批量更新状态：

```go
// 在 ServiceImpl 中
tx := s.db.Begin()
for _, task := range tasks {
    taskDAO.UpdateTaskStatus(tx, task.TaskID, TaskStatusSuccess)
}
tx.Commit()
```

### 12.2 队列优化

未来可扩展为 Redis 队列：

```go
type RedisTaskQueue struct {
    client *redis.Client
}

func (q *RedisTaskQueue) Enqueue(task *model.AsyncJobTask) error {
    data, _ := json.Marshal(task)
    return q.client.LPush(ctx, "task_queue", data).Err()
}
```

### 12.3 并发控制

限制同一类型任务的并发数：

```go
// 使用带缓冲的 channel 控制并发
semaphore := make(chan struct{}, maxConcurrency)

semaphore <- struct{}{}        // 获取许可
defer func() { <-semaphore }() // 释放许可
```

## 13. 扩展开发

### 13.1 自定义队列

实现 `TaskQueue` 接口：

```go
type TaskQueue interface {
    Enqueue(task *model.AsyncJobTask) error
    Dequeue() (*model.AsyncJobTask, error)
    Size() int
}
```

### 13.2 任务重试

在 Handler 中实现重试逻辑：

```go
func RetryableHandler(ctx context.Context, taskID string, params string) (string, error) {
    var lastErr error
    for i := 0; i < 3; i++ {
        result, err := doTask(ctx, params)
        if err == nil {
            return result, nil
        }
        lastErr = err
        time.Sleep(time.Second * time.Duration(i+1))
    }
    return "", fmt.Errorf("task failed after 3 retries: %w", lastErr)
}
```

### 13.3 任务优先级

扩展 AsyncJobTask 模型：

```go
type AsyncJobTask struct {
    // ... 现有字段
    Priority int `gorm:"column:c_priority"` // 优先级（数字越大优先级越高）
}

// 队列按优先级排序
type PriorityQueue []*AsyncJobTask

func (pq PriorityQueue) Less(i, j int) bool {
    return pq[i].Priority > pq[j].Priority
}
```

## 14. 相关文档

- [架构文档](./architecture.md) - 系统整体架构
- [CloudNode 模块](./cloudnode.md) - 云节点模块（AsyncTask 的主要使用者）
- [PackageMgr 模块](./packagemgr.md) - 代码包模块（AsyncTask 的使用者）
- [Database 模块](./database.md) - 数据库管理
