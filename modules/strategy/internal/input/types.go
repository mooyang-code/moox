package input

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// ErrNotReady indicates that the immutable period snapshot is not complete
// yet. Trigger consumers should retry the readiness event rather than marking
// it processed, so a later factor-period event can make the same period
// evaluable.
var ErrNotReady = errors.New("evaluation input is not ready")

// ErrLegacyProvenance identifies an old readiness marker that predates the
// immutable View index provenance contract. It is terminal for that broker
// delivery: retrying cannot manufacture the missing generation identifiers.
var ErrLegacyProvenance = errors.New("evaluation input has legacy provenance")

// ErrStaleViewSnapshot means the readiness event's View generation has been
// superseded before the input could be read. Such a delivery is terminal for
// this generation; a newer ready event (or an explicit recalc) owns the
// replacement input and the broker message must not be retried forever.
var ErrStaleViewSnapshot = errors.New("evaluation input View snapshot is stale")

// ErrStrictIncomplete means the dependency Views are readable, but the
// selected pool is missing a current source row or required factor column.
// This is a terminal skip for the current ready message; a later ready event
// may re-evaluate the same period without keeping the broker delivery alive
// forever.
var ErrStrictIncomplete = errors.New("evaluation input is strictly incomplete")

// ErrPoolInvalid means an explicitly configured pool could not be resolved
// against the current subject directory. It is terminal for this evaluation
// attempt: publishing an empty FULL target would otherwise be interpreted by
// Trade as an instruction to flatten the account.
var ErrPoolInvalid = errors.New("strategy pool is invalid")

// StrictIncompleteError carries the resolved pool that was checked before a
// strict readiness failure. Trigger handling uses it to scope terminal
// subject failures even when the pool was selected dynamically (without an
// explicit include list).
type StrictIncompleteError struct {
	Pool    PoolResult
	Missing []string
}

func (e *StrictIncompleteError) Error() string {
	if e == nil || len(e.Missing) == 0 {
		return ErrStrictIncomplete.Error()
	}
	return ErrStrictIncomplete.Error() + ": missing current rows or factor columns: " + strings.Join(e.Missing, ",")
}

func (e *StrictIncompleteError) Unwrap() error { return ErrStrictIncomplete }

// PoolItem is one instrument admitted by the compiled instrument-pool rule.
// Identity and metadata are kept separate from factor values so the evaluator
// never needs to know how the values were collected.
type PoolItem struct {
	InstrumentID string
	SubjectID    string
	Exchange     string
	Market       string
	QuoteAsset   string
	SeriesTag    string
}

// InstrumentInput combines an admitted instrument with the factor values for
// a single completed period.
type InstrumentInput struct {
	PoolItem
	Values         map[string]quant.Decimal
	PreviousValues map[string]quant.Decimal
	// ScopedFields marks aliases that are intentionally unavailable for this
	// instrument because the corresponding subject-scoped binding does not
	// cover it.  Other missing fields remain strict evaluation errors.
	ScopedFields      map[string]bool
	ScopedFieldsReady bool
}

// EvaluationInput is immutable input for one strategy period. The producer is
// responsible for omitting instruments with missing required factor values.
type EvaluationInput struct {
	SpaceID       string
	StrategyID    string
	PeriodEnd     string
	SourceViewID  string
	DataFrequency string
	// View provenance is populated by Storage-backed loaders when available.
	// It makes the immutable generation used for a scheduled evaluation
	// auditable; event-driven evaluations additionally carry the producer's
	// expected generations.
	SourceIndexID       string
	SourceIndexRevision uint64
	ResultIndexID       string
	ResultIndexRevision uint64
	Items               []InstrumentInput
	Ineligible          map[string]string
	// BarIndex and BarEndAt let holding rules advance by valid market bars
	// instead of assuming every calendar has a 24-hour cadence.
	BarIndex    int64
	BarDuration time.Duration
	BarEndAt    func(int) time.Time
}

// Ordered returns a deterministic copy sorted by instrument identity.
func (in EvaluationInput) Ordered() []InstrumentInput {
	items := append([]InstrumentInput(nil), in.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].InstrumentID < items[j].InstrumentID
	})
	return items
}
