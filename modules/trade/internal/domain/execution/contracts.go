package execution

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"time"
)

type AlgorithmDescriptor struct{ Name, Version string }
type SliceDraft struct {
	Sequence  int
	Quantity  shared.Decimal
	DependsOn []int
}
type SplitInput struct {
	Quantity, ReferencePrice, MaxNotional shared.Decimal
	Rules                                 instrument.Rules
	Parameters                            map[string]string
}
type PricingInput struct {
	Side           string
	ReferencePrice shared.Decimal
	Rules          instrument.Rules
	Parameters     map[string]string
}
type OrderQuote struct {
	Price       shared.Decimal
	TimeInForce string
	SlippageBPS int
}
type ExecutionState struct {
	Ready []Slice
	Now   time.Time
}
type ExecutionCommand struct {
	SliceID shared.ExecutionSliceID
	Type    string
}

type SplitAlgorithm interface {
	Descriptor() AlgorithmDescriptor
	Build(context.Context, SplitInput) ([]SliceDraft, error)
}
type PricingAlgorithm interface {
	Descriptor() AlgorithmDescriptor
	Quote(context.Context, PricingInput) (OrderQuote, error)
}
type ExecutionPolicy interface {
	Descriptor() AlgorithmDescriptor
	Next(context.Context, ExecutionState) ([]ExecutionCommand, error)
}

type SliceState string

const (
	SlicePlanned       SliceState = "PLANNED"
	SliceReady         SliceState = "READY"
	SliceSubmitting    SliceState = "SUBMITTING"
	SliceAcknowledged  SliceState = "ACKNOWLEDGED"
	SlicePartial       SliceState = "PARTIAL"
	SliceFilled        SliceState = "FILLED"
	SliceSubmitUnknown SliceState = "SUBMIT_UNKNOWN"
	SliceRejected      SliceState = "REJECTED"
	SliceExhausted     SliceState = "EXHAUSTED"
)

type Slice struct {
	ID                       shared.ExecutionSliceID
	Sequence                 int
	Quantity, FilledQuantity shared.Decimal
	State                    SliceState
	DependsOn                []int
}
