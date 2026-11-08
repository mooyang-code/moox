package replay

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/ledger"
)

// snapshotPosition 是写入 positions_json 的一个持仓：带上标的与市场，键仍是 engine.TargetKey。
type snapshotPosition struct {
	InstrumentID string `json:"instrument_id"`
	Market       string `json:"market"`
	ledger.Position
}

// snapshotLedger 返回持仓与现金的拷贝（写入 positions_json），按输出精度取整。
func snapshotLedger(book *ledger.Ledger) map[string]any {
	positions := make(map[string]snapshotPosition, len(book.Positions))
	for id, position := range book.Positions {
		copied := *position
		copied.Quantity, copied.Value = roundPlaces(copied.Quantity, quantityPlaces), round4(copied.Value)
		instrument, market := engine.SplitTargetKey(id)
		positions[id] = snapshotPosition{InstrumentID: instrument, Market: market, Position: copied}
	}
	return map[string]any{"cash": round4(book.Cash), "positions": positions}
}
