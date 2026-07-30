package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func smokeIdentityFor(exchangeName exchange.Exchange) smokeIdentity {
	name := strings.ToLower(string(exchangeName))
	return smokeIdentity{
		AccountID:        "testnet-smoke-" + name,
		LogicalAccountID: "testnet-smoke-logical-" + name,
	}
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
	identity := smokeIdentityFor(options.Exchange)
	existing, err := database.GetExchangeAccount(
		ctx,
		smokeSpaceID,
		identity.AccountID,
	)
	switch {
	case err == nil:
		if existing.SpaceID != smokeSpaceID ||
			existing.Exchange != string(options.Exchange) ||
			existing.MarketType != string(exchange.MarketTypeSpot) ||
			existing.ExecutionMode != string(exchange.ExecutionModeLive) ||
			existing.Environment != string(exchange.AccountEnvironmentTestnet) ||
			existing.CredentialSecretID != options.SecretID ||
			existing.SettlementAsset != "USDT" ||
			existing.Status != string(exchange.AccountStatusEnabled) {
			return smokeIdentity{}, errors.New(
				"existing smoke account must remain an enabled TESTNET LIVE SPOT account",
			)
		}
		return identity, validateSmokeLogicalAccount(ctx, database, identity)
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return smokeIdentity{}, err
	}
	err = database.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: smokeSpaceID, ExchangeAccountID: identity.AccountID,
			Name:     "Testnet smoke " + string(options.Exchange),
			Exchange: string(options.Exchange), MarketType: string(exchange.MarketTypeSpot),
			ExecutionMode:      string(exchange.ExecutionModeLive),
			Environment:        string(exchange.AccountEnvironmentTestnet),
			CredentialSecretID: options.SecretID,
			SettlementAsset:    "USDT",
			Status:             string(exchange.AccountStatusEnabled),
			SyncSymbols:        []string{options.Symbol},
			LeverageSettings:   store.LeverageSettings{},
			FillCursors:        store.FillCursors{},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: smokeSpaceID, LogicalAccountID: identity.LogicalAccountID,
			Name:            "Testnet smoke " + string(options.Exchange),
			ExecutionMode:   string(exchange.ExecutionModeLive),
			MarketType:      string(exchange.MarketTypeSpot),
			SettlementAsset: "USDT",
			AutomationState: "PAUSED",
			PauseReason:     "testnet smoke isolation",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: smokeSpaceID, LogicalAccountID: identity.LogicalAccountID,
			ExchangeAccountID: identity.AccountID, Enabled: true,
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
		logical.MarketType != string(exchange.MarketTypeSpot) ||
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
	if len(members) != 1 || members[0].ExchangeAccountID != identity.AccountID ||
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
	if current.Symbol != state.Symbol {
		return errors.New("queried symbol does not match persisted state")
	}
	return nil
}
