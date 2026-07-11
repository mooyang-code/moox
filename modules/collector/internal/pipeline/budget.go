package pipeline

import (
	"context"
	"time"
)

type Budget struct {
	deadline time.Time
	reserve  time.Duration
}

func NewBudget(parent context.Context, now time.Time, execution, reserve time.Duration) Budget {
	deadline := now.Add(execution)
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	return Budget{deadline: deadline, reserve: reserve}
}
func (b Budget) Deadline() time.Time { return b.deadline }
func (b Budget) CanStart(now time.Time, estimated time.Duration) bool {
	return !now.Add(estimated).After(b.deadline.Add(-b.reserve))
}
