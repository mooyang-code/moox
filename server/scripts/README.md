# Moox Server Scripts 工具集

本目录包含了 Moox Server 项目的各种构建、部署和测试工具。

## 🛠️ 工具列表

### 1. Makefile - 统一构建管理工具

**描述**: 项目的主要构建管理工具，提供了完整的构建、测试、部署流程管理。

**主要功能**:
- 完整的构建流程管理
- 依赖管理和代码检查
- 数据库初始化和管理
- 开发和生产环境支持

**使用方法**:
```bash
# 查看所有可用命令
make help

# 常用命令
make install VERSION=v1.0.0  # 完整构建和安装
make build                   # 仅构建服务器
make build-cmd              # 构建数据库工具
make build-all              # 构建所有组件
make dev                    # 开发模式运行
make run                    # 启动服务
make stop                   # 停止服务

# 数据库管理
make init-db                # 初始化数据库
make migrate-db             # 迁移数据库
make drop-db                # 删除数据库
make dev-db                 # 开发环境数据库设置

# 代码质量
make deps                   # 安装依赖
make proto                  # 生成protobuf代码
make check                  # 代码检查(lint+vet)
make test                   # 运行测试
```

### 2. build.sh - 构建脚本

**描述**: 独立的构建脚本，用于构建 Moox Server 并生成发布包。

**功能特点**:
- 自动创建目录结构
- 生成启动/停止脚本
- 拷贝配置文件
- 生成使用说明文档
- 版本信息嵌入

**使用方法**:
```bash
# 构建开发版本
./build.sh

# 构建指定版本
./build.sh v1.0.0

# 构建产物位置: ../release/
```

**构建产物结构**:
```
release/
├── bin/                    # 二进制文件
│   ├── moox-server        # 主程序
│   └── trpc_go.yaml       # TRPC配置
├── config/                 # 配置文件目录
├── log/                    # 日志目录
├── data/                   # 数据目录
├── start.sh               # 启动脚本
├── stop.sh                # 停止脚本
└── README.md              # 使用说明
```

### 3. deploy.sh - 部署脚本

**描述**: 自动化部署脚本，用于压缩发布包并上传到远程服务器。

**功能特点**:
- 自动压缩 release 目录
- SCP 上传到远程服务器
- 彩色输出提示信息
- 自动清理本地压缩文件

**使用方法**:
```bash
# 部署到远程服务器
./deploy.sh user@192.168.1.100

# 脚本会自动:
# 1. 压缩 ../release 目录为 moox.zip
# 2. 上传到远程服务器的 /home 目录
# 3. 清理本地压缩文件
```

### 4. test_gateway.sh - 网关测试脚本

**描述**: 用于测试 Moox 网关功能的自动化测试脚本。

**测试内容**:
1. 健康检查接口测试
2. 存储服务转发测试
3. 认证服务转发测试
4. 错误处理测试

**使用方法**:
```bash
# 运行网关测试
./test_gateway.sh

# 前提条件:
# - 网关服务运行在 localhost:18202
# - 已安装 jq（用于格式化 JSON 输出）
```

### 5. BUILD.md - 构建文档

**描述**: 详细的构建和部署指南文档。

**内容包括**:
- 快速开始指南
- 构建要求和选项
- 配置文件说明
- 部署步骤
- 环境变量配置
- 故障排除指南
- 性能优化建议

## 📋 快速开始

### 开发环境

```bash
# 1. 进入 scripts 目录
cd scripts

# 2. 安装依赖并检查代码
make deps check

# 3. 开发模式运行
make dev
```

### 生产部署

```bash
# 1. 构建生产版本
make install VERSION=v1.0.0

# 2. 部署到服务器
./deploy.sh user@production-server

# 3. 在服务器上启动
ssh user@production-server
cd /home
unzip moox.zip
cd release
./start.sh
```

## 🔧 常见使用场景

### 场景1: 完整构建和本地测试

```bash
# 构建所有组件
make build-all VERSION=v1.0.0

# 初始化数据库
make init-db

# 启动服务
make run

# 运行网关测试
./test_gateway.sh

# 停止服务
make stop
```

### 场景2: 快速开发迭代

```bash
# 清理并重置开发数据库
make dev-db

# 开发模式运行（自动重载）
make dev
```

### 场景3: 生产环境部署

```bash
# 完整构建
make install VERSION=v1.0.0

# 部署到多个服务器
for server in server1 server2 server3; do
    ./deploy.sh user@$server
done
```

## ⚠️ 注意事项

1. **构建要求**:
   - Go 1.19 或更高版本
   - CGO_ENABLED=1（SQLite支持）
   - protoc（Protocol Buffers编译器）

2. **权限问题**:
   - 确保脚本有执行权限: `chmod +x *.sh`
   - 部署时需要远程服务器的 SSH 访问权限

3. **环境配置**:
   - 开发环境使用 `auth-dev.yaml`
   - 生产环境使用 `auth-production.yaml`
   - 通过环境变量覆盖敏感配置

4. **数据库**:
   - 默认使用 SQLite（适合开发和小规模部署）
   - 生产环境建议使用 PostgreSQL

## 📚 更多信息

- 详细构建指南请参考 [BUILD.md](./BUILD.md)
- 项目主文档请参考上级目录的 README.md
- 遇到问题请查看故障排除章节或提交 Issue