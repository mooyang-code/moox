package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
)

// Status describes the realtime consumer's business progress, not just its
// transport connection. It is surfaced by Factor health checks so a handler
// stuck on one period is distinguishable from an idle healthy consumer.
type Status struct {
	LastReceivedAt    time.Time
	LastCompletedAt   time.Time
	LastAckAt         time.Time
	LastFailureAt     time.Time
	LastFailure       string
	InFlightStartedAt time.Time
	InFlightEventID   string
	ExecutionTimeouts int
	Stalled           bool
}

type progressState struct {
	mu                     sync.RWMutex
	lastReceivedAt         time.Time
	lastCompletedAt        time.Time
	lastAckAt              time.Time
	lastFailureAt          time.Time
	lastFailure            string
	inFlightStartedAt      time.Time
	inFlightEventID        string
	inFlightStallThreshold time.Duration
	timeoutEventAttempts   map[string]int
	executionTimeouts      int
}

func newProgressState() *progressState {
	return &progressState{timeoutEventAttempts: make(map[string]int)}
}

func (p *progressState) begin(eventID string, now time.Time) {
	p.beginWithThreshold(eventID, now, 0)
}

func (p *progressState) beginWithThreshold(eventID string, now time.Time, stallThreshold time.Duration) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.lastReceivedAt = now
	p.inFlightStartedAt = now
	p.inFlightEventID = eventID
	p.inFlightStallThreshold = stallThreshold
	p.mu.Unlock()
}

func (p *progressState) finish(eventID string, completed bool, err error, now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if completed {
		p.lastCompletedAt = now
		delete(p.timeoutEventAttempts, eventID)
		p.executionTimeouts = 0
	}
	if err != nil {
		p.lastFailureAt = now
		p.lastFailure = err.Error()
	}
	if p.inFlightEventID == eventID {
		p.inFlightStartedAt = time.Time{}
		p.inFlightEventID = ""
		p.inFlightStallThreshold = 0
	}
	p.mu.Unlock()
}

func (p *progressState) recordExecutionTimeout(eventID string) int {
	if p == nil {
		return 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timeoutEventAttempts == nil {
		p.timeoutEventAttempts = make(map[string]int)
	}
	p.timeoutEventAttempts[eventID]++
	p.executionTimeouts++
	return p.timeoutEventAttempts[eventID]
}

func (p *progressState) ReportAction(_ context.Context, _ *jetstream.Delivery, result jetstream.HandlerResult, actionErr error) {
	if p == nil {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	failure := errors.Join(result.Err, actionErr)
	if failure != nil {
		p.lastFailureAt = now
		p.lastFailure = failure.Error()
	}
	if actionErr == nil && result.Decision == jetstream.ACK {
		p.lastAckAt = now
	}
}

func (p *progressState) status(now time.Time, stallThreshold time.Duration) Status {
	if p == nil {
		return Status{}
	}
	p.mu.RLock()
	result := Status{
		LastReceivedAt: p.lastReceivedAt, LastCompletedAt: p.lastCompletedAt,
		LastAckAt: p.lastAckAt, LastFailureAt: p.lastFailureAt,
		LastFailure: p.lastFailure, InFlightStartedAt: p.inFlightStartedAt,
		InFlightEventID: p.inFlightEventID, ExecutionTimeouts: p.executionTimeouts,
	}
	currentThreshold := p.inFlightStallThreshold
	p.mu.RUnlock()
	if currentThreshold < stallThreshold {
		currentThreshold = stallThreshold
	}
	result.Stalled = currentThreshold > 0 && !result.InFlightStartedAt.IsZero() &&
		now.Sub(result.InFlightStartedAt) > currentThreshold
	// A completed timeout clears the in-flight marker before readiness is
	// sampled. Keep the consumer degraded until a later event completes, so
	// the watchdog can observe repeated timeout/retry loops.
	result.Stalled = result.Stalled || result.ExecutionTimeouts > 0
	return result
}
