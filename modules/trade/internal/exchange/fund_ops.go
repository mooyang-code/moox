package exchange

import "github.com/mooyang-code/moox/modules/trade/internal/domain/shared"

// DustAsset 是交易所报告的、可以兑换成 BNB 的小额资产。
type DustAsset struct {
	Asset    string
	Quantity shared.Decimal
	// ValueBTC 是交易所给出的折合 BTC 的价值，用来判断是否低于兑换门槛。
	ValueBTC shared.Decimal
}

// DustConversion 是一次小额资产兑换的结果。
type DustConversion struct {
	// Converted 是实际兑换成功的资产。
	Converted []string
	// BNBReceived 是换到的 BNB 数量。
	BNBReceived shared.Decimal
}
