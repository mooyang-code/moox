# Moox Server 构建和部署指南

## 快速开始

### 方式一：使用Makefile（推荐）

**从项目根目录运行（推荐）：**
```bash
# 查看所有可用命令
make help

# 完整构建和安装
make install VERSION=v1.0.0

# 启动服务
make run

# 停止服务
make stop
```

**从scripts目录运行：**
```bash
# 进入scripts目录
cd scripts

# 查看所有可用命令
make help

# 完整构建和安装
make install VERSION=v1.0.0

# 启动服务
make run

# 停止服务
make stop
```

### 方式二：使用构建脚本

```bash
# 进入scripts目录
cd scripts

# 构建指定版本
./build.sh v1.0.0

# 进入构建目录
cd ../../../go/bin/moox-server

# 启动服务
./start.sh

# 停止服务
./stop.sh
```

## 构建要求

- Go 1.19+
- CGO_ENABLED=1（用于SQLite支持）
- Git（用于获取提交信息）
- Protocol Buffers编译器（protoc）

## 构建选项

### 开发环境

```bash
# 进入scripts目录
cd scripts

# 开发模式运行（不构建）
make dev

# 或回到项目根目录直接运行
cd .. && CGO_ENABLED=1 go run . -conf=./config/trpc_go.yaml
```

### 生产环境

```bash
# 进入scripts目录
cd scripts

# 生产环境构建
make build VERSION=v1.0.0

# 或使用脚本
./build.sh v1.0.0
```

## 构建产物

构建完成后会在 `../../../go/bin/moox-server/` 目录下生成：

```
moox-server/
├── bin/                    # 二进制文件目录
│   └── moox-server         # 主程序二进制文件
├── start.sh                # 启动脚本
├── stop.sh                 # 停止脚本
├── README.md               # 使用说明
├── config/                 # 配置文件目录
│   ├── auth.yaml           # 默认配置（SQLite）
│   ├── auth-dev.yaml       # 开发环境配置
│   ├── auth-production.yaml # 生产环境配置
│   └── trpc_go.yaml        # TRPC框架配置
├── sql/                    # 数据库脚本
│   └── schema.sql          # 数据库表结构
├── logs/                   # 日志目录
└── data/                   # 数据目录（SQLite数据库文件）
```

## 配置文件说明

### 开发环境：`config/auth-dev.yaml`
- 宽松的安全设置
- 长令牌过期时间
- 详细的日志输出

### 生产环境：`config/auth-production.yaml`
- 严格的安全参数
- 环境变量配置
- 适合容器化部署

### 默认配置：`config/auth.yaml`
- 开箱即用的SQLite配置
- 平衡的安全设置

## 部署步骤

### 1. 构建服务

```bash
# 进入scripts目录
cd scripts

# 使用Makefile
make install VERSION=v1.0.0

# 或使用脚本
./build.sh v1.0.0
```

### 2. 配置环境

根据部署环境修改配置文件：

```bash
cd ../../../go/bin/moox-server

# 开发环境
cp config/auth-dev.yaml config/auth-current.yaml

# 生产环境
cp config/auth-production.yaml config/auth-current.yaml
```

### 3. 初始化数据库

```bash
# SQLite（默认）
sqlite3 data/auth.db < sql/schema.sql

# PostgreSQL（如果配置了）
psql -d your_database -f sql/schema.sql
```

### 4. 启动服务

```bash
# 使用启动脚本（推荐）
./start.sh

# 或直接运行
./bin/moox-server -conf=./config/trpc_go.yaml
```

### 5. 验证服务

```bash
# 检查服务状态
ps -ef | grep moox-server

# 查看日志
tail -f logs/app.log
```

## 环境变量

生产环境可以通过环境变量覆盖配置：

```bash
export DB_HOST="your-db-host"
export DB_USER="your-db-user"  
export DB_PASSWORD="your-secure-password"
export DB_NAME="moox_auth_prod"
export JWT_SECRET_KEY="your-super-secure-jwt-key"
```

## 服务管理

### 启动服务
```bash
make run
# 或
./start.sh
```

### 停止服务
```bash
make stop
# 或
./stop.sh
```

### 重启服务
```bash
make stop && make run
```

### 查看状态
```bash
# 查看进程
ps -ef | grep moox-server

# 查看日志
tail -f logs/app.log
```

## 故障排除

### 常见问题

1. **编译失败：`undefined: file_common_proto_init`**
   ```bash
   # 重新生成protobuf代码
   cd proto && make all
   ```

2. **CGO编译错误**
   ```bash
   # 确保CGO已启用
   export CGO_ENABLED=1
   ```

3. **数据库连接失败**
   - 检查数据库配置
   - 确保数据库已初始化
   - 验证连接权限

4. **端口被占用**
   ```bash
   # 查看端口占用
   lsof -i :8080
   ```

### 日志分析

```bash
# 实时查看日志
tail -f logs/app.log

# 查看错误日志
grep -i error logs/app.log

# 查看最近的日志
tail -n 100 logs/app.log
```

## 性能优化

### 编译优化

```bash
# 生产环境构建优化
CGO_ENABLED=1 go build -ldflags="-s -w" .
```

### 运行时优化

```bash
# 设置Go运行时参数
export GOMAXPROCS=4
export GOGC=100
```

## 容器化部署

如需容器化部署，可以基于构建产物创建Docker镜像：

```dockerfile
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY moox-server/. .
EXPOSE 8080
CMD ["./moox-server"]
``` 