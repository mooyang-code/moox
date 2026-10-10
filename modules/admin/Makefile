ROOT := ../..
APP_NAME := moox-admin

.PHONY: help build build-linux build-darwin build-windows build-all clean deps lint install release deploy linux darwin windows

help:
	@echo "MooX admin module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make build        - build moox-admin for local platform"
	@echo "  make build-linux  - build moox-admin for linux/amd64"
	@echo "  make release      - create repository release package"
	@echo "  make deploy       - 按 moox.toml 的部署表重新部署（moox-cli setup deploy-service）"

build:
	@cd $(ROOT) && ./scripts/build/build.sh admin

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh admin

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build/build.sh admin

build-windows:
	@echo "Windows build is not supported by scripts/build/build.sh"

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
	@cd $(ROOT) && ./scripts/release/release.sh

deploy:
	@cd $(ROOT) && go run ./modules/cli/cmd/moox-cli setup deploy-service --component admin

linux: build-linux
darwin: build-darwin
windows: build-windows
