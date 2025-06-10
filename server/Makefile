# Moox Server 主Makefile
# 所有构建命令都转发到scripts目录

.PHONY: help proto build clean dev run stop install deps check lint test

# 默认目标
all: 
	@cd scripts && $(MAKE) all

# 转发所有命令到scripts目录
help proto build clean dev run stop install deps check lint test:
	@cd scripts && $(MAKE) $@

# 带版本参数的构建
build-version:
	@cd scripts && $(MAKE) build VERSION=$(VERSION)

install-version:
	@cd scripts && $(MAKE) install VERSION=$(VERSION) 