# MooX Server

MooX Server 是一个基于 tRPC-Go 框架的服务端应用，提供认证、API网关、数据采集等功能。

## 构建说明

本项目使用 Makefile 进行构建管理，不依赖 CGO，支持跨平台编译。

### 查看帮助
```bash
make help
```

### 构建命令

#### 构建当前平台版本
```bash
make build
```

#### 构建 Linux 版本
```bash
make build-linux VERSION=v1.0.0
```
- 支持在任何平台上交叉编译 Linux 二进制文件
- 自动构建 amd64 和 arm64 两种架构
- 输出目录：`release/linux/`

#### 构建 macOS 版本
```bash
make build-darwin VERSION=v1.0.0
# 或
make build-macos VERSION=v1.0.0
```
- 支持在任何平台上交叉编译 macOS 二进制文件
- 自动构建 amd64 和 arm64 两种架构
- 输出目录：`release/darwin/`

#### 构建 Windows 版本
```bash
make build-windows VERSION=v1.0.0
```
- 支持在任何平台上交叉编译 Windows 二进制文件
- 自动构建 amd64 和 arm64 两种架构
- 输出目录：`release/windows/`

#### 构建所有平台版本
```bash
make build-all VERSION=v1.0.0
```

### 构建产物结构
```
release/
└── linux/              # Linux 平台
    ├── bin/            # 二进制文件目录
    │   ├── moox-server-amd64
    │   ├── moox-server-arm64
    │   └── moox-server # 默认为 amd64 版本
    ├── config/         # 配置文件目录
    │   └── trpc_go.yaml
    ├── data/           # 数据目录
    ├── log/            # 日志目录
    ├── start.sh        # 启动脚本
    └── stop.sh         # 停止脚本
```

## 部署说明

### 自动部署

使用 `make deploy` 命令可以自动部署到远程服务器：

```bash
# 自动检测远程服务器平台并部署
make deploy SERVER=ubuntu@43.132.204.177

# 指定部署 Linux 版本
make deploy SERVER=ubuntu@43.132.204.177 PLATFORM=linux

# 指定部署 macOS 版本
make deploy SERVER=user@192.168.1.100 PLATFORM=darwin
```

部署流程：
1. 自动检测或使用指定的目标平台
2. 打包对应平台的发布文件（排除 log 和 data 目录）
3. 上传到远程服务器
4. 自动停止旧服务（如果存在）
5. 解压新版本
6. 恢复旧版本的 data 和 log 目录
7. 启动新服务

### 手动部署

1. 构建目标平台版本
```bash
make build-linux VERSION=v1.0.0
```

2. 将 `release/linux/` 目录下的文件上传到服务器

3. 在服务器上执行
```bash
chmod +x start.sh stop.sh
./start.sh
```

## 开发命令

### 安装依赖
```bash
make deps
```

### 运行测试
```bash
make test
```

### 代码检查
```bash
make lint
```

### 开发模式运行
```bash
make dev
```

### 清理构建文件
```bash
make clean
```

## 服务管理

### 启动服务
```bash
./start.sh
```
- 自动检查并停止已有进程
- 服务以后台方式运行
- 日志输出到 `log/app.log`

### 停止服务
```bash
./stop.sh
```

### 查看日志
```bash
tail -f log/app.log
```

## 配置说明

主要配置文件：`config/trpc_go.yaml`

服务端口说明（11xxx 段，详见 `config/trpc_go.yaml`）：
- 11000: MooX HTTP 网关接口（/api/admin）
- 11001: API 服务接口（trpc.moox.api.stdhttp）
- 11100-11107: 各 RPC 服务 HTTP 端口（Auth/Dns/AsyncTask/Monitor/CollectMgr/CloudNodeMgr/Ssh/SpaceMgr）
- 11300-11305: 定时任务端口（dnsproxy/dnsprobe/keepalive/collectmgr/monitor/monitor.cleanup）

## 注意事项

1. 部署时会自动备份和恢复 data 和 log 目录
2. 默认数据库文件位置：`./data/moox.db`
3. 日志文件位置：`./log/`
4. 服务启动时会自动创建必要的目录

## 故障排查

### SQLite "out of memory" 错误
如果遇到此错误，请检查：
- data 目录是否有写入权限
- 磁盘空间是否充足
- 数据库文件路径是否正确