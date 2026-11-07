package tradepb

import "testing"

func TestCreateTradingAccountRequiresValidDistinctMarkets(t *testing.T) {
	base := func(markets ...MarketType) *CreateTradingAccountReq {
		return &CreateTradingAccountReq{
			Name: "live", Exchange: Exchange_EXCHANGE_BINANCE, Markets: markets,
			Live: &LiveConfig{Environment: AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
		}
	}
	for name, req := range map[string]*CreateTradingAccountReq{
		"现货":    base(MarketType_MARKET_TYPE_SPOT),
		"现货加合约": base(MarketType_MARKET_TYPE_SPOT, MarketType_MARKET_TYPE_SWAP),
		"合约加现货": base(MarketType_MARKET_TYPE_SWAP, MarketType_MARKET_TYPE_SPOT),
	} {
		if err := req.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]*CreateTradingAccountReq{
		"没有市场": base(),
		"未指定":  base(MarketType_MARKET_TYPE_UNSPECIFIED),
		"重复":   base(MarketType_MARKET_TYPE_SWAP, MarketType_MARKET_TYPE_SWAP),
		"越界":   base(MarketType(99)),
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s: 应被拒绝", name)
		}
	}
}

func TestCreatePaperSimulationMarginRulesFollowSwapInMarkets(t *testing.T) {
	paper := func(margin, settlement string, markets ...MarketType) *CreatePaperSimulationReq {
		return &CreatePaperSimulationReq{
			AccountName: "paper", Exchange: Exchange_EXCHANGE_BINANCE, Markets: markets, SettlementAsset: settlement,
			MarginMode: margin, InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
		}
	}
	swap, spot := MarketType_MARKET_TYPE_SWAP, MarketType_MARKET_TYPE_SPOT
	if err := paper("", "USDT", spot, swap).Validate(); err != nil {
		t.Fatalf("现货加合约、USDT 结算应合法：%v", err)
	}
	if err := paper("CROSS", "USDT", swap).Validate(); err != nil {
		t.Fatalf("合约全仓应合法：%v", err)
	}
	for name, req := range map[string]*CreatePaperSimulationReq{
		"没有合约不能设保证金模式": paper("CROSS", "USDT", spot),
		"合约必须全仓":       paper("ISOLATED", "USDT", swap),
		"含合约必须 USDT":   paper("", "BTC", spot, swap),
		"没有市场":         paper("", "USDT"),
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s: 应被拒绝", name)
		}
	}
}
