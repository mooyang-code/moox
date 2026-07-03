ROOT := ../..
APP_NAME := moox-admin
SERVER ?= $(MOOX_DEV_SSH_TARGET)

.PHONY: help build build-linux build-darwin build-windows build-all clean deps lint install release deploy linux darwin windows

help:
	@echo "MooX admin module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make build        - build moox-admin for local platform"
	@echo "  make build-linux  - build moox-admin for linux/amd64"
	@echo "  make release      - create repository release package"
	@echo "  make deploy SERVER=user@host - deploy admin through scripts/deploy-moox.sh"

build:
	@cd $(ROOT) && ./scripts/build.sh admin

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh admin

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build.sh admin

build-windows:
	@echo "Windows build is not supported by scripts/build.sh"

build-all: build-linux build-darwin

clean:
	@rm -rf bin release

# Kept as developer convenience targets; root workflows are authoritative.
deps:
	@go mod download

lint:
	@go vet ./...

install: build
	@mkdir -p $(ROOT)/bin
	@cp bin/$(APP_NAME) $(ROOT)/bin/$(APP_NAME) 2>/dev/null || true

release:
	@cd $(ROOT) && ./scripts/release.sh

deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "请指定 SERVER=user@host，或设置 MOOX_DEV_SSH_TARGET"; \
		exit 1; \
	fi
	@cd $(ROOT) && ./scripts/deploy-moox.sh --target "$(SERVER)" --no-storage --no-web-host --no-cloudnode --no-collector

linux: build-linux
darwin: build-darwin
windows: build-windows
