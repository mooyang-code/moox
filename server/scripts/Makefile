# Moox Server Makefile

.PHONY: help proto build build-all clean dev run stop install deps check lint test
.PHONY: build-cmd init-db drop-db migrate-db cmd-help clean-db dev-db

# 默认目标
all: deps check build-all

# 变量定义
APP_NAME=moox-server
DB_TOOL_NAME=moox-db-tool
VERSION ?= dev
BUILD_DIR=../release
BIN_DIR=../release/bin
PROTO_DIR=../proto
CMD_DIR=../cmd
SQL_DIR=../sql

# 帮助信息
help:
	@echo "🛠️  Moox Server 构建工具"
	@echo ""
	@echo "📦 构建目标:"
	@echo "  build      - 构建服务器二进制程序"
	@echo "  build-cmd  - 构建数据库管理工具"
	@echo "  build-all  - 构建所有二进制程序 (服务器 + 数据库工具)"
	@echo "  clean      - 清理所有构建文件"
	@echo ""
	@echo "🗄️  数据库管理:"
	@echo "  init-db    - 初始化数据库"
	@echo "  drop-db    - 删除数据库"
	@echo "  migrate-db - 迁移数据库"
	@echo "  clean-db   - 清理数据库文件"
	@echo "  cmd-help   - 显示数据库工具帮助"
	@echo "  dev-db     - 开发模式（清理并重新初始化数据库）"
	@echo ""
	@echo "🔧 开发工具:"
	@echo "  deps       - 安装Go依赖"
	@echo "  proto      - 生成protobuf代码"
	@echo "  check      - 代码检查(lint + vet)"
	@echo "  test       - 运行测试"
	@echo "  dev        - 开发模式运行服务器"
	@echo "  run        - 在构建目录运行服务"
	@echo "  stop       - 停止运行的服务"
	@echo "  install    - 完整构建并安装到release目录"
	@echo ""
	@echo "📁 目录结构:"
	@echo "  $(BUILD_DIR)/bin/      - 二进制文件"
	@echo "  $(BUILD_DIR)/sql/      - SQL文件"
	@echo "  $(BUILD_DIR)/data/     - 数据库文件"
	@echo ""
	@echo "💡 使用示例:"
	@echo "  make build-all VERSION=v1.0.0  - 构建指定版本"
	@echo "  make install VERSION=v1.0.0    - 安装指定版本到release目录"
	@echo "  make dev-db                     - 快速设置开发数据库"

# 安装依赖
deps:
	@echo "📋 正在安装Go依赖..."
	cd .. && go mod download && go mod tidy

# 生成protobuf代码
proto:
	@echo "🔧 正在生成protobuf代码..."
	cd $(PROTO_DIR) && $(MAKE) all

# 代码检查
check: lint vet

# Lint检查
lint:
	@echo "🔍 正在运行代码检查..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		cd .. && golangci-lint run ./...; \
	else \
		echo "⚠️  警告: golangci-lint 未安装，跳过lint检查"; \
		echo "安装命令: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Go vet检查
vet:
	@echo "🔍 正在运行go vet..."
	cd .. && go vet ./...

# 运行测试
test:
	@echo "🧪 正在运行测试..."
	cd .. && go test -v ./...

# 构建服务器
build:
	@echo "📦 正在构建 $(APP_NAME) 版本 $(VERSION)..."
	cd .. && scripts/build.sh $(VERSION)

# 构建数据库工具
build-cmd:
	@echo "📦 正在编译 $(DB_TOOL_NAME)..."
	@mkdir -p $(BIN_DIR)
	@mkdir -p $(BUILD_DIR)/sql
	@mkdir -p $(BUILD_DIR)/data
	@cd $(CMD_DIR) && CGO_ENABLED=1 go build -o $(BIN_DIR)/$(DB_TOOL_NAME) *.go
	@cp -r $(SQL_DIR)/* $(BUILD_DIR)/sql/
	@echo "✅ 编译完成: $(BIN_DIR)/$(DB_TOOL_NAME)"
	@echo "✅ SQL文件已复制到: $(BUILD_DIR)/sql/"

# 构建所有程序
build-all: build build-cmd
	@echo "🎉 所有程序构建完成！"
	@echo "   服务器: $(BIN_DIR)/$(APP_NAME)"
	@echo "   数据库工具: $(BIN_DIR)/$(DB_TOOL_NAME)"
	@echo "   SQL文件: $(BUILD_DIR)/sql/"

# 清理构建文件
clean:
	@echo "🧹 正在清理构建文件..."
	rm -rf $(BUILD_DIR)
	cd .. && go clean -cache

# 清理数据库文件
clean-db:
	@echo "🧹 清理数据库文件..."
	@rm -rf $(BUILD_DIR)/data
	@echo "✅ 数据库文件清理完成"

# 初始化数据库
init-db: build-cmd
	@echo "🚀 初始化数据库..."
	@cd $(BUILD_DIR) && ./bin/$(DB_TOOL_NAME) -init -sql=./sql

# 删除数据库
drop-db: build-cmd
	@echo "⚠️  删除数据库..."
	@cd $(BUILD_DIR) && ./bin/$(DB_TOOL_NAME) -drop

# 迁移数据库
migrate-db: build-cmd
	@echo "🔄 迁移数据库..."
	@cd $(BUILD_DIR) && ./bin/$(DB_TOOL_NAME) -migrate -sql=./sql

# 显示数据库工具帮助
cmd-help: build-cmd
	@cd $(BUILD_DIR) && ./bin/$(DB_TOOL_NAME) -help

# 开发模式数据库设置
dev-db: clean-db init-db
	@echo "🎯 开发数据库环境准备完成"

# 开发模式运行（本地直接运行）
dev:
	@echo "🚀 开发模式启动..."
	cd .. && CGO_ENABLED=1 go run . -conf=./config/trpc_go.yaml

# 在构建目录运行服务
run:
	@if [ -f "$(BUILD_DIR)/start.sh" ]; then \
		echo "🚀 启动服务..."; \
		cd $(BUILD_DIR) && ./start.sh; \
	else \
		echo "❌ 错误: 服务未构建，请先运行 'make build' 或 'make install'"; \
		exit 1; \
	fi

# 停止服务
stop:
	@if [ -f "$(BUILD_DIR)/stop.sh" ]; then \
		echo "🛑 停止服务..."; \
		cd $(BUILD_DIR) && ./stop.sh; \
	else \
		echo "⚠️  警告: 服务控制脚本不存在"; \
	fi

# 完整安装（构建 + 测试）
install: deps proto check build-all
	@echo "🎉 安装完成！"
	@echo "📁 构建目录: $(BUILD_DIR)"
	@echo "🚀 启动命令: make run"
	@echo "🛑 停止命令: make stop"
	@echo "🗄️  数据库管理: make init-db / make migrate-db / make drop-db" 