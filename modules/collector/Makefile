ROOT := ../..
APP_NAME := moox-collector

.PHONY: help build build-linux build-darwin build-scf package-scf clean deps lint linux darwin

help:
	@echo "MooX collector module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make build        - build moox-collector for local platform"
	@echo "  make build-linux  - build moox-collector for linux/amd64"
	@echo "  make build-scf    - build moox-collector-scf binary"
	@echo "  make package-scf  - create Tencent SCF zip package"

build:
	@cd $(ROOT) && ./scripts/build/build.sh collector

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh collector

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build/build.sh collector

build-scf:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh collector-scf

package-scf:
	@cd $(ROOT) && ./scripts/build/build-collector-scf-package.sh

clean:
	@rm -rf bin release scf-build collector-scf-*.zip

deps:
	@go mod download

lint:
	@go vet ./...

linux: build-linux
darwin: build-darwin
