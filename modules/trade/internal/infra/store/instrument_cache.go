package store

import (
	"context"

	"gorm.io/gorm"
)

// instrumentScope 是一个场所的标的集合：交易所、行情环境、市场类型。
type instrumentScope struct {
	exchange    string
	environment string
	marketType  string
}

// instrumentSet 是一个场所的全部标的，按交易所代码和内部 ID 建索引。
type instrumentSet struct {
	list     []InstrumentRecord
	bySymbol map[string]InstrumentRecord
	byID     map[string]InstrumentRecord
}

func (set *instrumentSet) records() []InstrumentRecord {
	return append([]InstrumentRecord(nil), set.list...)
}

func (set *instrumentSet) symbol(exchangeSymbol string) (InstrumentRecord, error) {
	if record, ok := set.bySymbol[exchangeSymbol]; ok {
		return record, nil
	}
	return InstrumentRecord{}, gorm.ErrRecordNotFound
}

func (set *instrumentSet) id(instrumentID string) (InstrumentRecord, error) {
	if record, ok := set.byID[instrumentID]; ok {
		return record, nil
	}
	return InstrumentRecord{}, gorm.ErrRecordNotFound
}

func loadInstrumentSet(db *gorm.DB, scope instrumentScope) (*instrumentSet, error) {
	records, err := queryInstruments(db, scope.exchange, scope.environment, scope.marketType)
	if err != nil {
		return nil, err
	}
	set := &instrumentSet{
		list:     records,
		bySymbol: make(map[string]InstrumentRecord, len(records)),
		byID:     make(map[string]InstrumentRecord, len(records)),
	}
	for _, record := range records {
		set.bySymbol[record.ExchangeSymbol] = record
		set.byID[record.InstrumentID] = record
	}
	return set, nil
}

func (s *Store) instrumentSet(ctx context.Context, scope instrumentScope) (*instrumentSet, error) {
	return s.instrumentSets.Get(scope, func() (*instrumentSet, error) {
		return loadInstrumentSet(s.db.WithContext(ctx), scope)
	})
}

func (tx *Tx) instrumentSet(scope instrumentScope) (*instrumentSet, error) {
	if tx.store == nil || tx.instrumentsWritten {
		return loadInstrumentSet(tx.db, scope)
	}
	return tx.store.instrumentSets.Get(scope, func() (*instrumentSet, error) {
		return loadInstrumentSet(tx.db, scope)
	})
}

// accountKey 是账户身份缓存的键。
type accountKey struct {
	spaceID          string
	tradingAccountID string
}
