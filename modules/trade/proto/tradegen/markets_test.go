package tradepb

import "testing"

func TestCreateTradingAccountRequiresValidDistinctMarkets(t *testing.T) {
	base := func(markets ...string) *CreateTradingAccountReq {
		return &CreateTradingAccountReq{
			Name: "live", Exchange: "binance", Markets: markets,
			Live: &LiveConfig{Environment: AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
		}
	}
	for name, req := range map[string]*CreateTradingAccountReq{
		"现货":    base("spot"),
		"现货加合约": base("spot", "swap"),
		"合约加现货": base("swap", "spot"),
	} {
		if err := req.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]*CreateTradingAccountReq{
		"没有市场": base(),
		"未指定":  base(""),
		"重复":   base("swap", "swap"),
		"越界":   base("stock"),
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s: 应被拒绝", name)
		}
	}
}

func TestCreatePaperAccountMarginRulesFollowSwapInMarkets(t *testing.T) {
	paper := func(margin, settlement string, markets ...string) *CreateTradingAccountReq {
		return &CreateTradingAccountReq{
			Name: "paper", Exchange: "binance", Markets: markets, SettlementAsset: settlement,
			MarginMode: margin,
			Paper:      &PaperConfig{InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0"},
		}
	}
	swap, spot := "swap", "spot"
	if err := paper("", "USDT", spot, swap).Validate(); err != nil {
		t.Fatalf("现货加合约、USDT 结算应合法：%v", err)
	}
	if err := paper("CROSS", "USDT", swap).Validate(); err != nil {
		t.Fatalf("合约全仓应合法：%v", err)
	}
	for name, req := range map[string]*CreateTradingAccountReq{
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

func TestCreateLogicalAccountRequiresThreeValueEnvironment(t *testing.T) {
	for _, env := range []AccountEnvironment{
		AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER,
		AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET,
		AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION,
	} {
		req := &CreateLogicalAccountReq{Name: "combo", Environment: env, SettlementAsset: "USDT"}
		if err := req.Validate(); err != nil {
			t.Errorf("环境 %s 应通过：%v", env, err)
		}
	}
	for _, env := range []AccountEnvironment{AccountEnvironment_ACCOUNT_ENVIRONMENT_UNSPECIFIED, AccountEnvironment(99)} {
		req := &CreateLogicalAccountReq{Name: "combo", Environment: env, SettlementAsset: "USDT"}
		if err := req.Validate(); err == nil {
			t.Errorf("非法环境 %v 应被拒绝", env)
		}
	}
}

func TestCreatePaperAccountSlippageAndOptionalFeesValidation(t *testing.T) {
	makeReq := func(maker, taker, slippage string) *CreateTradingAccountReq {
		return &CreateTradingAccountReq{
			Name: "paper", Exchange: "binance", Markets: []string{"spot"},
			SettlementAsset: "USDT",
			Paper: &PaperConfig{
				InitialBalance: "1000", MakerFeeRate: maker, TakerFeeRate: taker, SlippageBps: slippage,
			},
		}
	}
	for name, req := range map[string]*CreateTradingAccountReq{
		"费率滑点缺省按0":    makeReq("", "", ""),
		"边界滑点9999.99": makeReq("0.0002", "0.0005", "9999.99"),
	} {
		if err := req.Validate(); err != nil {
			t.Errorf("%s 应通过：%v", name, err)
		}
	}
	for name, req := range map[string]*CreateTradingAccountReq{
		"滑点等于10000": makeReq("0", "0", "10000"),
		"滑点大于10000": makeReq("0", "0", "10000.01"),
		"负滑点":       makeReq("0", "0", "-1"),
		"负费率":       makeReq("-0.001", "0", "0"),
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

// 交易所与市场只收小写：大写或旧枚举名都被拒；列表过滤的空串表示不过滤。
func TestExchangeAndMarketAreLowercaseWhitelists(t *testing.T) {
	for _, value := range []string{"binance", "okx", "ashare", "hkex"} {
		if !validExchange(value) {
			t.Errorf("交易所 %q 应被接受", value)
		}
	}
	for _, value := range []string{"", "BINANCE", "Okx", "nyse"} {
		if validExchange(value) {
			t.Errorf("交易所 %q 应被拒绝", value)
		}
	}
	for _, value := range []string{"spot", "swap", "equity"} {
		if !validMarketType(value) {
			t.Errorf("市场 %q 应被接受", value)
		}
	}
	for _, value := range []string{"SPOT", "Swap", "stock"} {
		if validMarketType(value) {
			t.Errorf("市场 %q 应被拒绝", value)
		}
	}
	if err := (&ListTradingAccountsReq{}).Validate(); err != nil {
		t.Errorf("不过滤时应通过：%v", err)
	}
	if err := (&ListTradingAccountsReq{Exchange: "okx", MarketType: "swap"}).Validate(); err != nil {
		t.Errorf("小写过滤应通过：%v", err)
	}
	if err := (&ListTradingAccountsReq{MarketType: "SWAP"}).Validate(); err == nil {
		t.Error("大写市场应被拒绝")
	}
}
