package storetest

import (
	"context"

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
