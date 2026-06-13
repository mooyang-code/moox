# Data Collector Makefile

.PHONY: help proto build build-all clean dev run stop install deps check lint test
.PHONY: build-collector init-data clean-data dev-data release docker docker-push coverage
.PHONY: demo-collector example-kline example-symbols
.PHONY: test-collector test-storage test-services test-infra perf-test integration-test test-all
.PHONY: fmt tidy bench build-scf run-serverless deploy

# 默认目标
all: deps check build-all

# 变量定义
APP_NAME := data-collector
COLLECTOR_NAME := data-collector
SYMTOOL_NAME := symtool
KLINEDUMP_NAME := klinedump
TRPC_SERVER_NAME := trpc-server
TRPC_CLIENT_NAME := trpc-client
VERSION ?= dev
BUILD_DIR := release
BIN_DIR := release/bin
PROTO_DIR := proto
CONFIGS_DIR := configs
DATA_DIR := data
LOG_DIR := log

# 构建信息
BUILD_TIME := $(shell date +"%Y-%m-%d %H:%M:%S")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GO_VERSION := $(shell go version | awk '{print $$3}')

# 构建标志
LDFLAGS := -X 'main.AppVersion=$(VERSION)' \
           -X 'main.BuildTime=$(BUILD_TIME)' \
           -X 'main.GitCommit=$(GIT_COMMIT)' \
           -X 'main.GoVersion=$(GO_VERSION)'
GO_BUILD_FLAGS := -ldflags "$(LDFLAGS)" -trimpath

# 平台变量
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

# 帮助信息
help:
	@echo "🛠️  Data Collector 构建工具"
	@echo ""
	@echo "📦 构建目标:"
	@echo "  build-collector    - 构建数据采集器"
	@echo "  build-all          - 构建所有程序（现在只有主程序）"
	@echo "  build              - build-all 的别名"
	@echo "  build-scf <版本号> - 构建腾讯云函数版本（需指定版本号，格式：vx.x.x）"
	@echo "  clean              - 清理所有构建文件"
	@echo ""
	@echo "🗄️  数据管理:"
	@echo "  init-data          - 初始化数据目录"
	@echo "  clean-data         - 清理数据文件"
	@echo "  dev-data           - 开发模式（清理并重新初始化数据）"
	@echo ""
	@echo "🔧 开发工具:"
	@echo "  deps               - 安装Go依赖"
	@echo "  proto              - 生成protobuf代码"
	@echo "  check              - 代码检查(lint + vet)"
	@echo "  test               - 运行测试"
	@echo "  coverage           - 生成测试覆盖率报告"
	@echo "  dev                - 开发模式运行采集器"
	@echo "  run                - 在构建目录运行服务"
	@echo "  run-serverless     - 本地运行云函数模式"
	@echo "  stop               - 停止运行的服务"
	@echo "  install            - 完整构建并安装到release目录"
	@echo "  deploy             - 部署到远程服务器"
	@echo ""
	@echo "📁 目录结构:"
	@echo "  $(BUILD_DIR)/bin/      - 二进制文件"
	@echo "  $(BUILD_DIR)/configs/  - 配置文件"
	@echo "  $(BUILD_DIR)/data/     - 数据文件"
	@echo "  $(BUILD_DIR)/log/      - 日志文件"
	@echo ""
	@echo "💡 使用示例:"
	@echo "  make build-all VERSION=v1.0.0  - 构建指定版本"
	@echo "  make install VERSION=v1.0.0    - 安装指定版本到release目录"
	@echo "  make dev-data                   - 快速设置开发环境"
	@echo "  make build-scf v0.0.1           - 构建云函数包（必须指定版本号 vx.x.x）"
	@echo "  make deploy SERVER=ubuntu@143.177.177.177  - 部署到远程服务器"

# 安装依赖
deps:
	@echo "📋 正在安装Go依赖..."
	go mod download && go mod tidy

# 生成protobuf代码
proto:
	@echo "🔧 正在生成protobuf代码..."
	@if [ -d "$(PROTO_DIR)" ]; then \
		cd $(PROTO_DIR) && find . -name "*.proto" -exec protoc --go_out=. --go-grpc_out=. {} \; ; \
	else \
		echo "⚠️  警告: proto目录不存在，跳过protobuf生成"; \
	fi

# 代码检查
check: lint vet

# Lint检查
lint:
	@echo "🔍 正在运行代码检查..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "⚠️  警告: golangci-lint 未安装，跳过lint检查"; \
		echo "安装命令: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Go vet检查
vet:
	@echo "🔍 正在运行go vet..."
	go vet ./...

# 运行测试
test:
	@echo "🧪 正在运行测试..."
	go test -v -race ./...

# 生成测试覆盖率报告
coverage:
	@echo "📊 正在生成测试覆盖率报告..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "✅ 覆盖率报告生成完成: coverage.html"

# 构建数据采集器
build-collector:
	@echo "📦 正在构建 $(COLLECTOR_NAME) 版本 $(VERSION)..."
	@mkdir -p $(BIN_DIR)
	go build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/$(COLLECTOR_NAME) ./cmd/standalone/main.go

# 构建所有程序（现在只有主程序）
build-all: build-collector
	@echo "🎉 程序构建完成！"
	@echo "   数据采集器: $(BIN_DIR)/$(COLLECTOR_NAME)"

# build 目标作为 build-all 的别名，保持向后兼容
build: build-all

# 清理构建文件
clean:
	@echo "🧹 正在清理构建文件..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	go clean -cache

# 清理数据文件
clean-data:
	@echo "🧹 清理数据文件..."
	@rm -rf $(BUILD_DIR)/$(DATA_DIR)
	@rm -rf $(BUILD_DIR)/$(LOG_DIR)
	@echo "✅ 数据文件清理完成"

# 初始化数据目录
init-data:
	@echo "🚀 初始化数据目录..."
	@mkdir -p $(BUILD_DIR)/$(DATA_DIR)
	@mkdir -p $(BUILD_DIR)/$(LOG_DIR)
	@echo "✅ 数据目录初始化完成"

# 开发模式数据设置
dev-data: clean-data init-data
	@echo "🎯 开发数据环境准备完成"

# 开发模式运行（本地直接运行）
dev:
	@echo "🚀 开发模式启动..."
	@if [ -f "$(CONFIGS_DIR)/config.yaml" ]; then \
		go run ./cmd/standalone/main.go --config=$(CONFIGS_DIR)/config.yaml; \
	else \
		go run ./cmd/standalone/main.go; \
	fi

# 在构建目录运行服务
run:
	@if [ -f "$(BUILD_DIR)/start.sh" ]; then \
		echo "🚀 启动服务..."; \
		cd $(BUILD_DIR) && ./start.sh; \
	else \
		echo "❌ 错误: 服务未构建，请先运行 'make build-all' 或 'make install'"; \
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
	@echo "📁 正在创建完整发布包..."
	@mkdir -p $(BUILD_DIR)/configs
	@mkdir -p $(BUILD_DIR)/$(DATA_DIR)
	@mkdir -p $(BUILD_DIR)/$(LOG_DIR)

	# 拷贝配置文件
	@if [ -d "$(CONFIGS_DIR)" ]; then \
		cp -r $(CONFIGS_DIR)/* $(BUILD_DIR)/configs/ 2>/dev/null || true; \
		echo "✅ 配置文件拷贝完成"; \
	fi

	# 拷贝配置模板
	@if [ -f "$(CONFIGS_DIR)/config.yaml" ]; then \
		cp $(CONFIGS_DIR)/config.yaml $(BUILD_DIR)/configs/config.yaml.example; \
		echo "✅ 配置模板拷贝完成"; \
	fi

	@echo "🎉 安装完成！"
	@echo "📁 构建目录: $(BUILD_DIR)"
	@echo "🚀 启动命令: make run"
	@echo "🛑 停止命令: make stop"

# 跨平台发布构建
release: clean deps check
	@echo "🚀 正在构建发布版本..."
	@mkdir -p release-dist
	@for platform in $(PLATFORMS); do \
		os=$$(echo $$platform | cut -d'/' -f1); \
		arch=$$(echo $$platform | cut -d'/' -f2); \
		echo "📦 构建 $$os/$$arch..."; \
		output_dir="release-dist/$(APP_NAME)-$(VERSION)-$$os-$$arch"; \
		mkdir -p $$output_dir/bin; \
		if [ "$$os" = "windows" ]; then \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $$output_dir/bin/$(COLLECTOR_NAME).exe ./cmd/standalone/main.go; \
		else \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $$output_dir/bin/$(COLLECTOR_NAME) ./cmd/standalone/main.go; \
		fi; \
		mkdir -p $$output_dir/configs $$output_dir/data $$output_dir/log; \
		if [ -d "$(CONFIGS_DIR)" ]; then cp -r $(CONFIGS_DIR)/* $$output_dir/configs/ 2>/dev/null || true; fi; \
		if [ -f "README.md" ]; then cp README.md $$output_dir/; fi; \
		cd release-dist && tar -czf $(APP_NAME)-$(VERSION)-$$os-$$arch.tar.gz $(APP_NAME)-$(VERSION)-$$os-$$arch; \
		cd ..; \
		echo "✅ $$os/$$arch 构建完成"; \
	done
	@echo "🎉 发布版本构建完成，输出目录: release-dist/"

# 构建Docker镜像
docker:
	@echo "🐳 正在构建Docker镜像..."
	docker build -t $(APP_NAME):$(VERSION) .
	docker tag $(APP_NAME):$(VERSION) $(APP_NAME):latest
	@echo "✅ Docker镜像构建完成"

# 推送Docker镜像
docker-push: docker
	@echo "🚀 正在推送Docker镜像..."
	docker push $(APP_NAME):$(VERSION)
	docker push $(APP_NAME):latest
	@echo "✅ Docker镜像推送完成"

# 运行代码生成
generate:
	@echo "🔧 正在运行代码生成..."
	go generate ./...

# 安装开发工具
install-tools:
	@echo "🔧 正在安装开发工具..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/golang/mock/mockgen@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@echo "✅ 开发工具安装完成"

# 初始化项目
init: install-tools deps init-data
	@echo "🚀 正在初始化项目..."
	@echo "✅ 项目初始化完成"

# 快速构建（跳过测试）
quick-build: clean deps build-all
	@echo "⚡ 快速构建完成"

# 格式化代码
fmt:
	@echo "🎨 正在格式化代码..."
	go fmt ./...

# 整理依赖
tidy:
	@echo "📋 正在整理依赖..."
	go mod tidy

# 运行基准测试
bench:
	@echo "🏃 正在运行基准测试..."
	go test -bench=. -benchmem ./...

# 运行示例程序
demo-collector:
	@echo "🎯 运行数据采集器演示..."
	go run cmd/demo/main.go

# TRPC 演示已移除，只保留主程序演示

# 运行示例代码
example-kline:
	@echo "🎯 运行K线采集器示例..."
	@if [ -d "examples/kline_collector" ]; then \
		go run ./examples/kline_collector/main.go; \
	else \
		echo "⚠️  警告: examples/kline_collector目录不存在"; \
	fi

example-symbols:
	@echo "🎯 运行交易对采集器示例..."
	@if [ -d "examples/symbols_collector" ]; then \
		go run ./examples/symbols_collector/main.go; \
	else \
		echo "⚠️  警告: examples/symbols_collector目录不存在"; \
	fi

# 模块化测试
test-core:
	@echo "🧪 测试核心框架模块..."
	go test -v ./internal/core/...

test-model:
	@echo "🧪 测试数据模型模块..."
	go test -v ./internal/model/...

test-source:
	@echo "🧪 测试数据源模块..."
	go test -v ./internal/source/...

test-storage:
	@echo "🧪 测试存储模块..."
	go test -v ./internal/storage/...

# 性能测试
perf-test:
	@echo "🏃 运行性能测试..."
	@if [ -d "test/perf" ]; then \
		go test -v ./test/perf/...; \
	else \
		echo "⚠️  警告: test/perf目录不存在"; \
	fi

# 集成测试
integration-test:
	@echo "🔗 运行集成测试..."
	@if [ -d "test/integration" ]; then \
		go test -v ./test/integration/...; \
	else \
		echo "⚠️  警告: test/integration目录不存在"; \
	fi

# 全面测试
test-all: test test-core test-model test-source test-storage perf-test integration-test
	@echo "✅ 所有测试完成"

# 云函数相关目标
build-scf:
	@# 检查是否提供了版本号参数
	@if [ -z "$(filter-out $@,$(MAKECMDGOALS))" ]; then \
		echo "❌ 错误: 请提供版本号参数"; \
		echo "使用方法: make build-scf v0.0.1"; \
		exit 1; \
	fi
	@# 获取版本号参数（第一个非目标参数）
	@SCF_VERSION="$(filter-out $@,$(MAKECMDGOALS))"; \
	echo "📝 检查版本号格式: $$SCF_VERSION"; \
	if ! echo "$$SCF_VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "❌ 错误: 版本号格式不正确"; \
		echo "正确格式: vx.x.x (例如: v0.0.1, v1.2.3)"; \
		exit 1; \
	fi; \
	echo "✅ 版本号格式校验通过: $$SCF_VERSION"; \
	echo "🔨 正在构建腾讯云函数版本..."; \
	GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o main ./cmd/serverless/main.go; \
	echo "📁 准备云函数配置文件..."; \
	mkdir -p scf-build; \
	cp -r configs/* scf-build/; \
	echo "📝 更新配置文件版本号..."; \
	if [ -f "scf-build/config.yaml" ]; then \
		sed -i.bak "s/version: \".*\"/version: \"$$SCF_VERSION\"/" scf-build/config.yaml; \
		rm -f scf-build/config.yaml.bak; \
		echo "✅ 配置文件版本号已更新为: $$SCF_VERSION"; \
	else \
		echo "⚠️  警告: config.yaml 文件不存在"; \
	fi; \
	sed -i.bak "s/version: \".*\"/version: \"$$SCF_VERSION\"/" configs/config.yaml; \
	rm -f configs/config.yaml.bak; \
	echo "✅ 源配置文件版本号已更新为: $$SCF_VERSION"; \
	cp main scf-build/; \
	echo "📦 打包云函数..."; \
	cd scf-build && zip -r ../collector-scf-$$SCF_VERSION.zip main *.yaml; \
	rm -rf scf-build; \
	rm -f main; \
	echo "✅ 云函数构建完成: collector-scf-$$SCF_VERSION.zip"

# 防止 Make 把版本号参数当作目标
%:
	@:

# 本地运行云函数模式
run-serverless:
	@echo "☁️  云函数模式启动..."
	@if [ -f "$(CONFIGS_DIR)/config.yaml" ]; then \
		go run ./cmd/serverless/main.go --config=$(CONFIGS_DIR)/config.yaml; \
	else \
		echo "❌ 错误: 云函数配置文件不存在: $(CONFIGS_DIR)/config.yaml"; \
		exit 1; \
	fi

# 部署到远程服务器
deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "❌ 请指定服务器地址"; \
		echo "使用方法: make deploy SERVER=ubuntu@143.177.177.177"; \
		exit 1; \
	fi
	@if [ ! -f "collector-scf.zip" ]; then \
		echo "❌ 错误: collector-scf.zip 文件不存在"; \
		echo "请先运行 'make build-scf' 构建云函数包"; \
		exit 1; \
	fi
	@echo "🚀 正在部署到远程服务器: $(SERVER)"
	@echo "📦 上传文件: collector-scf.zip"
	@scp collector-scf.zip $(SERVER):/tmp/
	@echo "✅ 部署完成: collector-scf.zip 已上传到 $(SERVER):/tmp/"

