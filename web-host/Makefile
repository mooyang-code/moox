ROOT := ..
APP_NAME := moox-web-host
SERVER ?= $(MOOX_DEV_SSH_TARGET)
STATIK_VERSION ?= v0.1.7

.PHONY: help statik build build-linux build-darwin build-windows build-all clean deps lint install release deploy linux darwin windows

help:
	@echo "MooX web-host module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make statik       - regenerate embedded statik assets from ../web/dist"
	@echo "  make build        - build moox-web-host with current embedded statik assets"
	@echo "  make build-linux  - build moox-web-host for linux/amd64"
	@echo "  make release      - create repository release package"
	@echo "  make deploy SERVER=user@host - deploy web-host through scripts/deploy-moox.sh"

statik:
	@if [ ! -d "$(ROOT)/web/dist" ]; then \
		echo "../web/dist not found; run frontend build first, or use scripts/deploy-moox.sh --build-web-assets"; \
		exit 1; \
	fi
	@command -v statik >/dev/null 2>&1 || { echo "statik not found; run: go install github.com/rakyll/statik@$(STATIK_VERSION)"; exit 1; }
	@statik -src=$(ROOT)/web/dist -dest=./internal

build:
	@cd $(ROOT) && ./scripts/build.sh web-host

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build.sh web-host

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
	@cd $(ROOT) && ./scripts/deploy-moox.sh --target "$(SERVER)" --no-storage --no-cloudnode --no-collector --build-web-assets

linux: build-linux
darwin: build-darwin
windows: build-windows
