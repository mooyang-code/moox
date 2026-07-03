ROOT := ../..
APP_NAME := moox-collector
SERVER ?= $(MOOX_DEV_SSH_TARGET)

.PHONY: help build build-linux build-darwin build-scf package-scf clean deps lint release deploy linux darwin

help:
	@echo "MooX collector module"
	@echo ""
	@echo "Targets proxy to repository-level scripts:"
	@echo "  make build        - build moox-collector for local platform"
	@echo "  make build-linux  - build moox-collector for linux/amd64"
	@echo "  make build-scf    - build moox-collector-scf binary"
	@echo "  make package-scf  - create Tencent SCF zip package"
	@echo "  make release      - create repository release package"
	@echo "  make deploy SERVER=user@host - deploy collector through scripts/deploy-moox.sh"

build:
	@cd $(ROOT) && ./scripts/build.sh collector

build-linux:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh collector

build-darwin:
	@cd $(ROOT) && TARGET_GOOS=darwin TARGET_GOARCH=amd64 ./scripts/build.sh collector

build-scf:
	@cd $(ROOT) && TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh collector-scf

package-scf:
	@cd $(ROOT) && ./scripts/build-collector-scf-package.sh

clean:
	@rm -rf bin release scf-build collector-scf-*.zip

deps:
	@go mod download

lint:
	@go vet ./...

release:
	@cd $(ROOT) && ./scripts/release.sh

deploy:
	@if [ -z "$(SERVER)" ]; then \
		echo "请指定 SERVER=user@host，或设置 MOOX_DEV_SSH_TARGET"; \
		exit 1; \
	fi
	@cd $(ROOT) && ./scripts/deploy-moox.sh --target "$(SERVER)" --no-storage --no-web-host --no-cloudnode

linux: build-linux
darwin: build-darwin
