package bootstrap

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTRPCConfigContainsOnlyApprovedServices(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Server struct {
			Service []struct {
				Name    string `yaml:"name"`
				Network string `yaml:"network"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(config.Server.Service))
	for _, service := range config.Server.Service {
		names = append(names, service.Name)
	}
	sort.Strings(names)
	want := []string{
		"trpc.moox.trade.Health",
		"trpc.moox.trade.TradeConsoleService",
		"trpc.moox.trade.equity.timer",
		"trpc.moox.trade.metrics.timer",
	}
	if len(names) != len(want) {
		t.Fatalf("service names = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("service names = %v, want %v", names, want)
		}
	}
	for _, service := range config.Server.Service {
		if service.Name == "trpc.moox.trade.equity.timer" {
			require.Equal(t, "*/20 * * * * *", service.Network, "采样周期必须短于权益有效期，否则策略目标转换会出现固定空档")
		}
	}
}

func TestExchangeCredentialPropagatesSingleSecretReadError(t *testing.T) {
	readErr := errors.New("secret service unavailable")
	secrets := &bootstrapSecretSource{err: readErr}

	_, err := exchangeCredential(
		context.Background(), secrets, exchange.ExchangeBinance, "secret-1",
	)
	if !errors.Is(err, readErr) {
		t.Fatalf("exchangeCredential() error = %v", err)
	}
	if secrets.getCalls != 1 {
		t.Fatalf("GetExchangeSecret() calls = %d, want 1", secrets.getCalls)
	}
}

func TestRegisterBuiltinsBindsSupportedExchanges(t *testing.T) {
	registry := execution.NewRegistry()
	require.NoError(t, registerBuiltins(registry, ashare.NewMarket(nil, nil)))
	for _, name := range []exchange.Exchange{
		exchange.ExchangeBinance,
		exchange.ExchangeOKX,
	} {
		adapter, err := registry.Bind(exchange.AccountConfig{
			TradingAccountID: "account-1",
			Exchange:         name,
			Markets:          []exchange.MarketType{exchange.MarketTypeSpot},
			ExecutionMode:    exchange.ExecutionModePaper,
			SettlementAsset:  "USDT",
		}, exchange.Credential{})
		if err != nil {
			t.Fatalf("Bind(%s) error = %v", name, err)
		}
		identity, ok := adapter.(execution.ExchangeIdentity)
		if !ok || identity.Exchange() != name {
			t.Fatalf("Bind(%s) adapter identity = %v", name, identity)
		}
	}

	_, err := registry.Bind(exchange.AccountConfig{
		TradingAccountID: "account-1",
		Exchange:         exchange.ExchangeOKX,
		Markets:          []exchange.MarketType{exchange.MarketTypeSpot},
		ExecutionMode:    exchange.ExecutionModeLive,
		SettlementAsset:  "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret"})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("OKX LIVE Bind() error = %v, want passphrase rejection", err)
	}
}

// A 股：模拟盘绑定公共行情适配器；实盘在 QMT 执行端接入前直接报错；建账户时校验场所与市场、结算资产。
func TestRegisterBuiltinsAShare(t *testing.T) {
	registry := execution.NewRegistry()
	require.NoError(t, registerBuiltins(registry, ashare.NewMarket(nil, nil)))
	config := exchange.AccountConfig{
		TradingAccountID: "account-1", Exchange: exchange.ExchangeAShare,
		Markets:       []exchange.MarketType{exchange.MarketTypeStock},
		ExecutionMode: exchange.ExecutionModePaper, Environment: exchange.AccountEnvironmentProduction, SettlementAsset: "CNY",
	}
	adapter, err := registry.Bind(config, exchange.Credential{})
	require.NoError(t, err)
	_, isPublic := adapter.(*ashare.Adapter)
	require.True(t, isPublic)

	config.ExecutionMode, config.Environment = exchange.ExecutionModeLive, exchange.AccountEnvironmentTestnet
	_, err = registry.Bind(config, exchange.Credential{})
	require.ErrorContains(t, err, "QMT 执行端尚未接入")

	stock := []exchange.MarketType{exchange.MarketTypeStock}
	spot := []exchange.MarketType{exchange.MarketTypeSpot}
	require.NoError(t, execution.ValidateAccount(registry, exchange.ExchangeAShare, stock, "CNY", exchange.ExecutionModePaper))
	require.ErrorContains(t, execution.ValidateAccount(registry, exchange.ExchangeAShare, spot, "CNY", exchange.ExecutionModePaper), "不支持开通市场")
	require.ErrorContains(t, execution.ValidateAccount(registry, exchange.ExchangeAShare, stock, "USDT", exchange.ExecutionModePaper), "结算资产只能是 CNY")
	require.ErrorContains(t, execution.ValidateAccount(registry, exchange.ExchangeAShare, stock, "CNY", exchange.ExecutionModeLive), "QMT 执行端尚未接入")
	require.ErrorContains(t, execution.ValidateAccount(registry, exchange.ExchangeBinance, stock, "CNY", exchange.ExecutionModePaper), "不支持开通市场")
	require.NoError(t, execution.ValidateAccount(registry, exchange.ExchangeOKX, spot, "USDT", exchange.ExecutionModeLive))
}

func TestBinanceLiveProductionRequiresInspectedAccountMode(t *testing.T) {
	registry := execution.NewRegistry()
	require.NoError(t, registerBuiltins(registry, ashare.NewMarket(nil, nil)))
	config := exchange.AccountConfig{
		TradingAccountID: "account-1", Exchange: exchange.ExchangeBinance,
		Markets:       []exchange.MarketType{exchange.MarketTypeSpot},
		ExecutionMode: exchange.ExecutionModeLive, Environment: exchange.AccountEnvironmentProduction, SettlementAsset: "USDT",
	}
	credential := exchange.Credential{APIKey: "key", APISecret: "secret"}
	_, err := registry.Bind(config, credential)
	require.ErrorContains(t, err, "账户类型还没有检测出来", "生产环境的实盘账户不能按默认值猜账户类型")

	config.AccountMode = exchange.AccountModeBinanceStandard
	_, err = registry.Bind(config, credential)
	require.NoError(t, err)

	// 测试网跳过检测，没有账户类型也能绑定。
	testnet := config
	testnet.AccountMode, testnet.Environment = exchange.AccountModeUnspecified, exchange.AccountEnvironmentTestnet
	_, err = registry.Bind(testnet, credential)
	require.NoError(t, err)
}

// 统一账户整个账户一个适配器；普通账户按市场拆开再合成。
func TestBindChoosesAdapterByInspectedAccountMode(t *testing.T) {
	registry := execution.NewRegistry()
	require.NoError(t, registerBuiltins(registry, ashare.NewMarket(nil, nil)))
	both := []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	config := exchange.AccountConfig{
		TradingAccountID: "account-1", Exchange: exchange.ExchangeBinance, Markets: both, MarginMode: exchange.MarginModeCross,
		ExecutionMode: exchange.ExecutionModeLive, Environment: exchange.AccountEnvironmentProduction, SettlementAsset: "USDT",
		AccountMode: exchange.AccountModeBinancePortfolioMargin,
	}
	credential := exchange.Credential{APIKey: "key", APISecret: "secret"}
	adapter, err := registry.Bind(config, credential)
	require.NoError(t, err)
	_, isPM := adapter.(*binance.PortfolioMargin)
	require.True(t, isPM, "统一账户应是一个整账户适配器，实际 %T", adapter)

	config.AccountMode = exchange.AccountModeBinanceStandard
	adapter, err = registry.Bind(config, credential)
	require.NoError(t, err)
	_, isMulti := adapter.(*execution.MultiMarket)
	require.True(t, isMulti, "普通账户应按市场拆开再合成，实际 %T", adapter)

	config.AccountMode, config.Environment = exchange.AccountModeBinancePortfolioMargin, exchange.AccountEnvironmentTestnet
	_, err = registry.Bind(config, credential)
	require.ErrorContains(t, err, "没有统一账户")
}

type bootstrapSecretSource struct {
	secret   accountapp.ExchangeSecret
	err      error
	getCalls int
}

func (s *bootstrapSecretSource) GetExchangeSecret(
	context.Context,
	string,
) (accountapp.ExchangeSecret, error) {
	s.getCalls++
	return s.secret, s.err
}

func TestBootstrapOwnsOneStoreAndOneShutdownHook(t *testing.T) {
	raw, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if got := strings.Count(source, "store.Open("); got != 1 {
		t.Fatalf("store.Open calls = %d, want 1", got)
	}
	if got := strings.Count(source, "RegisterOnShutdown("); got != 1 {
		t.Fatalf("shutdown hooks = %d, want 1", got)
	}
	for _, forbidden := range []string{
		"database.NewManager", "dao.New(", "RegisterAccountSvc",
		"RunRebalance", "fill_reconcile.timer", "order_recovery.timer",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("bootstrap still contains obsolete symbol %q", forbidden)
		}
	}
}

type extraOnlyAdapter struct {
	execution.ExecutionAdapter
}

func (extraOnlyAdapter) Exchange() exchange.Exchange { return "FAKE_EXCHANGE" }

func TestRegistryBindsExchangeWithOnlyExtraCredential(t *testing.T) {
	registry := execution.NewRegistry()
	fakeName := exchange.Exchange("FAKE_EXCHANGE")
	registry.Register(execution.ExchangeDescriptor{Exchange: fakeName}, func(
		_ exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		if credential.Extra["gateway"] == "" {
			return nil, errors.New("gateway is required")
		}
		return extraOnlyAdapter{}, nil
	})
	adapter, err := registry.Bind(exchange.AccountConfig{
		TradingAccountID: "account-1",
		Exchange:         fakeName,
		Markets:          []exchange.MarketType{exchange.MarketTypeSpot},
		ExecutionMode:    exchange.ExecutionModeLive,
		SettlementAsset:  "CNY",
	}, exchange.Credential{Extra: map[string]string{"gateway": "127.0.0.1:9000"}})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if _, ok := adapter.(execution.AccountEventSource); ok {
		t.Fatal("没有私有流的场所不应实现 AccountEventSource")
	}
}

// 同一个密钥 ID 下改了密钥内容、或账户从来没检测过：不能直接绑定。
func TestVerifyCredentialFingerprint(t *testing.T) {
	inspected := func(apiKey string) store.TradingAccountRecord {
		raw, err := exchange.Inspection{
			InspectedAt: time.Now().UTC(), CredentialFingerprint: exchange.CredentialFingerprint(apiKey),
		}.Encode()
		require.NoError(t, err)
		return store.TradingAccountRecord{InspectionJSON: raw}
	}
	require.NoError(t, verifyCredentialFingerprint(inspected("key-1"), exchange.Credential{APIKey: "key-1"}))
	require.ErrorContains(t, verifyCredentialFingerprint(inspected("key-1"), exchange.Credential{APIKey: "key-2"}), "密钥内容与检测时不一致")
	require.ErrorContains(t, verifyCredentialFingerprint(store.TradingAccountRecord{InspectionJSON: "{}"}, exchange.Credential{APIKey: "key-1"}), "还没有检测过")
	require.ErrorContains(t, verifyCredentialFingerprint(store.TradingAccountRecord{InspectionJSON: "not json"}, exchange.Credential{APIKey: "key-1"}), "重新检测")
	// 没有指纹的旧结果同样不放行。
	legacy, err := exchange.Inspection{InspectedAt: time.Now().UTC()}.Encode()
	require.NoError(t, err)
	require.ErrorContains(t, verifyCredentialFingerprint(store.TradingAccountRecord{InspectionJSON: legacy}, exchange.Credential{APIKey: "key-1"}), "还没有检测过")
}
