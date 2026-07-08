package testkit

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

// FakeExecutor returns one deterministic value per requested factor parameter.
type FakeExecutor struct {
	Latency time.Duration
	Calls   atomic.Int64
}

func (e *FakeExecutor) Execute(context.Context, *engine.FactorTask, *engine.DataFrame) (*engine.FactorResult, error) {
	e.Calls.Add(1)
	if e.Latency > 0 {
		time.Sleep(e.Latency)
	}
	return &engine.FactorResult{
		Columns: map[string]engine.FactorColumnResult{
			"Bias_20": {Tail: 1, Values: []any{1.0}},
		},
		ElapsedMS: e.Latency.Milliseconds(),
	}, nil
}

func (e *FakeExecutor) Close() error { return nil }
