package instrument

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var ErrInvalidInstrument = errors.New("trade: invalid Exchange instrument")

type Status string

const (
	StatusEnabled  Status = "ENABLED"
	StatusDisabled Status = "DISABLED"
)

type Instrument struct {
	Exchange             exchange.Exchange
	MarketType           exchange.MarketType
	Symbol               string
	InstrumentID         string
	BaseAsset            string
	QuoteAsset           string
	SettlementAsset      string
	Linear               bool
	ContractValue        shared.Decimal
	ContractValueAsset   string
	ExchangeQuantityStep shared.Decimal
	MinExchangeQuantity  shared.Decimal
	PriceTick            shared.Decimal
	MinNotional          shared.Decimal
	Status               Status
	ExchangeUpdatedAt    time.Time
}

func (i Instrument) Validate() error {
	if !i.Exchange.Valid() ||
		!i.MarketType.Valid() ||
		blank(i.Symbol) ||
		blank(i.BaseAsset) ||
		blank(i.QuoteAsset) ||
		i.ExchangeQuantityStep.Cmp(shared.Zero()) <= 0 ||
		i.PriceTick.Cmp(shared.Zero()) <= 0 ||
		i.MinExchangeQuantity.IsNegative() ||
		i.MinNotional.IsNegative() ||
		(i.Status != StatusEnabled && i.Status != StatusDisabled) {
		return invalidInstrument("missing, unsupported, or nonpositive field")
	}
	switch i.MarketType {
	case exchange.MarketTypeSpot:
		if i.Linear ||
			!i.ContractValue.IsZero() ||
			!blank(i.ContractValueAsset) {
			return invalidInstrument("SPOT cannot contain contract conversion")
		}
	case exchange.MarketTypeSwap:
		if !i.Linear ||
			blank(i.SettlementAsset) ||
			i.ContractValue.Cmp(shared.Zero()) <= 0 ||
			i.ContractValueAsset != i.BaseAsset ||
			i.MinExchangeQuantity.Cmp(shared.Zero()) <= 0 {
			return invalidInstrument("SWAP requires linear base-quantity conversion")
		}
	}
	return nil
}

func invalidInstrument(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidInstrument, reason)
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
