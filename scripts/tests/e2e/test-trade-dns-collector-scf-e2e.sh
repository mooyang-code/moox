#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"

if [[ "${MOOX_RUN_REAL_TRADE_DNS_E2E:-0}" != "1" ]]; then
  printf 'trade DNS/Collector E2E: set MOOX_RUN_REAL_TRADE_DNS_E2E=1 to run against production\n'
  exit 0
fi

: "${MOOX_GATEWAY_SERVICE_KEY_ID:?MOOX_GATEWAY_SERVICE_KEY_ID is required}"
: "${MOOX_GATEWAY_CALLER:?MOOX_GATEWAY_CALLER is required}"
: "${MOOX_GATEWAY_SERVICE_SECRET_KEY:?MOOX_GATEWAY_SERVICE_SECRET_KEY is required}"
: "${MOOX_COLLECTOR_SSH:?MOOX_COLLECTOR_SSH is required for live Collector refresh/readback}"
: "${MOOX_CLOUDNODE_SSH:?MOOX_CLOUDNODE_SSH is required for CloudNode readback}"

cd "${REPO_ROOT}"
go test ./modules/trade/test -run '^TestResolveDomainsProductionE2E$' -count=1 -v

expected_hash_file="$(mktemp "${TMPDIR:-/tmp}/moox-trade-dns-hash.XXXXXX")"
trap 'rm -f "${expected_hash_file}"' EXIT
MOOX_EXPECTED_DNS_HASH_FILE="${expected_hash_file}" \
  go test ./modules/collector/test -run '^TestTradeDNSCollectorEnvironmentProductionE2E$' -count=1 -v
read -r expected_hash expected_domain_count <"${expected_hash_file}"
if [[ ! "${expected_hash}" =~ ^[0-9a-f]{16}$ || ! "${expected_domain_count}" =~ ^[1-9][0-9]*$ ]]; then
  echo "Collector E2E did not produce a valid expected DNS hash" >&2
  exit 1
fi

# Restarting the live Collector is the explicit refresh/reconcile trigger for
# this opt-in production check. It makes the readback prove the current Trade
# snapshot was submitted, instead of accepting an old uniformly stale hash.
if [[ -n "${MOOX_COLLECTOR_SSH:-}" ]]; then
  collector_dir="${MOOX_COLLECTOR_DEPLOY_DIR:-/home/ubuntu/moox/prod}"
  printf -v restart_command 'cd %q && ./restart.sh collector' "${collector_dir}"
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${MOOX_COLLECTOR_SSH}" "${restart_command}"
fi

# DNS probe latency and provider answers can legitimately change between the
# local RPC assertion above and the live Collector's first refresh. Read the
# hash from the restarted Collector health payload and use that exact in-memory
# snapshot as the propagation target; the local hash remains useful as a
# diagnostic, but must not make this cross-process check flaky.
live_hash=""
if [[ -n "${MOOX_COLLECTOR_SSH:-}" ]]; then
  printf -v collector_dir_quoted '%q' "${collector_dir}"
  health_command="cd ${collector_dir_quoted} && set -a && source secrets/health-auth.env && set +a && timestamp=\$(date +%s) && nonce=\$(openssl rand -hex 32) && body_hash=\$(printf '' | openssl dgst -sha256 | awk '{print \$NF}') && canonical=\$(printf 'moox-request-v1\\nGET\\n/healthz\\n%s\\n%s\\n%s' \"\$body_hash\" \"\$timestamp\" \"\$nonce\") && signature=\$(printf '%s' \"\$canonical\" | openssl dgst -sha256 -hmac \"\$MOOX_HEALTH_AUTH_SECRET_KEY\" | awk '{print \$NF}') && auth=\"\$MOOX_HEALTH_AUTH_VERSION/\$MOOX_HEALTH_AUTH_ACCESS_KEY/\$timestamp/\$nonce/\$signature\" && curl -fsS --max-time 10 -H \"X-Moox-Health-Auth: \$auth\" http://127.0.0.1:11412/healthz"
  for health_attempt in $(seq 1 12); do
    if health_json=$(ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 "${MOOX_COLLECTOR_SSH}" "${health_command}" 2>/dev/null) &&
      health_fields=$(printf '%s' "${health_json}" | python3 -c 'import json,sys; x=json.load(sys.stdin).get("details", {}).get("dns_resolver", {}); print("|".join([str(x.get("managed_hash", "")), str(x.get("source", "")), str(x.get("route_count", "")), str(x.get("last_error_category", ""))]))' 2>/dev/null) &&
      IFS='|' read -r live_hash live_source live_route_count live_error_category <<<"${health_fields}" &&
      [[ "${live_hash}" =~ ^[0-9a-f]{16}$ && "${live_source}" == "trade" && "${live_route_count}" == "${expected_domain_count}" && -z "${live_error_category}" ]]; then
      break
    fi
    live_hash=""
    [[ "${health_attempt}" == "12" ]] || sleep 2
  done
  [[ -n "${live_hash}" ]] || { echo "Collector health did not expose a Trade-sourced DNS snapshot with the expected domain count after restart" >&2; exit 1; }
  if [[ "${live_hash}" != "${expected_hash}" ]]; then
    printf 'Collector live hash differs from local RPC sample (local=%s live=%s); using live snapshot for propagation check\n' "${expected_hash}" "${live_hash}"
  fi
fi

# Read back the durable CloudNode metadata written by the live Collector
# reconciler. The live SSH host is required in production mode above; CI can
# still run this script without production mode and will skip the remote leg.
if [[ -n "${MOOX_CLOUDNODE_SSH:-}" ]]; then
	cloudnode_db="${MOOX_CLOUDNODE_DB_PATH:-/home/ubuntu/moox/prod/data/cloudnode/moox_cloudnode.db}"
	converged=0
	propagation_hash=""
	for attempt in $(seq 1 36); do
		health_ok=1
		# DNS answers can rotate while the asynchronous CloudNode batch is still
    # applying. Re-read the live Collector snapshot on every attempt so the
    # proof follows the current authoritative hash instead of failing against
		# a hash captured before a normal five-minute refresh.
		if [[ -n "${MOOX_COLLECTOR_SSH:-}" ]]; then
			health_ok=0
			if health_json=$(ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 "${MOOX_COLLECTOR_SSH}" "${health_command}" 2>/dev/null) &&
        health_fields=$(printf '%s' "${health_json}" | python3 -c 'import json,sys; x=json.load(sys.stdin).get("details", {}).get("dns_resolver", {}); print("|".join([str(x.get("managed_hash", "")), str(x.get("source", "")), str(x.get("route_count", "")), str(x.get("last_error_category", ""))]))' 2>/dev/null) &&
				IFS='|' read -r current_live_hash current_live_source current_live_route_count current_live_error_category <<<"${health_fields}" &&
				[[ "${current_live_hash}" =~ ^[0-9a-f]{16}$ && "${current_live_source}" == "trade" && "${current_live_route_count}" == "${expected_domain_count}" && -z "${current_live_error_category}" ]]; then
				health_ok=1
				if [[ "${current_live_hash}" != "${live_hash}" ]]; then
          printf 'Collector live DNS hash advanced from %s to %s while CloudNode propagation was pending\n' "${live_hash:-<empty>}" "${current_live_hash}"
        fi
        live_hash="${current_live_hash}"
			fi
		fi
		if [[ "${health_ok}" != "1" || -z "${live_hash}" ]]; then
			printf 'Collector health is not a valid Trade snapshot (attempt %s/36), retrying\n' "${attempt}"
			[[ "${attempt}" == "36" ]] || sleep 10
			continue
		fi
    # deployment_ready is retained when Collector republishes a Timer and
    # clears its runtime assignment/hash fields. Exclude only a node that has
    # an explicit provider readback saying its Timer is disabled; missing or
    # partially cleared readback must stay in the denominator and fail the
    # enabled/populated/hash checks instead of disappearing from the proof.
		propagation_hash="${live_hash}"
    sql="select count(*), coalesce(sum(case when json_extract(c_metadata,'\$.timer_enabled')=1 then 1 else 0 end),0), coalesce(sum(case when trim(coalesce(json_extract(c_metadata,'\$.dns_hash'),'')) <> '' then 1 else 0 end),0), count(distinct nullif(trim(json_extract(c_metadata,'\$.dns_hash')),'')), coalesce(sum(case when trim(coalesce(json_extract(c_metadata,'\$.dns_hash'),'')) = '${propagation_hash}' then 1 else 0 end),0) from t_cloud_nodes where c_is_deleted=0 and c_space_id='crypto' and c_trigger_type='timer' and json_extract(c_metadata,'\$.biz_type')='market_fetcher' and json_extract(c_metadata,'\$.deployment_ready')=1 and not (coalesce(json_extract(c_metadata,'\$.timer_actual_type'),'')='timer' and coalesce(json_extract(c_metadata,'\$.timer_actual_enabled'),1)=0)"
    printf -v query_command 'timeout 10s sqlite3 -separator %q %q %q' '|' "${cloudnode_db}" "${sql}"
    if ! readback=$(ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 "${MOOX_CLOUDNODE_SSH}" \
      "${query_command}" 2>/dev/null); then
      printf 'CloudNode readback unavailable (attempt %s/36), retrying\n' "${attempt}"
      [[ "${attempt}" == "36" ]] || sleep 10
      continue
    fi
    IFS='|' read -r total_timers enabled_timers populated_timers distinct_hashes matching_hashes <<<"${readback}"
    if [[ "${total_timers}" =~ ^[1-9][0-9]*$ && "${enabled_timers}" == "${total_timers}" && "${populated_timers}" == "${total_timers}" && "${distinct_hashes}" == "1" && "${matching_hashes}" == "${total_timers}" ]]; then
      printf 'CloudNode readback: deployed_timer_nodes=%s enabled=%s populated_dns_routes=%s distinct_dns_hashes=%s live_hash=%s\n' "${total_timers}" "${enabled_timers}" "${populated_timers}" "${distinct_hashes}" "${propagation_hash}"
      converged=1
      break
    fi
    printf 'CloudNode readback still rolling: deployed=%s enabled=%s populated=%s distinct=%s matching_live=%s (attempt %s/36)\n' "${total_timers:-0}" "${enabled_timers:-0}" "${populated_timers:-0}" "${distinct_hashes:-0}" "${matching_hashes:-0}" "${attempt}"
    [[ "${attempt}" == "36" ]] || sleep 10
  done
  [[ "${converged}" == "1" ]] || { echo "CloudNode readback did not converge to the live Collector DNS hash ${propagation_hash}" >&2; exit 1; }
fi

printf 'PASS: Trade ResolveDomains and Collector managed SCF environment E2E\n'
