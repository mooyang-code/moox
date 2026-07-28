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

import (
	sdktrace "example.com/sdktrace"
	trace "example.com/trace"
)

type ExchangeAccount struct{}
type ExchangeAdapter struct{}
type ExchangeOrder struct{}
type List[T any] []T

// OpenTelemetry TracerProvider is an unrelated third-party Provider type.
type TracerProvider struct{}
type ConfigProvider struct{}

// Trade runtime uses OpenTelemetry TracerProvider.
var tradeTracerProvider trace.TracerProvider = trace.TracerProvider(nil)

func NewTradeRuntime(provider trace.TracerProvider) {
	_ = provider
	var runtime struct {
		tracerProvider trace.TracerProvider
	}
	runtime.tracerProvider = provider
}
func NewTradeConfig(provider ConfigProvider)         {}
func TradeRun(provider trace.TracerProvider) error {
	provider, err := Load()
	use(provider)
	if provider != nil {
		use(provider)
	}
	switch provider {
	case nil:
	}
	for provider != nil {
		break
	}
	_ = provider
	return err
}
func TradeTelemetryValue(provider trace.TracerProvider) trace.TracerProvider {
	return provider
}
func TradeAsync(provider trace.TracerProvider, ch chan trace.TracerProvider) {
	go use(provider)
	defer use(provider)
	ch <- provider
	switch provider {
	case nil:
	}
}
func NewTradeComposite(
	providers []trace.TracerProvider,
	array [2]*trace.TracerProvider,
	generic List[trace.TracerProvider],
	indexed map[string]chan<- *List[trace.TracerProvider],
) {}

func BuildTradeRuntime() {
	tradeTracerProvider := trace.TracerProvider(nil)
	_ = tradeTracerProvider
	providers := make([]trace.TracerProvider, 0)
	_ = providers
	provider := sdktrace.NewTracerProvider()
	_ = provider
}

type TradeTelemetry[T trace.TracerProvider] struct {
	Tracer T
	Provider trace.TracerProvider
	providers []ConfigProvider
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

message ExchangeBraceText {
  string marker = 1 [json_name = "{"];
  // {
}

message Telemetry {
  string provider = 1;
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
  vueConfig?: Vue.ConfigProvider;
  configList?: ReadonlyArray<ConfigProvider>;
  configPromise?: Promise<ConfigProvider[]>;
}

export interface ExchangeBraceText {
  marker: "{";
  // {
}
const providerAfterBrace = "telemetry";

export function configureTrade(
  provider: Vue.ConfigProvider,
) {}

export type ExchangeID = string;
const provider = "telemetry";
EOF

cat >"${TMP}/web/src/api/trade/accepted.yaml" <<'EOF'
telemetry:
  tracer_provider: otel
"telemetry":
  provider: otel
EOF

cat >"${TMP}/web/src/api/trade/accepted.md" <<'EOF'
Vue ConfigProvider configures the view.
Trade runtime uses OpenTelemetry TracerProvider.
Trade UI uses Vue ConfigProvider.

## General notes
Provider is an unrelated term here.
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

cat >"${TMP}/modules/trade/internal/bad_lowercase.go" <<'EOF'
package trade

type exchangeprovider struct{}
type tradebroker struct{}
type binancevenue struct{}
type okxplatform struct{}
EOF

cat >"${TMP}/modules/trade/internal/bad_statements.go" <<'EOF'
package trade

import exchangeprovider "example.com/binanceprovider"

func CheckTrade(ch chan string) {
	go binance.Provider()
	defer use(exchangeProvider)
	ch <- exchangeProvider
	exchangeProvider++
	goto exchangeProviderLabel
exchangeProviderLabel:
	switch 1 {
	case exchangeProvider:
	}
}
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

cat >"${TMP}/modules/trade/internal/bad_third_party_identifier.go" <<'EOF'
package trade

type TracerProvider struct{}
type ExchangeTracerProvider struct{}

type ExchangeAccount struct {
	Broker TracerProvider
}

func SyncExchange(broker TracerProvider) {}
EOF

cat >"${TMP}/modules/trade/internal/bad_provider_qualifier.go" <<'EOF'
package trade

type TradeTelemetry struct {
	Source binance.ConfigProvider
	Other OKX.TracerProvider
}

func SyncExchange(provider binance.ConfigProvider) {}
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

cat >"${TMP}/modules/trade/internal/bad_generic_constraints.go" <<'EOF'
package trade

type Provider interface{}

type ExchangeSource[T Provider] struct{}

func SyncExchange[T Provider](source T) {}
EOF

cat >"${TMP}/modules/trade/internal/bad_recursive_types.go" <<'EOF'
package trade

type Provider interface{}
type List[T any] []T
type Pair[A, B any] struct{}

type ExchangeCatalog struct {
	Embedded *List[Provider]
	Stream map[string]chan<- *List[Provider]
	Factory func(source *[2]Provider) map[string]Provider
	Pair Pair[string, Provider]
}
EOF

cat >"${TMP}/modules/trade/internal/bad_results.go" <<'EOF'
package trade

type Broker interface{}

func LoadTrade() Broker { return nil }
func OpenExchange() (broker string) { return "" }
EOF

cat >"${TMP}/modules/trade/internal/bad_short_decl.go" <<'EOF'
package trade

func BuildTradeRuntime() {
	ExchangeProvider := trace.TracerProvider(nil)
}
EOF

cat >"${TMP}/modules/trade/internal/bad_provider_shadow.go" <<'EOF'
package trade

func TradeShadow(provider trace.TracerProvider) {
	{
		provider := "binance"
		_ = provider
	}
}
EOF

cat >"${TMP}/modules/trade/internal/bad_body_expressions.go" <<'EOF'
package trade

func CheckTrade() any {
	binance.Provider()
	use(exchangeProvider)
	if binance.Provider() != nil {
	}
	switch exchangeProvider {
	}
	for exchangeProvider != nil {
		break
	}
	return provider
}
EOF

cat >"${TMP}/modules/trade/internal/bad_range.go" <<'EOF'
package trade

func Refresh() {
	for provider := range binanceExchanges {
		_ = provider
	}
}
EOF

cat >"${TMP}/modules/trade/internal/secretclient/bad_secretclient.go" <<'EOF'
package secretclient

func ResolveExchange(provider string) string {
	return provider
}

type ExchangeSecretInternal struct {
	Provider string
}

type ExchangeSecretYAML struct {
	Source string `yaml:"provider"`
}
EOF

cat >"${TMP}/modules/trade/internal/bad_secret_wire.go" <<'EOF'
package trade

type ExchangeSecretWire struct {
	Provider string `json:"provider,omitempty"`
}
EOF

cat >"${TMP}/modules/trade/internal/bad_struct_tags.go" <<'EOF'
package trade

type ExchangeWire struct {
	Source string `json:"provider,omitempty"`
	Other string `yaml:"broker"`
}
EOF

cat >"${TMP}/packages/tradeeventpb/bad.proto" <<'EOF'
syntax = "proto3";

message ExchangeAccount {
  string broker = 1;
}
EOF

cat >"${TMP}/packages/tradeeventpb/bad_multiline.proto" <<'EOF'
syntax = "proto3";

message ExchangeAccount
{
  string broker = 1;
}
EOF

cat >"${TMP}/packages/tradeeventpb/bad_lowercase.proto" <<'EOF'
syntax = "proto3";

message exchangeaccount {
  string exchangeprovider = 1;
  string binancebroker = 2;
}
EOF

cat >"${TMP}/packages/tradeeventpb/bad_braces.proto" <<'EOF'
syntax = "proto3";

message ExchangeStringBrace {
  string marker = 1 [json_name = "}"];
  string provider = 2;
}

message TradeCommentBrace {
  // }
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
  broker?: ConfigProvider;
  venue?: ConfigProvider[];
  platform?: Promise<ConfigProvider>;
}
EOF

cat >"${TMP}/web/src/api/trade/bad_provider_qualifier.ts" <<'EOF'
export interface TradeClient {
  provider?: Binance.ConfigProvider;
  source?: OKX.TracerProvider;
}
EOF

cat >"${TMP}/web/src/api/trade/bad_braces.ts" <<'EOF'
export interface ExchangeStringBrace {
  marker: "}";
  provider?: string;
}

export interface TradeCommentBrace {
  // }
  broker?: string;
}
EOF

cat >"${TMP}/web/src/api/trade/bad_function.ts" <<'EOF'
export function syncExchange(
  provider: string,
  broker: string,
  venue: string,
  platform: string,
) {}
EOF

cat >"${TMP}/web/src/api/trade/bad_multiline.ts" <<'EOF'
export interface ExchangeOrder
{
  venue?: string;
}

export type ExchangeRequest =
{
  platform?: string;
}
EOF

cat >"${TMP}/web/src/api/trade/bad_union.ts" <<'EOF'
export type ExchangeMode =
  | "spot"
  | Broker;

export type ExchangeSource =
  | "manual"
  | Provider;

export type ExchangeTrailing =
  "spot" |
  Broker;

export type ExchangeInline = "spot" |
  Provider;
EOF

cat >"${TMP}/web/src/api/trade/bad_lowercase.ts" <<'EOF'
export interface exchangeorder {
  exchangeprovider?: string;
  binancevenue?: string;
  okxplatform?: string;
}
EOF

cat >"${TMP}/web/src/api/trade/bad.yaml" <<'EOF'
trade_provider: binance
trade:
  provider: binance
  broker: spot
  platform: api
exchange:
  venue: spot
exchangeprovider: binance
"exchange":
  provider: binance
'trade':
  broker: spot
  venue: spot
  platform: api
EOF

cat >"${TMP}/web/src/api/trade/bad.md" <<'EOF'
Trade Platform: OKX

## Trade configuration
Provider
Broker
Platform

## Exchange details
Venue

## exchangeconfiguration
provider
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
  bad.go bad_interface.go bad_receiver.go bad_okx.go bad_lowercase.go \
  bad_statements.go bad_third_party_name.go \
  bad_third_party_identifier.go bad_provider_qualifier.go \
  bad_type_expression.go bad_local_var.go \
  bad_type_specs.go bad_generic_constraints.go bad_recursive_types.go \
  bad_results.go bad_short_decl.go bad_provider_shadow.go bad_body_expressions.go \
  bad_range.go bad_secretclient.go \
  bad_secret_wire.go bad_struct_tags.go bad.proto bad_multiline.proto \
  bad_lowercase.proto bad_braces.proto bad.ts bad_third_party_name.ts \
  bad_provider_qualifier.ts bad_braces.ts bad_function.ts bad_multiline.ts \
  bad_union.ts bad_lowercase.ts \
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
  bad_lowercase.go:3 bad_lowercase.go:4 bad_lowercase.go:5 bad_lowercase.go:6 \
  bad_statements.go:3 bad_statements.go:6 bad_statements.go:7 \
  bad_statements.go:8 bad_statements.go:9 bad_statements.go:10 \
  bad_statements.go:11 bad_statements.go:13 \
  bad_third_party_name.go:6 bad_third_party_name.go:9 bad_third_party_name.go:11 \
  bad_third_party_identifier.go:4 bad_third_party_identifier.go:7 \
  bad_third_party_identifier.go:10 \
  bad_provider_qualifier.go:4 bad_provider_qualifier.go:5 \
  bad_provider_qualifier.go:8 \
  bad_type_expression.go:6 bad_type_expression.go:10 bad_type_expression.go:13 \
  bad_local_var.go:4 bad_type_specs.go:5 bad_type_specs.go:6 \
  bad_generic_constraints.go:5 bad_generic_constraints.go:7 \
  bad_recursive_types.go:8 bad_recursive_types.go:9 bad_recursive_types.go:10 \
  bad_recursive_types.go:11 bad_results.go:5 bad_results.go:6 \
  bad_short_decl.go:4 bad_provider_shadow.go:5 \
  bad_body_expressions.go:4 bad_body_expressions.go:5 bad_body_expressions.go:6 \
  bad_body_expressions.go:8 bad_body_expressions.go:10 bad_body_expressions.go:13 \
  bad_range.go:4 bad_secretclient.go:8 \
  bad_secretclient.go:12 \
  bad_secret_wire.go:4 bad_struct_tags.go:4 bad_struct_tags.go:5 \
  bad_multiline.proto:5 bad_lowercase.proto:4 bad_lowercase.proto:5 \
  bad_braces.proto:5 bad_braces.proto:10 \
  bad_third_party_name.ts:2 bad_third_party_name.ts:3 \
  bad_third_party_name.ts:4 bad_third_party_name.ts:5 \
  bad_provider_qualifier.ts:2 bad_provider_qualifier.ts:3 \
  bad_braces.ts:3 bad_braces.ts:8 \
  bad_function.ts:2 bad_function.ts:3 bad_function.ts:4 bad_function.ts:5 \
  bad_multiline.ts:3 bad_multiline.ts:8 \
  bad_union.ts:3 bad_union.ts:7 bad_union.ts:11 bad_union.ts:14 \
  bad_lowercase.ts:2 bad_lowercase.ts:3 bad_lowercase.ts:4 \
  bad.yaml:3 bad.yaml:4 bad.yaml:5 bad.yaml:7 bad.yaml:8 \
  bad.yaml:10 bad.yaml:12 bad.yaml:13 bad.yaml:14 \
  bad.md:4 bad.md:5 bad.md:6 bad.md:9 bad.md:12; do
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
  "${TMP}/modules/trade/internal/bad_lowercase.go" \
  "${TMP}/modules/trade/internal/bad_statements.go" \
  "${TMP}/modules/trade/internal/bad_third_party_name.go" \
  "${TMP}/modules/trade/internal/bad_third_party_identifier.go" \
  "${TMP}/modules/trade/internal/bad_provider_qualifier.go" \
  "${TMP}/modules/trade/internal/bad_type_expression.go" \
  "${TMP}/modules/trade/internal/bad_local_var.go" \
  "${TMP}/modules/trade/internal/bad_type_specs.go" \
  "${TMP}/modules/trade/internal/bad_generic_constraints.go" \
  "${TMP}/modules/trade/internal/bad_recursive_types.go" \
  "${TMP}/modules/trade/internal/bad_results.go" \
  "${TMP}/modules/trade/internal/bad_short_decl.go" \
  "${TMP}/modules/trade/internal/bad_provider_shadow.go" \
  "${TMP}/modules/trade/internal/bad_body_expressions.go" \
  "${TMP}/modules/trade/internal/bad_range.go" \
  "${TMP}/modules/trade/internal/secretclient/bad_secretclient.go" \
  "${TMP}/modules/trade/internal/bad_secret_wire.go" \
  "${TMP}/modules/trade/internal/bad_struct_tags.go" \
  "${TMP}/packages/tradeeventpb/bad.proto" \
  "${TMP}/packages/tradeeventpb/bad_multiline.proto" \
  "${TMP}/packages/tradeeventpb/bad_lowercase.proto" \
  "${TMP}/packages/tradeeventpb/bad_braces.proto" \
  "${TMP}/web/src/api/trade/bad.ts" \
  "${TMP}/web/src/api/trade/bad_third_party_name.ts" \
  "${TMP}/web/src/api/trade/bad_provider_qualifier.ts" \
  "${TMP}/web/src/api/trade/bad_braces.ts" \
  "${TMP}/web/src/api/trade/bad_function.ts" \
  "${TMP}/web/src/api/trade/bad_multiline.ts" \
  "${TMP}/web/src/api/trade/bad_union.ts" \
  "${TMP}/web/src/api/trade/bad_lowercase.ts" \
  "${TMP}/web/src/api/trade/bad.yaml" \
  "${TMP}/web/src/api/trade/bad.md"

(
  cd "${TMP}"
  "${CHECKER}" modules/trade packages/tradeeventpb web/src/api/trade
)
"${CHECKER}" "${TMP}/modules/trade" "${TMP}/packages/tradeeventpb" "${TMP}/web/src/api/trade"

echo "trade Exchange terminology fixtures passed"
