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

modules=(admin collector eventbus factor hostagent monitor storage strategy trade)
validation_modules=(admin collector eventbus factor hostagent monitor storage strategy trade)

contains() {
  local needle="$1"
  shift
  local item
  for item in "$@"; do
    [[ "${item}" == "${needle}" ]] && return 0
  done
  return 1
}

[[ -f go.mod ]] || fail "missing root go.mod"
require_text go.mod 'trpc.group/trpc-go/trpc-filter/recovery v1.0.0' "missing recovery dependency in root go.mod"
require_text go.mod 'trpc.group/trpc-go/trpc-metrics-prometheus v1.0.0' "missing prometheus dependency in root go.mod"
require_text go.mod 'trpc.group/trpc-go/trpc-filter/validation v1.0.1' "missing validation dependency in root go.mod"

for module in "${modules[@]}"; do
	main="modules/${module}/cmd/server/main.go"
	if [[ "${module}" == factor ]]; then
		main="modules/factor/cmd/mgr/main.go"
	fi
	configs=("modules/${module}/config/trpc_go.yaml")
	if [[ "${module}" == storage ]]; then
		configs=(modules/storage/config/trpc_go*.yaml modules/storage/config/storage_view/trpc_go*.yaml)
	fi

	[[ -f "${main}" ]] || fail "missing server entrypoint for ${module}"

	require_text "${main}" 'trpc.group/trpc-go/trpc-filter/recovery' "missing recovery registration for ${module}"
	require_text "${main}" 'packages/healthz/trpcrecovery' "missing sanitized recovery registration for ${module}"
	require_text "${main}" 'trpc.group/trpc-go/trpc-metrics-prometheus' "missing prometheus registration for ${module}"
	# 服务的 main 空白引入 logcls：重要日志发给 Monitor，启用了 CLS 时另外上报。它在代码里给默认日志加上 moox 输出，
	# 所以配置文件里不能出现 writer: moox（线上的旧二进制不认识它，回滚二进制时会起不来）。
	# 主机代理不上报重要日志（它没有指标上报，主机停报由主机失联告警覆盖）。
	if [[ "${module}" != hostagent ]]; then
		require_text "${main}" 'packages/clsreporter/logcls' "missing log sink registration for ${module}"
	fi
	if grep -rn 'writer: moox' "modules/${module}/config" >/dev/null 2>&1; then
		fail "modules/${module}/config must not list writer: moox; the sink is installed in code"
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
	done

	if contains "${module}" "${validation_modules[@]}"; then
		require_text "${main}" 'trpc.group/trpc-go/trpc-filter/validation' "missing validation registration for ${module}"
	fi
done

# 已摘除未使用的 trpc-filter/masking 与空转的 packages/healthz/trpcotel，禁止回流。
if grep -Fq -- 'trpc.group/trpc-go/trpc-filter/masking' go.mod; then
	fail 'root go.mod must not depend on unused trpc.group/trpc-go/trpc-filter/masking'
fi
if rg -n 'trpc\.group/trpc-go/trpc-filter/masking' modules packages --glob '*.go' 2>/dev/null; then
	fail 'Go sources must not import removed trpc-filter/masking'
fi
if rg -n '^[[:space:]]*-[[:space:]]*masking[[:space:]]*$' modules/*/config/trpc_go*.yaml modules/*/config/*/trpc_go*.yaml 2>/dev/null; then
	fail 'tRPC configs must not declare removed masking filter'
fi
if [[ -e packages/healthz/trpcotel ]]; then
	fail 'removed no-op package packages/healthz/trpcotel must not exist'
fi
if rg -n 'packages/healthz/trpcotel' modules packages --glob '*.go' 2>/dev/null; then
	fail 'Go sources must not import removed packages/healthz/trpcotel'
fi
if rg -n '^[[:space:]]*-[[:space:]]*opentelemetry[[:space:]]*$' modules/*/config/trpc_go*.yaml modules/*/config/*/trpc_go*.yaml 2>/dev/null; then
	fail 'tRPC configs must not declare removed opentelemetry filter'
fi

# 主机网关只转发一次：只读调用的重发由调用方 gatewayclient 负责，网关再重试会放大上游请求量。
if grep -Fq -- 'client.WithFilter(' modules/hostgateway/internal/router/upstream.go; then
	fail 'host gateway upstream must not install client retry filters'
fi
if grep -Fq -- '          - baggage' modules/factor/config/trpc_go.yaml; then
	fail 'arbitrary W3C baggage must not cross the Factor to Storage boundary'
fi

# 矩阵之外的常驻服务同样要引入 logcls。
for main in modules/archive/cmd/mgr/main.go modules/collector/cmd/stream/main.go; do
	[[ -f "${main}" ]] || fail "missing server entrypoint ${main}"
	require_text "${main}" 'packages/clsreporter/logcls' "missing log sink registration"
done

# 各服务已经不再引入 trpc-log-cls，配置里再出现 writer: cls 会让进程在装配日志时直接退出。
if rg -n 'writer: (cls|moox)\b' modules/*/config deploy 2>/dev/null; then
	fail 'tRPC configs must not list writer: cls (no longer registered) or writer: moox (installed in code)'
fi

if rg -n '^  telemetry:$' modules/*/config/trpc_go*.yaml modules/*/config/*/trpc_go*.yaml; then
	fail 'tRPC configs must not declare an unregistered telemetry plugin'
fi

if rg -n 'filterextensions' modules/*/config/trpc_go*.yaml modules/*/config/*/trpc_go*.yaml modules/*/cmd/server/main.go; then
	fail 'filterextensions must remain deferred until method policies diverge'
fi

# tRPC 框架配置里的服务端只监听回环；对外的端口（11003、外部入口 11004）由主机网关自己的 app.yaml 配置。
for config in modules/*/config/trpc_go*.yaml modules/*/config/*/trpc_go*.yaml; do
	assert_loopback_server_services "${config}"
done

if rg -n '^  prometheus:$' modules/*/config/trpc_go*.yaml modules/*/config/*/trpc_go*.yaml; then
  fail 'prometheus must be nested under plugins.metrics.prometheus'
fi

if rg -n -g '*.yaml' -g '*.yml' -g '*.env' '^\s*(secret_id|secret_key|authorization|admin_jwt_secret):\s*[A-Za-z0-9][^[:space:]]*' modules deploy scripts; then
  fail 'committed remote logging credential found'
fi

bash skills/moox/scripts/test-cls-query.sh >/dev/null

printf 'PASS: tRPC plugin configuration and registration matrix\n'
