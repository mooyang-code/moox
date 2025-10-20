# Auth 模块文档

## 1. 模块概述

Auth 模块是 MooX Server 的用户认证和鉴权模块，提供用户登录、JWT 令牌管理、密码加密、登录保护等功能。

### 1.1 核心功能

- **用户登录**：基于用户名密码的登录认证
- **JWT 令牌**：生成和验证 JWT 访问令牌
- **密码加密**：Bcrypt + 盐值的密码哈希存储
- **登录保护**：失败次数限制、账户锁定
- **盐值验证**：防止重放攻击
- **用户管理**：用户创建、更新、删除、查询
- **密码管理**：修改密码、重置密码
- **登录历史**：记录用户登录日志

### 1.2 安全特性

- **密码不可逆加密**：Bcrypt 算法
- **盐值机制**：每个用户独立的盐值
- **时间戳验证**：防止重放攻击
- **失败锁定**：连续失败后锁定账户
- **JWT 过期**：令牌有效期控制
- **敏感信息脱敏**：日志和 API 返回不包含密码

## 2. 架构设计

### 2.1 模块结构

```
auth/
├── interface.go            # 对外接口定义
├── impl/                   # 业务逻辑实现
│   ├── init.go             # 服务初始化
│   ├── login.go            # 登录逻辑
│   ├── user.go             # 用户管理
│   ├── password.go         # 密码管理
│   └── helper.go           # 辅助函数
├── dao/                    # 数据访问层
│   ├── badger.go           # Badger 缓存封装
│   └── user.go             # 用户 DAO
├── model/                  # 数据模型
│   ├── user.go             # 用户实体
│   └── login_history.go    # 登录历史实体
├── utils/                  # 工具函数
│   ├── jwt.go              # JWT 工具
│   └── password.go         # 密码工具
├── config/                 # 配置定义
│   └── config.go           # 配置结构
└── api/                    # HTTP API（如有）
```

### 2.2 核心组件

| 组件 | 职责 | 说明 |
|------|------|------|
| **Service** | 服务接口 | 实现 protobuf 定义的 AuthAPIService |
| **AuthServiceImpl** | 服务实现 | 实现登录、用户管理、密码管理等核心逻辑 |
| **UserDAO** | 用户数据访问 | 用户 CRUD、缓存管理 |
| **CacheDB** | 缓存抽象 | Badger 缓存的封装 |
| **JWT Utils** | JWT 工具 | 生成和验证 JWT 令牌 |
| **Password Utils** | 密码工具 | 密码哈希、验证 |

## 3. 核心接口

### 3.1 Service 接口

```go
package auth

import (
    pb "github.com/mooyang-code/moox/server/proto/gen"
)

// Service 认证服务总接口
type Service interface {
    pb.AuthAPIService  // 实现 protobuf 生成的接口
    // 可以添加其他自定义接口
}

// NewService 新建认证服务
func NewService(cfg *config.Config, dbManager *database.Manager) (Service, error)
```

### 3.2 AuthAPIService 接口（protobuf 生成）

```go
type AuthAPIService interface {
    // 用户登录
    Login(ctx context.Context, req *LoginReq) (*LoginRsp, error)

    // 用户注册（管理员功能）
    Register(ctx context.Context, req *RegisterReq) (*RegisterRsp, error)

    // 获取登录盐值（前端登录前调用）
    GetLoginSalt(ctx context.Context, req *GetLoginSaltReq) (*GetLoginSaltRsp, error)

    // 修改密码
    ChangePassword(ctx context.Context, req *ChangePasswordReq) (*ChangePasswordRsp, error)

    // 用户管理
    GetUserList(ctx context.Context, req *GetUserListReq) (*GetUserListRsp, error)
    GetUserInfo(ctx context.Context, req *GetUserInfoReq) (*GetUserInfoRsp, error)
    UpdateUser(ctx context.Context, req *UpdateUserReq) (*UpdateUserRsp, error)
    DeleteUser(ctx context.Context, req *DeleteUserReq) (*DeleteUserRsp, error)
}
```

## 4. 数据模型

### 4.1 User（用户）

```go
type User struct {
    UserID         string    `gorm:"column:c_user_id;primaryKey"`
    Username       string    `gorm:"column:c_username;uniqueIndex"`
    PasswordHash   string    `gorm:"column:c_password_hash"`
    Salt           string    `gorm:"column:c_salt"`
    Email          string    `gorm:"column:c_email"`
    Role           string    `gorm:"column:c_role"`         // admin, user
    Status         int       `gorm:"column:c_status"`       // 0-禁用, 1-正常
    LastLoginIP    string    `gorm:"column:c_last_login_ip"`
    LastLoginTime  time.Time `gorm:"column:c_last_login_time"`
    CreateTime     time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime     time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

**字段说明**：
- `UserID`：用户唯一标识（UUID）
- `Username`：用户名（唯一）
- `PasswordHash`：密码哈希（Bcrypt）
- `Salt`：用户盐值（随机生成）
- `Role`：用户角色（admin, user）
- `Status`：用户状态（0-禁用, 1-正常）

### 4.2 LoginHistory（登录历史）

```go
type LoginHistory struct {
    ID            int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
    UserID        string    `gorm:"column:c_user_id;index"`
    Username      string    `gorm:"column:c_username"`
    LoginIP       string    `gorm:"column:c_login_ip"`
    LoginTime     time.Time `gorm:"column:c_login_time"`
    LoginResult   int       `gorm:"column:c_login_result"` // 1-成功, 2-失败
    FailureReason string    `gorm:"column:c_failure_reason"`
    UserAgent     string    `gorm:"column:c_user_agent"`
}
```

## 5. 认证流程

### 5.1 完整登录流程

```
┌─────────────┐
│ 1. 前端请求 │
│    盐值     │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 2. GetLogin │
│    Salt     │
│  - 生成随机盐│
│  - 缓存5分钟│
│  - 返回盐值 │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 3. 前端加密 │
│  hash = SHA │
│  (password+ │
│   dbSalt +  │
│   loginSalt+│
│   timestamp)│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 4. Login    │
│  - 验证盐值 │
│  - 验证时间戳│
│  - 查询用户 │
│  - 验证密码 │
│  - 生成JWT  │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 5. 返回     │
│   Access    │
│   Token     │
└─────────────┘
```

### 5.2 密码验证逻辑

```go
// 前端计算
clientHash = SHA256(password + dbSalt + loginSalt + timestamp)

// 后端验证
serverHash = SHA256(passwordHash + loginSalt + timestamp)

// 对比
if clientHash == serverHash {
    // 验证通过
}
```

### 5.3 盐值机制

```
┌───────────────────────────────────────────────┐
│                 盐值分层                      │
├───────────────────────────────────────────────┤
│ DB Salt (固定)                                │
│  - 用户创建时生成                             │
│  - 存储在数据库                               │
│  - 用于密码哈希                               │
├───────────────────────────────────────────────┤
│ Login Salt (临时)                             │
│  - 每次登录前生成                             │
│  - 缓存 5 分钟                                │
│  - 防止重放攻击                               │
├───────────────────────────────────────────────┤
│ Timestamp (时间戳)                            │
│  - 每次登录携带                               │
│  - 验证请求时效性                             │
│  - 5 分钟有效                                 │
└───────────────────────────────────────────────┘
```

## 6. 安全机制

### 6.1 密码加密

**存储过程**：
```go
// 1. 生成随机盐值
salt := utils.GenerateRandomSalt(16)

// 2. 使用 Bcrypt 哈希
passwordHash, err := bcrypt.GenerateFromPassword(
    []byte(password + salt),
    bcrypt.DefaultCost,
)

// 3. 存储 passwordHash 和 salt
user.PasswordHash = string(passwordHash)
user.Salt = salt
```

**验证过程**：
```go
// 1. 计算服务端哈希
serverHash := SHA256(passwordHash + loginSalt + timestamp)

// 2. 对比客户端哈希
if serverHash == clientHash {
    // 验证通过
}
```

### 6.2 登录保护

**失败计数**：
```go
// 缓存键
key := fmt.Sprintf("login_attempt:%s:%s", username, clientIP)

// 记录失败次数
attempts := cache.Get(key)
attempts++
cache.Set(key, attempts, 15*time.Minute)

// 检查是否超过限制
if attempts >= 5 {
    // 锁定账户 15 分钟
    return "账户已被锁定"
}
```

**锁定时间**：
- 默认：15 分钟
- 可配置：`config.Auth.LockDuration`

### 6.3 重放攻击防护

**时间戳验证**：
```go
// 检查时间戳有效性
if time.Now().Unix() - timestamp > 300 { // 5 分钟
    return "请求已过期"
}
```

**盐值一次性**：
```go
// 盐值使用后删除
cache.Delete(saltKey)
```

### 6.4 JWT 安全

**令牌结构**：
```go
type JWTClaims struct {
    UserID   string `json:"user_id"`
    Username string `json:"username"`
    Role     string `json:"role"`
    jwt.StandardClaims
}
```

**过期时间**：
- 默认：24 小时
- 可配置：`config.JWT.AccessExpired`

**签名算法**：
- HS256（HMAC + SHA256）

## 7. 使用指南

### 7.1 创建 Service

```go
import (
    "github.com/mooyang-code/moox/server/internal/service/auth"
    "github.com/mooyang-code/moox/server/internal/service/auth/config"
    "github.com/mooyang-code/moox/server/internal/service/database"
)

// 加载配置
cfg := &config.Config{
    JWT: config.JWTConfig{
        SecretKey:     "your-secret-key",
        AccessExpired: 24 * time.Hour,
    },
    Cache: config.CacheConfig{
        DataDir: "./data/cache",
    },
}

// 创建服务
authService, err := auth.NewService(cfg, dbManager)
if err != nil {
    log.Fatalf("Failed to create auth service: %v", err)
}
```

### 7.2 用户登录（API 调用）

**步骤1：获取登录盐值**

```http
POST /api/auth/get_login_salt
Content-Type: application/json

{
  "username": "admin"
}
```

**响应**：
```json
{
  "ret_info": {
    "code": 0,
    "msg": "success"
  },
  "salt": "abc123...",
  "timestamp": 1634567890
}
```

**步骤2：客户端计算密码哈希**

```javascript
// 伪代码
const passwordHash = SHA256(password + dbSalt + loginSalt + timestamp);
```

**步骤3：登录请求**

```http
POST /api/auth/login
Content-Type: application/json

{
  "username": "admin",
  "password_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "salt": "abc123...",
  "timestamp": 1634567890,
  "client_ip": "192.168.1.100"
}
```

**响应**：
```json
{
  "ret_info": {
    "code": 0,
    "msg": "登录成功"
  },
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user_info": {
    "user_id": "uuid",
    "username": "admin",
    "email": "admin@example.com",
    "role": "admin"
  }
}
```

### 7.3 修改密码

```http
POST /api/auth/change_password
Content-Type: application/json
Authorization: Bearer <access_token>

{
  "old_password": "old_pass",
  "new_password": "new_pass"
}
```

### 7.4 JWT 验证

**在中间件中验证**：

```go
import "github.com/mooyang-code/moox/server/internal/service/auth/utils"

func AuthMiddleware(secretKey string) gin.HandlerFunc {
    return func(c *gin.Context) {
        // 获取 token
        token := c.GetHeader("Authorization")
        token = strings.TrimPrefix(token, "Bearer ")

        // 验证 token
        claims, err := utils.ParseAccessToken(token, secretKey)
        if err != nil {
            c.JSON(401, gin.H{"error": "Unauthorized"})
            c.Abort()
            return
        }

        // 将用户信息存入上下文
        c.Set("user_id", claims.UserID)
        c.Set("username", claims.Username)
        c.Set("role", claims.Role)

        c.Next()
    }
}
```

## 8. 配置说明

### 8.1 配置结构

```go
type Config struct {
    JWT   JWTConfig   `yaml:"jwt"`
    Cache CacheConfig `yaml:"cache"`
    Auth  AuthConfig  `yaml:"auth"`
}

type JWTConfig struct {
    SecretKey     string        `yaml:"secret_key"`
    AccessExpired time.Duration `yaml:"access_expired"`
}

type CacheConfig struct {
    DataDir string `yaml:"data_dir"`
}

type AuthConfig struct {
    MaxLoginAttempts int           `yaml:"max_login_attempts"`
    LockDuration     time.Duration `yaml:"lock_duration"`
}
```

### 8.2 配置示例

```yaml
jwt:
  secret_key: "your-super-secret-key-change-in-production"
  access_expired: 24h

cache:
  data_dir: "./data/cache"

auth:
  max_login_attempts: 5
  lock_duration: 15m
```

### 8.3 环境变量覆盖

```bash
export JWT_SECRET="production-secret-key"
export MAX_LOGIN_ATTEMPTS=3
export LOCK_DURATION=30m
```

## 9. 依赖关系

### 9.1 内部依赖

```
auth (package level)
├── interface.go → impl/
├── impl/ → dao/, utils/, model/
├── dao/ → model/
└── api/ → interface.go (Service)
```

### 9.2 外部依赖

```
auth
├── database.Manager        # 数据库管理器
├── gorm.DB                 # GORM 数据库连接
├── badger.DB               # Badger 缓存
└── protobuf (pb)           # API 接口定义
```

### 9.3 被依赖关系

```
auth.Service (被以下模块使用)
├── bootstrap               # 启动时创建服务
├── API Gateway             # 认证中间件
└── 其他需要鉴权的模块     # 用户权限检查
```

## 10. API 参考

### 10.1 GetLoginSalt - 获取登录盐值

**请求**：
```protobuf
message GetLoginSaltReq {
    string username = 1;
}
```

**响应**：
```protobuf
message GetLoginSaltRsp {
    RetInfo ret_info = 1;
    string salt = 2;
    int64 timestamp = 3;
}
```

### 10.2 Login - 用户登录

**请求**：
```protobuf
message LoginReq {
    string username = 1;
    string password_hash = 2;
    string salt = 3;
    int64 timestamp = 4;
    string client_ip = 5;
}
```

**响应**：
```protobuf
message LoginRsp {
    RetInfo ret_info = 1;
    string access_token = 2;
    UserInfo user_info = 3;
}
```

### 10.3 Register - 用户注册

**请求**：
```protobuf
message RegisterReq {
    string username = 1;
    string password = 2;
    string email = 3;
    string role = 4;
}
```

**响应**：
```protobuf
message RegisterRsp {
    RetInfo ret_info = 1;
    string user_id = 2;
}
```

### 10.4 ChangePassword - 修改密码

**请求**：
```protobuf
message ChangePasswordReq {
    string user_id = 1;
    string old_password = 2;
    string new_password = 3;
}
```

**响应**：
```protobuf
message ChangePasswordRsp {
    RetInfo ret_info = 1;
}
```

## 11. 最佳实践

### 11.1 密码策略

```go
// 密码复杂度验证
func ValidatePassword(password string) error {
    if len(password) < 8 {
        return errors.New("密码长度至少 8 位")
    }
    if !regexp.MustCompile(`[a-z]`).MatchString(password) {
        return errors.New("密码必须包含小写字母")
    }
    if !regexp.MustCompile(`[A-Z]`).MatchString(password) {
        return errors.New("密码必须包含大写字母")
    }
    if !regexp.MustCompile(`[0-9]`).MatchString(password) {
        return errors.New("密码必须包含数字")
    }
    return nil
}
```

### 11.2 JWT 刷新机制

```go
// 实现 Refresh Token
type RefreshTokenClaims struct {
    UserID string `json:"user_id"`
    jwt.StandardClaims
}

// 生成 Refresh Token（有效期 7 天）
func GenerateRefreshToken(userID string) (string, error) {
    claims := RefreshTokenClaims{
        UserID: userID,
        StandardClaims: jwt.StandardClaims{
            ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Unix(),
        },
    }
    // ...
}
```

### 11.3 日志审计

```go
// 记录所有认证相关操作
log.InfoContextf(ctx, "[Auth] User %s login from %s", username, clientIP)
log.WarnContextf(ctx, "[Auth] Failed login attempt for %s from %s", username, clientIP)
log.InfoContextf(ctx, "[Auth] User %s changed password", userID)
```

### 11.4 错误处理

```go
// 统一错误返回，不泄露敏感信息
if err != nil {
    // 日志记录详细错误
    log.ErrorContextf(ctx, "Login failed: %v", err)

    // 返回通用错误信息
    return &pb.LoginRsp{
        RetInfo: &pb.RetInfo{
            Code: pb.EnumMooxErrorCode_NO_AUTH,
            Msg:  "用户名或密码错误",  // 不区分用户不存在或密码错误
        },
    }
}
```

## 12. 性能优化

### 12.1 缓存使用

```go
// 缓存用户信息（减少数据库查询）
cacheKey := fmt.Sprintf("user:%s", userID)
if cached, err := cache.Get(cacheKey); err == nil {
    return cached
}

// 从数据库查询
user, err := dao.GetUser(userID)
cache.Set(cacheKey, user, 5*time.Minute)
```

### 12.2 Bcrypt Cost 调优

```go
// 开发环境使用较低的 cost
const DevCost = 4

// 生产环境使用较高的 cost
const ProdCost = 12

cost := bcrypt.DefaultCost
if env == "production" {
    cost = ProdCost
}
```

### 12.3 批量查询优化

```go
// 批量查询用户列表时使用分页
func GetUserList(page, pageSize int) ([]*User, int64, error) {
    var users []*User
    var total int64

    db.Model(&User{}).Count(&total)
    db.Limit(pageSize).Offset((page-1) * pageSize).Find(&users)

    return users, total, nil
}
```

## 13. 常见问题

### 13.1 登录失败"盐值无效"

**原因**：
- 盐值已过期（5 分钟）
- 盐值已使用
- 客户端时钟不准

**解决**：
- 重新获取盐值
- 同步系统时间

### 13.2 JWT 验证失败

**原因**：
- Token 已过期
- 签名密钥不匹配
- Token 格式错误

**解决**：
- 重新登录获取新 Token
- 检查服务端配置

### 13.3 账户被锁定

**原因**：
- 连续登录失败次数过多

**解决**：
- 等待 15 分钟后自动解锁
- 管理员手动解锁（清除缓存）

## 14. 监控和日志

### 14.1 关键日志

```go
// 登录成功
log.InfoContextf(ctx, "[Auth] User %s logged in from %s", username, clientIP)

// 登录失败
log.WarnContextf(ctx, "[Auth] Failed login for %s from %s: %s", username, clientIP, reason)

// 账户锁定
log.WarnContextf(ctx, "[Auth] Account %s locked due to too many failed attempts", username)

// 密码修改
log.InfoContextf(ctx, "[Auth] User %s changed password", userID)
```

### 14.2 监控指标

建议监控：
- 登录成功率
- 平均登录时间
- 锁定账户数量
- JWT 验证失败次数
- 密码重置次数

### 14.3 告警规则

```yaml
alerts:
  - name: HighLoginFailureRate
    expr: login_failure_rate > 0.3
    message: "登录失败率超过 30%"

  - name: TooManyLockedAccounts
    expr: locked_accounts_count > 10
    message: "锁定账户数量超过 10 个"
```

## 15. 扩展开发

### 15.1 多因素认证（MFA）

```go
// 扩展 User 模型
type User struct {
    // ... 现有字段
    MFAEnabled bool   `gorm:"column:c_mfa_enabled"`
    MFASecret  string `gorm:"column:c_mfa_secret"`
}

// 验证 TOTP
func VerifyTOTP(secret, code string) bool {
    // 使用 github.com/pquerna/otp/totp
    return totp.Validate(code, secret)
}
```

### 15.2 OAuth2 集成

```go
// 添加第三方登录
type OAuthProvider struct {
    Provider     string // google, github, wechat
    AccessToken  string
    RefreshToken string
    ExpiresAt    time.Time
}

func LoginWithOAuth(provider, code string) (*User, error) {
    // 1. 使用 code 换取 access_token
    // 2. 获取用户信息
    // 3. 创建或更新本地用户
    // 4. 生成 JWT token
}
```

### 15.3 RBAC 权限控制

```go
// 定义权限
type Permission struct {
    Resource string // users, nodes, packages
    Action   string // create, read, update, delete
}

// 角色权限映射
var rolePermissions = map[string][]Permission{
    "admin": {
        {Resource: "*", Action: "*"},
    },
    "user": {
        {Resource: "nodes", Action: "read"},
        {Resource: "packages", Action: "read"},
    },
}

// 权限检查
func HasPermission(role, resource, action string) bool {
    // 实现权限检查逻辑
}
```

## 16. 相关文档

- [架构文档](./architecture.md) - 系统整体架构
- [Database 模块](./database.md) - 数据库管理
- [API 接口文档](./api.md) - 完整 API 参考
