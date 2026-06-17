# MooX Storage Makefile

.PHONY: help all deps proto lint test build build-linux build-darwin build-windows build-all release deploy clean dev install

all: deps proto build

APP_NAME ?= moox-storage
VERSION ?= dev
BUILD_TIME := $(shell date +"%Y-%m-%d_%H:%M:%S")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DIR := ./bin
RELEASE_DIR := ./release
PROTO_DIR := ./proto
CONFIG_DIR := ./config
SCHEMA_DIR := ./schema
LDFLAGS := -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)
CGO_ENABLED ?= 1

help:
	@echo "MooX Storage 构建工具"
	@echo ""
	@echo "目标:"
	@echo "  deps          安装 Go 依赖"
	@echo "  proto         生成 protobuf 代码"
	@echo "  test          运行单元测试和覆盖率检查"
	@echo "  build         构建当前平台二进制"
	@echo "  build-linux   构建 Linux amd64 二进制"
	@echo "  build-darwin  构建 macOS 当前架构二进制"
	@echo "  release       创建当前平台发布目录"
	@echo "  deploy        使用 scripts/deploy.sh 部署"
	@echo "  clean         清理构建产物"

deps:
	@echo "正在安装 Go 依赖..."
	go mod download
	go mod tidy

proto:
	@echo "正在生成 protobuf 代码..."
	cd $(PROTO_DIR) && $(MAKE) all

lint:
	@echo "正在运行代码检查..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint 未安装，跳过 lint"; \
	fi

test:
	@echo "正在运行测试..."
	CGO_ENABLED=$(CGO_ENABLED) go test -v -covermode=count -coverprofile=cover.out.tmp -coverpkg=./... '-gcflags=all=-N -l' ./...

build:
	@echo "正在构建 $(APP_NAME)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/moox-storage
	@echo "构建完成: $(BUILD_DIR)/$(APP_NAME)"

build-linux:
	@echo "正在构建 Linux amd64 二进制..."
	@mkdir -p $(RELEASE_DIR)/linux/bin $(RELEASE_DIR)/linux/config $(RELEASE_DIR)/linux/schema $(RELEASE_DIR)/linux/log $(RELEASE_DIR)/linux/database
	GOOS=linux GOARCH=amd64 CGO_ENABLED=$(CGO_ENABLED) go build -ldflags "$(LDFLAGS)" -o $(RELEASE_DIR)/linux/bin/$(APP_NAME) ./cmd/moox-storage
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/linux/config/ 2>/dev/null || true
	@cp -r $(SCHEMA_DIR)/* $(RELEASE_DIR)/linux/schema/ 2>/dev/null || true
	@$(MAKE) create-unix-scripts PLATFORM_DIR=$(RELEASE_DIR)/linux
	@echo "Linux 构建完成: $(RELEASE_DIR)/linux/bin/$(APP_NAME)"

build-darwin:
	@echo "正在构建 macOS 当前架构二进制..."
	@mkdir -p $(RELEASE_DIR)/darwin/bin $(RELEASE_DIR)/darwin/config $(RELEASE_DIR)/darwin/schema $(RELEASE_DIR)/darwin/log $(RELEASE_DIR)/darwin/database
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags "$(LDFLAGS)" -o $(RELEASE_DIR)/darwin/bin/$(APP_NAME) ./cmd/moox-storage
	@cp -r $(CONFIG_DIR)/* $(RELEASE_DIR)/darwin/config/ 2>/dev/null || true
	@cp -r $(SCHEMA_DIR)/* $(RELEASE_DIR)/darwin/schema/ 2>/dev/null || true
	@$(MAKE) create-unix-scripts PLATFORM_DIR=$(RELEASE_DIR)/darwin
	@echo "macOS 构建完成: $(RELEASE_DIR)/darwin/bin/$(APP_NAME)"

build-windows:
	@echo "Windows 构建需要在 Windows 或配置好 CGO 交叉工具链的环境中执行"
	@exit 1

build-all:
	@echo "请在对应目标平台执行 build-linux 或 build-darwin"
	@exit 1

release: clean
	@case "$$(uname -s)" in \
		Darwin) $(MAKE) build-darwin ;; \
		Linux) $(MAKE) build-linux ;; \
		*) echo "不支持的平台: $$(uname -s)"; exit 1 ;; \
	esac
	@echo "发布目录: $(RELEASE_DIR)"

create-unix-scripts:
	@echo "#!/usr/bin/env bash" > $(PLATFORM_DIR)/start.sh
	@echo "set -euo pipefail" >> $(PLATFORM_DIR)/start.sh
	@echo "APP_NAME=\"$(APP_NAME)\"" >> $(PLATFORM_DIR)/start.sh
	@echo "SCRIPT_DIR=\"\$$(cd \"\$$(dirname \"\$$0\")\" && pwd)\"" >> $(PLATFORM_DIR)/start.sh
	@echo "PID_FILE=\"\$$SCRIPT_DIR/\$$APP_NAME.pid\"" >> $(PLATFORM_DIR)/start.sh
	@echo "export STORAGE_CONFIG_PATH=\"\$$SCRIPT_DIR/config\"" >> $(PLATFORM_DIR)/start.sh
	@echo "mkdir -p \"\$$SCRIPT_DIR/log\" \"\$$SCRIPT_DIR/database\"" >> $(PLATFORM_DIR)/start.sh
	@echo "cd \"\$$SCRIPT_DIR\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"initializing metadata schema\"" >> $(PLATFORM_DIR)/start.sh
	@echo "./bin/\$$APP_NAME -init-metadata -conf=./config/trpc_go.yaml >> ./log/app.log 2>&1" >> $(PLATFORM_DIR)/start.sh
	@echo "nohup ./bin/\$$APP_NAME -conf=./config/trpc_go.yaml > ./log/app.log 2>&1 &" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \$$! > \"\$$PID_FILE\"" >> $(PLATFORM_DIR)/start.sh
	@echo "echo \"started \$$APP_NAME pid=\$$(cat \"\$$PID_FILE\")\"" >> $(PLATFORM_DIR)/start.sh
	@echo "#!/usr/bin/env bash" > $(PLATFORM_DIR)/stop.sh
	@echo "set -euo pipefail" >> $(PLATFORM_DIR)/stop.sh
	@echo "APP_NAME=\"$(APP_NAME)\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "SCRIPT_DIR=\"\$$(cd \"\$$(dirname \"\$$0\")\" && pwd)\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "PID_FILE=\"\$$SCRIPT_DIR/\$$APP_NAME.pid\"" >> $(PLATFORM_DIR)/stop.sh
	@echo "if [ -f \"\$$PID_FILE\" ]; then kill \"\$$(cat \"\$$PID_FILE\")\" 2>/dev/null || true; rm -f \"\$$PID_FILE\"; fi" >> $(PLATFORM_DIR)/stop.sh
	@chmod +x $(PLATFORM_DIR)/start.sh $(PLATFORM_DIR)/stop.sh

deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "请指定服务器地址: make deploy SERVER=user@host"; \
		exit 1; \
	fi
	@if [ ! -f "./scripts/deploy.sh" ]; then \
		echo "部署脚本 ./scripts/deploy.sh 不存在"; \
		exit 1; \
	fi
	./scripts/deploy.sh $(SERVER)

clean:
	@echo "正在清理构建文件..."
	rm -rf $(BUILD_DIR) $(RELEASE_DIR) cover.out.tmp
	go clean -cache

dev:
	CGO_ENABLED=$(CGO_ENABLED) go run ./cmd/moox-storage -conf=$(CONFIG_DIR)/trpc_go.yaml

install: build
	@mkdir -p ~/bin
	@cp $(BUILD_DIR)/$(APP_NAME) ~/bin/
	@echo "已安装到 ~/bin/$(APP_NAME)"
