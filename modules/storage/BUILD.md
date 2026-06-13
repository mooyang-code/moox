# xData-mini Storage 构建指南

本文档说明如何构建 storage 服务（包含 RocksDB 静态链接支持）。

## 🚨 重要说明

### CGO 和平台限制
- ✅ 本项目使用 CGO，默认建议在目标平台上编译
- ✅ Linux 产物可以在 Linux 上原生编译
- ✅ macOS 产物必须在 macOS 上编译
- ✅ Windows 产物必须在 Windows 上编译
- ✅ macOS 可以通过 `make build-linux-cross` 交叉构建 Linux amd64 产物，不依赖 Docker

### RocksDB 依赖
- 本项目使用 RocksDB 存储引擎
- 原生构建前**必须**先安装 RocksDB C++ 库；macOS 交叉构建会由脚本自动准备 Linux 版 RocksDB
- 已配置为**静态链接**，生成的二进制文件不依赖 librocksdb.so

---

## 快速开始

### 在 Linux 上构建（推荐流程）

```bash
# 1. 检查 RocksDB 是否已安装
./scripts/check_rocksdb.sh

# 2. 如果未安装，安装 RocksDB（首次构建必需，需要 10-20 分钟）
./scripts/install_rocksdb.sh

# 3. 构建
make build-linux VERSION=v1.0.0

# 4. 部署到目标服务器
make deploy SERVER=user@host
```

**说明**：
- `make build-linux` 包含完整的构建流程
- 自动检查环境、构建、生成启动脚本、验证静态链接
- 是推荐的标准构建方式

### 在 macOS 上构建

```bash
# 1. 安装 RocksDB（使用 Homebrew）
brew install rocksdb

# 2. 构建 macOS 版本
make build-darwin VERSION=v1.0.0

# 3. 运行
./release/darwin/bin/xdata-storage -conf=./config/trpc_go.yaml
```

### 在 macOS 上交叉构建 Linux amd64

如果合规要求不能使用 Docker，可以使用 `build-linux-cross`。脚本会在 `.deps/` 下前置检测并自动准备缺失依赖：

- Zig macOS 交叉工具链：来自 `https://ziglang.org/download/`
- CMake：来自 Kitware 官方 GitHub Release
- zlib、bzip2、lz4、zstd、snappy、RocksDB：来自各项目官方发布地址
- Ubuntu 22.04 GNU `libstdc++`/`libgcc` 运行库和开发包：来自 `https://archive.ubuntu.com/ubuntu/`

脚本会构建 Linux amd64 版 RocksDB 静态库，并生成临时 patched DuckDB 绑定，避免 DuckDB 预编译静态库和 Zig 默认 libc++ ABI 冲突。
默认交叉构建目标为 `x86_64-linux-gnu.2.35`，可在 Ubuntu 22.04 / glibc 2.35 及更新的 Linux amd64 环境运行。glibc 向后兼容，所以 glibc 2.36 或更新机器也使用这份默认产物。除非生产环境统一要求更高 glibc，不需要单独维护 glibc 2.36 包。

仓库中也保留了两份 Linux amd64 预编译包：

- `release/xdata-storage-linux-amd64.tar.gz`：完整运行包。它不是 macOS 专用产物，解压后得到的是 Linux 运行目录。
- `release/xdata-storage-linux-amd64-deps.tar.gz`：交叉构建依赖包，包含 Linux amd64 版 RocksDB/snappy/zlib/lz4/zstd/bzip2 静态库，以及 Ubuntu 22.04 GNU `libstdc++`/`libgcc` 头文件和运行库。`make build-linux-cross` 会优先解压它，避免重新编译 RocksDB 等依赖。

推荐流程：

```bash
# 1. 进入 storage 目录
cd storage

# 2. 确认 Go 和 Xcode Command Line Tools 可用
go version
xcode-select -p

# 3. 交叉构建 Linux amd64 运行包
make build-linux-cross VERSION=v1.0.0

# 4. 查看产物
ls -lh release/xdata-storage-linux-amd64.tar.gz
tar -tzf release/xdata-storage-linux-amd64.tar.gz | head
```

构建完成后，脚本会生成：

- `release/linux/bin/xdata-storage`：Linux amd64 ELF 二进制。
- `release/linux/config/`：运行配置。
- `release/linux/lib/`：Linux 运行时库。
- `release/xdata-storage-linux-amd64.tar.gz`：可上传到 Linux 机器解压运行的完整包。
- `release/xdata-storage-linux-amd64-deps.tar.gz`：下次 macOS 交叉构建复用的依赖包。

可选参数：

```bash
# 指定版本号
make build-linux-cross VERSION=v1.0.0

# 指定依赖缓存目录
make build-linux-cross VERSION=v1.0.0 ROCKSDB_LINUX_PREFIX=/path/to/linux-amd64-prefix

# 指定 Zig 版本
make build-linux-cross VERSION=v1.0.0 ZIG_VERSION=0.14.1

# 仅在确实需要更高 glibc 目标时指定；默认 2.35 覆盖面更广
make build-linux-cross VERSION=v1.0.0 LINUX_TARGET=x86_64-linux-gnu.2.35
```

验证方法：

```bash
# 本机只能确认文件类型
file release/linux/bin/xdata-storage

# 复制到 Linux 机器验证动态依赖和启动
scp release/xdata-storage-linux-amd64.tar.gz user@host:/tmp/
ssh user@host '
  mkdir -p ~/xdata/storage
  tar -xzf /tmp/xdata-storage-linux-amd64.tar.gz --strip-components=1 -C ~/xdata/storage
  cd ~/xdata/storage
  ldd ./bin/xdata-storage
  ./start.sh
  tail -n 100 ./log/app.log
'
```

交叉构建只能在 macOS 上完成编译。最终发布前，仍应把产物复制到 Linux 机器上执行 `ldd ./bin/xdata-storage` 并启动服务，确认目标机器的 glibc 和端口监听正常。

---

## 详细步骤

### 步骤 1: 安装前置依赖

#### Go 工具链
- 版本：参考 `go.mod` 文件
- 安装：https://golang.org/dl/

#### Make
```bash
# Linux (Ubuntu/Debian)
sudo apt-get install make

# Linux (CentOS/RHEL)
sudo yum install make

# macOS
xcode-select --install  # 包含 make
```

#### RocksDB C++ 库

**Linux（首次构建必需）：**
```bash
# 方式 A: 使用项目脚本（推荐）
./scripts/install_rocksdb.sh

# 方式 B: 使用 Makefile
make install-rocksdb

# 安装内容：
# - 静态库：/usr/local/lib/librocksdb.a
# - 动态库：/usr/local/lib/librocksdb.so
# - 头文件：/usr/local/include/rocksdb/
```

**macOS：**
```bash
brew install rocksdb
```

**检查安装状态：**
```bash
./scripts/check_rocksdb.sh
```

### 步骤 2: 生成 protobuf 代码（可选）

```bash
# 如果修改了 .proto 文件，需要重新生成
make proto
```

### 步骤 3: 构建项目

#### 使用 Makefile（标准方式）

```bash
# Linux（必须在 Linux 上执行）
make build-linux VERSION=v1.0.0

# macOS（必须在 macOS 上执行）
make build-darwin VERSION=v1.0.0

# Windows（必须在 Windows 上执行）
make build-windows VERSION=v1.0.0

# 当前平台（快速构建，用于开发测试）
make build
```

**优点**：
- ✅ 标准化构建流程
- ✅ 自动检查环境（RocksDB 库、头文件）
- ✅ 强制静态链接 RocksDB
- ✅ 自动生成启动/停止脚本
- ✅ 自动验证静态链接结果

构建完成后会自动验证，期望输出：
```
✓ 成功: 不依赖 librocksdb.so
```

#### 手动构建（高级用户）

如果需要完全自定义构建过程：

```bash
# 设置环境变量
export CGO_ENABLED=1
export CGO_CFLAGS="-I/usr/local/include"
export CGO_LDFLAGS="/usr/local/lib/librocksdb.a -lstdc++ -lm -lz -lsnappy -llz4 -lzstd -lbz2 -lpthread -ldl"

# 构建（关键：使用 grocksdb_no_link tag + extldflags）
go build -tags "grocksdb_no_link" \
  -ldflags "-X main.Version=v1.0.0 -X main.BuildTime=$(date +%Y-%m-%d_%H:%M:%S) -linkmode external -extldflags '-static-libgcc -static-libstdc++'" \
  -o ./bin/xdata-storage .

# 验证
ldd ./bin/xdata-storage | grep rocksdb
# 应该没有输出（表示不依赖 librocksdb.so）
```

### 步骤 4: 验证构建产物

```bash
# 检查文件是否存在
ls -la ./release/linux/

# 应该包含：
# - bin/xdata-storage        (二进制文件，200+ MB)
# - config/                  (配置目录)
# - log/                     (日志目录)
# - start.sh                 (启动脚本)
# - stop.sh                  (停止脚本)

# 验证静态链接（检查是否依赖 librocksdb.so）
ldd ./release/linux/bin/xdata-storage | grep rocksdb
# 应该没有输出（表示不依赖 librocksdb.so）

# 测试运行
./release/linux/bin/xdata-storage -h
```

---

## 构建产物说明

### 目录结构

```
release/
├── linux/                    # Linux 平台构建产物
│   ├── bin/
│   │   └── xdata-storage    # 二进制文件（静态链接，200+ MB）
│   ├── config/              # 配置文件
│   │   └── trpc_go.yaml
│   ├── log/                 # 日志目录（空）
│   ├── start.sh             # 启动脚本
│   └── stop.sh              # 停止脚本
├── darwin/                   # macOS 平台构建产物（结构相同）
└── windows/                  # Windows 平台构建产物
    ├── bin/
    │   └── xdata-storage.exe
    ├── config/
    ├── start.bat
    └── stop.bat
```

### 静态链接说明

**静态链接的内容**：
- ✅ RocksDB 库（librocksdb.a）
- ✅ C++ 标准库（libstdc++）
- ✅ GCC 运行时（libgcc_s）
- ✅ 压缩库（zlib, snappy, lz4, zstd, bz2）

**仍然动态链接**：
- ❌ 系统基础库（libc, libm, libdl, pthread）- 无法避免

**优势**：
- 部署时不需要在目标机器上安装 RocksDB
- 不依赖目标系统的 C++ 标准库版本
- 二进制文件完全独立，复制即可运行

**劣势**：
- 二进制文件较大（约 200-250 MB）

---

## 部署

### 远程部署

```bash
# 自动部署到远程服务器
make deploy SERVER=user@host

# 示例
make deploy SERVER=ubuntu@43.136.59.72
```

部署脚本会：
1. 检测远程平台类型
2. 停止旧服务（如果运行中）
3. 备份旧版本
4. 上传并解压新版本
5. 启动服务

### 手动部署

```bash
# 1. 打包构建产物
cd release
tar -czf xdata-storage-linux.tar.gz linux/

# 2. 上传到目标服务器
scp xdata-storage-linux.tar.gz user@host:/tmp/

# 3. SSH 到目标服务器
ssh user@host

# 4. 解压并运行
cd ~
tar -xzf /tmp/xdata-storage-linux.tar.gz
cd linux
./start.sh

# 5. 查看日志
tail -f log/app.log
```

---

## 开发和调试

### 开发模式运行

```bash
# 直接运行（不构建 release）
make dev

# 等同于
go run . -conf=./config/trpc_go.yaml
```

### 清理构建产物

```bash
# 清理所有构建产物和缓存
make clean

# 手动清理
rm -rf ./bin ./release
go clean -cache
```

### 完全清理（包括依赖缓存）

```bash
# 清理所有 Go 缓存（慎用！）
go clean -cache -modcache -testcache
rm -rf ./bin ./release
```

---

## 故障排查

### 问题 1: 编译失败 - "could not determine what C.rocksdb_slice_t refers to"

**原因**：未安装 RocksDB C++ 库

**解决**：
```bash
./scripts/install_rocksdb.sh
```

### 问题 2: 运行时报错 - "librocksdb.so.10.12: cannot open shared object file"

**原因**：编译时使用了动态链接而不是静态链接

**解决**：
```bash
# 清理缓存
go clean -cache -modcache
rm -rf ./bin ./release

# 重新构建
make build-linux VERSION=v1.0.0

# 验证（检查是否依赖动态库）
ldd ./release/linux/bin/xdata-storage | grep rocksdb
# 应该没有输出
```

### 问题 3: 构建后缺少 start.sh

**原因**：构建过程异常中断

**解决**：
```bash
# 重新完整构建
make build-linux VERSION=v1.0.0

# 或手动生成启动脚本
make create-unix-scripts PLATFORM_DIR=./release/linux
```

### 问题 4: 部署失败

**检查清单**：
```bash
# 1. 确认构建产物完整
ls -la ./release/linux/
# 必须包含：bin/, config/, start.sh, stop.sh

# 2. 确认静态链接成功
ldd ./release/linux/bin/xdata-storage | grep rocksdb
# 应该没有输出

# 3. 确认 SSH 访问正常
ssh user@host echo "OK"

# 4. 确认远程服务器有 unzip
ssh user@host which unzip
```

---

## 工具脚本说明

### scripts/ 目录

| 脚本 | 用途 | 使用场景 |
|------|------|----------|
| `install_rocksdb.sh` | 安装 RocksDB C++ 库 | 首次构建前（必需） |
| `check_rocksdb.sh` | 检查 RocksDB 安装状态 | 构建前验证环境 |
| `deploy.sh` | 部署到远程服务器 | 通过 make deploy 调用 |

### 完整构建流程（推荐）

```bash
# 1. 环境检查
./scripts/check_rocksdb.sh

# 2. 安装依赖（如果需要）
./scripts/install_rocksdb.sh

# 3. 构建（自动验证静态链接）
make build-linux VERSION=v1.0.0

# 4. 部署
make deploy SERVER=user@host
```

---

## 常用命令速查

```bash
# 查看所有 Make 命令
make help

# 安装 Go 依赖
make deps

# 生成 protobuf
make proto

# 代码检查
make lint

# 运行测试
make test

# 开发模式运行
make dev

# 构建当前平台
make build

# 构建 Linux（推荐，包含完整流程）
make build-linux VERSION=v1.0.0

# 验证静态链接（构建时自动验证）
ldd ./release/linux/bin/xdata-storage | grep rocksdb

# 部署
make deploy SERVER=user@host

# 清理
make clean
```

---

## 相关文档

- **RocksDB 静态链接问题修复**：[docs/QUICK_FIX_ROCKSDB.md](./docs/QUICK_FIX_ROCKSDB.md)
- **详细故障排查指南**：[docs/TROUBLESHOOTING_ROCKSDB.md](./docs/TROUBLESHOOTING_ROCKSDB.md)
- **完整构建和部署流程**：[docs/BUILD_AND_DEPLOY.md](./docs/BUILD_AND_DEPLOY.md)
- **静态链接技术细节**：[docs/STATIC_LINKING.md](./docs/STATIC_LINKING.md)

---

## 常见问题 FAQ

**Q: 为什么必须在 Linux 上编译 Linux 版本？**
A: 因为启用了 CGO，需要链接 C++ 库。Go 的交叉编译不支持 CGO。

**Q: 为什么二进制文件这么大（200+ MB）？**
A: 因为使用了静态链接，整个 RocksDB 库（约 100 MB）被编译进了二进制文件。

**Q: 能否减小二进制文件大小？**
A: 可以使用动态链接，但需要在目标机器上安装 RocksDB。静态链接虽然文件大，但部署简单。

**Q: macOS 为什么不用 install_rocksdb.sh？**
A: macOS 用户通常使用 Homebrew 管理依赖，更加方便和标准。

**Q: 如何更新 RocksDB 版本？**
A: 重新运行 `./scripts/install_rocksdb.sh`，它会从 GitHub main 分支拉取最新代码并编译。

**Q: 构建时出现 "permission denied"？**
A: 给脚本添加执行权限：`chmod +x ./scripts/*.sh`

---

## 技术支持

如果遇到构建问题：

1. 运行检查脚本并保存输出：
   ```bash
   ./scripts/check_rocksdb.sh > check.log
   ldd ./release/linux/bin/xdata-storage > linking.log
   ```

2. 提供以下信息：
   - 操作系统和版本：`uname -a`
   - Go 版本：`go version`
   - check.log 和 linking.log 内容
   - 完整的错误信息

3. 查看详细文档：
   - [docs/QUICK_FIX_ROCKSDB.md](./docs/QUICK_FIX_ROCKSDB.md)
   - [docs/TROUBLESHOOTING_ROCKSDB.md](./docs/TROUBLESHOOTING_ROCKSDB.md)
