# MooX Server Makefile

.PHONY: help build build-linux build-darwin build-windows build-all clean deps lint test install release

# Default target
all: deps build

# Variables
APP_NAME=moox-admin
VERSION ?= dev
BUILD_TIME=$(shell date +"%Y-%m-%d_%H:%M:%S")
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DIR=./bin
RELEASE_DIR=./release
CMD_DIR=./cmd
CONFIG_DIR=./config

# Build flags
LDFLAGS=-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)
# 不使用CGO
CGO_FLAGS=CGO_ENABLED=0

# Platform specific variables
PLATFORMS=linux darwin windows
ARCHITECTURES=amd64 arm64

# Help
help:
	@echo "🛠️  MooX Server 构建工具"
	@echo ""
	@echo "📦 构建目标:"
	@echo "  build         - 构建当前平台二进制文件"
	@echo "  build-linux   - 构建 Linux 平台二进制文件"
	@echo "  build-darwin  - 构建 macOS 平台二进制文件"
	@echo "  build-macos   - 构建 macOS 平台二进制文件 (同 build-darwin)"
	@echo "  build-windows - 构建 Windows 平台二进制文件"
	@echo "  build-all     - 构建所有平台二进制文件"
	@echo "  release       - 创建发布包"
	@echo "  deploy        - 部署到远程服务器"
	@echo "  clean         - 清理构建文件"
	@echo ""
	@echo "🔧 开发工具:"
	@echo "  deps          - 安装Go依赖"
	@echo "  lint          - 代码检查"
	@echo "  test          - 运行测试"
	@echo ""
	@echo "💡 使用示例:"
	@echo "  make build VERSION=v1.0.0        - 构建当前平台版本"
	@echo "  make build-linux VERSION=v1.0.0  - 构建Linux版本"
	@echo "  make build-all VERSION=v1.0.0    - 构建所有平台版本"
	@echo "  make release VERSION=v1.0.0      - 创建发布包"
	@echo "  make deploy SERVER=user@host [platform] - 部署到远程服务器，可选平台参数"

# Install dependencies
deps:
	@echo "📋 正在安装Go依赖..."
	go mod download
	go mod tidy

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
	go test -v ./...

# Build for current platform
build:
	@echo "📦 正在构建 $(APP_NAME) (当前平台)..."
	@mkdir -p $(BUILD_DIR)
	@if $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/moox-admin; then \
		echo "✅ 构建完成: $(BUILD_DIR)/$(APP_NAME)"; \
	else \
		echo "❌ 构建失败"; \
		exit 1; \
	fi

# Build for Linux
build-linux:
	@echo "📦 正在构建 Linux 平台二进制文件..."
	@mkdir -p $(RELEASE_DIR)/linux/bin
	@mkdir -p $(RELEASE_DIR)/linux/config
	@mkdir -p $(RELEASE_DIR)/linux/log
	@mkdir -p $(RELEASE_DIR)/linux/data
	@BUILD_FAILED=false; \
	for arch in $(ARCHITECTURES); do \
		echo "  平台: Linux"; \
		echo "  架构: $$arch"; \
		echo "  CGO: 已禁用"; \
		if ! GOOS=linux GOARCH=$$arch $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/linux/bin/$(APP_NAME)-$$arch ./cmd/moox-admin; then \
			echo "❌ Linux $$arch 构建失败"; \
			BUILD_FAILED=true; \
		else \
			if [ "$$arch" = "amd64" ]; then \
				cp $(RELEASE_DIR)/linux/bin/$(APP_NAME)-$$arch $(RELEASE_DIR)/linux/bin/$(APP_NAME); \
			fi; \
		fi; \
	done; \
	if [ "$$BUILD_FAILED" = "true" ]; then \
		echo "❌ Linux 构建失败"; \
		exit 1; \
	fi
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/linux/config/ 2>/dev/null || true
	@$(MAKE) create-unix-scripts PLATFORM_DIR=$(RELEASE_DIR)/linux
	@echo "✅ Linux 构建成功"

# Build for macOS
build-darwin:
	@echo "📦 正在构建 macOS 平台二进制文件..."
	@mkdir -p $(RELEASE_DIR)/darwin/bin
	@mkdir -p $(RELEASE_DIR)/darwin/config
	@mkdir -p $(RELEASE_DIR)/darwin/log
	@mkdir -p $(RELEASE_DIR)/darwin/data
	@BUILD_FAILED=false; \
	for arch in $(ARCHITECTURES); do \
		echo "  平台: Darwin"; \
		echo "  架构: $$arch"; \
		echo "  CGO: 已禁用"; \
		if ! GOOS=darwin GOARCH=$$arch $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/darwin/bin/$(APP_NAME)-$$arch ./cmd/moox-admin; then \
			echo "❌ macOS $$arch 构建失败"; \
			BUILD_FAILED=true; \
		else \
			if [ "$$arch" = "amd64" ]; then \
				cp $(RELEASE_DIR)/darwin/bin/$(APP_NAME)-$$arch $(RELEASE_DIR)/darwin/bin/$(APP_NAME); \
			fi; \
		fi; \
	done; \
	if [ "$$BUILD_FAILED" = "true" ]; then \
		echo "❌ macOS 构建失败"; \
		exit 1; \
	fi
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/darwin/config/ 2>/dev/null || true
	@$(MAKE) create-unix-scripts PLATFORM_DIR=$(RELEASE_DIR)/darwin
	@echo "✅ macOS 构建成功"

# Build for macOS (alias for build-darwin)
build-macos: build-darwin

# Build for Windows
build-windows:
	@echo "📦 正在构建 Windows 平台二进制文件..."
	@mkdir -p $(RELEASE_DIR)/windows/bin
	@mkdir -p $(RELEASE_DIR)/windows/config
	@mkdir -p $(RELEASE_DIR)/windows/log
	@mkdir -p $(RELEASE_DIR)/windows/data
	@BUILD_FAILED=false; \
	for arch in $(ARCHITECTURES); do \
		echo "  平台: Windows"; \
		echo "  架构: $$arch"; \
		echo "  CGO: 已禁用"; \
		if ! GOOS=windows GOARCH=$$arch $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/windows/bin/$(APP_NAME)-$$arch.exe ./cmd/moox-admin; then \
			echo "❌ Windows $$arch 构建失败"; \
			BUILD_FAILED=true; \
		else \
			if [ "$$arch" = "amd64" ]; then \
				cp $(RELEASE_DIR)/windows/bin/$(APP_NAME)-$$arch.exe $(RELEASE_DIR)/windows/bin/$(APP_NAME).exe; \
			fi; \
		fi; \
	done; \
	if [ "$$BUILD_FAILED" = "true" ]; then \
		echo "❌ Windows 构建失败"; \
		exit 1; \
	fi
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/windows/config/ 2>/dev/null || true
	@$(MAKE) create-windows-scripts PLATFORM_DIR=$(RELEASE_DIR)/windows
	@echo "✅ Windows 构建成功"

# Build for all platforms
build-all: clean
	@echo "📦 构建所有平台..."
	@$(MAKE) build-linux
	@$(MAKE) build-darwin
	@$(MAKE) build-windows
	@echo "✅ 所有平台构建完成"

# Create release package
release: build-all
	@echo "📦 创建发布包..."
	@echo "📁 发布目录: $(RELEASE_DIR)"
	@ls -la $(RELEASE_DIR)/

# Create Unix scripts (Linux/macOS)
create-unix-scripts:
	@echo "#!/bin/bash" > $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "APP_NAME=\"moox-admin\"" >> $(PLATFORM_DIR)/start.sh
	@echo "PID_FILE=\"./\$$APP_NAME.pid\"" >> $(PLATFORM_DIR)/start.sh
	@echo "SCRIPT_DIR=\$$(cd \"\$$(dirname \"\$$0\")\" && pwd)" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"检查已存在的进程...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "# 先检查是否有同名进程在运行" >> $(PLATFORM_DIR)/start.sh
	@echo "EXISTING_PIDS=\$$(pgrep -f \"bin/\$$APP_NAME\")" >> $(PLATFORM_DIR)/start.sh
	@echo "if [ -n \"\$$EXISTING_PIDS\" ]; then" >> $(PLATFORM_DIR)/start.sh
	@echo "    echo \"发现运行中的 \$$APP_NAME 进程: \$$EXISTING_PIDS\"" >> $(PLATFORM_DIR)/start.sh
	@echo "    echo \"正在停止这些进程...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "    kill \$$EXISTING_PIDS 2>/dev/null" >> $(PLATFORM_DIR)/start.sh
	@echo "    sleep 2" >> $(PLATFORM_DIR)/start.sh
	@echo "    # 如果进程仍然存在，强制终止" >> $(PLATFORM_DIR)/start.sh
	@echo "    if pgrep -f \"bin/\$$APP_NAME\" > /dev/null; then" >> $(PLATFORM_DIR)/start.sh
	@echo "        echo \"强制终止进程...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "        pkill -9 -f \"bin/\$$APP_NAME\"" >> $(PLATFORM_DIR)/start.sh
	@echo "    fi" >> $(PLATFORM_DIR)/start.sh
	@echo "fi" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "# 再检查 PID 文件" >> $(PLATFORM_DIR)/start.sh
	@echo "if [ -f \"\$$PID_FILE\" ]; then" >> $(PLATFORM_DIR)/start.sh
	@echo "    OLD_PID=\$$(cat \"\$$PID_FILE\")" >> $(PLATFORM_DIR)/start.sh
	@echo "    if ps -p \"\$$OLD_PID\" > /dev/null 2>&1; then" >> $(PLATFORM_DIR)/start.sh
	@echo "        echo \"发现 PID 文件中的进程 (PID: \$$OLD_PID)，正在停止...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "        kill \"\$$OLD_PID\"" >> $(PLATFORM_DIR)/start.sh
	@echo "        sleep 2" >> $(PLATFORM_DIR)/start.sh
	@echo "    fi" >> $(PLATFORM_DIR)/start.sh
	@echo "    rm -f \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/start.sh
	@echo "fi" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "cd \"\$$SCRIPT_DIR\"" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "# 确保log目录存在" >> $(PLATFORM_DIR)/start.sh
	@echo "mkdir -p log" >> $(PLATFORM_DIR)/start.sh
	@echo "" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"启动 \$$APP_NAME...\"" >> $(PLATFORM_DIR)/start.sh
	@echo "nohup ./bin/\$$APP_NAME -conf=./config/trpc_go.yaml > ./log/app.log 2>&1 &" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \$$! > \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"服务已启动 (PID: \$$(cat \$$PID_FILE))\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"日志文件: log/app.log\"" >> $(PLATFORM_DIR)/start.sh
	@echo "#!/bin/bash" > $(PLATFORM_DIR)/stop.sh
	@echo "" >> $(PLATFORM_DIR)/stop.sh
	@echo "APP_NAME=\"moox-admin\"" >> $(PLATFORM_DIR)/stop.sh
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
	@echo 'cd bin' >> $(PLATFORM_DIR)/start.bat
	@echo 'start moox-admin.exe' >> $(PLATFORM_DIR)/start.bat
	@echo 'echo Service started' >> $(PLATFORM_DIR)/start.bat
	@echo '@echo off' > $(PLATFORM_DIR)/stop.bat
	@echo 'taskkill /F /IM moox-admin.exe' >> $(PLATFORM_DIR)/stop.bat
	@echo 'echo Service stopped' >> $(PLATFORM_DIR)/stop.bat

# Deploy to remote server
# SERVER 可省略：未指定时从 infra/infra.local.yaml 读取 remote.ssh（见 scripts/infra-env.sh）。
deploy:
	@SERVER="$(SERVER)"; \
	if [ -z "$$SERVER" ]; then \
		if [ -f ../../scripts/infra-env.sh ]; then \
			. ../../scripts/infra-env.sh 2>/dev/null; \
			SERVER="$$REMOTE_SSH"; \
		fi; \
	fi; \
	if [ -z "$$SERVER" ]; then \
		echo "❌ 请指定服务器地址，或在 infra/infra.local.yaml 配置 remote.ssh"; \
		echo "使用方法: make deploy SERVER=user@host [platform]"; \
		echo "          平台参数可选: linux|darwin"; \
		echo "示例:"; \
		echo "  make deploy SERVER=ubuntu@<deploy-host>         # 自动检测平台"; \
		echo "  make deploy SERVER=ubuntu@<deploy-host> linux   # 指定Linux平台"; \
		echo "  make deploy                                    # 用 infra.local.yaml 的 remote.ssh"; \
		exit 1; \
	fi; \
	if [ ! -f "./scripts/deploy.sh" ]; then \
		echo "❌ 部署脚本 ./scripts/deploy.sh 不存在"; \
		exit 1; \
	fi; \
	if [ -n "$(filter linux darwin,$(word 2,$(MAKECMDGOALS)))" ]; then \
		echo "📦 使用指定平台: $(word 2,$(MAKECMDGOALS))"; \
		./scripts/deploy.sh $$SERVER $(word 2,$(MAKECMDGOALS)); \
	else \
		echo "📦 自动检测平台"; \
		./scripts/deploy.sh $$SERVER; \
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
	$(CGO_FLAGS) go run ./cmd/moox-admin

# Install to local bin
install: build
	@echo "📦 安装到本地..."
	@mkdir -p ~/bin
	@cp $(BUILD_DIR)/$(APP_NAME) ~/bin/
	@echo "✅ 已安装到 ~/bin/$(APP_NAME)"
