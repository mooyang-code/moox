#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

go run ./scripts/check-trade-exchange-terminology.go

violations=()

# Domain and core packages contain business rules and must not depend on delivery
# or persistence details. Infrastructure is the opposite direction of the edge.
for root in modules/*/internal/domain modules/*/internal/core; do
	[[ -d "${root}" ]] || continue
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: domain/core packages must not import internal application, service, infra, or rpc packages")
	done < <(rg -n '"github.com/mooyang-code/moox/modules/[^/]+/internal/(infra|service|services|application|rpc)/' "${root}" --glob '*.go' || true)
done

for root in modules/*/internal/infra; do
	[[ -d "${root}" ]] || continue
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: infrastructure packages must not import internal application, service, or rpc packages")
	done < <(rg -n '"github.com/mooyang-code/moox/modules/[^/]+/internal/(service|services|application|rpc)/' "${root}" --glob '*.go' || true)
done

# Keep the naming decisions executable so a new module cannot silently restore
# the layouts that this refactor removes.
legacy_paths=(
	'internal/services/'
	'internal/gateway/spacecontext/'
	'internal/sources/exchangetypes/'
	'internal/providers/tencent-scf/'
	'proto/gen'
)
for path in "${legacy_paths[@]}"; do
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: legacy layout path '${path}' is not allowed")
	done < <(rg -n --fixed-strings "${path}" modules --glob '*.go' --glob '*.sh' --glob 'go.mod' --glob '*.proto' --glob 'Makefile' || true)
done

while IFS= read -r directory; do
	violations+=("${directory}: generic internal/bus hides the event interaction role")
done < <(find modules -type d -path '*/internal/bus' -print 2>/dev/null || true)

for path in modules/storage/internal/eventcontract modules/archive/internal/consumer; do
	[[ ! -e "${path}" ]] || violations+=("${path}: legacy event interaction path is not allowed")
done

trade_obsolete_paths=(
	modules/trade/internal/service
	modules/trade/internal/infra/exchangebridge
	modules/trade/internal/application/command
	modules/trade/internal/application/rebalance
	modules/trade/internal/application/reconciliation
	modules/trade/internal/domain/rebalance
)
for path in "${trade_obsolete_paths[@]}"; do
	[[ ! -e "${path}" ]] || violations+=("${path}: obsolete Trade execution path is not allowed")
done

while IFS= read -r match; do
	violations+=("${match}: obsolete Trade execution vocabulary is not allowed")
done < <(rg -n \
	'TradeChannel|RebalanceRequested|RebalanceRun|RebalanceLeg|execution_plan|execution_slice|reconciliation|Saga' \
	modules/trade packages/tradeeventpb web/src/api/trade \
	--glob '*.{go,proto,ts,vue,yaml}' \
	--glob '!**/*_test.go' --glob '!**/*.test.ts' || true)

while IFS= read -r match; do
	violations+=("${match}: Strategy-to-Trade contracts must carry final target quantities")
done < <(rg -n 'target_weight|capital_amount|channel_id' \
	modules/strategy packages/tradeeventpb modules/trade web/src/api/trade \
	--glob '*.{go,proto,py,ts,vue,yaml}' \
	--glob '!**/*_test.go' --glob '!**/test/**' --glob '!**/tests/**' \
	--glob '!**/test_*.py' --glob '!**/*.test.ts' || true)

# quote_asset remains valid Exchange instrument metadata; it is forbidden only
# as a Strategy target-budget field by the typed TargetIntent contract.
current_trade_docs=(
	modules/trade/README.md
	modules/trade/DESIGN.md
	modules/trade/docs/exchange-apis.md
	docs/架构总览.md
	docs/策略模块架构设计.md
)
while IFS= read -r match; do
	violations+=("${match}: current Trade documentation references removed architecture")
done < <(rg -n \
	'TradeChannel|RebalanceRequested|RebalanceRun|RebalanceLeg|execution_plan|execution_slice|reconciliation|Saga|trade\.rebalance\.requested|RebalanceSvc|channel_id|TargetWeights|GroupTargetWeights|ExecutionPort' \
	"${current_trade_docs[@]}" || true)

while IFS= read -r match; do
	violations+=("${match}: package eventcontract hides a mapper, publisher, or consumer role")
done < <(rg -n '^package eventcontract$' modules --glob '*.go' || true)

while IFS= read -r match; do
	violations+=("${match}: transport-named business Consumer API is not allowed")
done < <(rg -n '\b(type|func New)NATSConsumer\b|\btype NATSConfig\b' \
	modules --glob '*.go' --glob '!**/*_test.go' || true)

while IFS= read -r match; do
	violations+=("${match}: eventconsumer packages must not declare Publisher implementations")
done < <(rg -n '^type [A-Za-z0-9_]*Publisher struct' \
	modules --glob '**/eventconsumer/*.go' --glob '!**/*_test.go' || true)

# Delivery and ACK policy belong to an eventconsumer transport adapter, not to
# the owner-domain handler. Collector taskrunner and Storage observability are
# explicit transport/telemetry exceptions rather than domain handlers.
while IFS= read -r match; do
	violations+=("${match}: JetStream delivery policy must stay inside an eventconsumer transport package")
done < <(rg -n 'jetstream\.(Delivery|HandlerResult)' modules \
	--glob '*.go' --glob '!**/*_test.go' --glob '!**/eventconsumer/**' \
	--glob '!modules/collector/internal/taskrunner/**' \
	--glob '!modules/storage/internal/observability/**' || true)

# packages/events is the sole owner of event names, streams, and subject
# patterns. Business modules consume Registry metadata instead of redeclaring it.
go test scripts/check-event-topology-declarations.go scripts/check-event-topology-declarations_test.go >/dev/null
while IFS= read -r match; do
	violations+=("${match}: Registry-owned event topology must not be redeclared in a business module")
done < <(go run scripts/check-event-topology-declarations.go modules)

event_legacy_symbols='DatasetRowsUpsertedSubjectPrefix|DatasetRowsUpsertedSubject|ParseDatasetRowsUpsertedSubject|ToSharedRows|ToLocalRows|NATSConsumer'
while IFS= read -r match; do
	violations+=("${match}: legacy event interaction symbol is not allowed")
done < <(rg -n "${event_legacy_symbols}" modules --glob '*.go' || true)

current_event_docs=(
	docs/架构总览.md
	docs/协议设计.md
	docs/存储层架构.md
	docs/策略模块架构设计.md
	docs/因子计算模块设计.md
	modules/archive/README.md
	modules/factor/README.md
	modules/factor/docs/realtime-verification.md
	modules/trade/README.md
)
while IFS= read -r match; do
	violations+=("${match}: current documentation references a legacy event interaction name")
done < <(rg -n 'internal/bus|internal/eventcontract|internal/consumer|NATSConsumer|ToSharedRows|ToLocalRows' "${current_event_docs[@]}" || true)

while IFS= read -r directory; do
	violations+=("${directory}: module-level tests must live under test/, not tests/")
done < <(find modules -mindepth 2 -maxdepth 2 -type d -name tests -print 2>/dev/null || true)

while IFS= read -r match; do
	file="${match%%:*}"
	rest="${match#*:}"
	line="${rest%%:*}"
	violations+=("${file}:${line}: official tRPC timer network does not support params, disable, or location options")
done < <(rg -n 'network:.*[?&](params|disable|location)=' modules --glob '*.yaml' || true)

if [[ -d packages/doctor ]]; then
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: packages/doctor must remain a pure check and report package")
	done < <(rg -n '"(github.com/mooyang-code/moox/modules/|trpc.group/|gorm.io/|github.com/prometheus/)' packages/doctor --glob '*.go' || true)
fi

required_timer_services=(
	"modules/admin/config/trpc_go.yaml:trpc.dnsproxy.timer"
	"modules/admin/config/trpc_go.yaml:trpc.dnsprobe.timer"
	"modules/collector/config/trpc_go.yaml:trpc.moox.collector.schedule.timer"
)
for required in "${required_timer_services[@]}"; do
	file="${required%%:*}"
	service="${required#*:}"
	if ! rg -q --fixed-strings "name: ${service}" "${file}"; then
		violations+=("${file}: required timer service ${service} is not configured")
	fi
done

if (( ${#violations[@]} > 0 )); then
	{
		echo "package boundary violations:"
		printf '  %s\n' "${violations[@]}"
	} >&2
	exit 1
fi

echo "==> package boundaries passed"
