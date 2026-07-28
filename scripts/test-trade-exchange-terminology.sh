#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
CHECKER="${TMP}/check-trade-exchange-terminology"
trap 'rm -rf "${TMP}"' EXIT

mkdir -p \
  "${TMP}/modules/trade/internal/secretclient" \
  "${TMP}/packages/tradeeventpb" \
  "${TMP}/web/src/api/trade"

go build -o "${CHECKER}" "${ROOT}/scripts/check-trade-exchange-terminology.go"

cat >"${TMP}/modules/trade/internal/accepted.go" <<'EOF'
package trade

type ExchangeAccount struct{}
type ExchangeAdapter struct{}
type ExchangeOrder struct{}

// OpenTelemetry TracerProvider is an unrelated third-party Provider type.
type TracerProvider struct{}
type ConfigProvider struct{}
EOF

cat >"${TMP}/modules/trade/internal/secretclient/client.go" <<'EOF'
package secretclient

type listSecretsRequest struct {
	Provider string `json:"provider"`
}
EOF

cat >"${TMP}/packages/tradeeventpb/accepted.proto" <<'EOF'
syntax = "proto3";

message ExchangeAccount {
  string exchange = 1;
}
EOF

cat >"${TMP}/packages/tradeeventpb/generated.pb.go" <<'EOF'
package tradeeventpb

type ExchangeProvider struct{}
EOF

cat >"${TMP}/web/src/api/trade/accepted.ts" <<'EOF'
export interface ExchangeOrder {
  exchange: string;
}
EOF

cat >"${TMP}/web/src/api/trade/accepted.yaml" <<'EOF'
telemetry:
  tracer_provider: otel
EOF

cat >"${TMP}/web/src/api/trade/accepted.md" <<'EOF'
Vue ConfigProvider configures the view.
EOF

cat >"${TMP}/modules/trade/internal/bad.go" <<'EOF'
package trade

type ExchangeProvider struct{}

func SyncExchangeAccounts(provider string) string {
	return provider
}

var providerName = "binance"
EOF

cat >"${TMP}/packages/tradeeventpb/bad.proto" <<'EOF'
syntax = "proto3";

message ExchangeAccount {
  string broker = 1;
}
EOF

cat >"${TMP}/web/src/api/trade/bad.ts" <<'EOF'
export interface ExchangeOrder {
  venue?: string;
}
EOF

cat >"${TMP}/web/src/api/trade/bad.yaml" <<'EOF'
trade_provider: binance
EOF

cat >"${TMP}/web/src/api/trade/bad.md" <<'EOF'
Trade Platform: OKX
EOF

if "${CHECKER}" "${TMP}/modules/trade" "${TMP}/packages/tradeeventpb" "${TMP}/web/src/api/trade" >"${TMP}/bad.out" 2>&1; then
  echo "checker accepted forbidden Exchange terminology" >&2
  exit 1
fi

for fixture in bad.go bad.proto bad.ts bad.yaml bad.md; do
  rg -q --fixed-strings "${fixture}:" "${TMP}/bad.out" || {
    echo "checker did not report ${fixture}" >&2
    cat "${TMP}/bad.out" >&2
    exit 1
  }
done

rm \
  "${TMP}/modules/trade/internal/bad.go" \
  "${TMP}/packages/tradeeventpb/bad.proto" \
  "${TMP}/web/src/api/trade/bad.ts" \
  "${TMP}/web/src/api/trade/bad.yaml" \
  "${TMP}/web/src/api/trade/bad.md"

"${CHECKER}" "${TMP}/modules/trade" "${TMP}/packages/tradeeventpb" "${TMP}/web/src/api/trade"

echo "trade Exchange terminology fixtures passed"
