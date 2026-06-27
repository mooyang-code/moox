// Package all 通过 blank import 注册所有内置交易所适配器。
// 在服务启动处 import 本包即可让 exchange.New("binance"|"okx") 可用。
package all

import (
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/binance"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/okx"
)
