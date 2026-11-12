package subjectsync

import (
	// 生产里标的规则随行情源目录登记（sources/catalog）；这个包不引用目录，测试里直接引用两个市场的规则包。
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
)
