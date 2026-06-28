package binance

import "github.com/mooyang-code/moox/modules/trade/internal/exchange"

func credFixture() exchange.Credential {
	return exchange.Credential{APIKey: "vmPUZE6mvfSDkbVHjU6f", APISecret: "NhqPtmdSJYdKjVHjA7PZj4Mge3R5YNiP1e3UZjInrB5"}
}

func orderTypeOf(s string) exchange.OrderType { return exchange.OrderType(s) }
