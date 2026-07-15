.PHONY: build build-gateway check-boundaries release deploy verify test-caddy test-gateway-deploy package-skill clean proto

build:
	./scripts/build.sh

build-gateway:
	./scripts/build.sh gateway

check-boundaries:
	./scripts/check-module-boundaries.sh

release:
	./scripts/release.sh

deploy:
	./scripts/deploy-moox.sh $(ARGS)

verify: check-boundaries test-gateway-deploy test-caddy
	cd modules/gateway && go test -count=1 ./...

test-caddy:
	bash scripts/test-caddy-config.sh
	bash scripts/test-deploy-moox-https.sh
	bash scripts/test-install-caddy-ca.sh
	bash skills/moox/scripts/test-caddy-prerequisite.sh
	bash skills/moox/scripts/test-caddy-ca.sh

test-gateway-deploy:
	bash scripts/test-deploy-moox-gateway.sh

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

clean:
	rm -rf bin release dist scripts/node_exporter/build
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +
