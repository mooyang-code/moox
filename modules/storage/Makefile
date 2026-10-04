ROOT := ../..
APP_NAME := moox-storage
SERVER ?= $(MOOX_DEV_SSH_TARGET)

.PHONY: help all deps proto lint build build-linux build-darwin build-windows build-all release deploy clean dev install linux darwin windows

all: deps proto build

help:
	@echo "MooX storage module"
	@echo ""
	@echo "Targets proxy to repository-level scripts where possible:"
	@echo "  make proto        - regenerate storage protobuf code"
	@echo "  make build        - build moox-storage and moox-storage-cli for local platform"
	@echo "  make build-linux  - build moox-storage and moox-storage-cli for linux/amd64"
	@echo "  make release      - create repository release package"
	@echo "  make deploy SERVER=user@host - deploy storage through scripts/deploy/deploy-moox.sh"
	@echo "  make clean        - remove local build artifacts"

deps:
	@go mod download

proto:
	@cd proto && $(MAKE) all

lint:
	@go vet ./...

build:
	@cd $(ROOT) && ./scripts/build/build.sh storage

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh storage

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build/build.sh storage

build-windows:
	@echo "Windows storage build requires a CGO toolchain and is not supported by scripts/build/build.sh"

build-all: build-linux build-darwin

release:
	@cd $(ROOT) && ./scripts/release/release.sh

deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "请指定 SERVER=user@host，或设置 MOOX_DEV_SSH_TARGET"; \
		exit 1; \
	fi
	@cd $(ROOT) && ./scripts/deploy/deploy-moox.sh --target "$(SERVER)" --no-web-host --no-cloudnode --no-collector

clean:
	@rm -rf bin release dist

dev: build
install: build
linux: build-linux
darwin: build-darwin
windows: build-windows
