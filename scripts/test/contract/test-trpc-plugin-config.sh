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

# Linux 下 tRPC 对 tcp+trpc 服务默认启用 tnet，其空闲定时器会把每 30～60 秒才复用一次的连接提前关掉，
# 调用方随后读到 ReadFrame EOF（171）。所有 tcp+trpc 服务端必须显式声明 transport: go-net；
# 检查用 tRPC 自己的配置加载，按框架默认值和 server 级继承判断最终生效的传输层。
(cd packages/healthz && go run ./cmd/trpctransportcheck ../../modules) || fail "tcp+trpc server must declare transport: go-net"

modules=(admin cloudfunction collector eventbus factor hostagent monitor storage strategy trade)
validation_modules=(admin cloudfunction collector eventbus factor hostagent monitor storage strategy trade)

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
	if [[ "${module}" == factor ]]; then
		main="modules/factor/cmd/mgr/main.go"
	fi
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
	require_text "${main}" 'packages/healthz/trpclog' "missing CLS service identity helper for ${module}"
	require_text "${main}" "InstallServiceName(\"${module}\")" "missing CLS service identity installation for ${module}"

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

# Validate the project's bounded retry behavior rather than requiring the
# removed Slime module, whose old dependency chain is no longer used.
(cd packages/trpcretry && go test -count=1 ./...)
# 只读重试目前只有主机网关转发在用，且只挂在只读、非 GatewayControl 的路由上。
require_text modules/hostgateway/internal/router/upstream.go 'if route.ReadOnly && !control {' 'gateway retry must remain scoped to read-only routes'
require_text modules/hostgateway/internal/router/upstream.go 'client.WithFilter(trpcretry.ReadOnly())' 'gateway read-only routes must use the bounded retry'
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

# Access 是对外签名入口（11004），按设计监听所有地址；其余服务端只监听回环。
for config in modules/*/config/trpc_go*.yaml; do
	[[ "${config}" == modules/access/config/trpc_go.yaml ]] && continue
	assert_loopback_server_services "${config}"
done

if rg -n '^  prometheus:$' modules/*/config/trpc_go.yaml; then
  fail 'prometheus must be nested under plugins.metrics.prometheus'
fi

if rg -n -g '*.yaml' -g '*.yml' -g '*.env' '^\s*(secret_id|secret_key|authorization|admin_jwt_secret):\s*[A-Za-z0-9][^[:space:]]*' modules deploy scripts; then
  fail 'committed remote logging credential found'
fi

bash skills/moox/scripts/test-cls-query.sh >/dev/null

printf 'PASS: tRPC plugin configuration and registration matrix\n'
