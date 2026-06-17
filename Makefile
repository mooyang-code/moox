.PHONY: build test release deploy acceptance package-skill clean proto

build:
	./scripts/build.sh

test:
	./scripts/test.sh

release:
	./scripts/release.sh

deploy:
	./scripts/deploy.sh

acceptance:
	./scripts/acceptance.sh

package-skill:
	./scripts/package-skill.sh

proto:
	$(MAKE) -C modules/storage proto
	$(MAKE) -C modules/control/proto all

clean:
	rm -rf bin release dist coverage scripts/node_exporter/build
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +
