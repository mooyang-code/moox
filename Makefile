.PHONY: build test release deploy acceptance package-skill clean proto

build:
	./build/build.sh

test:
	./build/test.sh

release:
	./build/release.sh

deploy:
	./build/deploy.sh

acceptance:
	./build/acceptance.sh

package-skill:
	./build/package-skill.sh

proto:
	$(MAKE) -C modules/storage proto
	$(MAKE) -C modules/control/proto all

clean:
	rm -rf bin release dist coverage
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +
