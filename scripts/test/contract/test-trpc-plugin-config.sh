#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
cd "${ROOT}"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_text() {
  local file="$1" text="$2" description="$3"
  grep -Fq -- "${text}" "${file}" || fail "${description}: ${file}"
}

assert_loopback_server_services() {
	local config="$1"
	awk '
	function check_service() {
		if (name != "" && protocol != "timer" && ip != "127.0.0.1") {
			printf "%s: %s uses ip %s\n", FILENAME, name, (ip == "" ? "<default>" : ip)
			failed = 1
		}
	}
	/^server:$/ { in_server = 1; next }
	in_server && /^(client|plugins):$/ { check_service(); in_server = 0; in_services = 0; next }
	in_server && /^  service:$/ { in_services = 1; next }
	in_server && in_services && /^    - name: / { check_service(); name = $3; ip = ""; protocol = ""; next }
	in_server && in_services && /^      ip: / { ip = $2; next }
	in_server && in_services && /^      protocol: / { protocol = $2; next }
	END { if (in_server) check_service(); exit failed }
	' "${config}" || fail "non-timer server listener is not loopback-only: ${config}"
}

modules=(admin archive cloudnode collector eventbus factor hostagent monitor storage strategy trade)
validation_modules=(admin cloudnode collector eventbus factor hostagent monitor storage strategy trade)

contains() {
  local needle="$1"
  shift
  local item
  for item in "$@"; do
    [[ "${item}" == "${needle}" ]] && return 0
  done
  return 1
}

for module in "${modules[@]}"; do
	modfile="modules/${module}/go.mod"
	main="modules/${module}/cmd/server/main.go"
	configs=("modules/${module}/config/trpc_go.yaml")
	if [[ "${module}" == storage ]]; then
		configs=(modules/storage/config/trpc_go*.yaml)
	fi

	[[ -f "${modfile}" ]] || fail "missing go.mod for ${module}"
	[[ -f "${main}" ]] || fail "missing server entrypoint for ${module}"

	require_text "${modfile}" 'trpc.group/trpc-go/trpc-filter/recovery v1.0.0' "missing recovery dependency for ${module}"
	require_text "${main}" 'trpc.group/trpc-go/trpc-filter/recovery' "missing recovery registration for ${module}"
	require_text "${main}" 'packages/healthz/trpcrecovery' "missing sanitized recovery registration for ${module}"
	require_text "${modfile}" 'trpc.group/trpc-go/trpc-metrics-prometheus v1.0.0' "missing prometheus dependency for ${module}"
	require_text "${main}" 'trpc.group/trpc-go/trpc-metrics-prometheus' "missing prometheus registration for ${module}"
	if [[ "${module}" != collector ]]; then
		require_text "${modfile}" 'trpc.group/trpc-go/trpc-log-cls v1.0.0' "missing CLS dependency for ${module}"
		require_text "${main}" 'trpc.group/trpc-go/trpc-log-cls' "missing CLS registration for ${module}"
	fi
	if [[ "${module}" == archive ]]; then
		require_text "modules/archive/internal/bootstrap/app.go" 'packages/healthz/trpclog' "missing CLS service identity helper for ${module}"
		require_text "modules/archive/internal/bootstrap/app.go" "InstallServiceName(\"${module}\")" "missing CLS service identity installation for ${module}"
	else
		require_text "${main}" 'packages/healthz/trpclog' "missing CLS service identity helper for ${module}"
		require_text "${main}" "InstallServiceName(\"${module}\")" "missing CLS service identity installation for ${module}"
	fi

	for config in "${configs[@]}"; do
		[[ -f "${config}" ]] || fail "missing tRPC config for ${module}"
		require_text "${config}" '    - recovery' "missing recovery filter for ${module}"
		require_text "${config}" '    - prometheus' "missing prometheus filter for ${module}"
		require_text "${config}" '  metrics:' "missing metrics plugin namespace for ${module}"
		require_text "${config}" '    prometheus:' "missing prometheus plugin config for ${module}"
		require_text "${config}" '      ip: 127.0.0.1' "prometheus listener must remain loopback-only for ${module}"
		require_text "${config}" '      enablepush: false' "prometheus push must stay disabled for ${module}"
		if contains "${module}" "${validation_modules[@]}"; then
			require_text "${config}" '    - validation' "missing validation filter for ${module}"
			require_text "${config}" '      enable_error_log: false' "validation request logging must be disabled for ${module}"
		fi
		if [[ "${module}" == storage ]]; then
			require_text "${config}" '    - opentelemetry' "missing staged OpenTelemetry filter for storage"
		fi
	done

	if contains "${module}" "${validation_modules[@]}"; then
		require_text "${modfile}" 'trpc.group/trpc-go/trpc-filter/validation v1.0.1' "missing validation dependency for ${module}"
		require_text "${main}" 'trpc.group/trpc-go/trpc-filter/validation' "missing validation registration for ${module}"
	fi
done

for module in admin trade; do
	require_text "modules/${module}/cmd/server/main.go" 'trpc.group/trpc-go/trpc-filter/masking' "missing masking registration for ${module}"
	require_text "modules/${module}/config/trpc_go.yaml" '    - masking' "missing masking filter for ${module}"
done

require_text packages/trpcretry/go.mod 'trpc.group/trpc-go/trpc-filter/slime v1.0.0' 'missing shared bounded read retry dependency'
require_text modules/factor/internal/storageio/client.go 'client.WithFilter(trpcretry.ReadOnly())' 'factor retry must remain scoped to the read call'
require_text modules/archive/internal/backfill/backfill.go 'client.WithFilter(trpcretry.ReadOnly())' 'archive retry must remain scoped to the read call'
require_text modules/monitor/internal/hostmetrics/storage_reader.go 'client.WithFilter(trpcretry.ReadOnly())' 'host metrics retry must remain scoped to the read call'
require_text modules/monitor/internal/metrics/storage.go 'client.WithFilter(trpcretry.ReadOnly())' 'metrics history retry must remain scoped to the read call'
require_text modules/factor/go.mod 'trpc.group/trpc-go/trpc-filter/transinfo-blocker v1.0.0' 'missing metadata boundary dependency'
require_text modules/factor/config/trpc_go.yaml '        mode: whitelist' 'metadata boundary must use an allowlist'
if grep -Fq -- '          - baggage' modules/factor/config/trpc_go.yaml; then
	fail 'arbitrary W3C baggage must not cross the Factor to Storage boundary'
fi
require_text packages/healthz/trpcotel/filter.go 'intentionally never records request or response bodies' 'tracing adapter must document body suppression'
require_text packages/healthz/trpcotel/filter_test.go 'TestFiltersNeverAttachBodies' 'tracing adapter must test body suppression'
require_text modules/admin/cmd/server/main.go 'packages/healthz/trpcotel' 'missing admin tracing registration'
require_text modules/storage/cmd/server/main.go 'packages/healthz/trpcotel' 'missing storage tracing registration'

if rg -n '^  telemetry:$' modules/*/config/trpc_go*.yaml; then
	fail 'local OpenTelemetry adapter is environment-configured and must not declare an unregistered telemetry plugin'
fi

if rg -n 'filterextensions' modules/*/config/trpc_go*.yaml modules/*/cmd/server/main.go; then
	fail 'filterextensions must remain deferred until method policies diverge'
fi

for config in modules/*/config/trpc_go*.yaml; do
	assert_loopback_server_services "${config}"
done

if rg -n '^  prometheus:$' modules/*/config/trpc_go.yaml; then
  fail 'prometheus must be nested under plugins.metrics.prometheus'
fi

if rg -n -g '*.yaml' -g '*.yml' -g '*.env' '^\s*(secret_id|secret_key|authorization|admin_jwt_secret):\s*[A-Za-z0-9][^[:space:]]*' modules deploy scripts; then
  fail 'committed remote logging credential found'
fi

require_text scripts/deploy/deploy-moox.sh '--enable-cls' 'deployment must expose explicit CLS activation'
require_text scripts/deploy/deploy-moox.sh 'source "${ROOT}/secrets/cls.env"' 'CLS credentials must come from target runtime environment'
require_text scripts/deploy/deploy-moox.sh 'source "${ROOT}/secrets/otel.env"' 'OTel endpoint must come from target runtime environment'
require_text scripts/deploy/deploy-moox.sh '--cloud-account-id' 'deployment must allow explicit CLS cloud account selection'
require_text scripts/deploy/deploy-moox.sh 'prepare_cls_preflight' 'deployment must prepare CLS before sync'
require_text skills/moox/scripts/cls-bootstrap.sh 'ops tencent cls prepare' 'MooX Skill must prepare CLS through moox-cli'
require_text skills/moox/scripts/cls-bootstrap.sh 'topic_id: ${MOOX_CLS_TOPIC_ID}' 'staged CLS writer must use the generated Topic ID resource'
require_text skills/moox/scripts/cls-bootstrap.sh 'MOOX_CLS_LOGSET_ID' 'CLS bootstrap must persist generated resource metadata'
require_text skills/moox/scripts/cls-bootstrap.sh 'MOOX_CLS_ACCOUNT_ID' 'CLS bootstrap must persist selected cloud account metadata'
require_text scripts/deploy/deploy-moox.sh 'source "${ROOT}/config/resources.env"' 'lifecycle must load generated resource metadata'
require_text skills/moox/scripts/cls-bootstrap.sh 'secret_id: \${MOOX_CLS_SECRET_ID}' 'staged CLS writer must retain credential placeholders'
require_text skills/moox/scripts/cls-bootstrap.sh '*/factor/config/trpc_go*.yaml' 'Factor CLS rendering must cover the service config'
require_text skills/moox/scripts/cls-bootstrap.sh 'level: " level' 'Factor CLS rendering must retain calculation info logs'
bash skills/moox/scripts/test-cls-query.sh >/dev/null
require_text 'docs/运维/tRPC插件运行基线.md' 'config/resources.env' 'operations baseline must document generated CLS resource metadata'
require_text 'docs/运维/tRPC插件运行基线.md' '${MOOX_CLS_SECRET_ID}' 'operations baseline must name the CLS secret-id placeholder'
require_text 'docs/运维/tRPC插件运行基线.md' '${MOOX_CLS_SECRET_KEY}' 'operations baseline must name the CLS secret-key placeholder'
require_text 'docs/运维/tRPC插件运行基线.md' 'service_name' 'operations baseline must document the CLS service identity field'
require_text 'docs/运维/tRPC插件运行基线.md' 'release archive sync or service shutdown' 'operations baseline must distinguish helper upload from release sync'
require_text skills/moox/SKILL.md 'architecture-matched `moox-cli` helper solely for preflight' 'MooX Skill must document temporary preflight helper cleanup'
require_text 'docs/运维/tRPC插件运行基线.md' '${MOOX_CLS_TOPIC_ID}' 'operations baseline must document the generated Topic ID placeholder'

printf 'PASS: tRPC plugin configuration and registration matrix\n'
