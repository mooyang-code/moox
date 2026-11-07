package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type smokeIdentity struct {
	AccountID        string
	LogicalAccountID string
}

// smokeIdentityFor 每个（交易所、市场）一个独立的账户：币安现货、合约测试网是两套域名和两把密钥，
// 现货、合约各用各的测试网账户，OKX 也按市场分开跑，互不影响。
func smokeIdentityFor(exchangeName exchange.Exchange, market exchange.MarketType) smokeIdentity {
	name := strings.ToLower(string(exchangeName)) + "-" + strings.ToLower(string(market))
	return smokeIdentity{
		AccountID:        "testnet-smoke-" + name,
		LogicalAccountID: "testnet-smoke-logical-" + name,
	}
}

// smokeAccountMode 是测试网账户的类型：币安测试网跳过检测，一律按普通账户；OKX 模拟盘是统一账户。
func smokeAccountMode(exchangeName exchange.Exchange) string {
	if exchangeName == exchange.ExchangeOKX {
		return string(exchange.AccountModeOKXUnified)
	}
	return string(exchange.AccountModeBinanceStandard)
}

// smokeMarginMode：合约账户一律全仓，现货为空。
func smokeMarginMode(market exchange.MarketType) string {
	if exchange.RuleFor(market).Leverage {
		return string(exchange.MarginModeCross)
	}
	return ""
}

func smokeLeverage(market exchange.MarketType) store.LeverageSettings {
	if exchange.RuleFor(market).Leverage {
		// 测试网冒烟固定 1 倍杠杆，不放大测试单的风险。
		return store.LeverageSettings{store.DefaultLeverageKey: "1"}
	}
	return store.LeverageSettings{}
}

func seedSmokeStore(
	ctx context.Context,
	database *store.Store,
	options smokeOptions,
) (smokeIdentity, error) {
	if database == nil ||
		options.Environment != exchange.AccountEnvironmentTestnet ||
		(options.Exchange != exchange.ExchangeBinance &&
			options.Exchange != exchange.ExchangeOKX) {
		return smokeIdentity{}, errors.New("testnet smoke store requires a supported TESTNET Exchange")
	}
	if options.MarketType != exchange.MarketTypeSpot && options.MarketType != exchange.MarketTypeSwap {
		return smokeIdentity{}, errors.New("testnet smoke store requires a SPOT or SWAP market")
	}
	identity := smokeIdentityFor(options.Exchange, options.MarketType)
	markets := []string{string(options.MarketType)}
	existing, err := database.GetTradingAccount(
		ctx,
		smokeSpaceID,
		identity.AccountID,
	)
	switch {
	case err == nil:
		if existing.SpaceID != smokeSpaceID ||
			existing.Exchange != string(options.Exchange) ||
			!slices.Equal(existing.Markets, markets) ||
			existing.ExecutionMode != string(exchange.ExecutionModeLive) ||
			existing.Environment != string(exchange.AccountEnvironmentTestnet) ||
			existing.CredentialSecretID != options.SecretID ||
			existing.SettlementAsset != "USDT" ||
			existing.Status != string(exchange.AccountStatusEnabled) {
			return smokeIdentity{}, errors.New(
				"existing smoke account must remain an enabled TESTNET LIVE account of the same market",
			)
		}
		return identity, validateSmokeLogicalAccount(ctx, database, identity)
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return smokeIdentity{}, err
	}
	err = database.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: smokeSpaceID, TradingAccountID: identity.AccountID,
			Name:     "Testnet smoke " + string(options.Exchange) + " " + string(options.MarketType),
			Exchange: string(options.Exchange), Markets: markets,
			ExecutionMode:      string(exchange.ExecutionModeLive),
			AccountMode:        smokeAccountMode(options.Exchange),
			Environment:        string(exchange.AccountEnvironmentTestnet),
			CredentialSecretID: options.SecretID,
			SettlementAsset:    "USDT",
			MarginMode:         smokeMarginMode(options.MarketType),
			Status:             string(exchange.AccountStatusEnabled),
			LeverageSettings:   smokeLeverage(options.MarketType),
			FillCursors:        store.FillCursors{},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: smokeSpaceID, LogicalAccountID: identity.LogicalAccountID,
			Name:            "Testnet smoke " + string(options.Exchange) + " " + string(options.MarketType),
			ExecutionMode:   string(exchange.ExecutionModeLive),
			SettlementAsset: "USDT",
			AutomationState: "PAUSED",
			PauseReason:     "testnet smoke isolation",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: smokeSpaceID, LogicalAccountID: identity.LogicalAccountID,
			TradingAccountID: identity.AccountID, Enabled: true,
		})
	})
	if err != nil {
		return smokeIdentity{}, err
	}
	return identity, nil
}

func validateSmokeLogicalAccount(
	ctx context.Context,
	database *store.Store,
	identity smokeIdentity,
) error {
	logical, err := database.GetLogicalAccount(
		ctx,
		smokeSpaceID,
		identity.LogicalAccountID,
	)
	if err != nil {
		return err
	}
	if logical.SpaceID != smokeSpaceID ||
		logical.ExecutionMode != string(exchange.ExecutionModeLive) ||
		logical.SettlementAsset != "USDT" ||
		logical.AutomationState != "PAUSED" {
		return errors.New("existing smoke LogicalAccount is not isolated and PAUSED")
	}
	members, err := database.ListLogicalAccountMembers(
		ctx,
		smokeSpaceID,
		identity.LogicalAccountID,
		false,
	)
	if err != nil {
		return err
	}
	if len(members) != 1 || members[0].TradingAccountID != identity.AccountID ||
		!members[0].Enabled {
		return errors.New("existing smoke LogicalAccount membership mismatch")
	}
	return nil
}

func credentialFromSecret(
	value accountapp.ExchangeSecret,
	exchangeName exchange.Exchange,
	secretID string,
) (exchange.Credential, error) {
	if value.SecretID != secretID ||
		value.Exchange != exchangeName ||
		value.Category != "exchange" ||
		value.Status != "active" ||
		strings.TrimSpace(value.KeyID) == "" ||
		strings.TrimSpace(value.SecretValue) == "" {
		return exchange.Credential{}, fmt.Errorf(
			"testnet smoke: Exchange credential %q metadata mismatch",
			secretID,
		)
	}
	var extra struct {
		Passphrase string `json:"passphrase"`
	}
	if strings.TrimSpace(value.ExtraConfig) != "" {
		if err := json.Unmarshal([]byte(value.ExtraConfig), &extra); err != nil {
			return exchange.Credential{}, fmt.Errorf(
				"testnet smoke: decode credential extra config: %w",
				err,
			)
		}
	}
	if exchangeName == exchange.ExchangeOKX &&
		strings.TrimSpace(extra.Passphrase) == "" {
		return exchange.Credential{}, errors.New("testnet smoke: OKX passphrase is required")
	}
	return exchange.Credential{
		APIKey: value.KeyID, APISecret: value.SecretValue,
		Passphrase: extra.Passphrase,
	}, nil
}

func validateQueriedOrder(state smokeState, current exchange.Order) error {
	if current.ClientOrderID != state.ClientOrderID {
		return errors.New("queried client order ID does not match persisted state")
	}
	if state.ExchangeOrderID != "" &&
		current.ExchangeOrderID != state.ExchangeOrderID {
		return errors.New("queried Exchange order ID does not match persisted state")
	}
	if current.ExchangeSymbol != state.ExchangeSymbol {
		return errors.New("queried symbol does not match persisted state")
	}
	return nil
}
