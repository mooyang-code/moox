// Package timerjob provides synchronous, observable tRPC timer handlers.
package timerjob

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"
)

// Result is the bounded outcome label recorded for a timer invocation.
type Result string

const (
	ResultSuccess Result = "success"
	ResultError   Result = "error"
	ResultTimeout Result = "timeout"
	ResultSkipped Result = "skipped_overlap"
)

// Job wraps one synchronous timer callback with timeout and local overlap protection.
type Job struct {
	name    string
	timeout time.Duration
	run     func(context.Context) error
	running atomic.Bool
}

// New constructs a guarded timer job.
func New(name string, timeout time.Duration, run func(context.Context) error) (*Job, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("timer job name is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timer job timeout must be positive")
	}
	if run == nil {
		return nil, fmt.Errorf("timer job callback is required")
	}
	return &Job{name: name, timeout: timeout, run: run}, nil
}

// Handle executes the callback synchronously or skips a concurrent local invocation.
func (j *Job) Handle(ctx context.Context) error {
	if !j.running.CompareAndSwap(false, true) {
		observe(j.name, ResultSkipped, 0)
		return nil
	}
	defer j.running.Store(false)

	jobCtx := trpc.CloneContext(ctx)
	jobCtx, cancel := context.WithTimeout(jobCtx, j.timeout)
	defer cancel()

	started := time.Now()
	err := j.run(jobCtx)
	if err == nil && jobCtx.Err() != nil {
		err = jobCtx.Err()
	}
	observe(j.name, classify(err, jobCtx.Err()), time.Since(started))
	return err
}

func classify(err, contextErr error) Result {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return ResultTimeout
	}
	if err != nil {
		return ResultError
	}
	return ResultSuccess
}
