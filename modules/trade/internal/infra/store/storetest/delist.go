package storetest

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// BlockInstrument 直接写一行下架名单（范围为空按 ALL）；已在名单里时不改动。生产代码只在收下架事件的事务里写名单。
func BlockInstrument(s *store.Store, record store.BlockedInstrumentRecord) (created bool, err error) {
	err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		created, err = tx.BlockInstrument(record)
		return err
	})
	return created, err
}

// UnblockInstrument 在单独的事务里解除名单并取消进行中的下架记录（与管理台解除走同一个 tx.UnblockInstrument）。
func UnblockInstrument(s *store.Store, exchange, marketType, exchangeSymbol string) (removed bool, err error) {
	err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		removed, _, err = tx.UnblockInstrument(exchange, marketType, exchangeSymbol, store.DelistUnblockedResult, time.Now().UnixMilli())
		return err
	})
	return removed, err
}
