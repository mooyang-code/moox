.PHONY: build build-gateway check-boundaries check-module-boundaries check-package-boundaries check-context check-format check-lint test-quality-gates release deploy test test-go test-web test-release verify verify-custom-setup test-caddy test-gateway-deploy test-strategy-deploy test-strategy-deploy-e2e package-skill clean proto

build:
	./scripts/build.sh

build-gateway:
	./scripts/build.sh gateway

check-boundaries: check-module-boundaries check-package-boundaries

check-module-boundaries:
	./scripts/check-module-boundaries.sh

check-package-boundaries:
	./scripts/check-package-boundaries.sh

check-context:
	./scripts/check-trpc-context.sh

check-format:
	./scripts/check-gofmt.sh
	pnpm --dir web run lint:prettier:check

check-lint:
	pnpm --dir web run lint:eslint:check

test-quality-gates:
	bash scripts/test-quality-gates.sh

release:
	./scripts/release.sh

deploy:
	./scripts/deploy-moox.sh $(ARGS)

test: test-go test-web

test-go:
	./scripts/test-go-workspace.sh

test-web:
	CI=true pnpm --dir web install --frozen-lockfile
	pnpm --dir web test
	pnpm --dir web build:prod

test-release:
	./scripts/test-release-contract.sh

verify: check-boundaries check-context test check-format check-lint test-quality-gates test-release test-gateway-deploy test-strategy-deploy test-caddy
	CI=true pnpm install --frozen-lockfile
	pnpm docs:build

verify-custom-setup:
	(cd packages/cloudprovider && go test -count=1 ./...)
	(cd modules/admin && go test -count=1 ./internal/service/setup/... ./internal/bootstrap ./schema && go test -count=1 ./test -run Setup)
	(cd modules/cli && go test -count=1 ./internal/setup/... ./internal/command && go test -count=1 ./test -run Setup)
	bash scripts/test-deploy-moox-admin-bootstrap.sh
	bash scripts/test-deploy-moox-control-profile.sh
	bash scripts/test-deploy-moox-storage-profile.sh
	bash skills/moox/scripts/test-custom-setup-contract.sh

test-caddy:
	bash scripts/test-caddy-config.sh
	bash scripts/test-deploy-moox-https.sh
	bash scripts/test-install-caddy-ca.sh
	bash skills/moox/scripts/test-caddy-prerequisite.sh
	bash skills/moox/scripts/test-caddy-ca.sh

test-gateway-deploy:
	bash scripts/test-deploy-moox-gateway.sh

test-strategy-deploy:
	bash scripts/test-deploy-moox-strategy.sh

test-strategy-deploy-e2e:
	bash scripts/test-deploy-moox-strategy-e2e.sh

package-skill:
	./scripts/package-skill.sh

proto:
	$(MAKE) -C packages/commonpb all
	$(MAKE) -C packages/messagepb all
	$(MAKE) -C packages/metricspb all
	$(MAKE) -C packages/hostmetricpb all
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
	rm -rf bin release dist scripts/node_exporter/build
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +
