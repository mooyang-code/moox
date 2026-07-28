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

import trace "example.com/trace"

type ExchangeAccount struct{}
type ExchangeAdapter struct{}
type ExchangeOrder struct{}
type List[T any] []T

// OpenTelemetry TracerProvider is an unrelated third-party Provider type.
type TracerProvider struct{}
type ConfigProvider struct{}

// Trade runtime uses OpenTelemetry TracerProvider.
var tradeTracerProvider trace.TracerProvider

func NewTradeRuntime(provider trace.TracerProvider) {}
func NewTradeConfig(provider ConfigProvider)         {}
func NewTradeComposite(
	providers []trace.TracerProvider,
	array [2]*trace.TracerProvider,
	generic List[trace.TracerProvider],
) {}

func BuildTradeRuntime() {
	tradeTracerProvider := trace.TracerProvider(nil)
}
EOF

cat >"${TMP}/modules/trade/internal/secretclient/client.go" <<'EOF'
package secretclient

type ExchangeSecretWire struct {
	Provider string `json:"provider,omitempty"`
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

export interface TradeClient {
  provider?: ConfigProvider;
  providers?: ConfigProvider[];
}
EOF

cat >"${TMP}/web/src/api/trade/accepted.yaml" <<'EOF'
telemetry:
  tracer_provider: otel
EOF

cat >"${TMP}/web/src/api/trade/accepted.md" <<'EOF'
Vue ConfigProvider configures the view.
Trade runtime uses OpenTelemetry TracerProvider.
Trade UI uses Vue ConfigProvider.
EOF

cat >"${TMP}/modules/trade/internal/bad.go" <<'EOF'
package trade

type ExchangeProvider struct{}

func SyncExchangeAccounts(provider string) string {
	return provider
}

var providerName = "binance"
EOF

cat >"${TMP}/modules/trade/internal/bad_interface.go" <<'EOF'
package trade

type ExchangeAdapter interface {
	Provider()
	Broker()
	Venue()
	Platform()
	List(provider string)
}
EOF

cat >"${TMP}/modules/trade/internal/bad_receiver.go" <<'EOF'
package trade

type ExchangeAccount struct{}

func (*ExchangeAccount) Provider() {}
func (*ExchangeAccount) Broker()   {}
func (*ExchangeAccount) Venue()    {}
func (*ExchangeAccount) Platform() {}
func (*ExchangeAccount) Sync(provider string) {}
EOF

cat >"${TMP}/modules/trade/internal/bad_okx.go" <<'EOF'
package trade

type OKXProvider struct{}
type OKXBroker struct{}
type OKXVenue struct{}
type OKXPlatform struct{}
EOF

cat >"${TMP}/modules/trade/internal/bad_third_party_name.go" <<'EOF'
package trade

type TracerProvider struct{}

type ExchangeAccount struct {
	ExchangeProvider TracerProvider
}

var ExchangeProvider TracerProvider

func SyncExchange(exchangeProvider TracerProvider) {}
EOF

cat >"${TMP}/modules/trade/internal/bad_type_expression.go" <<'EOF'
package trade

type Provider interface{}

type ExchangeAccount struct {
	Source Provider
}

type ExchangeAdapter interface {
	Provider
}

func SyncExchange(source Provider) {}
EOF

cat >"${TMP}/modules/trade/internal/bad_local_var.go" <<'EOF'
package trade

func SyncExchange() {
	var provider = "binance"
}
EOF

cat >"${TMP}/modules/trade/internal/bad_type_specs.go" <<'EOF'
package trade

type Provider interface{}

type ExchangeResolver func(source Provider)
type ExchangeSource Provider
EOF

cat >"${TMP}/modules/trade/internal/bad_short_decl.go" <<'EOF'
package trade

func BuildTradeRuntime() {
	ExchangeProvider := trace.TracerProvider(nil)
}
EOF

cat >"${TMP}/modules/trade/internal/secretclient/bad_secretclient.go" <<'EOF'
package secretclient

func ResolveExchange(provider string) string {
	return provider
}
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

cat >"${TMP}/web/src/api/trade/bad_third_party_name.ts" <<'EOF'
export interface TradeClient {
  exchangeProvider?: ConfigProvider;
}
EOF

cat >"${TMP}/web/src/api/trade/bad.yaml" <<'EOF'
trade_provider: binance
EOF

cat >"${TMP}/web/src/api/trade/bad.md" <<'EOF'
Trade Platform: OKX
EOF

if (
  cd "${TMP}"
  "${CHECKER}" modules/trade packages/tradeeventpb web/src/api/trade
) >"${TMP}/bad.out" 2>&1; then
  echo "checker accepted forbidden Exchange terminology" >&2
  exit 1
fi

missing_fixture=false
for fixture in \
  bad.go bad_interface.go bad_receiver.go bad_okx.go bad_third_party_name.go \
  bad_type_expression.go bad_local_var.go bad_type_specs.go bad_short_decl.go \
  bad_secretclient.go bad.proto bad.ts bad_third_party_name.ts \
  bad.yaml bad.md; do
  if ! rg -q --fixed-strings "${fixture}:" "${TMP}/bad.out"; then
    echo "checker did not report ${fixture}" >&2
    missing_fixture=true
  fi
done
if [[ "${missing_fixture}" == true ]]; then
  cat "${TMP}/bad.out" >&2
  exit 1
fi
for expected in \
  bad_interface.go:4 bad_interface.go:5 bad_interface.go:6 bad_interface.go:7 bad_interface.go:8 \
  bad_receiver.go:5 bad_receiver.go:6 bad_receiver.go:7 bad_receiver.go:8 bad_receiver.go:9 \
  bad_okx.go:3 bad_okx.go:4 bad_okx.go:5 bad_okx.go:6 \
  bad_third_party_name.go:6 bad_third_party_name.go:9 bad_third_party_name.go:11 \
  bad_type_expression.go:6 bad_type_expression.go:10 bad_type_expression.go:13 \
  bad_local_var.go:4 bad_type_specs.go:5 bad_type_specs.go:6 bad_short_decl.go:4 \
  bad_third_party_name.ts:2; do
  rg -q --fixed-strings "${expected}:" "${TMP}/bad.out" || {
    echo "checker did not report ${expected}" >&2
    cat "${TMP}/bad.out" >&2
    exit 1
  }
done

rm \
  "${TMP}/modules/trade/internal/bad.go" \
  "${TMP}/modules/trade/internal/bad_interface.go" \
  "${TMP}/modules/trade/internal/bad_receiver.go" \
  "${TMP}/modules/trade/internal/bad_okx.go" \
  "${TMP}/modules/trade/internal/bad_third_party_name.go" \
  "${TMP}/modules/trade/internal/bad_type_expression.go" \
  "${TMP}/modules/trade/internal/bad_local_var.go" \
  "${TMP}/modules/trade/internal/bad_type_specs.go" \
  "${TMP}/modules/trade/internal/bad_short_decl.go" \
  "${TMP}/modules/trade/internal/secretclient/bad_secretclient.go" \
  "${TMP}/packages/tradeeventpb/bad.proto" \
  "${TMP}/web/src/api/trade/bad.ts" \
  "${TMP}/web/src/api/trade/bad_third_party_name.ts" \
  "${TMP}/web/src/api/trade/bad.yaml" \
  "${TMP}/web/src/api/trade/bad.md"

(
  cd "${TMP}"
  "${CHECKER}" modules/trade packages/tradeeventpb web/src/api/trade
)
"${CHECKER}" "${TMP}/modules/trade" "${TMP}/packages/tradeeventpb" "${TMP}/web/src/api/trade"

echo "trade Exchange terminology fixtures passed"
