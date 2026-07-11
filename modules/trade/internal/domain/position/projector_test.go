package position

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestPositionProjection(t *testing.T) {
	p := Position{Symbol: "BTC", Quantity: shared.Zero(), AveragePrice: shared.Zero(), RealizedPnL: shared.Zero()}
	p = p.Apply(Fill{Side: "BUY", Quantity: shared.MustDecimal("2"), Price: shared.MustDecimal("10")})
	p = p.Apply(Fill{Side: "SELL", Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("12")})
	if p.Quantity.String() != "1" || p.RealizedPnL.String() != "2" {
		t.Fatalf("%+v", p)
	}
}
