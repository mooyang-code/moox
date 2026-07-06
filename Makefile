.PHONY: build check-boundaries release deploy package-skill clean proto

build:
	./scripts/build.sh

check-boundaries:
	./scripts/check-module-boundaries.sh

release:
	./scripts/release.sh

deploy:
	./scripts/deploy-moox.sh $(ARGS)

package-skill:
	./scripts/package-skill.sh

proto:
	$(MAKE) -C packages/commonpb all
	$(MAKE) -C modules/storage proto
	$(MAKE) -C modules/admin/proto all
	$(MAKE) -C modules/trade/proto all
	$(MAKE) -C modules/collector/proto all
	$(MAKE) -C modules/factor/proto all
	$(MAKE) -C modules/cloudnode/proto all

clean:
	rm -rf bin release dist scripts/node_exporter/build
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +
