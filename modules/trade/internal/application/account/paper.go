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

// CreatePaperCommand 是创建模拟盘执行账户的命令，支持单独创建、同事务新建组合账户，
// 或同事务加入一个已暂停的模拟盘组合账户。
type CreatePaperCommand struct {
	ControlMode        string
	SpaceID            string
	AccountName        string
	LogicalAccountName string
	// LogicalAccountID 非空时，新账户在创建的同一个事务里加入该已有组合账户（此时 LogicalAccountName 必须为空）。
	LogicalAccountID string
	// Priority 是加入已有组合时的优先级；为 0 时排在现有成员之后。
	Priority int
	Exchange exchange.Exchange
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

// PaperAccountResult 是创建或关闭模拟盘执行账户后返回的结果，包含账户及受影响的组合账户（如有）。
type PaperAccountResult struct {
	Account        store.TradingAccountRecord
	LogicalAccount store.LogicalAccountRecord
}

func (s *Service) rawStore() *store.Store {
	if s == nil {
		return nil
	}
	if s.RawStore != nil {
		return s.RawStore
	}
	switch repo := s.Store.(type) {
	case Repository:
		return repo.Store
	case *Repository:
		return repo.Store
	default:
		return nil
	}
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
func (s *Service) CreatePaperAccount(ctx context.Context, command CreatePaperCommand) (PaperAccountResult, error) {
	raw := s.rawStore()
	if raw == nil {
		return PaperAccountResult{}, ErrServiceNotConfigured
	}
	retentionDays := tradingaccount.DefaultOrderRetentionDays
	if command.OrderRetentionDays != nil {
		if !tradingaccount.ValidOrderRetentionDays(*command.OrderRetentionDays) {
			return PaperAccountResult{}, ErrInvalidOrderRetention
		}
		retentionDays = *command.OrderRetentionDays
	}
	command.AccountName = strings.TrimSpace(command.AccountName)
	command.LogicalAccountName = strings.TrimSpace(command.LogicalAccountName)
	command.LogicalAccountID = strings.TrimSpace(command.LogicalAccountID)
	command.SettlementAsset = strings.ToUpper(strings.TrimSpace(command.SettlementAsset))
	command.MarginMode = exchange.MarginMode(strings.ToUpper(strings.TrimSpace(string(command.MarginMode))))
	if command.SpaceID == "" || command.AccountName == "" ||
		!command.Exchange.Valid() || command.SettlementAsset == "" {
		return PaperAccountResult{}, fmt.Errorf("%w: 模拟账户缺少必填字段", tradingaccount.ErrInvalidAccount)
	}
	markets, err := exchange.NormalizeMarkets(command.Markets)
	if err != nil {
		return PaperAccountResult{}, fmt.Errorf("%w: %v", tradingaccount.ErrInvalidAccount, err)
	}
	if err := execution.ValidateAccount(s.Exchanges, command.Exchange, markets, command.SettlementAsset, exchange.ExecutionModePaper); err != nil {
		return PaperAccountResult{}, fmt.Errorf("%w: %v", store.ErrInvalidRecord, err)
	}
	if slices.ContainsFunc(markets, func(market exchange.MarketType) bool { return exchange.RuleAt(command.Exchange, market).Fee != nil }) {
		// 有自己费用函数的场所（A 股、港股）只有一个佣金比例（复用 taker 费率字段），挂单、吃单一样。
		command.MakerFeeRate = command.TakerFeeRate
	}
	hasLeverage := slices.ContainsFunc(markets, func(market exchange.MarketType) bool {
		return exchange.RuleFor(market).Leverage
	})
	if !hasLeverage && command.MarginMode != exchange.MarginModeUnspecified {
		return PaperAccountResult{}, fmt.Errorf("%w: 没有开通 SWAP 的账户不能配置保证金模式", tradingaccount.ErrInvalidAccount)
	}
	if hasLeverage && command.SettlementAsset != "USDT" {
		return PaperAccountResult{}, fmt.Errorf("%w: SWAP 账户结算资产必须为 USDT", tradingaccount.ErrInvalidAccount)
	}
	if hasLeverage && command.MarginMode != exchange.MarginModeUnspecified && command.MarginMode != exchange.MarginModeCross {
		return PaperAccountResult{}, fmt.Errorf("%w: SWAP 账户仅支持全仓保证金模式", tradingaccount.ErrInvalidAccount)
	}
	if command.InitialBalance.Cmp(shared.Zero()) <= 0 {
		return PaperAccountResult{}, fmt.Errorf("%w: 初始资金必须大于 0", tradingaccount.ErrInvalidAccount)
	}
	if command.MakerFeeRate.IsNegative() || command.TakerFeeRate.IsNegative() || command.SlippageBPS.IsNegative() {
		return PaperAccountResult{}, fmt.Errorf("%w: 费率与滑点不能为负数", tradingaccount.ErrInvalidAccount)
	}
	if command.SlippageBPS.Cmp(shared.MustDecimal("10000")) >= 0 {
		return PaperAccountResult{}, fmt.Errorf("%w: 滑点必须小于 10000 基点", tradingaccount.ErrInvalidAccount)
	}
	if hasLeverage && command.MarginMode == exchange.MarginModeUnspecified {
		command.MarginMode = exchange.MarginModeCross
	}
	leverageSettings := store.LeverageSettings{}
	domainLeverages := map[string]shared.Decimal{}
	if hasLeverage {
		// 模拟盘建户表单不提供单标的杠杆，持久化默认 1x 保证预留、撮合与成交入账口径一致。
		leverageSettings[store.DefaultLeverageKey] = "1"
		domainLeverages[store.DefaultLeverageKey] = shared.MustDecimal("1")
	}
	if command.LogicalAccountID != "" && command.LogicalAccountName != "" {
		return PaperAccountResult{}, fmt.Errorf("%w: 加入已有组合和新建组合只能选一个", tradingaccount.ErrInvalidAccount)
	}
	accountID := s.newID()
	// 复用领域模型统一校验，防止落库任何不可用的模拟账户配置。
	if err := (tradingaccount.Account{
		ID: accountID, SpaceID: command.SpaceID, Name: command.AccountName,
		Exchange: command.Exchange, Markets: markets,
		ExecutionMode: exchange.ExecutionModePaper, Environment: exchange.AccountEnvironmentPaper,
		SettlementAsset: command.SettlementAsset, MarginMode: command.MarginMode,
		Status: exchange.AccountStatusEnabled, LeverageSettings: domainLeverages,
		Paper: &tradingaccount.PaperConfig{
			InitialBalance: command.InitialBalance, MakerFeeRate: command.MakerFeeRate,
			TakerFeeRate: command.TakerFeeRate, SlippageBPS: command.SlippageBPS,
			OrderRetentionDays: retentionDays,
		},
	}).Validate(); err != nil {
		return PaperAccountResult{}, err
	}
	account := store.TradingAccountRecord{
		SpaceID: command.SpaceID, TradingAccountID: accountID, Name: command.AccountName,
		Exchange: string(command.Exchange), Markets: marketStrings(markets),
		ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: command.SettlementAsset,
		MarginMode: string(command.MarginMode), Status: "ENABLED", Ready: false,
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
	// 加入已有组合：账户和成员关系在同一个事务里写入。刚建的模拟账户不可能有持仓，不需要先同步、也不用等会话建立。
	if command.LogicalAccountID != "" {
		return s.createPaperInto(ctx, raw, command, account)
	}
	// 不填组合账户名称时只建执行账户，之后再加入已有的组合账户。
	if command.LogicalAccountName == "" {
		if err := raw.Transaction(ctx, func(tx *store.Tx) error { return tx.CreateTradingAccount(account) }); err != nil {
			return PaperAccountResult{}, err
		}
		stored, err := raw.GetTradingAccount(ctx, command.SpaceID, accountID)
		if err != nil {
			return PaperAccountResult{}, err
		}
		return PaperAccountResult{Account: stored}, nil
	}
	logicalID := s.newID()
	logical := store.LogicalAccountRecord{
		ControlMode: command.ControlMode,
		SpaceID:     command.SpaceID, LogicalAccountID: logicalID, Name: command.LogicalAccountName,
		ExecutionMode: "PAPER", SettlementAsset: command.SettlementAsset,
		AutomationState: "PAUSED", PauseReason: "新建的模拟账户，等待恢复自动执行",
	}
	if logical.ControlMode == "" {
		logical.ControlMode = "STRATEGY"
	}
	err = raw.Transaction(ctx, func(tx *store.Tx) error {
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
		return PaperAccountResult{}, err
	}
	storedAccount, err := raw.GetTradingAccount(ctx, command.SpaceID, accountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	storedLogical, err := raw.GetLogicalAccount(ctx, command.SpaceID, logicalID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	return PaperAccountResult{Account: storedAccount, LogicalAccount: storedLogical}, nil
}

// createPaperInto 创建模拟账户并加入已有的组合账户。组合必须已暂停、是模拟盘、结算资产一致；
// 环境、成员唯一等其余检查由 PutLogicalAccountMember 在同一个事务里完成。
func (s *Service) createPaperInto(
	ctx context.Context,
	raw *store.Store,
	command CreatePaperCommand,
	account store.TradingAccountRecord,
) (PaperAccountResult, error) {
	logical, err := raw.GetLogicalAccount(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	switch {
	case logical.ExecutionMode != "PAPER":
		return PaperAccountResult{}, fmt.Errorf("%w: 只能加入模拟盘的组合账户", store.ErrInvalidRecord)
	case !strings.EqualFold(logical.SettlementAsset, command.SettlementAsset):
		return PaperAccountResult{}, fmt.Errorf("%w: 组合账户的结算资产是 %s，和模拟账户的 %s 不一致", store.ErrInvalidRecord, logical.SettlementAsset, command.SettlementAsset)
	case logical.AutomationState != "PAUSED":
		return PaperAccountResult{}, fmt.Errorf("%w: 组合账户正在自动执行，先暂停再加入新账户", store.ErrInvalidRecord)
	}
	unlock, err := raw.LockMemberChange(ctx, command.SpaceID, command.LogicalAccountID, account.TradingAccountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	defer unlock()
	// 拿锁后重读组合状态，防止锁前暂停判断过期。
	logical, err = raw.GetLogicalAccount(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	if logical.AutomationState != "PAUSED" {
		return PaperAccountResult{}, fmt.Errorf("%w: 组合账户正在自动执行，先暂停再加入新账户", store.ErrInvalidRecord)
	}
	priority := command.Priority
	if priority <= 0 {
		members, listErr := raw.ListLogicalAccountMembers(ctx, command.SpaceID, command.LogicalAccountID, true)
		if listErr != nil {
			return PaperAccountResult{}, listErr
		}
		for _, member := range members {
			priority = max(priority, member.Priority)
		}
		priority++
	}
	err = raw.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: command.SpaceID, LogicalAccountID: command.LogicalAccountID,
			TradingAccountID: account.TradingAccountID, Enabled: true, Priority: priority,
		})
	})
	if err != nil {
		return PaperAccountResult{}, err
	}
	storedAccount, err := raw.GetTradingAccount(ctx, command.SpaceID, account.TradingAccountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	storedLogical, err := raw.GetLogicalAccount(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	return PaperAccountResult{Account: storedAccount, LogicalAccount: storedLogical}, nil
}

// ClosePaperAccount 原子关停模拟盘执行账户：撤单释放预留、刷新余额快照、停用账户，并暂停所属组合账户且移出成员关系。
func (s *Service) ClosePaperAccount(ctx context.Context, spaceID, accountID string) (PaperAccountResult, error) {
	raw := s.rawStore()
	if raw == nil {
		return PaperAccountResult{}, ErrServiceNotConfigured
	}
	account, err := raw.GetTradingAccountByID(ctx, accountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	if account.SpaceID != spaceID {
		return PaperAccountResult{}, fmt.Errorf("%w: account space mismatch", store.ErrInvalidRecord)
	}
	// 停用成员也要找到组合并加成员变更锁，关闭时会删除全部成员关系；
	// 拿锁后重查归属，若加锁窗口内发生并发加入/移出/换组则释放锁并按最新归属重试。
	var logicalID string
	var unlock func()
	for {
		var targetLogicalID string
		logical, _, findErr := raw.FindLogicalAccountMembership(ctx, spaceID, accountID)
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			u, lockErr := raw.LockTradingAccountContext(ctx, accountID)
			if lockErr != nil {
				return PaperAccountResult{}, lockErr
			}
			unlock = u
		} else if findErr != nil {
			return PaperAccountResult{}, findErr
		} else {
			targetLogicalID = logical.LogicalAccountID
			u, lockErr := raw.LockMemberChange(ctx, spaceID, targetLogicalID, accountID)
			if lockErr != nil {
				return PaperAccountResult{}, lockErr
			}
			unlock = u
		}
		actual, _, recheckErr := raw.FindLogicalAccountMembership(ctx, spaceID, accountID)
		var actualLogicalID string
		if recheckErr == nil {
			actualLogicalID = actual.LogicalAccountID
		} else if !errors.Is(recheckErr, gorm.ErrRecordNotFound) {
			unlock()
			return PaperAccountResult{}, recheckErr
		}
		if actualLogicalID == targetLogicalID {
			logicalID = actualLogicalID
			break
		}
		unlock()
	}
	defer unlock()
	err = raw.Transaction(ctx, func(tx *store.Tx) error {
		return tx.ClosePaperAccount(spaceID, accountID, s.now())
	})
	if err != nil {
		return PaperAccountResult{}, err
	}
	closedAccount, err := raw.GetTradingAccount(ctx, spaceID, accountID)
	if err != nil {
		return PaperAccountResult{}, err
	}
	result := PaperAccountResult{Account: closedAccount}
	if logicalID != "" {
		if updatedLogical, getErr := raw.GetLogicalAccount(ctx, spaceID, logicalID); getErr == nil {
			result.LogicalAccount = updatedLogical
		}
	}
	return result, nil
}
