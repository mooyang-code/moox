package jetstream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type runnerFakeConsumer struct {
	mu      sync.Mutex
	batches [][]*Delivery
	errs    []error
	fetches int
	onFetch func()
}

func (f *runnerFakeConsumer) Fetch(_ context.Context, _ int) ([]*Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if f.onFetch != nil {
		f.onFetch()
	}
	var deliveries []*Delivery
	if len(f.batches) > 0 {
		deliveries = f.batches[0]
		f.batches = f.batches[1:]
	}
	var err error
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	if len(f.batches) == 0 && len(deliveries) == 0 && err == nil {
		return nil, nats.ErrTimeout
	}
	return deliveries, err
}

func (f *runnerFakeConsumer) Close() error { return nil }

type runnerFakeHandler struct {
	result   HandlerResult
	seen     int
	onHandle func()
}

func (h *runnerFakeHandler) Handle(context.Context, *Delivery) HandlerResult {
	h.seen++
	if h.onHandle != nil {
		h.onHandle()
	}
	return h.result
}

func runnerDelivery(actions *[]string, errs map[string]error) *Delivery {
	return &Delivery{
		ackFn:      func(context.Context) error { *actions = append(*actions, "ack"); return errs["ack"] },
		nakFn:      func(context.Context, time.Duration) error { *actions = append(*actions, "nak"); return errs["nak"] },
		termFn:     func(context.Context) error { *actions = append(*actions, "term"); return errs["term"] },
		progressFn: func(context.Context) error { *actions = append(*actions, "progress"); return errs["progress"] },
	}
}

func TestRunnerACK(t *testing.T) {
	var actions []string
	ctx, cancel := context.WithCancel(context.Background())
	delivery := runnerDelivery(&actions, nil)
	delivery.ackFn = func(context.Context) error {
		actions = append(actions, "ack")
		cancel()
		return nil
	}
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{delivery}}}
	handler := &runnerFakeHandler{result: HandlerResult{Decision: ACK}}
	runner := NewRunner(consumer, handler, RunnerConfig{BatchSize: 1})
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(actions) != 1 || actions[0] != "ack" {
		t.Fatalf("actions = %v, want [ack]", actions)
	}
}

func TestRunnerRetryAndTerm(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision HandlerDecision
		want     string
	}{
		{name: "retry", decision: RETRY, want: "nak"},
		{name: "term", decision: TERM, want: "term"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var actions []string
			ctx, cancel := context.WithCancel(context.Background())
			delivery := runnerDelivery(&actions, nil)
			if test.decision == RETRY {
				delivery.nakFn = func(context.Context, time.Duration) error { actions = append(actions, "nak"); cancel(); return nil }
			} else {
				delivery.termFn = func(context.Context) error { actions = append(actions, "term"); cancel(); return nil }
			}
			consumer := &runnerFakeConsumer{batches: [][]*Delivery{{delivery}}}
			handler := &runnerFakeHandler{result: HandlerResult{Decision: test.decision, Delay: time.Second}}
			if err := NewRunner(consumer, handler, RunnerConfig{}).Run(ctx); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(actions) != 1 || actions[0] != test.want {
				t.Fatalf("actions = %v, want [%s]", actions, test.want)
			}
		})
	}
}

func TestRunnerReportsHandlerAndTransportErrors(t *testing.T) {
	var actions []string
	ackErr := errors.New("ack failed")
	handlerErr := errors.New("handler failed")
	var reported []error
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{runnerDelivery(&actions, map[string]error{"ack": ackErr})}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewRunner(consumer, &runnerFakeHandler{result: HandlerResult{Decision: ACK, Err: handlerErr}}, RunnerConfig{ErrorReporter: ErrorReporterFunc(func(err error) { reported = append(reported, err) })})
	err := runner.Run(ctx)
	if !errors.Is(err, ackErr) || !errors.Is(err, handlerErr) {
		t.Fatalf("Run() error = %v, want handler and ack errors", err)
	}
	if len(reported) == 0 {
		t.Fatal("ErrorReporter did not receive an error")
	}
	if len(actions) != 1 || actions[0] != "ack" {
		t.Fatalf("actions = %v, want [ack]", actions)
	}
}

func TestRunnerRetryStopsBatchAndNaksRemainingWithSameDelay(t *testing.T) {
	var actions []string
	var delays []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := runnerDelivery(&actions, nil)
	second := runnerDelivery(&actions, nil)
	third := runnerDelivery(&actions, nil)
	first.nakFn = func(_ context.Context, _ time.Duration) error {
		actions = append(actions, "nak")
		cancel()
		return nil
	}
	second.nakFn = func(_ context.Context, delay time.Duration) error {
		actions = append(actions, "second-nak")
		delays = append(delays, delay)
		return nil
	}
	third.nakFn = func(_ context.Context, delay time.Duration) error {
		actions = append(actions, "third-nak")
		delays = append(delays, delay)
		return nil
	}
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{first, second, third}}}
	handler := DeliveryHandlerFunc(func(_ context.Context, delivery *Delivery) HandlerResult {
		if delivery == first {
			return HandlerResult{Decision: RETRY, Delay: 3 * time.Second}
		}
		return HandlerResult{Decision: ACK}
	})
	if err := NewRunner(consumer, handler, RunnerConfig{}).Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := actions; len(got) != 3 || got[0] != "nak" || got[1] != "second-nak" || got[2] != "third-nak" {
		t.Fatalf("actions = %v, want current and remaining NAKs", got)
	}
	if len(delays) != 2 || delays[0] != 3*time.Second || delays[1] != 3*time.Second {
		t.Fatalf("remaining delays = %v, want 3s", delays)
	}
}

func TestRunnerAggregatesRemainingNAKErrors(t *testing.T) {
	firstErr := errors.New("first nak failed")
	secondErr := errors.New("second nak failed")
	var actions []string
	first := runnerDelivery(&actions, map[string]error{"nak": firstErr})
	second := runnerDelivery(&actions, map[string]error{"nak": secondErr})
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{first, second}}}
	err := NewRunner(consumer, DeliveryHandlerFunc(func(context.Context, *Delivery) HandlerResult {
		return HandlerResult{Decision: RETRY, Delay: time.Second}
	}), RunnerConfig{}).Run(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Run() error = %v, want both NAK errors", err)
	}
}

func TestRunnerProcessesDeliveriesWithDecodeError(t *testing.T) {
	var actions []string
	ctx, cancel := context.WithCancel(context.Background())
	delivery := runnerDelivery(&actions, nil)
	delivery.termFn = func(context.Context) error { actions = append(actions, "term"); cancel(); return nil }
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{delivery}}, errs: []error{ErrDecode}}
	handler := &runnerFakeHandler{result: HandlerResult{Decision: TERM}}
	if err := NewRunner(consumer, handler, RunnerConfig{}).Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if handler.seen != 1 || len(actions) != 1 || actions[0] != "term" {
		t.Fatalf("seen=%d actions=%v, want one handled TERM", handler.seen, actions)
	}
}

func TestRunnerDoesNotHideDecodeAndTransportError(t *testing.T) {
	transportErr := errors.New("poison term failed")
	consumer := &runnerFakeConsumer{errs: []error{errors.Join(ErrDecode, transportErr)}}
	err := NewRunner(consumer, &runnerFakeHandler{result: HandlerResult{Decision: ACK}}, RunnerConfig{}).Run(context.Background())
	if !errors.Is(err, transportErr) {
		t.Fatalf("Run() error = %v, want poison transport error", err)
	}
}

func TestRunnerCancellationDoesNotStartNextDelivery(t *testing.T) {
	var actions []string
	first := runnerDelivery(&actions, nil)
	second := runnerDelivery(&actions, nil)
	ctx, cancel := context.WithCancel(context.Background())
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{first, second}}}
	consumer.onFetch = func() { cancel() }
	if err := NewRunner(consumer, &runnerFakeHandler{result: HandlerResult{Decision: ACK}}, RunnerConfig{}).Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions = %v, want no delivery started after cancellation", actions)
	}
}

func TestRunnerInProgressHeartbeat(t *testing.T) {
	var actions []string
	consumer := &runnerFakeConsumer{batches: [][]*Delivery{{runnerDelivery(&actions, nil)}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivery := consumer.batches[0][0]
	handler := DeliveryHandlerFunc(func(ctx context.Context, _ *Delivery) HandlerResult {
		time.Sleep(5 * time.Millisecond)
		cancel()
		return HandlerResult{Decision: RETRY}
	})
	_ = delivery
	if err := NewRunner(consumer, handler, RunnerConfig{InProgressInterval: time.Millisecond}).Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !containsString(actions, "progress") {
		t.Fatalf("actions = %v, want an in-progress heartbeat", actions)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
