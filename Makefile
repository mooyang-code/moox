.PHONY: build build-host-gateway build-storage-linux check-boundaries check-factor-split check-module-boundaries check-package-boundaries check-format check-lint test-quality-gates test-docs-architecture test-greenfield-contract test-storage-boundary test-storage-consistency test-storage-datanode-management-contract test-build-linux-local-contract test-collector-scf-package-contract e2e-storage-datanode-management test-event-contracts test-eventbus-topology test-storage-market-pipeline test-kline-resample test-script-contracts test-script-e2e test-scripts test-skill-contracts proto-check test test-go test-web verify-pr verify verify-custom-setup test-caddy package-skill clean proto

build:
	./scripts/build/build.sh

build-host-gateway:
	./scripts/build/build.sh host-gateway

build-storage-linux:
	bash scripts/build/build-linux-local.sh storage

check-boundaries: check-module-boundaries check-package-boundaries check-factor-split

check-factor-split:
	./scripts/check/check-factor-split.sh

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

test-build-linux-local-contract:
	bash scripts/test/contract/test-build-linux-local.sh

test-event-contracts:
	bash scripts/check/verify-event-contracts.sh

test-eventbus-topology:
	cd modules/eventbus && go test ./...

test-storage-view-event-pipeline:
	@rg -q '^func TestViewEventConsumerProcessesIndependentDatasetLanesE2E' modules/storage/internal/service/e2e/view_consumer_concurrency_e2e_test.go
	cd modules/storage && CGO_ENABLED=1 go test ./internal/service/e2e -run '^TestViewEventConsumerProcessesIndependentDatasetLanesE2E$$' -count=1

.PHONY: test-host-gateway-control-e2e
test-host-gateway-control-e2e:
	bash scripts/test/gates/test-host-gateway-control-e2e.sh

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

test: test-go test-web test-rust

# Rust SDK、示例与测试样本：格式、clippy（-D warnings）、单元测试、宏的编译期错误用例；
# 然后重新构建测试 wasm，并要求它们的一致性测试输出摘要与固定在 packages/factorudf/testdata/digests.json 里的预期值相同
# （摘要只取决于行为，不取决于 wasm 字节：不同平台构建出的字节可能不同，行为必须相同）。
.PHONY: test-rust udf-testdata
test-rust: udf-testdata
	cd sdk/rust && cargo fmt --all -- --check
	cd sdk/rust && cargo clippy --workspace --all-targets --locked -- -D warnings
	cd sdk/rust && cargo test --workspace --locked
	cd packages/factorudf && go test -count=1 -run 'TestPinnedConformanceDigests' ./...

udf-testdata:
	bash scripts/build/build-udf-testdata.sh

test-go:
	./scripts/test/contract/test-go-workspace.sh

.PHONY: test-console-proxy
test-console-proxy:
	cd modules/consoleproxy && go test -count=1 ./... && go vet ./...

test-web:
	CI=true pnpm --dir web install --frozen-lockfile
	pnpm --dir web test
	pnpm --dir web build:prod

proto-check:
	$(MAKE) proto
	@test -z "$$(git status --porcelain)"

.PHONY: check-go-module-graph
check-go-module-graph:
	bash scripts/ci/check-go-module-graph.sh

# proto-check 需要 trpc-open、protoc、protoc-gen-go 等代码生成工具，且要求生成结果与提交逐字节一致。
# GitHub 托管运行器上没有这些工具：这里用 SKIP_PROTO_CHECK=1 跳过，完整检查由自托管运行器（verify-trusted）执行。
PROTO_CHECK := $(if $(SKIP_PROTO_CHECK),,proto-check)

# make verify 由下面几组检查组成，PR 上的 CI 按组并行运行（make verify-ci-<组>）；本机和自托管运行器的 make verify 仍然依次全跑。
# 新增检查时放进某一组，verify 与各组自动保持一致。组之间互不依赖：需要前端依赖的检查都放在 web 组，由 test-web 先安装。
VERIFY_PR_UNITS := test-deployment-packages test-host-deployment test-host-identity-bundles test-unit-install test-unit-runtime test-unit-bootstrap check-go-module-graph $(PROTO_CHECK) test-greenfield-contract test-unit-core
VERIFY_PR_CONTRACTS := test-event-contracts test-eventbus-topology test-storage-view-event-pipeline test-storage-datanode-management-contract test-build-linux-local-contract test-collector-scf-package-contract
VERIFY_CORE_CHECKS := check-boundaries test-quality-gates test-docs-architecture test-caddy test-skill-contracts
VERIFY_STORAGE_CHECKS := test-storage-boundary test-storage-consistency

verify-pr: $(VERIFY_PR_UNITS) $(VERIFY_PR_CONTRACTS)

.PHONY: verify-ci-core verify-ci-contracts verify-ci-storage verify-ci-web
verify-ci-core: $(VERIFY_PR_UNITS) $(VERIFY_CORE_CHECKS)
verify-ci-contracts: $(VERIFY_PR_CONTRACTS)
verify-ci-storage: $(VERIFY_STORAGE_CHECKS)
verify-ci-web: test-web check-format check-lint
	CI=true pnpm install --frozen-lockfile
	pnpm docs:build

# Go 工作区测试不属于任何一组：CI 里按 MOOX_GO_SHARD 切成几片并行跑，完整的 make verify 通过 test 目标运行它。
verify: verify-ci-core verify-ci-contracts verify-ci-storage test verify-ci-web

verify-custom-setup:
	(cd packages/cloudprovider && go test -count=1 ./...)
	(cd modules/admin && go test -count=1 ./internal/service/setup/... ./internal/bootstrap ./schema && go test -count=1 ./test -run Setup)
	(cd modules/cli && go test -count=1 ./internal/setup/... ./internal/command && go test -count=1 ./test -run Setup)
	bash skills/moox/scripts/test-private-network-contract.sh

test-caddy:
	bash scripts/test/contract/test-install-caddy-ca.sh
	bash skills/moox/scripts/test-caddy-ca.sh

test-collector-scf-package-contract:
	bash scripts/build/build-collector-scf-package_test.sh

test-script-contracts:
	@set -e; for script in scripts/test/contract/*.sh; do bash "$$script"; done

test-skill-contracts:
	bash scripts/build/package-skill_test.sh
	bash skills/moox/scripts/test-data-query-contract.sh
	bash skills/moox/scripts/test-private-network-contract.sh
	bash skills/moox/scripts/test-eventbus-credentials-contract.sh
	bash skills/moox/scripts/test-native-release.sh
	bash skills/moox/scripts/test-factor-convert-contract.sh
	bash skills/moox/scripts/test-strategy-check-contract.sh
	bash skills/moox/scripts/test-monitor-db-check.sh

test-script-e2e:
	@set -e; for script in scripts/test/e2e/*.sh; do bash "$$script"; done

test-scripts: test-script-contracts test-script-e2e

package-skill:
	./scripts/build/package-skill.sh

proto:
	$(MAKE) -C packages/commonpb all
	$(MAKE) -C packages/gatewayroute/proto all
	$(MAKE) -C packages/metricspb all
	$(MAKE) -C packages/hostmetricpb all
	$(MAKE) -C packages/tradeeventpb all
	$(MAKE) -C packages/storagepb generate
	$(MAKE) -C packages/events all
	$(MAKE) -C packages/marketfetchpb all
	$(MAKE) -C modules/storage proto
	$(MAKE) -C modules/admin/proto all
	$(MAKE) -C modules/trade/proto all
	$(MAKE) -C modules/collector/proto all
	$(MAKE) -C modules/egressproxy/proto all
	$(MAKE) -C modules/factor/proto all
	$(MAKE) -C modules/cloudfunction/proto all
	$(MAKE) -C modules/monitor/proto all
	$(MAKE) -C modules/eventbus/proto all
	$(MAKE) -C modules/hostagent/proto all
	$(MAKE) -C modules/strategy/proto all

clean:
	rm -rf bin release dist
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +

.PHONY: test-deployment-packages test-unit-package-linux
test-deployment-packages:
	go test ./modules/cli/internal/setup/unitpackage ./modules/cli/internal/command -run 'TestUnit|TestSetupPackage'

test-unit-package-linux:
	bash scripts/test/gates/test-unit-package-linux.sh

.PHONY: test-host-deployment
test-host-deployment:
	go test ./modules/admin/internal/service/sysdeploy/... ./modules/admin/cmd/cli ./modules/cli/internal/setup/client ./modules/cli/internal/doctor ./modules/cli/internal/command -run 'TestHostTopologyAPIOverRealTRPC|TestTopology|TestSyncHostDeployments|TestDoctorDeployments'

.PHONY: test-host-identity-bundles test-unit-bundle-linux
test-host-identity-bundles:
	go test ./modules/admin/cmd/cli ./modules/admin/internal/pki ./packages/gatewayclient ./packages/servicecatalog/hostgatewayconfig -run 'TestBootstrap|TestHostBundle|TestInfo|TestPersistentCA|TestFileConfig'
	go test ./modules/cli/internal/setup/unitbundle

test-unit-bundle-linux:
	bash scripts/test/gates/test-unit-bundle-linux.sh

.PHONY: test-unit-runtime test-unit-runtime-linux
.PHONY: test-unit-install test-unit-install-linux
.PHONY: test-unit-bootstrap test-unit-bootstrap-linux
.PHONY: test-unit-core test-unit-core-linux
test-unit-core:
	go test ./modules/cli/internal/setup/unitdeploy

test-unit-core-linux:
	bash scripts/test/gates/test-unit-core-linux.sh

test-unit-bootstrap:
	go test ./modules/cli/internal/setup/unitbootstrap

test-unit-bootstrap-linux:
	bash scripts/test/gates/test-unit-bootstrap-linux.sh

test-unit-install:
	go test ./modules/cli/internal/setup/unitinstall

test-unit-install-linux:
	bash scripts/test/gates/test-unit-install-linux.sh

test-unit-runtime:
	go test ./modules/cli/internal/setup/unitruntime ./modules/cli/cmd/moox-runtime

test-unit-runtime-linux:
	bash scripts/test/gates/test-unit-runtime-linux.sh
