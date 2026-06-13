# Node Exporter Makefile
# 通过 wget 下载官方预编译二进制，部署到目标机器

.PHONY: help build build-linux build-linux-amd64 build-linux-arm64 clean deploy

# Variables
APP_NAME=node_exporter
VERSION ?= 1.10.2
DOWNLOAD_BASE_URL=https://github.com/prometheus/node_exporter/releases/download/v$(VERSION)
BUILD_DIR=./build
LISTEN_PORT ?= 9100

# Help
help:
	@echo "Node Exporter 构建部署工具"
	@echo ""
	@echo "构建目标 (wget 下载预编译二进制):"
	@echo "  build               - 下载 Linux amd64 二进制 (默认)"
	@echo "  build-linux         - 下载 Linux amd64 二进制 (同 build)"
	@echo "  build-linux-amd64   - 下载 Linux amd64 二进制"
	@echo "  build-linux-arm64   - 下载 Linux arm64 二进制"
	@echo "  clean               - 清理构建文件"
	@echo ""
	@echo "部署目标:"
	@echo "  deploy              - 部署到远程服务器"
	@echo ""
	@echo "使用示例:"
	@echo "  make build                                          # 下载 Linux amd64 (默认)"
	@echo "  make build-linux-arm64                              # 下载 Linux arm64"
	@echo "  make build VERSION=1.10.2                           # 指定版本号"
	@echo "  make deploy SERVER=user@host                        # 部署到远程服务器 (默认 amd64)"
	@echo "  make deploy SERVER=user@host ARCH=arm64             # 部署 arm64 版本"
	@echo "  make deploy SERVER=user@host LISTEN_PORT=9200       # 指定监听端口"

# Default build target
build: build-linux-amd64

# Alias: build-linux -> build-linux-amd64
build-linux: build-linux-amd64

# Build (download) for Linux amd64
build-linux-amd64:
	@echo "下载 node_exporter v$(VERSION) linux-amd64..."
	@mkdir -p $(BUILD_DIR)
	@TARBALL="node_exporter-$(VERSION).linux-amd64.tar.gz"; \
	if [ -f "$(BUILD_DIR)/$$TARBALL" ]; then \
		echo "文件已存在，跳过下载: $(BUILD_DIR)/$$TARBALL"; \
	else \
		curl -fSL -o "$(BUILD_DIR)/$$TARBALL" \
			"$(DOWNLOAD_BASE_URL)/$$TARBALL" || { echo "下载失败"; exit 1; }; \
	fi; \
	echo "解压二进制..."; \
	tar -xzf "$(BUILD_DIR)/$$TARBALL" -C $(BUILD_DIR) --strip-components=1 \
		"node_exporter-$(VERSION).linux-amd64/node_exporter"; \
	chmod +x $(BUILD_DIR)/node_exporter; \
	echo "构建完成: $(BUILD_DIR)/node_exporter (linux-amd64)"

# Build (download) for Linux arm64
build-linux-arm64:
	@echo "下载 node_exporter v$(VERSION) linux-arm64..."
	@mkdir -p $(BUILD_DIR)
	@TARBALL="node_exporter-$(VERSION).linux-arm64.tar.gz"; \
	if [ -f "$(BUILD_DIR)/$$TARBALL" ]; then \
		echo "文件已存在，跳过下载: $(BUILD_DIR)/$$TARBALL"; \
	else \
		curl -fSL -o "$(BUILD_DIR)/$$TARBALL" \
			"$(DOWNLOAD_BASE_URL)/$$TARBALL" || { echo "下载失败"; exit 1; }; \
	fi; \
	echo "解压二进制..."; \
	tar -xzf "$(BUILD_DIR)/$$TARBALL" -C $(BUILD_DIR) --strip-components=1 \
		"node_exporter-$(VERSION).linux-arm64/node_exporter"; \
	chmod +x $(BUILD_DIR)/node_exporter; \
	echo "构建完成: $(BUILD_DIR)/node_exporter (linux-arm64)"

# Deploy to remote server
deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "请指定服务器地址"; \
		echo "使用方法: make deploy SERVER=user@host [ARCH=amd64|arm64] [LISTEN_PORT=9100]"; \
		exit 1; \
	fi
	@if [ ! -f "./scripts/deploy.sh" ]; then \
		echo "部署脚本 ./scripts/deploy.sh 不存在"; \
		exit 1; \
	fi
	@./scripts/deploy.sh "$(SERVER)" "$(or $(ARCH),amd64)" "$(VERSION)" "$(LISTEN_PORT)"

# Clean build artifacts
clean:
	@echo "清理构建文件..."
	rm -rf $(BUILD_DIR)
