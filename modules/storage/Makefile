# xData-mini Storage Makefile

.PHONY: help build build-linux build-linux-cross build-darwin build-windows build-all clean deps proto lint test prepare-grocksdb-headers install release install-rocksdb check-rocksdb

# Default target
all: deps proto build

# Variables
APP_NAME=xdata-storage
VERSION ?= dev
BUILD_TIME=$(shell date +"%Y-%m-%d_%H:%M:%S")
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DIR=./bin
RELEASE_DIR=./release
CMD_DIR=./cmd
PROTO_DIR=./proto
CONFIG_DIR=./config
GROCKSDB_INCLUDE_DIR ?= .cache/grocksdb/include
TEST_GOARCH ?= amd64

# Build flags
LDFLAGS=-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT) $(EXTRA_LDFLAGS)
CGO_FLAGS=CGO_ENABLED=1
# RocksDB CGO flags - 必须导出为环境变量
export CGO_CFLAGS := -I/usr/local/include
# 完全静态链接 RocksDB - 直接指定静态库路径
# 注意：不使用 -lrocksdb，而是直接指定 .a 文件路径
export CGO_LDFLAGS := /usr/local/lib/librocksdb.a -lstdc++ -lm -lz -lsnappy -llz4 -lzstd -lbz2 -lpthread -ldl
# 静态链接标准库（通过 Go 的 -extldflags 传递）
export EXTRA_LDFLAGS := -linkmode external -extldflags "-static-libgcc -static-libstdc++"
# Build tags - 使用 grocksdb_no_link 禁用库的默认链接配置
BUILD_TAGS := grocksdb_no_link

# Platform specific variables
PLATFORMS=linux darwin
ARCHITECTURES=amd64 arm64

# Help
help:
	@echo "🛠️  xData-mini Storage 构建工具"
	@echo ""
	@echo "📦 构建目标:"
	@echo "  build         - 构建当前平台二进制文件"
	@echo "  build-linux   - 构建 Linux 平台二进制文件 (需在 Linux 上执行，启用 CGO)"
	@echo "  build-linux-cross - 在 macOS 上交叉构建 Linux amd64 二进制文件 (自动准备依赖，无需 Docker)"
	@echo "  build-darwin  - 构建 macOS 平台二进制文件 (需在 macOS 上执行，启用 CGO)"
	@echo "  build-macos   - 构建 macOS 平台二进制文件 (同 build-darwin)"
	@echo "  build-windows - 构建 Windows 平台二进制文件 (需在 Windows 上执行，启用 CGO)"
	@echo "  build-all     - 构建所有平台二进制文件 (已弃用)"
	@echo "  release       - 创建发布包"
	@echo "  deploy        - 部署到远程服务器"
	@echo "  clean         - 清理构建文件"
	@echo ""
	@echo "🔧 开发工具:"
	@echo "  deps          - 安装Go依赖"
	@echo "  proto         - 生成protobuf代码"
	@echo "  lint          - 代码检查"
	@echo "  test          - 运行测试"
	@echo ""
	@echo "🗄️  RocksDB 工具:"
	@echo "  install-rocksdb - 安装 RocksDB C++ 库"
	@echo "  check-rocksdb   - 检查 RocksDB 安装状态"
	@echo ""
	@echo "💡 使用示例:"
	@echo "  make build-linux VERSION=v1.0.0   - 在 Linux 上构建Linux版本"
	@echo "  make build-linux-cross VERSION=v1.0.0 - 在 macOS 上交叉构建 Linux amd64 版本"
	@echo "  make build-darwin VERSION=v1.0.0  - 在 macOS 上构建macOS版本"
	@echo "  make build-windows VERSION=v1.0.0 - 在 Windows 上构建Windows版本"
	@echo "  make release VERSION=v1.0.0       - 创建当前平台的发布包"
	@echo "  make deploy SERVER=user@host [platform] - 部署到远程服务器，可选平台参数"

# Install dependencies
deps:
	@echo "📋 正在安装Go依赖..."
	go mod download
	go mod tidy

# Generate protobuf
proto:
	@echo "🔧 正在生成protobuf代码..."
	cd $(PROTO_DIR) && $(MAKE) all

# Code check
lint:
	@echo "🔍 正在运行代码检查..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "⚠️  警告: golangci-lint 未安装，跳过lint检查"; \
		echo "安装命令: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Run tests
test:
	@echo "🧪 正在运行测试..."
	@GROCKSDB_HEADERS="$$(./scripts/prepare_grocksdb_headers.sh $(GROCKSDB_INCLUDE_DIR))"; \
	GOARCH=$(TEST_GOARCH) CGO_ENABLED=1 CGO_CFLAGS="-I$${GROCKSDB_HEADERS} $${CGO_CFLAGS:-}" CGO_LDFLAGS="" \
	go test -v -tags "$(BUILD_TAGS)" -covermode=count -coverprofile=cover.out.tmp -coverpkg=./... '-gcflags=all=-N -l' ./...

prepare-grocksdb-headers:
	@./scripts/prepare_grocksdb_headers.sh $(GROCKSDB_INCLUDE_DIR)

# Build for current platform
build:
	@echo "📦 正在构建 $(APP_NAME) (当前平台)..."
	@mkdir -p $(BUILD_DIR)
	$(CGO_FLAGS) go build -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) .
	@echo "✅ 构建完成: $(BUILD_DIR)/$(APP_NAME)"

# Build for Linux
build-linux:
	@echo "================================"
	@echo "RocksDB 静态链接构建"
	@echo "================================"
	@echo ""
	@# 检查当前平台
	@if [ "$$(uname -s)" != "Linux" ]; then \
		echo "❌ 错误: 必须在 Linux 平台上构建"; \
		echo "  当前平台: $$(uname -s)"; \
		exit 1; \
	fi
	@echo "✓ 平台检查通过: Linux $$(uname -m)"
	@echo ""
	@# 步骤 1: 检查 RocksDB 静态库
	@echo "步骤 1: 检查 RocksDB 静态库"
	@echo "------------------------------------"
	@STATIC_LIB=""; \
	for lib_path in /usr/local/lib/librocksdb.a /usr/local/lib64/librocksdb.a; do \
		if [ -f "$$lib_path" ]; then \
			STATIC_LIB="$$lib_path"; \
			echo "✓ 找到静态库: $$lib_path"; \
			ls -lh "$$lib_path"; \
			break; \
		fi; \
	done; \
	if [ -z "$$STATIC_LIB" ]; then \
		echo "❌ 错误: 未找到 librocksdb.a"; \
		echo ""; \
		echo "请先安装 RocksDB 静态库:"; \
		echo "  make install-rocksdb"; \
		exit 1; \
	fi
	@echo ""
	@# 步骤 2: 检查头文件
	@echo "步骤 2: 检查 RocksDB 头文件"
	@echo "------------------------------------"
	@if [ ! -d "/usr/local/include/rocksdb" ]; then \
		echo "❌ 错误: 未找到 RocksDB 头文件"; \
		exit 1; \
	fi
	@echo "✓ 头文件存在: /usr/local/include/rocksdb"
	@echo ""
	@# 步骤 3: 清理旧的构建产物
	@echo "步骤 3: 清理旧的构建产物"
	@echo "------------------------------------"
	@rm -rf $(RELEASE_DIR)/linux
	@echo "✓ 清理完成"
	@echo ""
	@# 步骤 4: 创建输出目录
	@echo "步骤 4: 创建输出目录"
	@echo "------------------------------------"
	@mkdir -p $(RELEASE_DIR)/linux/bin
	@mkdir -p $(RELEASE_DIR)/linux/config
	@mkdir -p $(RELEASE_DIR)/linux/log
	@echo "✓ 目录创建完成"
	@echo ""
	@# 步骤 5: 构建
	@echo "步骤 5: 开始编译（这可能需要几分钟）"
	@echo "------------------------------------"
	@CURRENT_ARCH=$$(uname -m | sed 's/x86_64/amd64/'); \
	STATIC_LIB=$$(ls /usr/local/lib/librocksdb.a 2>/dev/null || ls /usr/local/lib64/librocksdb.a 2>/dev/null); \
	BUILD_LDFLAGS="-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT) -linkmode external -extldflags '-static-libgcc -static-libstdc++'"; \
	echo "目标架构: $$CURRENT_ARCH"; \
	echo ""; \
	CGO_ENABLED=1 \
	CGO_CFLAGS="-I/usr/local/include" \
	CGO_LDFLAGS="$$STATIC_LIB -lstdc++ -lm -lz -lsnappy -llz4 -lzstd -lbz2 -lpthread -ldl" \
	GOOS=linux GOARCH=$$CURRENT_ARCH \
	go build -tags "grocksdb_no_link" -ldflags "$$BUILD_LDFLAGS" \
		-o $(RELEASE_DIR)/linux/bin/$(APP_NAME) .
	@if [ $$? -eq 0 ]; then \
		echo ""; \
		echo "✓ 编译成功"; \
	else \
		echo ""; \
		echo "❌ 编译失败"; \
		exit 1; \
	fi
	@echo ""
	@# 步骤 6: 复制配置文件
	@echo "步骤 6: 复制配置文件"
	@echo "------------------------------------"
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/linux/config/ 2>/dev/null || true
	@echo "✓ 配置文件复制完成"
	@echo ""
	@# 步骤 7: 生成启动脚本
	@echo "步骤 7: 生成启动和停止脚本"
	@echo "------------------------------------"
	@$(MAKE) create-unix-scripts PLATFORM_DIR=$(RELEASE_DIR)/linux
	@echo "✓ 启动脚本已生成: start.sh"
	@echo "✓ 停止脚本已生成: stop.sh"
	@echo ""
	@# 步骤 8: 验证二进制文件
	@echo "步骤 8: 验证二进制文件"
	@echo "------------------------------------"
	@BINARY="$(RELEASE_DIR)/linux/bin/$(APP_NAME)"; \
	echo "文件信息:"; \
	file "$$BINARY"; \
	echo ""; \
	echo "文件大小:"; \
	ls -lh "$$BINARY"; \
	echo ""; \
	echo "检查 RocksDB 依赖（应该为空）:"; \
	if ldd "$$BINARY" | grep -q "librocksdb"; then \
		echo "❌ 警告: 仍然依赖 librocksdb.so 动态库！"; \
		echo ""; \
		ldd "$$BINARY" | grep rocksdb; \
		echo ""; \
		echo "可能的原因:"; \
		echo "1. CGO_LDFLAGS 没有正确生效"; \
		echo "2. grocksdb 有自己的链接配置覆盖了环境变量"; \
		exit 1; \
	else \
		echo "✓ 成功: 不依赖 librocksdb.so"; \
	fi
	@echo ""
	@echo "================================"
	@echo "✓ 构建完成！"
	@echo "================================"
	@echo "二进制文件: $(RELEASE_DIR)/linux/bin/$(APP_NAME)"
	@echo ""
	@echo "下一步:"
	@echo "1. 测试二进制: $(RELEASE_DIR)/linux/bin/$(APP_NAME) -h"
	@echo "2. 部署到服务器: make deploy SERVER=user@host"
	@echo ""


# Build Linux amd64 from macOS with a cross C/C++ toolchain.
build-linux-cross:
	@echo "📦 正在从 macOS 交叉构建 Linux amd64 二进制文件..."
	@chmod +x ./scripts/build_linux_cross.sh
	@VERSION="$(VERSION)" \
		BUILD_TIME="$(BUILD_TIME)" \
		GIT_COMMIT="$(GIT_COMMIT)" \
		APP_NAME="$(APP_NAME)" \
		RELEASE_DIR="$(RELEASE_DIR)" \
		CONFIG_DIR="$(CONFIG_DIR)" \
		./scripts/build_linux_cross.sh

# Build for macOS
build-darwin:
	@echo "📦 正在构建 macOS 平台二进制文件..."
	@# 检查当前平台
	@if [ "$$(uname -s)" != "Darwin" ]; then \
		echo "❌ 错误: 当前不是 macOS 平台，无法构建 macOS 二进制文件"; \
		echo "  当前平台: $$(uname -s)"; \
		echo "  目标平台: Darwin (macOS)"; \
		exit 1; \
	fi
	@mkdir -p $(RELEASE_DIR)/darwin/bin
	@mkdir -p $(RELEASE_DIR)/darwin/config
	@mkdir -p $(RELEASE_DIR)/darwin/log
	@# 获取当前机器架构
	@CURRENT_ARCH=$$(uname -m | sed 's/x86_64/amd64/'); \
	echo "  平台: Darwin"; \
	echo "  架构: $$CURRENT_ARCH"; \
	echo "  CGO: 已启用"; \
	GOOS=darwin GOARCH=$$CURRENT_ARCH $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
		-o $(RELEASE_DIR)/darwin/bin/$(APP_NAME) .
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/darwin/config/ 2>/dev/null || true
	@$(MAKE) create-unix-scripts PLATFORM_DIR=$(RELEASE_DIR)/darwin
	@echo "✅ macOS 构建成功"

# Build for macOS (alias for build-darwin)
build-macos: build-darwin

# Build for Windows
build-windows:
	@echo "📦 正在构建 Windows 平台二进制文件..."
	@# 检查当前平台 (支持 Windows, MINGW, MSYS, CYGWIN)
	@OS_NAME=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	case "$$OS_NAME" in \
		*mingw*|*msys*|*cygwin*|*nt*) \
			echo "  检测到 Windows 环境: $$OS_NAME" \
			;; \
		*) \
			echo "❌ 错误: 当前不是 Windows 平台，无法构建 Windows 二进制文件"; \
			echo "  当前平台: $$OS_NAME"; \
			echo "  目标平台: Windows"; \
			exit 1 \
			;; \
	esac
	@mkdir -p $(RELEASE_DIR)/windows/bin
	@mkdir -p $(RELEASE_DIR)/windows/config
	@mkdir -p $(RELEASE_DIR)/windows/log
	@# 获取当前机器架构
	@CURRENT_ARCH=$$(uname -m | sed 's/x86_64/amd64/'); \
	echo "  平台: Windows"; \
	echo "  架构: $$CURRENT_ARCH"; \
	echo "  CGO: 已启用"; \
	GOOS=windows GOARCH=$$CURRENT_ARCH $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
		-o $(RELEASE_DIR)/windows/bin/$(APP_NAME).exe .
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/windows/config/ 2>/dev/null || true
	@$(MAKE) create-windows-scripts PLATFORM_DIR=$(RELEASE_DIR)/windows
	@echo "✅ Windows 构建成功"

# Build for all platforms
build-all:
	@echo "❌ 错误: build-all 已弃用"
	@echo "由于启用了平台验证和 CGO，每个平台的构建必须在对应的操作系统上执行:"
	@echo "  - Linux 二进制文件: 在 Linux 系统上执行 'make build-linux'"
	@echo "  - macOS 二进制文件: 在 macOS 系统上执行 'make build-darwin'"
	@echo "  - Windows 二进制文件: 在 Windows 系统上执行 'make build-windows'"
	@exit 1

# Create release package
release: clean
	@echo "📦 创建发布包..."
	@echo "⚠️  注意: 由于启用了平台验证，请在目标平台上分别执行构建命令"
	@echo "当前平台: $$(uname -s)"
	@# 根据当前平台执行对应的构建
	@case "$$(uname -s)" in \
		Darwin) \
			$(MAKE) build-darwin; \
			;; \
		Linux) \
			$(MAKE) build-linux; \
			;; \
		*) \
			echo "❌ 不支持的平台: $$(uname -s)"; \
			exit 1; \
			;; \
	esac
	@echo "📁 发布目录: $(RELEASE_DIR)"
	@ls -la $(RELEASE_DIR)/

# Create Unix scripts (Linux/macOS)
create-unix-scripts:
	@echo "#!/bin/bash" > $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "APP_NAME=\"xdata-storage\"" >> $(PLATFORM_DIR)/start.sh
	@echo "PID_FILE=\"./\$$APP_NAME.pid\"" >> $(PLATFORM_DIR)/start.sh
	@echo "SCRIPT_DIR=\$$(cd \"\$$(dirname \"\$$0\")\" && pwd)" >> $(PLATFORM_DIR)/start.sh
	@echo "NATS_PORT=8811" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"检查已存在的进程...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "if [ -f \"\$$PID_FILE\" ]; then" >> $(PLATFORM_DIR)/start.sh
	@echo "    OLD_PID=\$$(cat \"\$$PID_FILE\")" >> $(PLATFORM_DIR)/start.sh
	@echo "    if ps -p \"\$$OLD_PID\" > /dev/null 2>&1; then" >> $(PLATFORM_DIR)/start.sh
	@echo "        echo \"发现运行中的服务 (PID: \$$OLD_PID)，正在停止...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "        kill \"\$$OLD_PID\"" >> $(PLATFORM_DIR)/start.sh
	@echo "        sleep 2" >> $(PLATFORM_DIR)/start.sh
	@echo "    fi" >> $(PLATFORM_DIR)/start.sh
	@echo "    rm -f \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/start.sh
	@echo "fi" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "# 检查端口占用情况" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"检查端口 \$$NATS_PORT 占用情况...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "PORT_PIDS=\$$(lsof -ti :\$$NATS_PORT 2>/dev/null)" >> $(PLATFORM_DIR)/start.sh
	@echo "if [ ! -z \"\$$PORT_PIDS\" ]; then" >> $(PLATFORM_DIR)/start.sh
	@echo "    echo \"发现端口 \$$NATS_PORT 被占用，正在停止相关进程...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "    for PID in \$$PORT_PIDS; do" >> $(PLATFORM_DIR)/start.sh
	@echo "        echo \"停止进程 PID: \$$PID\"" >> $(PLATFORM_DIR)/start.sh
	@echo "        kill \"\$$PID\" 2>/dev/null" >> $(PLATFORM_DIR)/start.sh
	@echo "    done" >> $(PLATFORM_DIR)/start.sh
	@echo "    sleep 2" >> $(PLATFORM_DIR)/start.sh
	@echo "fi" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "# 设置配置文件路径" >> $(PLATFORM_DIR)/start.sh
	@echo "export STORAGE_CONFIG_PATH=\"\$$SCRIPT_DIR/config\"" >> $(PLATFORM_DIR)/start.sh
	@echo "export STORAGE_DATABASE_PATH=\"\$$SCRIPT_DIR/database\"" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "cd \"\$$SCRIPT_DIR\"" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "# 确保log和database目录存在" >> $(PLATFORM_DIR)/start.sh
	@echo "mkdir -p log" >> $(PLATFORM_DIR)/start.sh
	@echo "mkdir -p database" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"启动 \$$APP_NAME...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"配置路径: \$$STORAGE_CONFIG_PATH\"" >> $(PLATFORM_DIR)/start.sh
	@echo "nohup ./bin/\$$APP_NAME -conf=./config/trpc_go.yaml > ./log/app.log 2>&1 &" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \$$! > \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"服务已启动 (PID: \$$(cat \$$PID_FILE))\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"日志文件: log/app.log\"" >> $(PLATFORM_DIR)/start.sh
	@echo "#!/bin/bash" > $(PLATFORM_DIR)/stop.sh
	@echo "" >> $(PLATFORM_DIR)/stop.sh
	@echo "APP_NAME=\"xdata-storage\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "PID_FILE=\"./\$$APP_NAME.pid\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "" >> $(PLATFORM_DIR)/stop.sh
	@echo "if [ ! -f \"\$$PID_FILE\" ]; then" >> $(PLATFORM_DIR)/stop.sh
	@echo "    echo \"PID文件不存在，服务可能没有运行\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "    exit 1" >> $(PLATFORM_DIR)/stop.sh
	@echo "fi" >> $(PLATFORM_DIR)/stop.sh
	@echo "" >> $(PLATFORM_DIR)/stop.sh
	@echo "PID=\$$(cat \"\$$PID_FILE\")" >> $(PLATFORM_DIR)/stop.sh
	@echo "if ps -p \"\$$PID\" > /dev/null 2>&1; then" >> $(PLATFORM_DIR)/stop.sh
	@echo "    echo \"停止服务 (PID: \$$PID)...\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "    kill \"\$$PID\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "    sleep 2" >> $(PLATFORM_DIR)/stop.sh
	@echo "    if ps -p \"\$$PID\" > /dev/null 2>&1; then" >> $(PLATFORM_DIR)/stop.sh
	@echo "        echo \"强制停止服务...\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "        kill -9 \"\$$PID\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "    fi" >> $(PLATFORM_DIR)/stop.sh
	@echo "    rm -f \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "    echo \"服务已停止\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "else" >> $(PLATFORM_DIR)/stop.sh
	@echo "    echo \"服务没有运行\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "    rm -f \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "fi" >> $(PLATFORM_DIR)/stop.sh
	@chmod +x $(PLATFORM_DIR)/start.sh
	@chmod +x $(PLATFORM_DIR)/stop.sh

# Create Windows scripts
create-windows-scripts:
	@echo '@echo off' > $(PLATFORM_DIR)/start.bat
	@echo 'setlocal' >> $(PLATFORM_DIR)/start.bat
	@echo 'set "SCRIPT_DIR=%~dp0"' >> $(PLATFORM_DIR)/start.bat
	@echo 'cd /d "%SCRIPT_DIR%"' >> $(PLATFORM_DIR)/start.bat
	@echo '' >> $(PLATFORM_DIR)/start.bat
	@echo 'REM 设置环境变量' >> $(PLATFORM_DIR)/start.bat
	@echo 'set "STORAGE_CONFIG_PATH=%SCRIPT_DIR%config"' >> $(PLATFORM_DIR)/start.bat
	@echo 'set "STORAGE_DATABASE_PATH=%SCRIPT_DIR%database"' >> $(PLATFORM_DIR)/start.bat
	@echo '' >> $(PLATFORM_DIR)/start.bat
	@echo 'REM 确保log和database目录存在' >> $(PLATFORM_DIR)/start.bat
	@echo 'if not exist "log" mkdir "log"' >> $(PLATFORM_DIR)/start.bat
	@echo 'if not exist "database" mkdir "database"' >> $(PLATFORM_DIR)/start.bat
	@echo '' >> $(PLATFORM_DIR)/start.bat
	@echo 'echo Starting xdata-storage service...' >> $(PLATFORM_DIR)/start.bat
	@echo 'cd bin' >> $(PLATFORM_DIR)/start.bat
	@echo 'start xdata-storage.exe -conf=../config/trpc_go.yaml' >> $(PLATFORM_DIR)/start.bat
	@echo 'echo Service started' >> $(PLATFORM_DIR)/start.bat
	@echo '@echo off' > $(PLATFORM_DIR)/stop.bat
	@echo 'taskkill /F /IM xdata-storage.exe' >> $(PLATFORM_DIR)/stop.bat
	@echo 'echo Service stopped' >> $(PLATFORM_DIR)/stop.bat

# Deploy to remote server
deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "❌ 请指定服务器地址"; \
		echo "使用方法: make deploy SERVER=user@host [platform]"; \
		echo "          平台参数可选: linux|darwin"; \
		echo "示例:"; \
		echo "  make deploy SERVER=ubuntu@43.132.204.177         # 自动检测平台"; \
		echo "  make deploy SERVER=ubuntu@43.132.204.177 linux   # 指定Linux平台"; \
		exit 1; \
	fi
	@if [ ! -f "./scripts/deploy.sh" ]; then \
		echo "❌ 部署脚本 ./scripts/deploy.sh 不存在"; \
		exit 1; \
	fi
	@if [ -n "$(filter linux darwin,$(word 2,$(MAKECMDGOALS)))" ]; then \
		echo "📦 使用指定平台: $(word 2,$(MAKECMDGOALS))"; \
		./scripts/deploy.sh $(SERVER) $(word 2,$(MAKECMDGOALS)); \
	else \
		echo "📦 自动检测平台"; \
		./scripts/deploy.sh $(SERVER); \
	fi

# 支持平台参数作为目标
linux:
	@:
darwin:
	@:

# Clean build artifacts
clean:
	@echo "🧹 正在清理构建文件..."
	rm -rf $(BUILD_DIR)
	rm -rf $(RELEASE_DIR)
	go clean -cache

# Development shortcuts
dev:
	@echo "🚀 开发模式启动..."
	$(CGO_FLAGS) go run . -conf=$(CONFIG_DIR)/trpc_go.yaml

# Install to local bin
install: build
	@echo "📦 安装到本地..."
	@mkdir -p ~/bin
	@cp $(BUILD_DIR)/$(APP_NAME) ~/bin/
	@echo "✅ 已安装到 ~/bin/$(APP_NAME)"

# RocksDB management
install-rocksdb:
	@echo "🗄️  安装 RocksDB C++ 库..."
	@chmod +x ./scripts/install_rocksdb.sh
	@./scripts/install_rocksdb.sh

check-rocksdb:
	@echo "🔍 检查 RocksDB 安装状态..."
	@chmod +x ./scripts/check_rocksdb.sh
	@./scripts/check_rocksdb.sh
