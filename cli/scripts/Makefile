# Moox CLI Makefile

.PHONY: help build clean dev run install uninstall deps check lint test cross-build

# 默认目标
all: deps check build

# 变量定义
APP_NAME=moox-cli
VERSION ?= dev
BUILD_DIR=../release
SCRIPT_DIR=$(shell pwd)
PROJECT_ROOT=$(shell cd .. && pwd)

# 帮助信息
help:
	@echo "Moox CLI 构建工具"
	@echo ""
	@echo "可用目标:"
	@echo "  help        - 显示此帮助信息"
	@echo "  deps        - 安装Go依赖"
	@echo "  check       - 代码检查(lint + vet)"
	@echo "  lint        - 运行golangci-lint"
	@echo "  test        - 运行测试"
	@echo "  build       - 构建当前平台二进制程序 (默认版本: dev, 包含配置文件)"
	@echo "  cross-build - 跨平台构建所有目标平台 (包含配置文件)"
	@echo "  clean       - 清理构建文件"
	@echo "  dev         - 开发模式运行"
	@echo "  run         - 在构建目录运行CLI工具"
	@echo "  install     - 系统安装(需要sudo权限)"
	@echo "  uninstall   - 系统卸载(需要sudo权限)"
	@echo ""
	@echo "使用示例:"
	@echo "  make build VERSION=v1.0.0        - 构建指定版本"
	@echo "  make cross-build VERSION=v1.0.0  - 跨平台构建指定版本"
	@echo "  sudo make install               - 系统安装"

# 安装依赖
deps:
	@echo "正在安装Go依赖..."
	cd .. && go mod download && go mod tidy

# 代码检查
check: lint vet

# Lint检查
lint:
	@echo "正在运行代码检查..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		cd .. && golangci-lint run ./...; \
	else \
		echo "警告: golangci-lint 未安装，跳过lint检查"; \
		echo "安装命令: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Go vet检查
vet:
	@echo "正在运行go vet..."
	cd .. && go vet ./...

# 运行测试
test:
	@echo "正在运行测试..."
	cd .. && go test -v ./...

# 构建项目（当前平台）
build:
	@echo "正在构建 $(APP_NAME) 版本 $(VERSION) (当前平台)..."
	cd .. && \
	mkdir -p release/bin && \
	CGO_ENABLED=1 go build \
		-ldflags "-X 'main.Version=$(VERSION)' -X 'main.BuildTime=$$(date '+%Y-%m-%d %H:%M:%S')' -X 'main.GitCommit=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)'" \
		-o release/bin/$(APP_NAME) .
	@echo "拷贝配置文件..."
	cd .. && \
	cp config/cli-example.yaml release/bin/cli-example.yaml && \
	if [ -f "config/cli.yaml" ]; then \
		cp config/cli.yaml release/bin/cli.yaml; \
	fi
	@echo "构建完成: $(BUILD_DIR)/bin/$(APP_NAME)"
	@echo "配置文件已拷贝到: $(BUILD_DIR)/bin/"

# 跨平台构建
cross-build:
	@echo "正在跨平台构建 $(APP_NAME) 版本 $(VERSION)..."
	cd .. && scripts/build.sh $(VERSION)

# 清理构建文件
clean:
	@echo "正在清理构建文件..."
	rm -rf $(BUILD_DIR)
	cd .. && go clean -cache

# 开发模式运行（本地直接运行）
dev:
	@echo "开发模式启动..."
	cd .. && CGO_ENABLED=1 go run .

# 在构建目录运行CLI工具
run:
	@if [ -f "$(BUILD_DIR)/bin/$(APP_NAME)" ]; then \
		echo "运行 $(APP_NAME)..."; \
		cd $(BUILD_DIR) && ./bin/$(APP_NAME) --help; \
	else \
		echo "错误: 工具未构建，请先运行 'make build' 或 'make cross-build'"; \
		exit 1; \
	fi

# 系统安装
install:
	@if [ -f "$(BUILD_DIR)/install.sh" ]; then \
		echo "安装 $(APP_NAME) 到系统..."; \
		cd $(BUILD_DIR) && sudo ./install.sh; \
	else \
		echo "错误: 安装脚本不存在，请先运行 'make cross-build'"; \
		exit 1; \
	fi

# 系统卸载
uninstall:
	@if [ -f "$(BUILD_DIR)/uninstall.sh" ]; then \
		echo "从系统卸载 $(APP_NAME)..."; \
		cd $(BUILD_DIR) && sudo ./uninstall.sh; \
	else \
		echo "错误: 卸载脚本不存在"; \
		exit 1; \
	fi

# 显示构建信息
info:
	@echo "构建信息:"
	@echo "  APP_NAME: $(APP_NAME)"
	@echo "  VERSION: $(VERSION)"
	@echo "  BUILD_DIR: $(BUILD_DIR)"
	@echo "  SCRIPT_DIR: $(SCRIPT_DIR)"
	@echo "  PROJECT_ROOT: $(PROJECT_ROOT)"
	@echo "  GO_VERSION: $$(go version)"
	@echo "  GIT_COMMIT: $$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

# 发布准备
release: deps check cross-build
	@echo "发布准备完成！"
	@echo "构建目录: $(BUILD_DIR)"
	@echo "版本: $(VERSION)"
	@if [ -f "$(BUILD_DIR)/README.md" ]; then \
		echo "查看使用说明: $(BUILD_DIR)/README.md"; \
	fi

# 检查环境
env:
	@echo "检查开发环境..."
	@echo "Go版本: $$(go version)"
	@echo "Git版本: $$(git --version 2>/dev/null || echo 'Git未安装')"
	@echo "CGO支持: $$(go env CGO_ENABLED)"
	@echo "操作系统: $$(go env GOOS)"
	@echo "架构: $$(go env GOARCH)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint: $$(golangci-lint version)"; \
	else \
		echo "golangci-lint: 未安装"; \
	fi

# 开发工具设置
setup:
	@echo "设置开发工具..."
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "安装 golangci-lint..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	@echo "开发工具设置完成" 
