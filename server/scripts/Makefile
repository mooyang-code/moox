# Moox Server Makefile

.PHONY: help proto build clean dev run stop install deps check lint test

# 默认目标
all: deps check build

# 变量定义
APP_NAME=moox-server
VERSION ?= dev
BUILD_DIR=../release
PROTO_DIR=../proto

# 帮助信息
help:
	@echo "Moox Server 构建工具"
	@echo ""
	@echo "可用目标:"
	@echo "  help     - 显示此帮助信息"
	@echo "  deps     - 安装Go依赖"
	@echo "  proto    - 生成protobuf代码"
	@echo "  check    - 代码检查(lint + vet)"
	@echo "  lint     - 运行golangci-lint"
	@echo "  test     - 运行测试"
	@echo "  build    - 构建二进制程序 (默认版本: dev)"
	@echo "  clean    - 清理构建文件"
	@echo "  dev      - 开发模式运行"
	@echo "  run      - 在构建目录运行服务"
	@echo "  stop     - 停止运行的服务"
	@echo "  install  - 完整构建并安装到bin目录"
	@echo ""
	@echo "使用示例:"
	@echo "  make build VERSION=v1.0.0  - 构建指定版本"
	@echo "  make install VERSION=v1.0.0 - 安装指定版本到bin目录"

# 安装依赖
deps:
	@echo "正在安装Go依赖..."
	cd .. && go mod download && go mod tidy

# 生成protobuf代码
proto:
	@echo "正在生成protobuf代码..."
	cd $(PROTO_DIR) && $(MAKE) all

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

# 构建项目
build:
	@echo "正在构建 $(APP_NAME) 版本 $(VERSION)..."
	cd .. && scripts/build.sh $(VERSION)

# 清理构建文件
clean:
	@echo "正在清理构建文件..."
	rm -rf $(BUILD_DIR)
	cd .. && go clean -cache && go clean -modcache

# 开发模式运行（本地直接运行）
dev:
	@echo "开发模式启动..."
	cd .. && CGO_ENABLED=1 go run . -conf=./config/trpc_go.yaml

# 在构建目录运行服务
run:
	@if [ -f "$(BUILD_DIR)/start.sh" ]; then \
		echo "启动服务..."; \
		cd $(BUILD_DIR) && ./start.sh; \
	else \
		echo "错误: 服务未构建，请先运行 'make build' 或 'make install'"; \
		exit 1; \
	fi

# 停止服务
stop:
	@if [ -f "$(BUILD_DIR)/stop.sh" ]; then \
		echo "停止服务..."; \
		cd $(BUILD_DIR) && ./stop.sh; \
	else \
		echo "警告: 服务控制脚本不存在"; \
	fi

# 完整安装（构建 + 测试）
install: deps proto check build
	@echo "安装完成！"
	@echo "构建目录: $(BUILD_DIR)"
	@echo "启动命令: make run"
	@echo "停止命令: make stop" 