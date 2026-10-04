ROOT := ../..
APP_NAME := moox-cli

.PHONY: help build build-linux build-darwin build-windows build-all clean deps lint install release linux darwin windows

help:
	@echo "MooX CLI module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make build        - build moox-cli for local platform"
	@echo "  make build-linux  - build moox-cli for linux/amd64"
	@echo "  make release      - create repository release package"
	@echo "  make clean        - remove local build artifacts"

build:
	@cd $(ROOT) && ./scripts/build/build.sh cli

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh cli

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build/build.sh cli

build-windows:
	@echo "Windows build is not supported by scripts/build/build.sh"

build-all: build-linux build-darwin

clean:
	@rm -rf bin release dist

deps:
	@go mod download

lint:
	@go vet ./...

install: build
	@mkdir -p $(ROOT)/bin
	@cp bin/$(APP_NAME) $(ROOT)/bin/$(APP_NAME) 2>/dev/null || true

release:
	@cd $(ROOT) && ./scripts/release/release.sh

linux: build-linux
darwin: build-darwin
windows: build-windows
