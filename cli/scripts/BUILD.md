# Moox CLI 构建和部署指南

## 快速开始

### 方式一：使用Makefile（推荐）

**从项目根目录运行（推荐）：**
```bash
# 查看所有可用命令
make help

# 完整构建和发布准备
make release VERSION=v1.0.0

# 开发模式运行
make dev

# 构建当前平台
make build VERSION=v1.0.0

# 跨平台构建
make cross-build VERSION=v1.0.0
```

**从scripts目录运行：**
```bash
# 进入scripts目录
cd scripts

# 查看所有可用命令
make help

# 跨平台构建
make cross-build VERSION=v1.0.0

# 系统安装（需要sudo）
sudo make install

# 系统卸载
sudo make uninstall
```

### 方式二：使用构建脚本

```bash
# 进入scripts目录
cd scripts

# 构建指定版本（跨平台）
./build.sh v1.0.0

# 进入构建目录
cd ../release

# 直接运行（开发用）
./bin/moox --help

# 系统安装（生产用）
sudo ./install.sh

# 系统卸载
sudo ./uninstall.sh
```

## 构建要求

- Go 1.19+
- CGO_ENABLED=1（用于SQLite支持）
- Git（用于获取提交信息）

## 构建选项

### 开发环境

```bash
# 进入scripts目录
cd scripts

# 开发模式运行（不构建）
make dev

# 或回到项目根目录直接运行
cd .. && go run . --help
```

### 生产环境

```bash
# 进入scripts目录
cd scripts

# 单平台构建（当前平台）
make build VERSION=v1.0.0

# 跨平台构建（所有支持平台）
make cross-build VERSION=v1.0.0
```

## 构建产物

跨平台构建完成后会在 `../release/` 目录下生成：

```
release/
├── bin/                     # 当前平台二进制文件
│   └── moox                 # CLI工具主程序
├── linux-amd64/             # Linux x64平台
│   └── moox
├── darwin-amd64/            # macOS Intel平台  
│   └── moox
├── darwin-arm64/            # macOS Apple Silicon平台
│   └── moox
├── windows-amd64/           # Windows x64平台
│   └── moox.exe
├── config/                  # 配置文件目录
│   ├── cli.yaml             # 当前配置文件
│   └── cli-example.yaml     # 示例配置文件
├── install.sh               # 系统安装脚本
├── uninstall.sh             # 系统卸载脚本
├── README.md                # 使用说明
└── VERSION                  # 版本信息
```

## 配置文件说明

### 主配置文件：`config/cli.yaml`
```yaml
# 元数据数据库配置
metadata_database:
  storage_device: "sqlite:./data/metadata.db"

# 存储服务配置
storage:
  target: "127.0.0.1:18102"

# Moox认证服务配置  
moox:
  auth_target: "127.0.0.1:18200"

# 消息服务配置
message:
  server: "nats:localhost:4222"
  consumer: "MY_CONSUMER"
  subject: "storage.datadetail.change"
```

## 部署步骤

### 1. 构建CLI工具

```bash
# 进入scripts目录
cd scripts

# 跨平台构建
make cross-build VERSION=v1.0.0

# 或使用脚本
./build.sh v1.0.0
```

### 2. 配置环境

#### 开发环境（本地运行）

```bash
cd ../release

# 直接运行
./bin/moox --help

# 或添加到PATH
export PATH=$PATH:$(pwd)/bin
moox --help
```

#### 生产环境（系统安装）

```bash
cd ../release

# 系统安装
sudo ./install.sh

# 全局使用
moox --help

# 编辑配置
sudo vim /etc/moox/cli.yaml
```

### 3. 验证安装

```bash
# 检查版本
moox --help

# 测试认证功能
moox auth --help

# 测试数据库功能  
moox db --help

# 测试存储功能
moox storage --help
```

## 使用场景

### 用户认证

```bash
# 用户注册（交互式）
moox auth register

# 用户注册（命令行参数）
moox auth register --username myuser --password mypass --nickname "我的昵称" --email user@example.com

# 中文命令支持
moox 认证 注册 --username myuser --password mypass
```

### 数据库操作

```bash
# 查看数据库帮助
moox db --help

# 创建表
moox db --create-table mytable

# 查看表结构
moox db --show-schema mytable

# 查看表数据
moox db --show-data mytable
```

### 存储服务

```bash
# 查看存储帮助
moox storage --help

# 写入数据
moox storage --interface set --project-id 1 --dataset-id 1 --object-id mydata

# 读取数据
moox storage --interface get --project-id 1 --dataset-id 1 --object-id mydata
```

### 消息队列

```bash
# 查看消息帮助
moox msg --help

# 监听消息
moox msg --listen --subject "storage.datadetail.change"
```

## 多平台支持

### 支持的平台
- **Linux x64**: linux-amd64/moox
- **macOS Intel**: darwin-amd64/moox  
- **macOS Apple Silicon**: darwin-arm64/moox
- **Windows x64**: windows-amd64/moox.exe

### 平台特定安装

#### Linux
```bash
# 下载对应平台的二进制文件
cp linux-amd64/moox /usr/local/bin/
chmod +x /usr/local/bin/moox
```

#### macOS
```bash
# Intel Mac
cp darwin-amd64/moox /usr/local/bin/

# Apple Silicon Mac  
cp darwin-arm64/moox /usr/local/bin/

chmod +x /usr/local/bin/moox
```

#### Windows
```bash
# 拷贝到PATH目录
copy windows-amd64\moox.exe C:\Windows\System32\
```

## 环境变量

CLI工具可以通过环境变量覆盖配置：

```bash
export MOOX_AUTH_TARGET="your-auth-server:port"
export STORAGE_TARGET="your-storage-server:port"
export METADATA_DB="sqlite:./custom/path/metadata.db"
```

## 开发指南

### 环境设置

```bash
# 检查开发环境
make env

# 设置开发工具
make setup

# 安装依赖
make deps
```

### 代码检查

```bash
# 运行所有检查
make check

# 单独运行lint
make lint

# 运行测试
make test
```

### 调试和开发

```bash
# 开发模式运行
make dev

# 构建当前平台进行测试
make build

# 清理构建文件
make clean
```

## 故障排除

### 常见问题

1. **构建失败**
   ```bash
   # 检查Go环境
   make env
   
   # 重新安装依赖
   make deps
   ```

2. **权限错误**
   ```bash
   # 确保脚本有执行权限
   chmod +x scripts/build.sh
   
   # 系统安装需要sudo
   sudo ./install.sh
   ```

3. **配置错误**
   ```bash
   # 检查配置文件语法
   moox --help
   
   # 使用示例配置
   cp config/cli-example.yaml config/cli.yaml
   ```

### 性能优化

- 使用CGO编译以获得更好的SQLite性能
- 生产环境建议使用系统安装方式
- 配置文件放在固定位置便于管理

## 发布流程

```bash
# 1. 代码检查
make check

# 2. 运行测试  
make test

# 3. 构建发布版本
make release VERSION=v1.0.0

# 4. 验证构建产物
cd release && ./bin/moox --help

# 5. 打包分发
tar -czf moox-cli-v1.0.0.tar.gz release/
``` 