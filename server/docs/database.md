# Database 模块文档

## 1. 模块概述

Database 模块是 MooX Server 的基础设施模块，负责数据库连接管理、缓存管理和数据持久化。

### 1.1 核心功能

- **数据库管理**：统一的数据库连接管理
- **缓存管理**：基于 BadgerDB 的键值缓存
- **连接池管理**：GORM 连接池配置
- **自动迁移**：数据库表结构自动迁移
- **多数据库支持**：SQLite / MySQL / PostgreSQL

### 1.2 技术选型

- **ORM**：GORM v2
- **主数据库**：SQLite（默认）/ MySQL / PostgreSQL
- **缓存**：BadgerDB（嵌入式 KV 存储）
- **连接池**：database/sql 标准连接池

## 2. 架构设计

### 2.1 模块结构

```
database/
└── manager.go              # 数据库管理器
```

### 2.2 核心组件

| 组件 | 职责 | 说明 |
|------|------|------|
| **Manager** | 数据库管理器 | 统一管理 DB 和 Cache 连接 |
| **GORM** | ORM 层 | 提供对象关系映射 |
| **BadgerDB** | 缓存层 | 提供高性能 KV 缓存 |

### 2.3 分层架构

```
┌─────────────────────────────────────────┐
│         Application Layer               │
│  (Service, API, Executors)              │
└───────────────┬─────────────────────────┘
                │
┌───────────────▼─────────────────────────┐
│            DAO Layer                    │
│  (Data Access Objects)                  │
└───────────────┬─────────────────────────┘
                │
┌───────────────▼─────────────────────────┐
│         Database Module                 │
│  ┌───────────────────────────────────┐  │
│  │  Manager                          │  │
│  │  - db: *gorm.DB                   │  │
│  │  - cache: *badger.DB              │  │
│  └───────────────────────────────────┘  │
└─────────────────────────────────────────┘
                │
        ┌───────┴───────┐
        ▼               ▼
┌───────────────┐ ┌───────────────┐
│   SQLite      │ │   BadgerDB    │
│   Database    │ │   Cache       │
└───────────────┘ └───────────────┘
```

## 3. 核心接口

### 3.1 Manager 接口

```go
package database

type Manager struct {
    db    *gorm.DB
    cache *badger.DB
}

// NewManager 创建数据库管理器
func NewManager() *Manager

// Initialize 初始化数据库连接
func (dm *Manager) Initialize(dbPath string) error

// InitializeCache 初始化缓存（BadgerDB）
func (dm *Manager) InitializeCache(cacheDir string) error

// GetDB 获取数据库连接
func (dm *Manager) GetDB() *gorm.DB

// GetCache 获取缓存连接
func (dm *Manager) GetCache() *badger.DB

// CreateInstance 创建新的数据库实例（独立连接）
func (dm *Manager) CreateInstance() *gorm.DB

// Close 关闭数据库连接和缓存
func (dm *Manager) Close() error
```

## 4. 数据库类型

### 4.1 SQLite（默认）

**优势**：
- 零配置，无需安装数据库服务
- 单文件存储，便于备份和迁移
- 适合中小型部署

**配置**：
```yaml
database:
  type: sqlite
  path: ./data/moox.db
```

**连接字符串**：
```go
db, err := gorm.Open(sqlite.Open("./data/moox.db"), &gorm.Config{})
```

### 4.2 MySQL

**优势**：
- 高性能、高并发
- 丰富的工具生态
- 适合大型生产环境

**配置**：
```yaml
database:
  type: mysql
  host: localhost
  port: 3306
  user: root
  password: password
  dbname: moox
  max_idle_conns: 10
  max_open_conns: 100
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m
```

**连接字符串**：
```go
dsn := "user:password@tcp(localhost:3306)/moox?charset=utf8mb4&parseTime=True&loc=Local"
db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
```

### 4.3 PostgreSQL

**优势**：
- 强大的 JSON 支持
- 严格的 ACID 保证
- 适合复杂查询

**配置**：
```yaml
database:
  type: postgres
  host: localhost
  port: 5432
  user: postgres
  password: password
  dbname: moox
```

**连接字符串**：
```go
dsn := "host=localhost user=postgres password=password dbname=moox port=5432 sslmode=disable"
db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
```

## 5. 使用指南

### 5.1 初始化数据库

```go
import (
    "github.com/mooyang-code/moox/server/internal/service/database"
)

// 创建 Manager
dbManager := database.NewManager()

// 初始化数据库（SQLite）
err := dbManager.Initialize("./data/moox.db")
if err != nil {
    log.Fatalf("Failed to initialize database: %v", err)
}

// 初始化缓存
err = dbManager.InitializeCache("./data/cache")
if err != nil {
    log.Fatalf("Failed to initialize cache: %v", err)
}

// 确保程序退出时关闭连接
defer dbManager.Close()
```

### 5.2 获取数据库连接

```go
// 在 DAO 层获取 DB 连接
db := dbManager.GetDB()

// 执行查询
var users []User
db.Find(&users)
```

### 5.3 使用缓存

```go
// 获取缓存连接
cache := dbManager.GetCache()

// 写入缓存
err := cache.Update(func(txn *badger.Txn) error {
    return txn.Set([]byte("key"), []byte("value"))
})

// 读取缓存
err := cache.View(func(txn *badger.Txn) error {
    item, err := txn.Get([]byte("key"))
    if err != nil {
        return err
    }

    value, err := item.ValueCopy(nil)
    fmt.Println(string(value))
    return err
})
```

### 5.4 表结构迁移

```go
// 在各模块的 DAO 中执行自动迁移
func (dao *UserDAO) AutoMigrate() error {
    return dao.db.AutoMigrate(&model.User{})
}

// 在 bootstrap 中统一调用
db := dbManager.GetDB()
db.AutoMigrate(
    &model.User{},
    &model.CloudAccount{},
    &model.SCFNode{},
    &model.FunctionPackage{},
    &model.AsyncJob{},
    &model.AsyncJobTask{},
    // ...
)
```

## 6. GORM 最佳实践

### 6.1 基本 CRUD

```go
// 创建
user := &model.User{
    UserID:   "user-001",
    Username: "admin",
}
db.Create(user)

// 查询单条
var user model.User
db.First(&user, "c_user_id = ?", "user-001")

// 查询列表
var users []model.User
db.Where("c_status = ?", 1).Find(&users)

// 更新
db.Model(&user).Update("c_status", 0)

// 删除
db.Delete(&user, "c_user_id = ?", "user-001")
```

### 6.2 事务处理

```go
// 自动事务
err := db.Transaction(func(tx *gorm.DB) error {
    // 在事务中执行操作
    if err := tx.Create(&user).Error; err != nil {
        return err
    }

    if err := tx.Create(&account).Error; err != nil {
        return err
    }

    return nil
})

// 手动事务
tx := db.Begin()
defer func() {
    if r := recover(); r != nil {
        tx.Rollback()
    }
}()

if err := tx.Create(&user).Error; err != nil {
    tx.Rollback()
    return err
}

if err := tx.Create(&account).Error; err != nil {
    tx.Rollback()
    return err
}

tx.Commit()
```

### 6.3 预加载关联

```go
// 预加载一对多关联
type User struct {
    ID       uint
    Accounts []Account `gorm:"foreignKey:UserID"`
}

db.Preload("Accounts").Find(&users)

// 预加载多层关联
db.Preload("Accounts.Nodes").Find(&users)
```

### 6.4 原始 SQL

```go
// 执行原始 SQL
var result []map[string]interface{}
db.Raw("SELECT * FROM t_users WHERE c_status = ?", 1).Scan(&result)

// 执行更新 SQL
db.Exec("UPDATE t_users SET c_status = ? WHERE c_user_id = ?", 0, "user-001")
```

### 6.5 连接池配置

```go
sqlDB, err := db.DB()

// 设置连接池参数
sqlDB.SetMaxIdleConns(10)           // 最大空闲连接数
sqlDB.SetMaxOpenConns(100)          // 最大打开连接数
sqlDB.SetConnMaxLifetime(time.Hour) // 连接最大生命周期
sqlDB.SetConnMaxIdleTime(10 * time.Minute) // 连接最大空闲时间
```

## 7. BadgerDB 缓存使用

### 7.1 基本操作

```go
cache := dbManager.GetCache()

// 写入
err := cache.Update(func(txn *badger.Txn) error {
    return txn.Set([]byte("user:001"), []byte(`{"name":"admin"}`))
})

// 读取
var value []byte
err := cache.View(func(txn *badger.Txn) error {
    item, err := txn.Get([]byte("user:001"))
    if err != nil {
        return err
    }
    value, err = item.ValueCopy(nil)
    return err
})

// 删除
err := cache.Update(func(txn *badger.Txn) error {
    return txn.Delete([]byte("user:001"))
})
```

### 7.2 设置过期时间

```go
// 设置 TTL（5 分钟）
err := cache.Update(func(txn *badger.Txn) error {
    entry := badger.NewEntry([]byte("key"), []byte("value")).
        WithTTL(5 * time.Minute)
    return txn.SetEntry(entry)
})
```

### 7.3 批量操作

```go
// 批量写入
wb := cache.NewWriteBatch()
defer wb.Cancel()

for _, item := range items {
    err := wb.Set([]byte(item.Key), []byte(item.Value))
    if err != nil {
        return err
    }
}

err := wb.Flush()
```

### 7.4 迭代器

```go
// 遍历所有键
err := cache.View(func(txn *badger.Txn) error {
    opts := badger.DefaultIteratorOptions
    opts.PrefetchSize = 10

    it := txn.NewIterator(opts)
    defer it.Close()

    for it.Rewind(); it.Valid(); it.Next() {
        item := it.Item()
        key := item.Key()
        value, _ := item.ValueCopy(nil)

        fmt.Printf("Key: %s, Value: %s\n", key, value)
    }

    return nil
})
```

## 8. 数据模型设计规范

### 8.1 表命名规范

```go
// 表名使用 t_ 前缀
type User struct {
    // ...
}

func (User) TableName() string {
    return "t_users"
}
```

### 8.2 字段命名规范

```go
// 字段使用 c_ 前缀
type User struct {
    UserID     string    `gorm:"column:c_user_id;primaryKey"`
    Username   string    `gorm:"column:c_username;uniqueIndex"`
    Status     int       `gorm:"column:c_status;index"`
    CreateTime time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

### 8.3 索引设计

```go
type User struct {
    // 主键索引
    UserID   string `gorm:"column:c_user_id;primaryKey"`

    // 唯一索引
    Username string `gorm:"column:c_username;uniqueIndex"`

    // 普通索引
    Status   int    `gorm:"column:c_status;index"`

    // 组合索引
    Email    string `gorm:"column:c_email;index:idx_email_status"`
    Status   int    `gorm:"column:c_status;index:idx_email_status"`
}
```

### 8.4 软删除

```go
import "gorm.io/gorm"

type User struct {
    // ... 其他字段
    DeletedAt gorm.DeletedAt `gorm:"column:c_deleted_at;index"`
}

// 软删除
db.Delete(&user)

// 永久删除
db.Unscoped().Delete(&user)

// 查询包含软删除的记录
db.Unscoped().Find(&users)
```

## 9. 性能优化

### 9.1 批量插入

```go
// 批量插入（推荐）
users := []*model.User{
    {UserID: "001", Username: "user1"},
    {UserID: "002", Username: "user2"},
}
db.CreateInBatches(users, 100) // 每批 100 条

// 避免循环插入
for _, user := range users {
    db.Create(user) // 性能差
}
```

### 9.2 选择字段

```go
// 只查询需要的字段
db.Select("c_user_id", "c_username").Find(&users)

// 排除字段
db.Omit("c_password_hash").Find(&users)
```

### 9.3 分页查询

```go
// 使用 Limit 和 Offset
var users []model.User
page := 1
pageSize := 20
offset := (page - 1) * pageSize

db.Limit(pageSize).Offset(offset).Find(&users)

// 获取总数
var total int64
db.Model(&model.User{}).Count(&total)
```

### 9.4 索引优化

```sql
-- 为频繁查询的字段添加索引
CREATE INDEX idx_status ON t_users(c_status);
CREATE INDEX idx_create_time ON t_users(c_create_time);

-- 组合索引
CREATE INDEX idx_status_create_time ON t_users(c_status, c_create_time);
```

### 9.5 使用缓存

```go
// 缓存热点数据
func GetUser(userID string) (*model.User, error) {
    // 1. 先查缓存
    if cached, err := getFromCache(userID); err == nil {
        return cached, nil
    }

    // 2. 查数据库
    var user model.User
    if err := db.First(&user, "c_user_id = ?", userID).Error; err != nil {
        return nil, err
    }

    // 3. 写入缓存
    setToCache(userID, &user)

    return &user, nil
}
```

## 10. 监控和日志

### 10.1 SQL 日志

```go
import (
    "gorm.io/gorm/logger"
)

// 开启 SQL 日志
newLogger := logger.New(
    log.New(os.Stdout, "\r\n", log.LstdFlags),
    logger.Config{
        SlowThreshold:             200 * time.Millisecond, // 慢查询阈值
        LogLevel:                  logger.Info,
        IgnoreRecordNotFoundError: true,
        Colorful:                  true,
    },
)

db, err := gorm.Open(sqlite.Open("test.db"), &gorm.Config{
    Logger: newLogger,
})
```

### 10.2 性能监控

```go
// 记录查询时间
start := time.Now()
db.Find(&users)
duration := time.Since(start)

if duration > 100*time.Millisecond {
    log.Warnf("[DB] Slow query: %v", duration)
}
```

### 10.3 连接池监控

```go
sqlDB, _ := db.DB()
stats := sqlDB.Stats()

log.Infof("[DB] OpenConnections: %d", stats.OpenConnections)
log.Infof("[DB] InUse: %d", stats.InUse)
log.Infof("[DB] Idle: %d", stats.Idle)
```

## 11. 故障处理

### 11.1 常见问题

| 问题 | 可能原因 | 解决方法 |
|------|----------|----------|
| 连接超时 | 连接池满 | 增加 MaxOpenConns |
| 慢查询 | 缺少索引 | 添加索引 |
| 死锁 | 事务顺序不一致 | 统一事务顺序 |
| 数据库锁定 | SQLite 并发写 | 使用 WAL 模式或改用 MySQL |

### 11.2 SQLite WAL 模式

```go
// 启用 WAL 模式（提高并发性能）
db.Exec("PRAGMA journal_mode=WAL;")
```

### 11.3 备份和恢复

**SQLite 备份**：
```bash
# 在线备份
sqlite3 moox.db ".backup 'moox_backup.db'"

# 导出 SQL
sqlite3 moox.db ".dump" > moox_backup.sql

# 恢复
sqlite3 moox.db < moox_backup.sql
```

**MySQL 备份**：
```bash
# 备份
mysqldump -u root -p moox > moox_backup.sql

# 恢复
mysql -u root -p moox < moox_backup.sql
```

## 12. 配置参考

### 12.1 完整配置示例

```yaml
database:
  # 数据库类型：sqlite, mysql, postgres
  type: sqlite

  # SQLite 配置
  path: ./data/moox.db

  # MySQL/PostgreSQL 配置
  host: localhost
  port: 3306
  user: root
  password: password
  dbname: moox

  # 连接池配置
  max_idle_conns: 10
  max_open_conns: 100
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m

# 缓存配置
cache:
  data_dir: ./data/cache
```

### 12.2 环境变量

```bash
export DB_TYPE="mysql"
export DB_HOST="localhost"
export DB_PORT="3306"
export DB_USER="root"
export DB_PASSWORD="password"
export DB_NAME="moox"
```

## 13. 相关文档

- [架构文档](./architecture.md) - 系统整体架构
- [AsyncTask 模块](./asynctask.md) - 异步任务模块
- [Auth 模块](./auth.md) - 认证模块
- [CloudNode 模块](./cloudnode.md) - 云节点模块
- [PackageMgr 模块](./packagemgr.md) - 代码包模块
