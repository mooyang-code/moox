package rpc

import (
	// 生产里标的规则由装配入口（marketwiring）引用后登记；这个包不引用装配层，测试里直接引用两个市场的规则包。
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
)
