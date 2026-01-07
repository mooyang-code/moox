# CloudNode 模块文档

## 1. 模块概述

CloudNode 模块是 MooX Server 的核心业务模块，负责管理多云厂商的云函数节点，包括云账户管理、节点创建/部署/删除、心跳监控等功能。

### 1.1 核心功能

- **云账户管理**：管理多个云厂商账户（腾讯云、阿里云等）
- **密钥加密存储**：云账户 SecretID 和 SecretKey 加密存储
- **云节点管理**：创建、部署、删除、查询云函数节点
- **异步任务编排**：通过 AsyncTask 执行耗时操作
- **心跳监控**：实时监控节点健康状态
- **Provider 抽象**：统一不同云厂商的 API 接口
- **批量操作**：支持批量创建、部署、删除节点

### 1.2 支持的云厂商

- **腾讯云 SCF**（Serverless Cloud Function）
- **阿里云 FC**（Function Compute）- 待实现
- **AWS Lambda** - 待实现

## 2. 架构设计

### 2.1 模块结构

```
cloudnode/
├── manager/                    # 服务管理层
│   ├── cloud_account.go        # 云账户服务
│   └── cloud_node.go           # 云节点服务（SCF）
├── executors/                  # 异步任务执行器
│   ├── create_node.go          # 创建节点执行器
│   ├── deploy_node.go          # 部署节点执行器
│   ├── delete_node.go          # 删除节点执行器
│   └── register.go             # 执行器注册
├── provider/                   # 云厂商抽象层
│   ├── types.go                # Provider 接口定义
│   ├── config.go               # Provider 配置
│   ├── factory.go              # Provider 工厂
│   └── tencent/                # 腾讯云实现
│       ├── client.go           # 腾讯云客户端
│       └── types.go            # 腾讯云类型定义
├── dao/                        # 数据访问层
│   ├── cloud_account.go        # 云账户 DAO
│   └── cloud_node.go           # 云节点 DAO
├── model/                      # 数据模型
│   ├── cloud_account.go        # 云账户实体（加密钩子）
│   └── cloud_node.go           # 云节点实体
├── gateway/                    # TRPC Gateway
│   ├── handler.go              # Gateway 处理器
│   └── register.go             # Gateway 注册
└── api/                        # HTTP API
    ├── cloud_account.go        # 云账户 API
    ├── cloud_node.go           # 云节点 API
    ├── cloud_function_invoke.go # 函数调用 API
    ├── router.go               # 路由注册
    └── types.go                # API 类型定义
```

### 2.2 核心组件

| 组件 | 职责 | 说明 |
|------|------|------|
| **CloudAccountService** | 云账户管理 | CRUD、密钥加密存储 |
| **SCFNodeService** | 云节点管理 | 节点创建、部署、删除、查询 |
| **HeartbeatService** | 心跳管理 | 节点健康监控、状态更新 |
| **Provider** | 云厂商抽象 | 统一不同云厂商 API |
| **AccountFactory** | Provider 工厂 | 根据账户创建 Provider 客户端 |
| **Executors** | 异步任务执行 | 注册到 AsyncTask 的处理器 |

### 2.3 分层架构

```
┌─────────────────────────────────────────────┐
│          API Layer (HTTP + Gateway)         │
│  - CloudAccountHandler                      │
│  - CloudNodeHandler                         │
│  - GatewayHandler (TRPC)                    │
└─────────────────┬───────────────────────────┘
                  │
┌─────────────────▼───────────────────────────┐
│           Manager Layer (Service)           │
│  - CloudAccountService                      │
│  - SCFNodeService                           │
│  - HeartbeatService                         │
└─────────────────┬───────────────────────────┘
                  │
      ┌───────────┼───────────┐
      │           │           │
┌─────▼─────┐ ┌──▼─────┐ ┌──▼──────────┐
│ Provider  │ │  DAO   │ │  AsyncTask  │
│  Layer    │ │ Layer  │ │  Executors  │
└───────────┘ └────────┘ └─────────────┘
```

## 3. 核心接口

### 3.1 CloudAccountService 接口

```go
type CloudAccountService interface {
    // 实现 provider.CloudAccountService
    provider.CloudAccountService

    // CRUD 操作
    CreateAccount(ctx context.Context, account *model.CloudAccount) error
    UpdateAccount(ctx context.Context, account *model.CloudAccount) error
    DeleteAccount(ctx context.Context, accountID string) error
    GetAccount(ctx context.Context, accountID string) (*model.CloudAccount, error)
    ListAccounts(ctx context.Context) ([]*model.CloudAccount, error)
    ListAccountsByProvider(ctx context.Context, provider string) ([]*model.CloudAccount, error)
}
```

### 3.2 SCFNodeService 接口

```go
type SCFNodeService interface {
    // 查询操作
    GetNodeList(ctx context.Context) ([]*model.SCFNode, error)
    GetNode(ctx context.Context, nodeID string) (*model.SCFNode, error)
    GetNodesByType(ctx context.Context, nodeType string) ([]*model.SCFNode, error)
    GetOnlineNodes(ctx context.Context) ([]*model.SCFNode, error)

    // 节点 CRUD（直接调用云厂商 API）
    CreateNode(ctx context.Context, node *model.SCFNode, codeConfig *FunctionCodeConfig) (*model.SCFNode, error)
    UpdateNode(ctx context.Context, node *model.SCFNode) error
    DeleteNode(ctx context.Context, nodeID string) error

    // 数据库操作
    SaveNodeToDB(ctx context.Context, node *model.SCFNode) error
    UpdateNodeStatus(ctx context.Context, nodeID string, status int) error

    // 状态管理
    Heartbeat(ctx context.Context, nodeID string, currentLoad string) error
}
```

### 3.3 Provider.Client 接口

```go
type Client interface {
    // 云函数操作
    CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*CreateFunctionResponse, error)
    UpdateFunction(ctx context.Context, req *UpdateFunctionRequest) error
    DeleteFunction(ctx context.Context, req *DeleteFunctionRequest) error
    InvokeFunction(ctx context.Context, req *InvokeFunctionRequest) (*InvokeFunctionResponse, error)
    GetFunctionLogs(ctx context.Context, req *GetFunctionLogsRequest) (*GetFunctionLogsResponse, error)

    // Trigger 操作
    CreateTrigger(ctx context.Context, req *CreateTriggerRequest) error
    DeleteTrigger(ctx context.Context, req *DeleteTriggerRequest) error

    // 环境变量操作
    UpdateEnvironment(ctx context.Context, req *UpdateEnvironmentRequest) error
}
```

## 4. 数据模型

### 4.1 CloudAccount（云账户）

```go
type CloudAccount struct {
    AccountID   string    `gorm:"column:c_account_id;primaryKey"`
    AccountName string    `gorm:"column:c_account_name"`
    Provider    string    `gorm:"column:c_provider"`        // tencent, aliyun
    SecretID    string    `gorm:"column:c_secret_id"`       // 加密存储
    SecretKey   string    `gorm:"column:c_secret_key"`      // 加密存储
    Region      string    `gorm:"column:c_region"`
    Status      int       `gorm:"column:c_status"`          // 0-禁用, 1-正常
    Description string    `gorm:"column:c_description"`
    CreateTime  time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime  time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

**加密钩子**：
```go
func (c *CloudAccount) BeforeCreate(tx *gorm.DB) error {
    return c.encryptSecrets()  // 创建前加密
}

func (c *CloudAccount) AfterFind(tx *gorm.DB) error {
    return c.decryptSecrets()  // 查询后解密
}
```

### 4.2 SCFNode（云节点）

```go
type SCFNode struct {
    NodeID         string    `gorm:"column:c_node_id;primaryKey"`
    NodeType       string    `gorm:"column:c_node_type"`        // http, dns, mix
    FunctionName   string    `gorm:"column:c_function_name"`
    CloudAccountID string    `gorm:"column:c_cloud_account_id;index"`
    Region         string    `gorm:"column:c_region"`
    Runtime        string    `gorm:"column:c_runtime"`          // Go1, Python3.6
    PackageID      *int64    `gorm:"column:c_package_id"`
    Status         int       `gorm:"column:c_status"`           // 0-离线, 1-在线, 2-创建中, 3-部署中
    IsDeployed     bool      `gorm:"column:c_is_deployed"`
    IsInitialized  bool      `gorm:"column:c_is_initialized"`
    CurrentLoad    string    `gorm:"column:c_current_load"`
    LastHeartbeat  time.Time `gorm:"column:c_last_heartbeat"`
    CreateTime     time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime     time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

**节点状态**：
- `0` - 离线
- `1` - 在线
- `2` - 创建中
- `3` - 部署中

### 4.3 Heartbeat（心跳记录）

```go
type Heartbeat struct {
    NodeID            string    `gorm:"column:c_node_id;primaryKey"`
    LastHeartbeatTime time.Time `gorm:"column:c_last_heartbeat_time"`
    CurrentLoad       string    `gorm:"column:c_current_load"`
    Status            int       `gorm:"column:c_status"`
    UpdateTime        time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

## 5. 核心流程

### 5.1 创建节点完整流程

```
┌──────────────┐
│ 1. API请求   │
│  POST /create│
└──────┬───────┘
       │
       ▼
┌──────────────┐
│ 2. 创建Job   │
│  - 生成jobID │
│  - Task列表  │
└──────┬───────┘
       │
       ▼
┌──────────────┐
│ 3. Worker    │
│    执行      │
│  CreateNode  │
│  Executor    │
└──────┬───────┘
       │
       ├─> 获取云账户
       ├─> 获取Provider
       ├─> 获取代码包
       ├─> 构造请求
       ├─> 调用云API创建函数
       ├─> 保存节点到DB
       └─> 更新任务状态
       │
       ▼
┌──────────────┐
│ 4. 返回结果  │
│  - nodeID    │
│  - status    │
└──────────────┘
```

### 5.2 部署节点流程

```
┌──────────────┐
│ 1. API请求   │
│  POST /deploy│
└──────┬───────┘
       │
       ▼
┌──────────────┐
│ 2. 创建Job   │
│  - DEPLOY_   │
│    NODE Task │
└──────┬───────┘
       │
       ▼
┌──────────────┐
│ 3. Worker    │
│    执行      │
│  DeployNode  │
│  Executor    │
└──────┬───────┘
       │
       ├─> 查询节点信息
       ├─> 获取代码包
       ├─> 上传代码到云
       ├─> 更新函数配置
       ├─> 更新DB状态
       └─> 标记已部署
       │
       ▼
┌──────────────┐
│ 4. 返回结果  │
└──────────────┘
```

### 5.3 心跳监控流程

```
┌─────────────┐
│ 节点定时发送│
│   心跳请求  │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ Heartbeat   │
│   Manager   │
│ - 更新状态  │
│ - 记录时间  │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 后台监控    │
│ 协程检查    │
│ - 超时检测  │
│ - 标记离线  │
└─────────────┘
```

## 6. Provider 抽象层

### 6.1 设计目的

统一不同云厂商的 API 差异，提供一致的接口。

### 6.2 腾讯云实现

```go
type TencentClient struct {
    secretID  string
    secretKey string
    region    string
    scfClient *scf.Client
}

func (c *TencentClient) CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*CreateFunctionResponse, error) {
    // 构造腾讯云 SCF 请求
    scfReq := scf.NewCreateFunctionRequest()
    scfReq.FunctionName = &req.FunctionName
    scfReq.Runtime = &req.Runtime
    scfReq.Code = &scf.Code{
        ZipFile: &req.CodeZipBase64,
    }

    // 调用腾讯云 API
    resp, err := c.scfClient.CreateFunction(scfReq)
    if err != nil {
        return nil, fmt.Errorf("tencent create function failed: %w", err)
    }

    return &CreateFunctionResponse{
        FunctionName: *resp.Response.FunctionName,
    }, nil
}
```

### 6.3 扩展新云厂商

```go
// 1. 实现 Provider.Client 接口
type AliyunClient struct {
    accessKeyID     string
    accessKeySecret string
    region          string
}

func (c *AliyunClient) CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*CreateFunctionResponse, error) {
    // 调用阿里云 FC API
}

// 2. 注册到工厂
func init() {
    RegisterProvider("aliyun", NewAliyunClient)
}
```

## 7. 异步任务集成

### 7.1 注册执行器

```go
// executors/register.go
func RegisterHandlers(
    asyncService asynctask.Service,
    scfNodeService cloudnodemgr.SCFNodeService,
    packageService packagemgr.Service,
) {
    // 创建执行器
    createExecutor := NewCreateNodeExecutor(scfNodeService, packageService)
    deployExecutor := NewDeployNodeExecutor(scfNodeService, packageService)
    deleteExecutor := NewDeleteNodeExecutor(scfNodeService)

    // 注册到 AsyncTask
    asyncService.RegisterHandler("CREATE_NODE", createExecutor.Execute, "创建节点")
    asyncService.RegisterHandler("DEPLOY_NODE", deployExecutor.Execute, "部署节点")
    asyncService.RegisterHandler("DELETE_NODE", deleteExecutor.Execute, "删除节点")
}
```

### 7.2 执行器实现

```go
// executors/create_node.go
type CreateNodeExecutor struct {
    nodeService    cloudnodemgr.SCFNodeService
    packageService packagemgr.Service
}

func (e *CreateNodeExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
    // 1. 解析参数
    var req CreateNodeRequest
    json.Unmarshal([]byte(requestParams), &req)

    // 2. 获取代码包
    pkg, err := e.packageService.GetPackageDetailModel(ctx, req.PackageID)

    // 3. 调用 nodeService 创建节点
    node := &model.SCFNode{
        NodeType:       req.NodeType,
        CloudAccountID: req.CloudAccountID,
        Region:         req.Region,
        PackageID:      &req.PackageID,
    }

    codeConfig := &cloudnodemgr.FunctionCodeConfig{
        Runtime:   pkg.Runtime,
        COSBucket: pkg.COSBucket,
        COSPath:   pkg.COSPath,
    }

    createdNode, err := e.nodeService.CreateNode(ctx, node, codeConfig)

    // 4. 返回结果
    result := CreateNodeResult{
        NodeID: createdNode.NodeID,
        Status: "created",
    }

    resultJSON, _ := json.Marshal(result)
    return string(resultJSON), nil
}
```

## 8. 使用指南

### 8.1 创建云账户

```http
POST /api/cloudnode/account/create
Content-Type: application/json

{
  "account_id": "tencent-acc-001",
  "account_name": "腾讯云主账号",
  "provider": "tencent",
  "secret_id": "AKIDxxxxxxxxxxxxx",
  "secret_key": "xxxxxxxxxxxxxxxx",
  "region": "ap-guangzhou",
  "description": "主账号"
}
```

### 8.2 创建云节点

```http
POST /api/cloudnode/create
Content-Type: application/json

{
  "node_type": "http",
  "cloud_account_id": "tencent-acc-001",
  "region": "ap-guangzhou",
  "package_id": 123,
  "count": 5
}
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "job_id": "uuid-xxx"
  }
}
```

### 8.3 部署节点

```http
POST /api/cloudnode/deploy
Content-Type: application/json

{
  "node_ids": ["node-1", "node-2"],
  "package_id": 456
}
```

### 8.4 查询节点列表

```http
GET /api/cloudnode/list?status=1&node_type=http
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "nodes": [
      {
        "node_id": "node-1",
        "function_name": "moox-node-1",
        "cloud_account_id": "tencent-acc-001",
        "region": "ap-guangzhou",
        "status": 1,
        "is_deployed": true,
        "last_heartbeat": "2023-10-23T10:30:00Z"
      }
    ],
    "total": 1
  }
}
```

## 9. 依赖关系

### 9.1 内部依赖

```
cloudnode (package level)
├── manager/ → dao/, provider/, model/
├── executors/ → manager/, packagemgr.Service, asynctask.Service
├── provider/ → model/
├── api/ → manager/
└── gateway/ → manager/
```

### 9.2 外部依赖

```
cloudnode
├── database.Manager        # 数据库管理器
├── asynctask.Service       # 异步任务服务
├── packagemgr.Service      # 代码包服务
├── crypto                  # 加密工具
└── 云厂商 SDK               # 腾讯云 SDK 等
```

### 9.3 被依赖关系

```
cloudnode (被以下模块依赖)
├── collector               # 采集器需要查询节点
└── gateway                 # Gateway 需要分发请求到节点
```

## 10. 配置说明

### 10.1 云账户配置

通过 API 动态添加，不在配置文件中硬编码。

### 10.2 Worker 配置

```yaml
worker:
  node_creation_worker_count: 3    # 节点创建 Worker 数量
  node_deployment_worker_count: 3  # 节点部署 Worker 数量
  node_deletion_worker_count: 3    # 节点删除 Worker 数量
```

### 10.3 加密密钥配置

```yaml
security:
  encryption_key: "moox-cloud-secret-key-32bytes"
```

或通过环境变量：
```bash
export MOOX_ENCRYPTION_KEY="your-32-byte-key"
```

## 11. 最佳实践

### 11.1 云账户管理

1. **分离生产和测试账户**
2. **定期轮换密钥**
3. **使用只读账户进行监控**
4. **为不同region创建不同账户**

### 11.2 节点命名规范

```go
// 生成唯一节点名称
func GenerateNodeName(nodeType, region string) string {
    timestamp := time.Now().Unix()
    random := rand.Intn(10000)
    return fmt.Sprintf("moox-%s-%s-%d-%04d", nodeType, region, timestamp, random)
}
```

### 11.3 错误处理

```go
// 区分临时错误和永久错误
func HandleProviderError(err error) error {
    if isThrottlingError(err) {
        // 可重试的错误
        return fmt.Errorf("rate limited, please retry: %w", err)
    }
    if isAuthError(err) {
        // 不可重试的错误
        return fmt.Errorf("authentication failed, check credentials: %w", err)
    }
    return err
}
```

## 12. 监控和调试

### 12.1 关键日志

```go
log.InfoContextf(ctx, "[CloudNode] Creating node: %s", nodeID)
log.InfoContextf(ctx, "[CloudNode] Node created successfully: %s", nodeID)
log.ErrorContextf(ctx, "[CloudNode] Failed to create node: %v", err)
log.WarnContextf(ctx, "[CloudNode] Node %s offline, last heartbeat: %s", nodeID, lastHeartbeat)
```

### 12.2 监控指标

- 节点总数（按状态、类型分组）
- 在线率
- 心跳延迟
- 创建/部署/删除成功率
- 云 API 调用延迟

### 12.3 故障排查

| 问题 | 可能原因 | 解决方法 |
|------|----------|----------|
| 节点创建失败 | 云账户密钥错误 | 检查云账户配置 |
| 节点一直离线 | 代码包问题 | 检查代码包是否正确 |
| 心跳超时 | 网络问题 | 检查节点网络连接 |
| API 调用失败 | 配额不足 | 检查云厂商配额 |

## 13. 性能优化

### 13.1 批量操作

```go
// 批量创建节点
func BatchCreateNodes(nodes []*model.SCFNode) error {
    return db.Transaction(func(tx *gorm.DB) error {
        for _, node := range nodes {
            if err := tx.Create(node).Error; err != nil {
                return err
            }
        }
        return nil
    })
}
```

### 13.2 Provider 客户端缓存

```go
type AccountFactory struct {
    clients sync.Map // 缓存 Provider 客户端
}

func (f *AccountFactory) GetCloudProviderByAccount(accountID string) provider.Client {
    if cached, ok := f.clients.Load(accountID); ok {
        return cached.(provider.Client)
    }

    // 创建新客户端
    client := createProviderClient(accountID)
    f.clients.Store(accountID, client)
    return client
}
```

### 13.3 异步删除

```go
// 删除节点时异步清理
func DeleteNode(nodeID string) error {
    // 立即标记为删除中
    updateNodeStatus(nodeID, StatusDeleting)

    // 异步清理
    go func() {
        // 删除云端函数
        provider.DeleteFunction()

        // 删除数据库记录
        dao.DeleteNode(nodeID)
    }()

    return nil
}
```

## 14. 安全加固

### 14.1 密钥加密

参考 [cloud_account_encryption.md](./cloud_account_encryption.md)

### 14.2 API 鉴权

```go
// 检查用户是否有权限操作该账户
func CheckAccountPermission(userID, accountID string) bool {
    account, _ := dao.GetAccount(accountID)
    return account.OwnerID == userID || isAdmin(userID)
}
```

### 14.3 操作审计

```go
// 记录敏感操作
func AuditLog(ctx context.Context, operation, resource string) {
    log.InfoContextf(ctx, "[Audit] User %s performed %s on %s",
        getUserID(ctx), operation, resource)
}
```

## 15. 相关文档

- [架构文档](./architecture.md)
- [AsyncTask 模块](./asynctask.md)
- [PackageMgr 模块](./packagemgr.md)
- [云账户加密说明](./cloud_account_encryption.md)
- [Provider 扩展指南](./provider_extension.md)
