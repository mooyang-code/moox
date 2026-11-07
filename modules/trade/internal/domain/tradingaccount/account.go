package tradingaccount

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrInvalidAccount       = errors.New("trade: invalid trading account")
	ErrAccountNotExecutable = errors.New("trade: trading account is not executable")
)

type LiveConfig struct {
	Environment        exchange.AccountEnvironment
	CredentialSecretID string
}

type PaperConfig struct {
	InitialBalance shared.Decimal
	MakerFeeRate   shared.Decimal
	TakerFeeRate   shared.Decimal
	SlippageBPS    shared.Decimal
}

type Account struct {
	ID       string
	SpaceID  string
	Name     string
	Exchange exchange.Exchange
	// Markets 是账户已开通的市场，按固定顺序排列：加密货币取 SPOT、SWAP 的子集，股票只有 STOCK。
	Markets       []exchange.MarketType
	ExecutionMode exchange.ExecutionMode
	// AccountMode 只用于加密货币实盘账户，由交易所检测写入，用户不能改。
	AccountMode exchange.AccountMode
	// InspectionJSON 是最近一次检测的结果（权限、持仓模式、检测时间、阻断项）。
	InspectionJSON string
	Live           *LiveConfig
	Paper          *PaperConfig
	// Environment and CredentialSecretID are retained as in-memory projections
	// for existing exchange adapters; persistence uses Live/Paper configuration.
	Environment        exchange.AccountEnvironment
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         exchange.MarginMode
	Status             exchange.AccountStatus
	Ready              bool
	// LeverageSettings 只对 SWAP 生效，键是内部标的 ID，"*" 是账户默认杠杆。
	LeverageSettings   map[string]shared.Decimal
	Snapshot           exchange.AccountSnapshot
	SnapshotSourceTime time.Time
	LastSyncAt         time.Time
	LastReadyAt        time.Time
	LastError          string
}

func (a Account) Validate() error {
	if blank(a.ID) ||
		blank(a.SpaceID) ||
		blank(a.Name) ||
		!a.Exchange.Valid() ||
		!a.ExecutionMode.Valid() ||
		blank(a.SettlementAsset) ||
		!validStatus(a.Status) {
		return invalidAccount("missing or unsupported required field")
	}
	if err := a.validateMarkets(); err != nil {
		return err
	}
	if a.Live == nil && a.ExecutionMode == exchange.ExecutionModeLive && a.Environment.ValidLive() && a.CredentialSecretID != "" {
		a.Live = &LiveConfig{Environment: a.Environment, CredentialSecretID: a.CredentialSecretID}
	}
	if a.Paper == nil && a.ExecutionMode == exchange.ExecutionModePaper && a.Environment == exchange.AccountEnvironmentPaper {
		a.Paper = &PaperConfig{InitialBalance: shared.MustDecimal("1"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero()}
	}
	if (a.Live == nil) == (a.Paper == nil) {
		return invalidAccount("exactly one of LIVE and PAPER config is required")
	}
	if a.ExecutionMode == exchange.ExecutionModeLive {
		if a.Live == nil || !a.Live.Environment.ValidLive() || blank(a.Live.CredentialSecretID) {
			return invalidAccount("LIVE requires environment and credential")
		}
	} else if a.Paper == nil && a.Environment == exchange.AccountEnvironmentPaper {
		a.Paper = &PaperConfig{InitialBalance: shared.MustDecimal("1"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero()}
	} else if a.Paper == nil {
		return invalidAccount("PAPER requires paper config")
	} else if a.Paper.InitialBalance.Cmp(shared.Zero()) <= 0 || a.Paper.MakerFeeRate.Cmp(shared.Zero()) < 0 || a.Paper.TakerFeeRate.Cmp(shared.Zero()) < 0 || a.Paper.SlippageBPS.Cmp(shared.Zero()) < 0 || a.Paper.SlippageBPS.Cmp(shared.MustDecimal("10000")) >= 0 {
		return invalidAccount("invalid paper config")
	}
	switch {
	case !a.HasLeverageMarket():
		if a.MarginMode != exchange.MarginModeUnspecified || len(a.LeverageSettings) != 0 {
			return invalidAccount("没有开通 SWAP 的账户不能配置保证金模式或杠杆")
		}
	default:
		if a.MarginMode != exchange.MarginModeCross {
			return invalidAccount("SWAP 一律全仓，保证金模式必须是 CROSS")
		}
		for symbol, leverage := range a.LeverageSettings {
			if blank(symbol) || leverage.Cmp(shared.Zero()) <= 0 {
				return invalidAccount("SWAP 杠杆必须为正数且绑定标的")
			}
		}
	}
	return nil
}

// validateMarkets 校验已开通市场的规范写法，以及账户类型只出现在加密货币实盘账户上。
func (a Account) validateMarkets() error {
	normalized, err := exchange.NormalizeMarkets(a.Markets)
	if err != nil {
		return invalidAccount(err.Error())
	}
	if !slices.Equal(normalized, a.Markets) {
		return invalidAccount("已开通的市场必须去重并按 SPOT、SWAP、STOCK 的顺序排列")
	}
	if !a.AccountMode.Valid() {
		return invalidAccount("不支持的账户类型")
	}
	if a.AccountMode != exchange.AccountModeUnspecified &&
		(a.ExecutionMode != exchange.ExecutionModeLive || a.SupportsMarket(exchange.MarketTypeStock)) {
		return invalidAccount("账户类型只用于加密货币实盘账户")
	}
	return nil
}

// SupportsMarket 报告账户是否已开通某个市场。
func (a Account) SupportsMarket(market exchange.MarketType) bool {
	return slices.Contains(a.Markets, market)
}

// HasLeverageMarket 报告账户是否开通了需要配置杠杆和保证金模式的市场（SWAP）。
// WalletFor 返回某个市场的资金在这个账户里所在的钱包；模拟盘只有一个统一钱包。
func (a Account) WalletFor(market exchange.MarketType) exchange.Wallet {
	if a.ExecutionMode == exchange.ExecutionModePaper {
		return exchange.WalletUnified
	}
	return exchange.WalletFor(a.Exchange, a.AccountMode, market)
}

// SharesFundsAcrossMarkets 报告各市场是否共用一个资金池：币安统一账户、OKX 统一账户和模拟盘都是，
// 现货买入和合约保证金花同一份结算币；币安普通账户的现货、合约钱包各用各的。
// 余额里的钱包标签（统一账户现货在 MARGIN、合约在 UNIFIED）只用于展示和对账，不决定资金池。
func (a Account) SharesFundsAcrossMarkets() bool {
	if a.ExecutionMode == exchange.ExecutionModePaper {
		return true
	}
	mode := a.AccountMode
	if mode == exchange.AccountModeUnspecified && a.Exchange == exchange.ExchangeOKX {
		mode = exchange.AccountModeOKXUnified
	}
	return mode == exchange.AccountModeOKXUnified || mode == exchange.AccountModeBinancePortfolioMargin
}

// MarketsSharingFunds 返回和某个市场占用同一份资金的市场：共用资金池时是全部已开通市场，
// 否则是同一个钱包里的市场。
func (a Account) MarketsSharingFunds(market exchange.MarketType) []string {
	var markets []string
	wallet := a.WalletFor(market)
	for _, candidate := range a.Markets {
		if a.SharesFundsAcrossMarkets() || a.WalletFor(candidate) == wallet {
			markets = append(markets, string(candidate))
		}
	}
	return markets
}

func (a Account) HasLeverageMarket() bool {
	return slices.ContainsFunc(a.Markets, func(market exchange.MarketType) bool {
		return exchange.RuleFor(market).Leverage
	})
}

// MarketForInstrument 返回内部标的 ID 在这个账户里所属的市场。
func (a Account) MarketForInstrument(instrumentID string) exchange.MarketType {
	return exchange.ResolveInstrumentMarket(instrumentID, a.Markets)
}

func (a Account) ExecutionEligibility() error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.Status != exchange.AccountStatusEnabled || !a.Ready {
		return ErrAccountNotExecutable
	}
	return nil
}

func (a Account) MarketDataEnvironment() exchange.AccountEnvironment {
	if a.Paper != nil {
		return exchange.AccountEnvironmentProduction
	}
	if a.Live != nil {
		return a.Live.Environment
	}
	return a.Environment
}

func validStatus(status exchange.AccountStatus) bool {
	return status == exchange.AccountStatusEnabled ||
		status == exchange.AccountStatusDisabled ||
		status == exchange.AccountStatusError
}

func invalidAccount(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAccount, reason)
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
