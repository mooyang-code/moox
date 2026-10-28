ROOT := ..
APP_NAME := moox-web-host
STATIK_VERSION ?= v0.1.7

.PHONY: help statik build build-linux build-darwin build-windows build-all clean deps lint install linux darwin windows

help:
	@echo "MooX web-host module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make statik       - regenerate embedded statik assets from ../web/dist"
	@echo "  make build        - build moox-web-host with current embedded statik assets"
	@echo "  make build-linux  - build moox-web-host for linux/amd64"

statik:
	@if [ ! -d "$(ROOT)/web/dist" ]; then \
		echo "../web/dist not found; run frontend build first"; \
		exit 1; \
	fi
	@if [ ! -d "$(ROOT)/web/dist/static/js" ]; then \
		echo "../web/dist/static/js not found: the frontend build is incomplete (placeholder pages would be embedded); build web on the compile host first"; \
		exit 1; \
	fi
	@go run github.com/rakyll/statik@$(STATIK_VERSION) -src=$(ROOT)/web/dist -dest=./internal

build:
	@cd $(ROOT) && ./scripts/build/build.sh web-host

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host

build-windows:
	@cd $(ROOT) && TARGET_GOOS=windows TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host

build-all: build-linux build-darwin build-windows

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

linux: build-linux
darwin: build-darwin
windows: build-windows
