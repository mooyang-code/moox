.PHONY: check-timer-startup build build-host-gateway build-storage-linux check-boundaries check-factor-split check-module-boundaries check-proto-time-fields check-package-boundaries check-format check-lint test-quality-gates test-docs-architecture test-greenfield-contract test-trpc-plugin-config test-go-workspace-shard-contract test-check-build-env test-build-factor test-storage-boundary test-storage-consistency test-storage-seed-contract test-build-linux-local-contract test-build-version-contract test-collector-scf-package-contract test-release-bundle-manifest test-event-contracts test-eventbus-topology test-script-contracts test-script-e2e test-scripts test-skill-contracts proto-check test test-go test-web verify-pr verify verify-custom-setup test-moox-ca package-skill clean proto

build:
	./scripts/build/build.sh

build-host-gateway:
	./scripts/build/build.sh host-gateway

build-storage-linux:
	bash scripts/build/build-linux-local.sh storage

check-boundaries: check-module-boundaries check-package-boundaries check-factor-split check-timer-startup

check-factor-split:
	./scripts/check/check-factor-split.sh

check-timer-startup:
	./scripts/check/check-timer-startup.sh

check-module-boundaries:
	./scripts/check/check-module-boundaries.sh

check-proto-time-fields:
	bash scripts/test/contract/test-check-proto-time-fields.sh
	./scripts/check/check-proto-time-fields.sh

check-package-boundaries:
	./scripts/check/check-package-boundaries.sh

test-storage-boundary:
	bash scripts/test/contract/test-storage-boundary-contract.sh

test-storage-consistency:
	bash scripts/test/contract/test-storage-consistency-contract.sh

test-storage-seed-contract:
	bash scripts/test/contract/test-storage-seed-contract.sh

test-build-linux-local-contract:
	bash scripts/test/contract/test-build-linux-local.sh

# 发布构建出来的每个服务组件都要能上报版本号（不是 dev）。
test-build-version-contract:
	bash scripts/test/contract/test-build-version-injection.sh

test-event-contracts:
	bash scripts/check/verify-event-contracts.sh

test-eventbus-topology:
	cd modules/eventbus && go test ./...

# 起真实进程的联调：编译产物后依次跑 scripts/test/gates/ 下的脚本。主机网关那一条要 Linux 的 127.0.0.2，别的系统上跳过。
.PHONY: test-e2e-gates
test-e2e-gates:
	bash scripts/test/gates/run-e2e-gates.sh

test-trpc-plugin-config:
	bash scripts/test/contract/test-trpc-plugin-config.sh

test-go-workspace-shard-contract:
	bash scripts/test/contract/test-go-workspace-shard-contract.sh

test-check-build-env:
	bash scripts/test/contract/test-check-build-env.sh

test-build-factor:
	bash scripts/test/contract/test-build-factor.sh

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

.PHONY: test-release-bundle-manifest
test-release-bundle-manifest:
	./scripts/test/contract/test-release-bundle-manifest.sh

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
VERIFY_PR_UNITS := test-deployment-packages test-host-deployment test-host-identity-bundles check-go-module-graph $(PROTO_CHECK) test-greenfield-contract
VERIFY_PR_CONTRACTS := test-event-contracts test-eventbus-topology test-storage-seed-contract test-build-linux-local-contract test-build-version-contract test-check-build-env test-build-factor test-collector-scf-package-contract test-release-bundle-manifest test-e2e-gates
VERIFY_CORE_CHECKS := check-boundaries check-proto-time-fields test-quality-gates test-trpc-plugin-config test-go-workspace-shard-contract test-docs-architecture test-moox-ca test-skill-contracts
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
	(cd modules/admin && go test -count=1 ./internal/service/secret/... ./internal/bootstrap ./schema && go test -count=1 ./test -run Setup)
	(cd modules/cli && go test -count=1 ./internal/setup/... ./internal/command && go test -count=1 ./test -run Setup)
	bash skills/moox/scripts/test-private-network-contract.sh

test-moox-ca:
	bash scripts/test/contract/test-install-moox-ca.sh

test-collector-scf-package-contract:
	bash scripts/build/build-collector-scf-package_test.sh

test-script-contracts:
	@set -e; for script in scripts/test/contract/*.sh; do bash "$$script"; done

test-skill-contracts:
	bash scripts/build/package-skill_test.sh
	bash skills/moox/scripts/test-private-network-contract.sh
	bash skills/moox/scripts/test-eventbus-credentials-contract.sh
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
	$(MAKE) -C packages/gatewayclient/directorypb all
	$(MAKE) -C packages/metricspb all
	$(MAKE) -C packages/hostmetricpb all
	$(MAKE) -C packages/events all
	$(MAKE) -C modules/storage proto
	$(MAKE) -C modules/admin/proto all
	$(MAKE) -C modules/trade/proto all
	$(MAKE) -C modules/collector/proto all
	$(MAKE) -C modules/egressproxy/proto all
	$(MAKE) -C modules/factor/proto all
	$(MAKE) -C modules/monitor/proto all
	$(MAKE) -C modules/eventbus/proto all
	$(MAKE) -C modules/hostagent/proto all
	$(MAKE) -C modules/strategy/proto all
	$(MAKE) -C modules/archive/proto all

clean:
	rm -rf bin release dist
	find modules -type d \( -name bin -o -name release -o -name .cache \) -prune -exec rm -rf {} +

.PHONY: test-deployment-packages
test-deployment-packages:
	go test ./modules/cli/internal/setup/bundle ./modules/cli/internal/setup/fetchunit ./modules/cli/internal/setup/deploy ./modules/cli/internal/setup/deployscan

.PHONY: test-host-deployment
test-host-deployment:
	go test ./modules/admin/internal/service/sysdeploy/... ./modules/admin/cmd/cli ./modules/cli/internal/setup/client ./modules/cli/internal/doctor ./modules/cli/internal/command -run 'TestHostTopologyAPIOverRealTRPC|TestTopology|TestSyncTopology|TestDoctorDeployments'

.PHONY: test-host-identity-bundles
test-host-identity-bundles:
	go test ./modules/admin/cmd/cli ./modules/admin/internal/pki ./packages/gatewayclient ./packages/servicecatalog/hostgatewayconfig -run 'TestBootstrap|TestHostBundle|TestInfo|TestPersistentCA|TestFileConfig'
	go test ./modules/cli/internal/setup/identitybundle
