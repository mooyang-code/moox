package papersimulation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type CreateCommand struct {
	SpaceID            string
	AccountName        string
	LogicalAccountName string
	Exchange           exchange.Exchange
	MarketType         exchange.MarketType
	SettlementAsset    string
	MarginMode         exchange.MarginMode
	InitialBalance     shared.Decimal
	MakerFeeRate       shared.Decimal
	TakerFeeRate       shared.Decimal
	SlippageBPS        shared.Decimal
}

type Result struct {
	Account        store.TradingAccountRecord
	LogicalAccount store.LogicalAccountRecord
}

type Service struct {
	Store *store.Store
}

func (s *Service) Create(ctx context.Context, command CreateCommand) (Result, error) {
	if s == nil || s.Store == nil {
		return Result{}, fmt.Errorf("paper simulation: store is not configured")
	}
	command.SettlementAsset = strings.ToUpper(strings.TrimSpace(command.SettlementAsset))
	command.MarginMode = exchange.MarginMode(strings.ToUpper(strings.TrimSpace(string(command.MarginMode))))
	if command.SpaceID == "" || command.AccountName == "" || command.LogicalAccountName == "" ||
		!command.Exchange.Valid() || !command.MarketType.Valid() || command.SettlementAsset == "" {
		return Result{}, fmt.Errorf("paper simulation: incomplete create command")
	}
	if command.MarketType == exchange.MarketTypeSpot && command.MarginMode != exchange.MarginModeUnspecified {
		return Result{}, fmt.Errorf("paper simulation: SPOT cannot configure margin mode")
	}
	if command.MarketType == exchange.MarketTypeSwap && command.SettlementAsset != "USDT" {
		return Result{}, fmt.Errorf("paper simulation: SWAP requires USDT settlement")
	}
	if command.MarketType == exchange.MarketTypeSwap && command.MarginMode != exchange.MarginModeUnspecified && command.MarginMode != exchange.MarginModeCross {
		return Result{}, fmt.Errorf("paper simulation: SWAP requires CROSS margin mode")
	}
	if command.InitialBalance.Cmp(shared.Zero()) <= 0 {
		return Result{}, fmt.Errorf("paper simulation: initial balance must be positive")
	}
	if command.MarketType == exchange.MarketTypeSwap && command.MarginMode == exchange.MarginModeUnspecified {
		command.MarginMode = exchange.MarginModeCross
	}
	leverageSettings := store.LeverageSettings{}
	if command.MarketType == exchange.MarketTypeSwap {
		// The simulation form has no instrument-specific leverage yet. Persist a
		// conservative wildcard so reservation, matching and Fill reduction use
		// the same 1x value after restart.
		leverageSettings["*"] = "1"
	}
	accountID, logicalID := stableIDs(command.SpaceID, command.AccountName, command.LogicalAccountName)
	account := store.TradingAccountRecord{
		SpaceID: command.SpaceID, TradingAccountID: accountID, Name: command.AccountName,
		Exchange: string(command.Exchange), MarketType: string(command.MarketType),
		ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: command.SettlementAsset,
		MarginMode: string(command.MarginMode), Status: "ENABLED", Ready: false,
		LeverageSettings: leverageSettings,
		Snapshot: store.TradingAccountSnapshot{
			Balances: []store.AssetBalance{{Asset: command.SettlementAsset, Available: command.InitialBalance.String(), Total: command.InitialBalance.String()}},
			Equity:   command.InitialBalance.String(), AvailableFunds: command.InitialBalance.String(),
			ExchangeUpdatedAt: 1,
		},
		PaperConfig: &store.PaperAccountConfigRecord{
			SpaceID: command.SpaceID, TradingAccountID: accountID,
			InitialBalance: command.InitialBalance.String(), MakerFeeRate: command.MakerFeeRate.String(),
			TakerFeeRate: command.TakerFeeRate.String(), SlippageBPS: command.SlippageBPS.String(),
		},
	}
	logical := store.LogicalAccountRecord{
		SpaceID: command.SpaceID, LogicalAccountID: logicalID, Name: command.LogicalAccountName,
		ExecutionMode: "PAPER", MarketType: string(command.MarketType), SettlementAsset: command.SettlementAsset,
		AutomationState: "PAUSED", PauseReason: "paper simulation created",
	}
	err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(logical); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: command.SpaceID, LogicalAccountID: logicalID, TradingAccountID: accountID, Enabled: true, Priority: 1,
		})
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Account: account, LogicalAccount: logical}, nil
}

func (s *Service) Close(ctx context.Context, spaceID, accountID string) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("paper simulation: store is not configured")
	}
	account, err := s.Store.GetTradingAccountByID(ctx, accountID)
	if err != nil {
		return err
	}
	logicalID := ""
	members, listErr := s.Store.ListLogicalAccounts(ctx, spaceID)
	if listErr != nil {
		return listErr
	}
	for _, logical := range members {
		if logical.ExecutionMode != "PAPER" {
			continue
		}
		memberRows, memberErr := s.Store.ListLogicalAccountMembers(ctx, spaceID, logical.LogicalAccountID, true)
		if memberErr != nil {
			return memberErr
		}
		for _, member := range memberRows {
			if member.TradingAccountID == accountID && member.Enabled {
				logicalID = logical.LogicalAccountID
				break
			}
		}
		if logicalID != "" {
			break
		}
	}
	// Every lifecycle mutation uses the same lock order as target execution:
	// logical account, logical execution, then trading account.
	unlockLogical := func() {}
	unlockExecution := func() {}
	if logicalID != "" {
		unlockLogical = s.Store.LockLogicalAccount(spaceID, logicalID)
		unlockExecution = s.Store.LockLogicalAccountExecution(spaceID, logicalID)
	}
	defer unlockExecution()
	defer unlockLogical()
	unlockAccount := s.Store.LockTradingAccount(accountID)
	defer unlockAccount()
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if account.SpaceID != spaceID {
			return fmt.Errorf("%w: account space mismatch", store.ErrInvalidRecord)
		}
		return tx.ClosePaperSimulation(spaceID, accountID)
	})
}

func stableIDs(spaceID, accountName, logicalName string) (string, string) {
	sum := sha256.Sum256([]byte(spaceID + "\x00" + accountName + "\x00" + logicalName))
	suffix := hex.EncodeToString(sum[:8])
	return "paper-account-" + suffix, "paper-logical-" + suffix
}
