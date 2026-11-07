package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestParseOptionsRequiresExplicitConfirmation(t *testing.T) {
	_, err := parseOptions(
		[]string{"--phase", "submit", "--exchange", "BINANCE", "--secret-id", "secret-1"},
		func(string) string { return "" },
	)
	if err == nil || !strings.Contains(err.Error(), "MOOX_TRADE_TESTNET_CONFIRM=YES") {
		t.Fatalf("parseOptions() error = %v", err)
	}
}

func TestParseOptionsPinsTestnetAndRejectsUnsupportedExchange(t *testing.T) {
	env := func(key string) string {
		if key == "MOOX_TRADE_TESTNET_CONFIRM" {
			return "YES"
		}
		return ""
	}
	_, err := parseOptions(
		[]string{"--phase", "submit", "--exchange", "COINBASE", "--secret-id", "secret-1"},
		env,
	)
	if err == nil || !strings.Contains(err.Error(), "BINANCE or OKX") {
		t.Fatalf("parseOptions() error = %v", err)
	}

	options, err := parseOptions(
		[]string{
			"--phase", "submit",
			"--exchange", "okx",
			"--secret-id", "secret-1",
			"--database", filepath.Join(t.TempDir(), "trade.db"),
			"--state", filepath.Join(t.TempDir(), "state.json"),
		},
		env,
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Exchange != exchange.ExchangeOKX ||
		options.Environment != exchange.AccountEnvironmentTestnet {
		t.Fatalf("options = %+v", options)
	}
}

func TestPlanPassiveBuyRoundsToRulesAndCapsExposure(t *testing.T) {
	plan, err := planPassiveBuy(
		shared.MustDecimal("100"),
		shared.MustDecimal("0.1"),
		shared.MustDecimal("0.001"),
		shared.MustDecimal("0.001"),
		shared.MustDecimal("5"),
		shared.MustDecimal("1"),
		shared.MustDecimal("20"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Price.String() != "90" || plan.Quantity.String() != "0.056" {
		t.Fatalf("plan = price %s quantity %s", plan.Price, plan.Quantity)
	}
	if plan.Price.Mul(plan.Quantity).Cmp(shared.MustDecimal("20")) > 0 {
		t.Fatalf("plan notional exceeds cap: %+v", plan)
	}
}

func TestPlanPassiveBuyRejectsExchangeMinimumAboveSafetyCap(t *testing.T) {
	_, err := planPassiveBuy(
		shared.MustDecimal("100"),
		shared.MustDecimal("0.1"),
		shared.MustDecimal("1"),
		shared.MustDecimal("1"),
		shared.MustDecimal("50"),
		shared.MustDecimal("1"),
		shared.MustDecimal("20"),
	)
	if err == nil || !strings.Contains(err.Error(), "safety cap") {
		t.Fatalf("planPassiveBuy() error = %v", err)
	}
}

func TestSmokeStateRoundTripContainsNoCredentialMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := smokeState{
		Version:           1,
		Exchange:          exchange.ExchangeBinance,
		Environment:       exchange.AccountEnvironmentTestnet,
		MarketType:        exchange.MarketTypeSpot,
		SpaceID:           smokeSpaceID,
		AccountID:         "testnet-binance",
		LogicalAccountID:  "logical-binance",
		InstrumentID:      "BTC-USDT",
		ExchangeSymbol:    "BTCUSDT",
		BaseAsset:         "BTC",
		BaselineBaseTotal: "0",
		ClientOrderID:     "cid",
		OrderID:           "oid",
		ExchangeOrderID:   "eid",
	}
	if err := writeState(path, state); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"api_key", "api_secret", "passphrase", "secret_value"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(forbidden)) {
			t.Fatalf("state contains credential field %q: %s", forbidden, raw)
		}
	}
	got, err := readState(path, exchange.ExchangeBinance, exchange.MarketTypeSpot)
	if err != nil {
		t.Fatal(err)
	}
	if got != state {
		t.Fatalf("state = %+v, want %+v", got, state)
	}
}

func TestReadStateRejectsProductionOrWrongExchange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{
		"version":1,
		"exchange":"BINANCE",
		"environment":"PRODUCTION",
		"space_id":"testnet-smoke",
		"account_id":"account",
		"logical_account_id":"logical",
		"instrument_id":"BTC-USDT",
		"exchange_symbol":"BTCUSDT",
		"base_asset":"BTC",
		"baseline_base_total":"0",
		"client_order_id":"cid",
		"order_id":"oid",
		"exchange_order_id":"eid"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeBinance, exchange.MarketTypeSpot); err == nil ||
		!strings.Contains(err.Error(), "TESTNET") {
		t.Fatalf("readState() error = %v", err)
	}

	valid := smokeState{
		Version: 1, Exchange: exchange.ExchangeBinance,
		Environment: exchange.AccountEnvironmentTestnet, MarketType: exchange.MarketTypeSpot,
		SpaceID: smokeSpaceID, AccountID: "account",
		LogicalAccountID: "logical", InstrumentID: "BTC-USDT", ExchangeSymbol: "BTCUSDT",
		BaseAsset: "BTC", BaselineBaseTotal: "0",
		ClientOrderID: "cid", OrderID: "oid", ExchangeOrderID: "eid",
	}
	if err := writeState(path, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeOKX, exchange.MarketTypeSpot); err == nil ||
		!strings.Contains(err.Error(), "exchange mismatch") {
		t.Fatalf("readState() error = %v", err)
	}
	valid.BaselineBaseTotal = "not-a-decimal"
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeBinance, exchange.MarketTypeSpot); err == nil ||
		!strings.Contains(err.Error(), "baseline") {
		t.Fatalf("readState() error = %v", err)
	}
}

func TestSeedSmokeStoreCreatesOnlyTestnetLiveSpotAccount(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	options := smokeOptions{
		Exchange: exchange.ExchangeBinance, Environment: exchange.AccountEnvironmentTestnet,
		SecretID: "binance-testnet", ExchangeSymbol: "BTCUSDT", MarketType: exchange.MarketTypeSpot,
	}
	identity, err := seedSmokeStore(ctx, database, options)
	if err != nil {
		t.Fatal(err)
	}
	account, err := database.GetTradingAccountByID(ctx, identity.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Environment != "TESTNET" || account.ExecutionMode != "LIVE" ||
		!slices.Equal(account.Markets, []string{"SPOT"}) || account.CredentialSecretID != options.SecretID {
		t.Fatalf("account = %+v", account)
	}
	logical, err := database.GetLogicalAccount(ctx, smokeSpaceID, identity.LogicalAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if logical.AutomationState != "PAUSED" {
		t.Fatalf("logical account = %+v", logical)
	}
	members, err := database.ListLogicalAccountMembers(
		ctx,
		smokeSpaceID,
		identity.LogicalAccountID,
		false,
	)
	if err != nil || len(members) != 1 || members[0].TradingAccountID != identity.AccountID {
		t.Fatalf("members = %+v, err = %v", members, err)
	}
}

func TestSeedSmokeStoreRejectsExistingNonTestnetAccount(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	options := smokeOptions{
		Exchange: exchange.ExchangeBinance, Environment: exchange.AccountEnvironmentTestnet,
		SecretID: "binance-testnet", ExchangeSymbol: "BTCUSDT", MarketType: exchange.MarketTypeSpot,
	}
	identity := smokeIdentityFor(options.Exchange, options.MarketType)
	if err := database.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: smokeSpaceID, TradingAccountID: identity.AccountID,
			Name: "bad", Exchange: "BINANCE", Markets: []string{"SPOT"},
			ExecutionMode: "LIVE", Environment: "PRODUCTION",
			CredentialSecretID: options.SecretID, SettlementAsset: "USDT",
			Status: "ENABLED"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := seedSmokeStore(ctx, database, options); err == nil ||
		!strings.Contains(err.Error(), "TESTNET") {
		t.Fatalf("seedSmokeStore() error = %v", err)
	}
}

func TestCredentialFromSecretValidatesExchangeAndOKXPassphrase(t *testing.T) {
	_, err := credentialFromSecret(accountapp.ExchangeSecret{
		SecretID: "s", Category: "exchange", Exchange: exchange.ExchangeOKX,
		Status: "active", KeyID: "key", SecretValue: "secret",
	}, exchange.ExchangeBinance, "s")
	if err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("credentialFromSecret() error = %v", err)
	}

	_, err = credentialFromSecret(accountapp.ExchangeSecret{
		SecretID: "s", Category: "exchange", Exchange: exchange.ExchangeOKX,
		Status: "active", KeyID: "key", SecretValue: "secret",
	}, exchange.ExchangeOKX, "s")
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("credentialFromSecret() error = %v", err)
	}

	credential, err := credentialFromSecret(accountapp.ExchangeSecret{
		SecretID: "s", Category: "exchange", Exchange: exchange.ExchangeOKX,
		Status: "active", KeyID: "key", SecretValue: "secret",
		ExtraConfig: `{"passphrase":"pass"}`,
	}, exchange.ExchangeOKX, "s")
	if err != nil {
		t.Fatal(err)
	}
	if credential.APIKey != "key" || credential.APISecret != "secret" ||
		credential.Passphrase != "pass" {
		t.Fatalf("credential = %+v", credential)
	}
}

func TestValidateQueriedOrderRequiresStableClientAndExchangeIdentity(t *testing.T) {
	state := smokeState{
		Version: 1, Exchange: exchange.ExchangeBinance,
		Environment: exchange.AccountEnvironmentTestnet, MarketType: exchange.MarketTypeSpot,
		SpaceID: smokeSpaceID, AccountID: "account", LogicalAccountID: "logical",
		InstrumentID: "BTC-USDT", ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", BaselineBaseTotal: "0",
		ClientOrderID: "cid", OrderID: "oid",
		ExchangeOrderID: "eid",
	}
	order := exchange.Order{Market: exchange.MarketTypeSwap,
		ClientOrderID: "cid", ExchangeOrderID: "eid", ExchangeSymbol: "BTCUSDT",
		Status: exchange.OrderStatusOpen,
	}
	if err := validateQueriedOrder(state, order); err != nil {
		t.Fatal(err)
	}
	order.ClientOrderID = "different"
	if err := validateQueriedOrder(state, order); err == nil ||
		!strings.Contains(err.Error(), "client order ID") {
		t.Fatalf("validateQueriedOrder() error = %v", err)
	}
	order.ClientOrderID = "cid"
	order.ExchangeOrderID = "different"
	if err := validateQueriedOrder(state, order); err == nil ||
		!strings.Contains(err.Error(), "Exchange order ID") {
		t.Fatalf("validateQueriedOrder() error = %v", err)
	}
	state.ExchangeOrderID = ""
	if err := validateQueriedOrder(state, order); err != nil {
		t.Fatalf("unknown-submit state should accept first authoritative ID: %v", err)
	}
}

func TestShellHarnessSkipsNonzeroWithoutConfirmationOrBothSecrets(t *testing.T) {
	script := filepath.Clean(filepath.Join("..", "..", "scripts", "testnet-smoke.sh"))
	for _, test := range []struct {
		name string
		env  []string
	}{
		{name: "missing confirmation"},
		{name: "missing secrets", env: []string{"MOOX_TRADE_TESTNET_CONFIRM=YES"}},
		{name: "missing Binance futures testnet secret", env: []string{
			"MOOX_TRADE_TESTNET_CONFIRM=YES", "MOOX_BINANCE_TESTNET_SECRET_ID=s", "MOOX_OKX_TESTNET_SECRET_ID=o",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bash", script)
			command.Env = append([]string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
			}, test.env...)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("script succeeded unexpectedly: %s", output)
			}
			if !bytes.Contains(output, []byte("SKIP")) {
				t.Fatalf("script did not report SKIP: %s", output)
			}
		})
	}
}

func TestShellHarnessUsesSecretIDsAndTwoRestartPhases(t *testing.T) {
	path := filepath.Clean(filepath.Join("..", "..", "scripts", "testnet-smoke.sh"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"MOOX_BINANCE_TESTNET_SECRET_ID",
		"MOOX_BINANCE_TESTNET_SWAP_SECRET_ID",
		"MOOX_OKX_TESTNET_SECRET_ID",
		"run_exchange BINANCE SPOT",
		"run_exchange BINANCE SWAP",
		"run_exchange OKX SPOT",
		"run_exchange OKX SWAP",
		"--market",
		"--phase submit",
		"--phase recover",
		"BINANCE PASS submit/query/stream/sync/restart/cleanup",
		"BINANCE SWAP PASS submit/query/stream/sync/restart/cleanup",
		"OKX PASS submit/query/stream/sync/restart/cleanup",
		"OKX SWAP PASS submit/query/stream/sync/restart/cleanup",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("script missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"MOOX_TRADE_TESTNET_API_KEY",
		"MOOX_TRADE_TESTNET_API_SECRET",
		"MOOX_TRADE_TESTNET_ENDPOINT",
		"MOOX_TRADE_TESTNET_OKX_SIMULATED",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("script retains unsafe legacy input %q", forbidden)
		}
	}
}

func TestPlanPassiveBuyCountsContractValueInNotional(t *testing.T) {
	// OKX 永续：1 张 = 0.01 BTC，最小 1 张；价格 100 × 0.9 = 90，名义 = 90 × 1 × 0.01 = 0.9，在安全上限内。
	plan, err := planPassiveBuy(
		shared.MustDecimal("100"), shared.MustDecimal("0.1"), shared.MustDecimal("1"), shared.MustDecimal("1"),
		shared.Zero(), shared.MustDecimal("0.01"), shared.MustDecimal("20"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Quantity.String() != "0.01" {
		t.Fatalf("下单数量要折成基础资产数量：%s", plan.Quantity)
	}
	// 漏掉合约面值时（把张数当基础资产）名义会被高估 100 倍：面值 100 的合约最小 1 张名义 9000，必须被安全上限拦住。
	if _, err := planPassiveBuy(
		shared.MustDecimal("100"), shared.MustDecimal("0.1"), shared.MustDecimal("1"), shared.MustDecimal("1"),
		shared.Zero(), shared.MustDecimal("100"), shared.MustDecimal("20"),
	); err == nil || !strings.Contains(err.Error(), "safety cap") {
		t.Fatalf("planPassiveBuy() error = %v", err)
	}
}

func TestParseOptionsDefaultsSymbolPerMarketAndRejectsStock(t *testing.T) {
	env := func(key string) string {
		if key == "MOOX_TRADE_TESTNET_CONFIRM" {
			return "YES"
		}
		return ""
	}
	base := []string{"--phase", "submit", "--secret-id", "secret-1", "--database", filepath.Join(t.TempDir(), "trade.db"), "--state", filepath.Join(t.TempDir(), "state.json")}
	for _, test := range []struct {
		exchange, market, symbol string
	}{
		{"BINANCE", "SPOT", "BTCUSDT"}, {"BINANCE", "SWAP", "BTCUSDT"}, {"OKX", "SPOT", "BTC-USDT"}, {"OKX", "SWAP", "BTC-USDT-SWAP"},
	} {
		options, err := parseOptions(append([]string{"--exchange", test.exchange, "--market", test.market}, base...), env)
		if err != nil {
			t.Fatal(err)
		}
		if options.ExchangeSymbol != test.symbol || string(options.MarketType) != test.market || options.Environment != exchange.AccountEnvironmentTestnet {
			t.Fatalf("%s %s：options = %+v", test.exchange, test.market, options)
		}
	}
	if _, err := parseOptions(append([]string{"--exchange", "BINANCE", "--market", "STOCK"}, base...), env); err == nil {
		t.Fatal("STOCK 不是加密货币测试网的市场")
	}
}

// 现货、合约各用各的测试网账户：账户标识、市场、保证金模式和杠杆互不相同，能放进同一个库里。
func TestSeedSmokeStoreCreatesSeparateAccountPerMarket(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seed := func(name exchange.Exchange, market exchange.MarketType, secret string) store.TradingAccountRecord {
		identity, err := seedSmokeStore(ctx, database, smokeOptions{
			Exchange: name, Environment: exchange.AccountEnvironmentTestnet, SecretID: secret, MarketType: market,
		})
		if err != nil {
			t.Fatal(err)
		}
		account, err := database.GetTradingAccountByID(ctx, identity.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		return account
	}
	spot := seed(exchange.ExchangeBinance, exchange.MarketTypeSpot, "binance-spot-testnet")
	swap := seed(exchange.ExchangeBinance, exchange.MarketTypeSwap, "binance-swap-testnet")
	okxSwap := seed(exchange.ExchangeOKX, exchange.MarketTypeSwap, "okx-demo")
	if spot.TradingAccountID == swap.TradingAccountID || swap.TradingAccountID == okxSwap.TradingAccountID {
		t.Fatal("每个（交易所、市场）要有独立的账户")
	}
	if !slices.Equal(spot.Markets, []string{"SPOT"}) || spot.MarginMode != "" || spot.CredentialSecretID != "binance-spot-testnet" ||
		spot.AccountMode != "BINANCE_STANDARD" {
		t.Fatalf("现货账户 = %+v", spot)
	}
	if !slices.Equal(swap.Markets, []string{"SWAP"}) || swap.MarginMode != "CROSS" || swap.CredentialSecretID != "binance-swap-testnet" ||
		swap.LeverageSettings[store.DefaultLeverageKey] != "1" || swap.AccountMode != "BINANCE_STANDARD" {
		t.Fatalf("合约账户 = %+v", swap)
	}
	if okxSwap.AccountMode != "OKX_UNIFIED" {
		t.Fatalf("OKX 模拟盘账户类型 = %q", okxSwap.AccountMode)
	}
	// 同一把密钥再次进来不会改动已有账户的市场。
	if _, err := seedSmokeStore(ctx, database, smokeOptions{
		Exchange: exchange.ExchangeBinance, Environment: exchange.AccountEnvironmentTestnet, SecretID: "other", MarketType: exchange.MarketTypeSwap,
	}); err == nil {
		t.Fatal("已有冒烟账户换了密钥应被拒绝，不能悄悄改")
	}
}

func TestSmokeStateIsBoundToMarket(t *testing.T) {
	state := smokeState{
		Version: 1, Exchange: exchange.ExchangeBinance, Environment: exchange.AccountEnvironmentTestnet, MarketType: exchange.MarketTypeSwap,
		SpaceID: smokeSpaceID, AccountID: "a", LogicalAccountID: "l", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		BaseAsset: "BTC", BaselineBaseTotal: "0", ClientOrderID: "c", OrderID: "o",
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeState(path, state); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeBinance, exchange.MarketTypeSwap); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeBinance, exchange.MarketTypeSpot); err == nil ||
		!strings.Contains(err.Error(), "market mismatch") {
		t.Fatalf("现货恢复阶段不能读合约的状态文件：%v", err)
	}
}
