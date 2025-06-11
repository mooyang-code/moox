# Moox Server 主Makefile
# 所有构建命令都转发到scripts目录

.PHONY: help proto build build-cmd build-all clean dev run stop install deps check lint test
.PHONY: init-db drop-db migrate-db cmd-help clean-db dev-db

# 默认目标
all: 
	@cd scripts && $(MAKE) all

# 转发所有命令到scripts目录
help proto build build-cmd build-all clean dev run stop install deps check lint test:
	@cd scripts && $(MAKE) $@

# 转发数据库管理命令到scripts目录
init-db drop-db migrate-db cmd-help clean-db dev-db:
	@cd scripts && $(MAKE) $@

# 带版本参数的构建
build-version:
	@cd scripts && $(MAKE) build VERSION=$(VERSION)

build-all-version:
	@cd scripts && $(MAKE) build-all VERSION=$(VERSION)

install-version:
	@cd scripts && $(MAKE) install VERSION=$(VERSION) 