package instrument

import "github.com/mooyang-code/moox/modules/trade/internal/domain/shared"

type LeverageBracket struct {
	MaxNotional shared.Decimal
	MaxLeverage int
}

type Rules struct {
	Version             string
	Symbol              string
	TickSize            shared.Decimal
	StepSize            shared.Decimal
	MinQuantity         shared.Decimal
	MinNotional         shared.Decimal
	LeverageBrackets    []LeverageBracket
	SupportsSTP         bool
	SupportsNativeAmend bool
}
