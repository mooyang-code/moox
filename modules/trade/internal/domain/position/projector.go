package position

import "github.com/mooyang-code/moox/modules/trade/internal/domain/shared"

type Position struct {
	Symbol                              string
	Quantity, AveragePrice, RealizedPnL shared.Decimal
	Version                             uint64
}
type Fill struct {
	Side            string
	Quantity, Price shared.Decimal
}

func (p Position) Apply(f Fill) Position {
	signed := f.Quantity
	if f.Side == "SELL" {
		signed = signed.Neg()
	}
	old := p.Quantity
	next := old.Add(signed)
	if old.IsZero() || (old.IsNegative() == signed.IsNegative()) {
		oldCost := p.AveragePrice.Mul(old.Abs())
		addCost := f.Price.Mul(signed.Abs())
		if !next.IsZero() {
			p.AveragePrice = oldCost.Add(addCost).Mul(shared.MustDecimal("1")).Div(next.Abs())
		}
	} else {
		closed := signed.Abs()
		if old.Abs().Cmp(closed) < 0 {
			closed = old.Abs()
		}
		pnl := f.Price.Sub(p.AveragePrice).Mul(closed)
		if old.IsNegative() {
			pnl = pnl.Neg()
		}
		p.RealizedPnL = p.RealizedPnL.Add(pnl)
		if next.IsZero() {
			p.AveragePrice = shared.Zero()
		} else if old.IsNegative() != next.IsNegative() {
			p.AveragePrice = f.Price
		}
	}
	p.Quantity = next
	p.Version++
	return p
}
