# Moox CLI Makefile

.PHONY: help build build-linux build-darwin build-windows build-all clean deps lint test install release

# Default target
all: deps build

# Variables
APP_NAME=moox-cli
VERSION ?= dev
BUILD_TIME=$(shell date +"%Y-%m-%d_%H:%M:%S")
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DIR=./bin
RELEASE_DIR=./release
CONFIG_DIR=./config
SQL_DIR=./sql

# Build flags
LDFLAGS=-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)
# 不使用CGO
CGO_FLAGS=CGO_ENABLED=0

# Platform specific variables
PLATFORMS=linux darwin windows
ARCHITECTURES=amd64 arm64

# Help
help:
	@echo "🛠️  Moox CLI 构建工具"
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
	@echo "  make deploy SERVER=user@host     - 自动检测平台并部署"

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
	$(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) .
	@echo "✅ 构建完成: $(BUILD_DIR)/$(APP_NAME)"

# Build for Linux
build-linux:
	@echo "📦 正在构建 Linux 平台二进制文件..."
	@mkdir -p $(RELEASE_DIR)/linux/bin
	@mkdir -p $(RELEASE_DIR)/linux/config
	@mkdir -p $(RELEASE_DIR)/linux/sql
	@mkdir -p $(RELEASE_DIR)/linux/data
	@for arch in $(ARCHITECTURES); do \
		echo "  平台: Linux"; \
		echo "  架构: $$arch"; \
		echo "  CGO: 已禁用"; \
		GOOS=linux GOARCH=$$arch $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/linux/bin/$(APP_NAME)-$$arch .; \
		if [ "$$arch" = "amd64" ]; then \
			cp $(RELEASE_DIR)/linux/bin/$(APP_NAME)-$$arch $(RELEASE_DIR)/linux/bin/$(APP_NAME); \
		fi; \
	done
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/linux/config/ 2>/dev/null || true
	@cp -r $(SQL_DIR)/* $(RELEASE_DIR)/linux/sql/ 2>/dev/null || true
	@echo "✅ Linux 构建成功"

# Build for macOS
build-darwin:
	@echo "📦 正在构建 macOS 平台二进制文件..."
	@mkdir -p $(RELEASE_DIR)/darwin/bin
	@mkdir -p $(RELEASE_DIR)/darwin/config
	@mkdir -p $(RELEASE_DIR)/darwin/sql
	@mkdir -p $(RELEASE_DIR)/darwin/data
	@for arch in $(ARCHITECTURES); do \
		echo "  平台: Darwin"; \
		echo "  架构: $$arch"; \
		echo "  CGO: 已禁用"; \
		GOOS=darwin GOARCH=$$arch $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/darwin/bin/$(APP_NAME)-$$arch .; \
		if [ "$$arch" = "amd64" ]; then \
			cp $(RELEASE_DIR)/darwin/bin/$(APP_NAME)-$$arch $(RELEASE_DIR)/darwin/bin/$(APP_NAME); \
		fi; \
	done
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/darwin/config/ 2>/dev/null || true
	@cp -r $(SQL_DIR)/* $(RELEASE_DIR)/darwin/sql/ 2>/dev/null || true
	@echo "✅ macOS 构建成功"

# Build for macOS (alias for build-darwin)
build-macos: build-darwin

# Build for Windows
build-windows:
	@echo "📦 正在构建 Windows 平台二进制文件..."
	@mkdir -p $(RELEASE_DIR)/windows/bin
	@mkdir -p $(RELEASE_DIR)/windows/config
	@mkdir -p $(RELEASE_DIR)/windows/sql
	@mkdir -p $(RELEASE_DIR)/windows/data
	@for arch in $(ARCHITECTURES); do \
		echo "  平台: Windows"; \
		echo "  架构: $$arch"; \
		echo "  CGO: 已禁用"; \
		GOOS=windows GOARCH=$$arch $(CGO_FLAGS) go build -ldflags "$(LDFLAGS)" \
			-o $(RELEASE_DIR)/windows/bin/$(APP_NAME)-$$arch.exe .; \
		if [ "$$arch" = "amd64" ]; then \
			cp $(RELEASE_DIR)/windows/bin/$(APP_NAME)-$$arch.exe $(RELEASE_DIR)/windows/bin/$(APP_NAME).exe; \
		fi; \
	done
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/windows/config/ 2>/dev/null || true
	@cp -r $(SQL_DIR)/* $(RELEASE_DIR)/windows/sql/ 2>/dev/null || true
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

# Deploy to remote server
deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "❌ 请指定服务器地址"; \
		echo "使用方法: make deploy SERVER=user@host [PLATFORM=linux|darwin]"; \
		exit 1; \
	fi
	@./scripts/deploy.sh $(SERVER) $(PLATFORM)

# Clean build artifacts
clean:
	@echo "🧹 正在清理构建文件..."
	rm -rf $(BUILD_DIR)
	rm -rf $(RELEASE_DIR)
	go clean -cache

# Development shortcuts
dev:
	@echo "🚀 开发模式启动..."
	$(CGO_FLAGS) go run .

# Install to local bin
install: build
	@echo "📦 安装到本地..."
	@mkdir -p ~/bin
	@cp $(BUILD_DIR)/$(APP_NAME) ~/bin/
	@echo "✅ 已安装到 ~/bin/$(APP_NAME)"