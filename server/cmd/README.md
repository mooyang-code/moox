# Moox 数据库管理工具

这是 Moox 认证服务的专用数据库管理命令行工具，用于初始化数据库表结构和管理数据库操作。

## 🚀 功能特性

- ✅ 数据库初始化（根据 SQL schema 文件）
- ✅ 数据库迁移
- ✅ 数据库删除（危险操作）
- ✅ 彩色终端输出
- ✅ 详细的操作日志
- ✅ 自动创建数据目录

## 📁 项目结构

```
server/
├── cmd/
│   ├── main.go          # 命令行工具主程序（流程控制）
│   ├── database.go      # 数据库操作逻辑
│   ├── sql.go           # SQL 文件处理逻辑
│   ├── utils.go         # 工具函数和常量
│   ├── Makefile         # 编译和管理脚本
│   └── README.md        # 使用说明（本文件）
├── sql/
│   └── schema.sql       # 数据库表结构定义
├── config/
│   └── auth.yaml        # 数据库配置文件
└── data/                # 数据库文件存储目录（自动创建）
    └── auth.db          # SQLite 数据库文件
```

## 🛠️ 安装和编译

### 方法一：使用 Makefile（推荐）

```bash
# 进入 cmd 目录
cd server/cmd

# 查看所有可用命令
make

# 编译工具
make build

# 编译并初始化数据库（一键开发环境）
make dev
```

### 方法二：手动编译

```bash
# 进入 cmd 目录
cd server/cmd

# 编译
go build -o ../bin/moox-db-tool main.go
```

## 📖 使用方法

### 基本命令

```bash
# 显示帮助信息
./moox-db-tool -help

# 初始化数据库（创建表和索引）
./moox-db-tool -init

# 执行数据库迁移
./moox-db-tool -migrate

# 删除数据库文件（危险操作！）
./moox-db-tool -drop
```

### 高级用法

```bash
# 指定自定义数据库路径
./moox-db-tool -init -db=/path/to/custom/database.db

# 指定自定义 SQL 文件目录
./moox-db-tool -init -sql=/path/to/sql/files

# 组合参数使用
./moox-db-tool -init -db=./test.db -sql=./custom-sql
```

### 使用 Makefile（推荐）

```bash
# 初始化数据库
make init-db

# 迁移数据库
make migrate-db

# 删除数据库
make drop-db

# 显示工具帮助
make help

# 清理并重新初始化（开发环境）
make dev
```

## 📋 命令参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-db` | `../data/auth.db` | 数据库文件路径 |
| `-sql` | `../sql` | SQL 文件目录路径 |
| `-init` | `false` | 初始化数据库 |
| `-migrate` | `false` | 执行数据库迁移 |
| `-drop` | `false` | 删除数据库文件 |
| `-help` | `false` | 显示帮助信息 |

## 🎯 使用场景

### 1. 首次部署

```bash
# 编译工具
make build

# 初始化数据库
make init-db
```

### 2. 开发环境设置

```bash
# 一键设置开发环境（清理+编译+初始化）
make dev
```

### 3. 数据库结构更新

```bash
# 执行迁移
make migrate-db
```

### 4. 重置数据库

```bash
# 删除现有数据库
make drop-db

# 重新初始化
make init-db
```

## 📊 数据库表结构

工具会根据 `sql/schema.sql` 文件创建以下表：

- `t_users` - 用户表
- `t_active_tokens` - 活跃令牌表（JWT 会话管理）
- `t_login_history` - 登录历史表（安全审计）
- `t_user_actions` - 用户操作日志表（审计）

以及相应的索引优化查询性能。

## 🔧 配置文件

数据库配置位于 `config/auth.yaml`：

```yaml
database:
  host: ""                    # SQLite 不需要 host
  port: 0                     # SQLite 不需要 port  
  user: ""                    # SQLite 不需要用户名
  password: ""                # SQLite 不需要密码
  dbname: "../data/auth.db"   # SQLite 数据库文件路径
```

## ⚠️ 注意事项

1. **数据备份**：执行 `-drop` 操作前请备份重要数据
2. **权限要求**：确保应用有权限在 `data` 目录创建和写入文件
3. **路径规范**：相对路径基于工具执行时的当前目录
4. **并发安全**：SQLite 支持并发读取，写入时会自动加锁

## 🐛 故障排除

### 问题：Permission denied
```bash
# 解决：确保数据目录有写入权限
chmod 755 ../data
```

### 问题：Table already exists
```bash
# 这是正常警告，不影响功能
# 如需重新创建表，先执行 -drop 再执行 -init
```

### 问题：SQL syntax error
```bash
# 检查 SQL 文件格式
# 确保每条 SQL 语句以分号结尾
```

## 💻 代码结构

### 文件组织

- **main.go**: 主程序入口，负责命令行参数解析和流程控制
- **database.go**: 数据库操作逻辑，包含初始化、迁移、删除等功能
- **sql.go**: SQL 文件处理逻辑，负责读取和解析 SQL 文件
- **utils.go**: 工具函数，包含颜色常量和帮助信息显示

### 核心函数

#### database.go
- `initDatabase()`: 初始化数据库
- `migrateDatabase()`: 执行数据库迁移
- `dropDatabase()`: 删除数据库文件

#### sql.go
- `readSQLFromFile()`: 从文件读取并解析 SQL 语句
- `executeSQLStatements()`: 执行 SQL 语句列表

#### utils.go
- `showUsage()`: 显示使用帮助信息
- 颜色常量定义

## 📝 开发说明

### 添加新的数据库操作

1. 在 `sql/schema.sql` 中添加新的表定义
2. 重新编译工具：`make build`
3. 执行迁移：`make migrate-db`

### 扩展工具功能

1. 修改 `main.go` 添加新的命令行选项
2. 在相应的模块文件中实现对应的处理函数
3. 更新 Makefile 添加新的便捷命令
4. 更新本 README 文档

### 代码规范

- 每个文件负责特定的功能模块
- 使用清晰的函数命名和注释
- 保持函数职责单一
- 错误处理要完善和用户友好

## 📄 许可证

本项目遵循 MIT 许可证。 