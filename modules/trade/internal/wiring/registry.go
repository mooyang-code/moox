package wiring

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/hkex"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/okx"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// errASharePending 是 A 股实盘还不能用的原因：下单要经 QMT 执行端（第 4 批）。
var errASharePending = errors.New("A 股实盘要通过 QMT 执行端下单，QMT 执行端尚未接入")

// errHKEXPaperOnly 是港股不能建实盘账户的原因：只做了模拟盘，没有接任何券商。
var errHKEXPaperOnly = errors.New("港股只支持模拟盘，没有实盘下单通道")

// RegisterCryptoExchanges 注册加密货币交易所（币安、OKX）的描述与绑定工厂。
func RegisterCryptoExchanges(registry *execution.Registry) {
	cryptoExchange := func(name exchange.Exchange, inspect exchange.Inspector) execution.ExchangeDescriptor {
		return execution.ExchangeDescriptor{
			Exchange:     name,
			OrderTypes:   []exchange.OrderType{exchange.OrderTypeMarket, exchange.OrderTypeLimit},
			FillPolicies: []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC, exchange.FillPolicyFOK},
			Markets:      []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
			Inspect:      inspect,
		}
	}
	binanceDescriptor := cryptoExchange(exchange.ExchangeBinance, binance.Inspect)
	// 统一账户整个账户一个适配器（一个余额接口、一条私有流）；普通账户按市场各一个，由注册表合成。
	binanceDescriptor.WholeAccount = func(config exchange.AccountConfig) bool {
		return config.AccountMode == exchange.AccountModeBinancePortfolioMargin
	}
	registry.Register(binanceDescriptor, func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		if err := requireAPIKey(config, credential); err != nil {
			return nil, err
		}
		if err := requireInspectedMode(config); err != nil {
			return nil, err
		}
		if config.AccountMode == exchange.AccountModeBinancePortfolioMargin {
			if config.Environment == exchange.AccountEnvironmentTestnet {
				return nil, errors.New("币安测试网没有统一账户")
			}
			return binance.NewPortfolioMargin(config, credential), nil
		}
		return binance.New(config, credential), nil
	})
	okxExchange := cryptoExchange(exchange.ExchangeOKX, okx.Inspect)
	registry.Register(okxExchange, func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		if err := requireAPIKey(config, credential); err != nil {
			return nil, err
		}
		if config.ExecutionMode == exchange.ExecutionModeLive &&
			strings.TrimSpace(credential.Passphrase) == "" {
			return nil, errors.New("OKX 实盘账户缺少 passphrase")
		}
		return okx.New(config, credential), nil
	})
}

// RegisterBuiltins 是唯一的内置场所注册列表，新增场所时在这里加一行。
func RegisterBuiltins(registry *execution.Registry, ashareMarket, hkexMarket *stockfeed.Market) error {
	RegisterCryptoExchanges(registry)
	// A 股：模拟盘用腾讯快照做行情、由 Trade 撮合；实盘（仿真 TESTNET、实盘 PRODUCTION）走 QMT 执行端，尚未接入。
	session, err := ashare.NewSession()
	if err != nil {
		return fmt.Errorf("A 股交易时段：%w", err)
	}
	registry.Register(execution.ExchangeDescriptor{
		Exchange:         exchange.ExchangeAShare,
		OrderTypes:       []exchange.OrderType{exchange.OrderTypeLimit},
		FillPolicies:     []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC},
		Markets:          []exchange.MarketType{exchange.MarketTypeEquity},
		SettlementAssets: []string{ashare.SettlementAsset},
		LiveUnavailable:  errASharePending.Error(),
		Session:          session,
		SessionHours:     "A 股 09:30-11:30、13:00-14:57",
	}, func(config exchange.AccountConfig, _ exchange.Credential) (execution.ExecutionAdapter, error) {
		if config.ExecutionMode == exchange.ExecutionModeLive {
			return nil, errASharePending
		}
		return stockfeed.NewAdapter(ashareMarket), nil
	})
	// 港股：只有模拟盘，用腾讯实时快照做行情、由 Trade 撮合；没有实盘通道，建实盘账户和绑定都直接报错。
	hkSession, err := hkex.NewSession()
	if err != nil {
		return fmt.Errorf("港股交易时段：%w", err)
	}
	registry.Register(execution.ExchangeDescriptor{
		Exchange:         exchange.ExchangeHKEX,
		OrderTypes:       []exchange.OrderType{exchange.OrderTypeLimit},
		FillPolicies:     []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC},
		Markets:          []exchange.MarketType{exchange.MarketTypeEquity},
		SettlementAssets: []string{hkex.SettlementAsset},
		LiveUnavailable:  errHKEXPaperOnly.Error(),
		Session:          hkSession,
		SessionHours:     hkex.SessionHours,
		InstrumentScope:  hkex.InstrumentScope,
	}, func(config exchange.AccountConfig, _ exchange.Credential) (execution.ExecutionAdapter, error) {
		if config.ExecutionMode == exchange.ExecutionModeLive {
			return nil, errHKEXPaperOnly
		}
		return stockfeed.NewAdapter(hkexMarket), nil
	})
	return nil
}

// requireAPIKey：模拟盘用空凭据绑定公共行情，只有实盘才要求 API Key 与 Secret。
func requireAPIKey(config exchange.AccountConfig, credential exchange.Credential) error {
	if config.ExecutionMode == exchange.ExecutionModeLive &&
		(strings.TrimSpace(credential.APIKey) == "" || strings.TrimSpace(credential.APISecret) == "") {
		return errors.New("实盘账户缺少交易密钥")
	}
	return nil
}

// requireInspectedMode：币安生产环境的实盘账户必须先检测出账户类型（统一账户和普通账户的接口完全不同），不能按默认值猜。
// 测试网跳过检测，一律按普通账户。
func requireInspectedMode(config exchange.AccountConfig) error {
	if config.ExecutionMode == exchange.ExecutionModeLive &&
		config.Environment == exchange.AccountEnvironmentProduction &&
		config.AccountMode == exchange.AccountModeUnspecified {
		return errors.New("账户类型还没有检测出来：请先在管理台对这个账户“重新检测”")
	}
	return nil
}
