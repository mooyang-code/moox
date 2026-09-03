.PHONY: build build-gateway build-storage-linux check-boundaries check-module-boundaries check-package-boundaries check-format check-lint test-quality-gates test-docs-architecture test-greenfield-contract test-storage-boundary test-storage-consistency test-storage-datanode-management-contract test-build-storage-linux-contract test-collector-scf-package-contract e2e-storage-datanode-management test-event-contracts test-eventbus-topology test-storage-market-pipeline test-storage-view-series-capacity test-factor-view-ready-batch-e2e test-kline-resample test-script-contracts test-script-e2e test-scripts test-skill-contracts proto-check release release-binaries release-matrix deploy publish-release-binaries test test-go test-web test-release verify-pr verify verify-custom-setup test-caddy test-gateway-deploy test-strategy-deploy test-strategy-deploy-e2e package-skill clean proto

build:
	./scripts/build/build.sh

build-gateway:
	./scripts/build/build.sh gateway

build-storage-linux:
	./scripts/build/build.sh cli
	bash scripts/build/build-storage-linux.sh

check-boundaries: check-module-boundaries check-package-boundaries

check-module-boundaries:
	./scripts/check/check-module-boundaries.sh

check-package-boundaries:
	./scripts/check/check-package-boundaries.sh

test-storage-boundary:
	bash scripts/test/contract/test-storage-boundary-contract.sh

test-storage-consistency:
	bash scripts/test/contract/test-storage-consistency-contract.sh

test-storage-datanode-management-contract:
	bash scripts/test/contract/test-storage-datanode-management-contract.sh

test-build-storage-linux-contract:
	bash scripts/test/contract/test-build-storage-linux-contract.sh

test-event-contracts:
	bash scripts/check/verify-event-contracts.sh

test-eventbus-topology:
	cd modules/eventbus && go test ./...

test-storage-view-event-pipeline:
	@rg -q '^func TestViewEventConsumerProcessesIndependentDatasetLanesE2E' modules/storage/internal/service/e2e/view_consumer_concurrency_e2e_test.go
	cd modules/storage && CGO_ENABLED=1 go test ./internal/service/e2e -run '^TestViewEventConsumerProcessesIndependentDatasetLanesE2E$$' -count=1

test-storage-view-series-capacity:
	bash scripts/test/e2e/test-storage-view-series-capacity.sh

test-factor-view-ready-batch-e2e:
	bash scripts/test/e2e/test-factor-view-ready-e2e.sh

test-kline-resample:
	bash scripts/test/e2e/test-kline-resample.sh

e2e-storage-datanode-management:
	bash scripts/test/e2e/storage-datanode-management.sh

check-format:
	./scripts/check/check-gofmt.sh
	pnpm --dir web run lint:prettier:check

check-lint:
	pnpm --dir web run lint:eslint:check

test-quality-gates:
	bash scripts/test/contract/test-quality-gates.sh

test-docs-architecture:
	bash scripts/test/contract/test-docs-architecture.sh

test-greenfield-contract:
	bash scripts/test/contract/test-greenfield-contract.sh

release:
	./scripts/release/release.sh

release-binaries:
	./scripts/build/build-release-binaries.sh

release-matrix:
	./scripts/release/release-matrix.sh

deploy:
	./scripts/deploy/deploy-moox.sh $(ARGS)

publish-release-binaries:
	./scripts/release/publish-release-binaries.sh $(ARGS)

test: test-go test-web

test-go:
	./scripts/test/contract/test-go-workspace.sh

test-web:
	CI=true pnpm --dir web install --frozen-lockfile
	pnpm --dir web test
	pnpm --dir web build:prod

test-release:
	./scripts/test/contract/test-release-contract.sh

proto-check:
	$(MAKE) proto
	@test -z "$$(git status --porcelain)"

verify-pr: proto-check test-greenfield-contract test-event-contracts test-eventbus-topology test-storage-view-event-pipeline test-storage-view-series-capacity test-factor-view-ready-batch-e2e test-storage-datanode-management-contract test-build-storage-linux-contract test-collector-scf-package-contract

verify: verify-pr check-boundaries test-storage-boundary test-storage-consistency test check-format check-lint test-quality-gates test-docs-architecture test-release test-gateway-deploy test-strategy-deploy test-strategy-deploy-e2e test-caddy test-skill-contracts
	CI=true pnpm install --frozen-lockfile
	pnpm docs:build

verify-custom-setup:
	(cd packages/cloudprovider && go test -count=1 ./...)
	(cd modules/admin && go test -count=1 ./internal/service/setup/... ./internal/bootstrap ./schema && go test -count=1 ./test -run Setup)
	(cd modules/cli && go test -count=1 ./internal/setup/... ./internal/command && go test -count=1 ./test -run Setup)
	bash scripts/test/contract/test-deploy-moox-admin-bootstrap.sh
	bash scripts/test/contract/test-deploy-moox-control-profile.sh
	bash scripts/test/contract/test-deploy-moox-storage-profile.sh
	bash scripts/test/contract/test-deploy-moox-storage-view.sh
	bash skills/moox/scripts/test-custom-setup-contract.sh

test-caddy:
	bash scripts/test/contract/test-caddy-config.sh
	bash scripts/test/contract/test-deploy-moox-https.sh
	bash scripts/test/contract/test-install-caddy-ca.sh
	bash skills/moox/scripts/test-caddy-prerequisite.sh
	bash skills/moox/scripts/test-caddy-ca.sh

test-collector-scf-package-contract:
	bash scripts/build/build-collector-scf-package_test.sh

test-script-contracts:
	@set -e; for script in scripts/test/contract/*.sh; do bash "$$script"; done

test-skill-contracts:
	bash scripts/build/package-skill_test.sh
	bash skills/moox/scripts/test-data-query-contract.sh

test-script-e2e:
	@set -e; for script in scripts/test/e2e/*.sh; do bash "$$script"; done

test-scripts: test-script-contracts test-script-e2e

test-gateway-deploy:
	bash scripts/test/contract/test-deploy-moox-gateway.sh

test-strategy-deploy:
	bash scripts/test/contract/test-deploy-moox-strategy.sh

test-strategy-deploy-e2e:
	bash scripts/test/e2e/test-deploy-moox-strategy-e2e.sh

package-skill:
	./scripts/build/package-skill.sh

proto:
	$(MAKE) -C packages/commonpb all
	$(MAKE) -C packages/metricspb all
	$(MAKE) -C packages/hostmetricpb all
	$(MAKE) -C packages/observabilitypb all
	$(MAKE) -C packages/cloudjobpb all
	$(MAKE) -C packages/tradeeventpb all
	$(MAKE) -C packages/storagepb generate
	$(MAKE) -C packages/events all
	$(MAKE) -C packages/marketfetchpb all
	$(MAKE) -C modules/storage proto
	$(MAKE) -C modules/admin/proto all
	$(MAKE) -C modules/trade/proto all
	$(MAKE) -C modules/collector/proto all
	$(MAKE) -C modules/factor/proto all
	$(MAKE) -C modules/cloudnode/proto all
	$(MAKE) -C modules/monitor/proto all
	$(MAKE) -C modules/eventbus/proto all
	$(MAKE) -C modules/hostagent/proto all
	$(MAKE) -C modules/strategy/proto all

clean:
	rm -rf bin release dist
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +
