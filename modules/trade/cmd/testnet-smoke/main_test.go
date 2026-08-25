package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		SpaceID:           smokeSpaceID,
		AccountID:         "testnet-binance",
		LogicalAccountID:  "logical-binance",
		Symbol:            "BTCUSDT",
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
	got, err := readState(path, exchange.ExchangeBinance)
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
		"symbol":"BTCUSDT",
		"base_asset":"BTC",
		"baseline_base_total":"0",
		"client_order_id":"cid",
		"order_id":"oid",
		"exchange_order_id":"eid"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeBinance); err == nil ||
		!strings.Contains(err.Error(), "TESTNET") {
		t.Fatalf("readState() error = %v", err)
	}

	valid := smokeState{
		Version: 1, Exchange: exchange.ExchangeBinance,
		Environment: exchange.AccountEnvironmentTestnet,
		SpaceID:     smokeSpaceID, AccountID: "account",
		LogicalAccountID: "logical", Symbol: "BTCUSDT",
		BaseAsset: "BTC", BaselineBaseTotal: "0",
		ClientOrderID: "cid", OrderID: "oid", ExchangeOrderID: "eid",
	}
	if err := writeState(path, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(path, exchange.ExchangeOKX); err == nil ||
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
	if _, err := readState(path, exchange.ExchangeBinance); err == nil ||
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
		SecretID: "binance-testnet", Symbol: "BTCUSDT",
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
		account.MarketType != "SPOT" || account.CredentialSecretID != options.SecretID {
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
		SecretID: "binance-testnet", Symbol: "BTCUSDT",
	}
	identity := smokeIdentityFor(options.Exchange)
	if err := database.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: smokeSpaceID, TradingAccountID: identity.AccountID,
			Name: "bad", Exchange: "BINANCE", MarketType: "SPOT",
			ExecutionMode: "LIVE", Environment: "PRODUCTION",
			CredentialSecretID: options.SecretID, SettlementAsset: "USDT",
			Status: "ENABLED", SyncSymbols: []string{"BTCUSDT"},
		})
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
		Environment: exchange.AccountEnvironmentTestnet,
		SpaceID:     smokeSpaceID, AccountID: "account", LogicalAccountID: "logical",
		Symbol: "BTCUSDT", BaseAsset: "BTC", BaselineBaseTotal: "0",
		ClientOrderID: "cid", OrderID: "oid",
		ExchangeOrderID: "eid",
	}
	order := exchange.Order{
		ClientOrderID: "cid", ExchangeOrderID: "eid", Symbol: "BTCUSDT",
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
		"MOOX_OKX_TESTNET_SECRET_ID",
		"--phase submit",
		"--phase recover",
		"BINANCE PASS submit/query/stream/sync/restart/cleanup",
		"OKX PASS submit/query/stream/sync/restart/cleanup",
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
