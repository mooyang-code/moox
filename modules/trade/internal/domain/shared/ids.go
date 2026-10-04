package shared

type OrderID string
type FillID string
type InstrumentID string
type ExchangeSymbol string

func (v InstrumentID) String() string   { return string(v) }
func (v ExchangeSymbol) String() string { return string(v) }
