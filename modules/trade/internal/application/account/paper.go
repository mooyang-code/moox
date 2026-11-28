package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

// CreatePaperCommand 是创建模拟盘执行账户的命令；与实盘账户统一，建户与加入组合分步完成。
type CreatePaperCommand struct {
	SpaceID     string
	AccountName string
	Exchange    exchange.Exchange
	// Markets 是模拟账户开通的市场集合：加密货币取 spot、swap 的子集，股票只有 equity。
	Markets         []exchange.MarketType
	SettlementAsset string
	MarginMode      exchange.MarginMode
	InitialBalance  shared.Decimal
	MakerFeeRate    shared.Decimal
	TakerFeeRate    shared.Decimal
	SlippageBPS     shared.Decimal
	// OrderRetentionDays 是订单保留天数（0 表示不清理）；nil 时取默认 5 天。
	OrderRetentionDays *int
}

func (s *Service) newID() string {
	if s != nil && s.NewID != nil {
		return s.NewID()
	}
	id, err := gonanoid.New()
	if err != nil {
		panic(fmt.Sprintf("generate trade account ID: %v", err))
	}
	return id
}

// CreatePaperAccount 创建模拟盘执行账户；ID 使用随机 NanoID，不再对名称哈希，避免改名或重建冲突。
func (s *Service) CreatePaperAccount(ctx context.Context, command CreatePaperCommand) (store.TradingAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.TradingAccountRecord{}, ErrServiceNotConfigured
	}
	retentionDays := tradingaccount.DefaultOrderRetentionDays
	if command.OrderRetentionDays != nil {
		if !tradingaccount.ValidOrderRetentionDays(*command.OrderRetentionDays) {
			return store.TradingAccountRecord{}, ErrInvalidOrderRetention
		}
		retentionDays = *command.OrderRetentionDays
	}
	command.AccountName = strings.TrimSpace(command.AccountName)
	command.SettlementAsset = strings.ToUpper(strings.TrimSpace(command.SettlementAsset))
	command.MarginMode = exchange.MarginMode(strings.ToUpper(strings.TrimSpace(string(command.MarginMode))))
	if command.SpaceID == "" || command.AccountName == "" ||
		!command.Exchange.Valid() || command.SettlementAsset == "" {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: 模拟账户缺少必填字段", tradingaccount.ErrInvalidAccount)
	}
	markets, err := exchange.NormalizeMarkets(command.Markets)
	if err != nil {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: %v", tradingaccount.ErrInvalidAccount, err)
	}
	if err := execution.ValidateAccount(s.Exchanges, command.Exchange, markets, command.SettlementAsset, exchange.ExecutionModePaper); err != nil {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: %v", store.ErrInvalidRecord, err)
	}
	if slices.ContainsFunc(markets, func(market exchange.MarketType) bool { return exchange.RuleAt(command.Exchange, market).Fee != nil }) {
		// 有自己费用函数的场所（A 股、港股）只有一个佣金比例（复用 taker 费率字段），挂单、吃单一样。
		command.MakerFeeRate = command.TakerFeeRate
	}
	hasLeverage := slices.ContainsFunc(markets, func(market exchange.MarketType) bool {
		return exchange.RuleFor(market).Leverage
	})
	if !hasLeverage && command.MarginMode != exchange.MarginModeUnspecified {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: 没有开通 SWAP 的账户不能配置保证金模式", tradingaccount.ErrInvalidAccount)
	}
	if hasLeverage && command.SettlementAsset != "USDT" {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: SWAP 账户结算资产必须为 USDT", tradingaccount.ErrInvalidAccount)
	}
	if hasLeverage && command.MarginMode != exchange.MarginModeUnspecified && command.MarginMode != exchange.MarginModeCross {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: SWAP 账户仅支持全仓保证金模式", tradingaccount.ErrInvalidAccount)
	}
	if command.InitialBalance.Cmp(shared.Zero()) <= 0 {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: 初始资金必须大于 0", tradingaccount.ErrInvalidAccount)
	}
	if command.MakerFeeRate.IsNegative() || command.TakerFeeRate.IsNegative() || command.SlippageBPS.IsNegative() {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: 费率与滑点不能为负数", tradingaccount.ErrInvalidAccount)
	}
	if command.SlippageBPS.Cmp(shared.MustDecimal("10000")) >= 0 {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: 滑点必须小于 10000 基点", tradingaccount.ErrInvalidAccount)
	}
	if hasLeverage && command.MarginMode == exchange.MarginModeUnspecified {
		command.MarginMode = exchange.MarginModeCross
	}
	leverageSettings := store.LeverageSettings{}
	if hasLeverage {
		// 模拟盘建户表单不提供单标的杠杆，持久化默认 1x 保证预留、撮合与成交入账口径一致。
		leverageSettings[store.DefaultLeverageKey] = "1"
	}
	accountID := s.newID()
	account := store.TradingAccountRecord{
		SpaceID: command.SpaceID, TradingAccountID: accountID, Name: command.AccountName,
		Exchange: string(command.Exchange), Markets: marketStrings(markets),
		ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
		SettlementAsset: command.SettlementAsset,
		MarginMode:      string(command.MarginMode), Status: string(exchange.AccountStatusEnabled), Ready: false,
		LeverageSettings: leverageSettings,
		Snapshot: store.TradingAccountSnapshot{
			Balances: []store.AssetBalance{{
				Wallet: string(exchange.WalletUnified), Asset: command.SettlementAsset,
				Available: command.InitialBalance.String(), Total: command.InitialBalance.String(), Borrowed: "0",
			}},
			Equity: command.InitialBalance.String(), AvailableFunds: command.InitialBalance.String(),
			ExchangeUpdatedAt: 1,
		},
		PaperConfig: &store.PaperAccountConfigRecord{
			SpaceID: command.SpaceID, TradingAccountID: accountID,
			InitialBalance: command.InitialBalance.String(), MakerFeeRate: command.MakerFeeRate.String(),
			TakerFeeRate: command.TakerFeeRate.String(), SlippageBPS: command.SlippageBPS.String(),
			OrderRetentionDays: retentionDays,
		},
	}
	if err := account.Validate(); err != nil {
		return store.TradingAccountRecord{}, err
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.CreateTradingAccount(account) }); err != nil {
		return store.TradingAccountRecord{}, err
	}
	return s.Store.GetTradingAccount(ctx, command.SpaceID, accountID)
}

// ClosePaperAccount 原子关停模拟盘执行账户：要求账户已移出组合账户，撤单释放预留、刷新余额快照并停用账户。
func (s *Service) ClosePaperAccount(ctx context.Context, spaceID, accountID string) (store.TradingAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.TradingAccountRecord{}, ErrServiceNotConfigured
	}
	account, err := s.Store.GetTradingAccountByID(ctx, accountID)
	if err != nil {
		return store.TradingAccountRecord{}, err
	}
	if account.SpaceID != spaceID {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: account space mismatch", store.ErrInvalidRecord)
	}
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, accountID)
	if lockErr != nil {
		return store.TradingAccountRecord{}, lockErr
	}
	defer unlock()
	logical, _, findErr := s.Store.FindLogicalAccountMembership(ctx, spaceID, accountID)
	if findErr == nil {
		return store.TradingAccountRecord{}, fmt.Errorf("%w（组合账户 %s）", ErrAccountInLogicalAccount, logical.Name)
	}
	if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return store.TradingAccountRecord{}, findErr
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.ClosePaperAccount(spaceID, accountID, s.now())
	}); err != nil {
		return store.TradingAccountRecord{}, err
	}
	return s.Store.GetTradingAccount(ctx, spaceID, accountID)
}
