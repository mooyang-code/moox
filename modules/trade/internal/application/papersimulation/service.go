package papersimulation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type CreateCommand struct {
	ControlMode        string
	SpaceID            string
	AccountName        string
	LogicalAccountName string
	Exchange           exchange.Exchange
	// Markets 是模拟账户开通的市场集合：加密货币取 SPOT、SWAP 的子集，股票只有 STOCK。
	Markets         []exchange.MarketType
	SettlementAsset string
	MarginMode      exchange.MarginMode
	InitialBalance  shared.Decimal
	MakerFeeRate    shared.Decimal
	TakerFeeRate    shared.Decimal
	SlippageBPS     shared.Decimal
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
	if command.SpaceID == "" || command.AccountName == "" ||
		!command.Exchange.Valid() || command.SettlementAsset == "" {
		return Result{}, fmt.Errorf("paper simulation: incomplete create command")
	}
	markets, err := exchange.NormalizeMarkets(command.Markets)
	if err != nil {
		return Result{}, fmt.Errorf("paper simulation: %w", err)
	}
	hasLeverage := slices.ContainsFunc(markets, func(market exchange.MarketType) bool {
		return exchange.RuleFor(market).Leverage
	})
	if !hasLeverage && command.MarginMode != exchange.MarginModeUnspecified {
		return Result{}, fmt.Errorf("paper simulation: 没有开通 SWAP 的账户不能配置保证金模式")
	}
	if hasLeverage && command.SettlementAsset != "USDT" {
		return Result{}, fmt.Errorf("paper simulation: SWAP requires USDT settlement")
	}
	if hasLeverage && command.MarginMode != exchange.MarginModeUnspecified && command.MarginMode != exchange.MarginModeCross {
		return Result{}, fmt.Errorf("paper simulation: SWAP requires CROSS margin mode")
	}
	if command.InitialBalance.Cmp(shared.Zero()) <= 0 {
		return Result{}, fmt.Errorf("paper simulation: initial balance must be positive")
	}
	if hasLeverage && command.MarginMode == exchange.MarginModeUnspecified {
		command.MarginMode = exchange.MarginModeCross
	}
	leverageSettings := store.LeverageSettings{}
	if hasLeverage {
		// The simulation form has no instrument-specific leverage yet. Persist a
		// conservative wildcard so reservation, matching and Fill reduction use
		// the same 1x value after restart.
		leverageSettings["*"] = "1"
	}
	accountID, logicalID := stableIDs(command.SpaceID, command.AccountName, command.LogicalAccountName)
	account := store.TradingAccountRecord{
		SpaceID: command.SpaceID, TradingAccountID: accountID, Name: command.AccountName,
		Exchange: string(command.Exchange), Markets: marketStrings(markets),
		ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: command.SettlementAsset,
		MarginMode: string(command.MarginMode), Status: "ENABLED", Ready: false,
		LeverageSettings: leverageSettings,
		Snapshot: store.TradingAccountSnapshot{
			Balances: []store.AssetBalance{{Wallet: string(exchange.WalletUnified), Asset: command.SettlementAsset, Available: command.InitialBalance.String(), Total: command.InitialBalance.String(), Borrowed: "0"}},
			Equity:   command.InitialBalance.String(), AvailableFunds: command.InitialBalance.String(),
			ExchangeUpdatedAt: 1,
		},
		PaperConfig: &store.PaperAccountConfigRecord{
			SpaceID: command.SpaceID, TradingAccountID: accountID,
			InitialBalance: command.InitialBalance.String(), MakerFeeRate: command.MakerFeeRate.String(),
			TakerFeeRate: command.TakerFeeRate.String(), SlippageBPS: command.SlippageBPS.String(),
		},
	}
	// 不填组合账户名称时只建执行账户，之后再加入已有的组合账户。
	if command.LogicalAccountName == "" {
		err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.CreateTradingAccount(account) })
		if err != nil {
			return Result{}, err
		}
		return Result{Account: account}, nil
	}
	logical := store.LogicalAccountRecord{
		ControlMode: command.ControlMode,
		SpaceID:     command.SpaceID, LogicalAccountID: logicalID, Name: command.LogicalAccountName,
		ExecutionMode: "PAPER", SettlementAsset: command.SettlementAsset,
		AutomationState: "PAUSED", PauseReason: "新建的模拟账户，等待恢复自动执行",
	}
	if logical.ControlMode == "" {
		logical.ControlMode = "STRATEGY"
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
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
	// 停用成员也要找到组合并加成员变更锁，关闭时会删除全部成员关系。
	logical, _, findErr := s.Store.FindLogicalAccountMembership(ctx, spaceID, accountID)
	if errors.Is(findErr, gorm.ErrRecordNotFound) {
		unlock, lockErr := s.Store.LockTradingAccountContext(ctx, accountID)
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	} else if findErr != nil {
		return findErr
	} else {
		unlock, lockErr := s.Store.LockMemberChange(ctx, spaceID, logical.LogicalAccountID, accountID)
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if account.SpaceID != spaceID {
			return fmt.Errorf("%w: account space mismatch", store.ErrInvalidRecord)
		}
		return tx.ClosePaperSimulation(spaceID, accountID)
	})
}

func marketStrings(markets []exchange.MarketType) []string {
	result := make([]string, len(markets))
	for i, market := range markets {
		result[i] = string(market)
	}
	return result
}

func stableIDs(spaceID, accountName, logicalName string) (string, string) {
	sum := sha256.Sum256([]byte(spaceID + "\x00" + accountName + "\x00" + logicalName))
	suffix := hex.EncodeToString(sum[:8])
	return "paper-account-" + suffix, "paper-logical-" + suffix
}
