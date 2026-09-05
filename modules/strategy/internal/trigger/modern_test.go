package trigger

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func TestModernDSLInstanceProducesTargetResult(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	dsl := `name: demo
triggers:
  event: {name: factor.ready}
data: {bar: 1m, calendar: crypto_24x7}
rules:
  rank:
    pool: [BTC, ETH]
    score: bias
    select: {top: 1}
    weight: "1"
`
	if err := repo.SaveStrategyDefinition(context.Background(), store.StrategyDefinition{StrategyID: "s1", StrategyName: "demo", DSLYaml: dsl, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.CreateInstance(context.Background(), store.StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", InputBindingsJSON: json.RawMessage(`{"source_view_id":"source","frequency":"1m"}`), Enabled: true, SessionID: &session, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	period := now.Add(-time.Minute).Truncate(time.Minute)
	loader := fakeInputLoader{value: input.EvaluationInput{SpaceID: "space", StrategyID: "s1", PeriodEnd: period.Format(time.RFC3339Nano), SourceViewID: "source", DataFrequency: "1m", Items: []input.InstrumentInput{{PoolItem: input.PoolItem{InstrumentID: "BTC", SubjectID: "btc"}, Values: map[string]quant.Decimal{"bias": quant.Must("1")}}, {PoolItem: input.PoolItem{InstrumentID: "ETH", SubjectID: "eth"}, Values: map[string]quant.Decimal{"bias": quant.Must("2")}}}}}
	p := &Processor{Store: repo, Loader: loader, Now: func() time.Time { return now }}
	if err := p.Handle(context.Background(), PeriodReady{MessageID: "m1", EventName: "factor.ready", SpaceID: "space", ViewID: "factor", Frequency: "1m", PeriodTime: period, Status: "degraded", BindingStatuses: map[string]string{"unrelated": "degraded"}}); err != nil {
		t.Fatal(err)
	}
	result, err := repo.LatestResult(context.Background(), "i1", session)
	if err != nil {
		t.Fatal(err)
	}
	var targets []domain.InstrumentTarget
	if err := json.Unmarshal(result.TargetsJSON, &targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].InstrumentID != "ETH" || targets[0].TargetWeight != "1" {
		t.Fatalf("targets=%s", result.TargetsJSON)
	}
}

type blockingModernLoader struct {
	started       chan struct{}
	secondStarted chan struct{}
	release       chan struct{}
	mu            sync.Mutex
	calls         int
}

func (l *blockingModernLoader) Load(_ context.Context, _ domain.StrategyRunner, _ compiler.CompiledStrategy, period time.Time) (input.EvaluationInput, error) {
	l.mu.Lock()
	l.calls++
	call := l.calls
	l.mu.Unlock()
	if call == 1 {
		close(l.started)
		<-l.release
	} else if call == 2 {
		close(l.secondStarted)
	}
	return input.EvaluationInput{
		SpaceID: "space", StrategyID: "s1", PeriodEnd: period.Format(time.RFC3339Nano),
		SourceViewID: "source", DataFrequency: "1m",
		Items: []input.InstrumentInput{{PoolItem: input.PoolItem{InstrumentID: "BTC", SubjectID: "btc"}, Values: map[string]quant.Decimal{"bias": quant.Must("1")}}},
	}, nil
}

func TestModernEvaluationsAreSerializedAcrossTriggers(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(2_000_000).UTC()
	dsl := `name: serial
triggers:
  event: {name: factor.ready}
data: {bar: 1m, calendar: crypto_24x7}
rules:
  rank:
    pool: [BTC]
    score: bias
    select: {top: 1}
    weight: "1"
`
	if err := repo.SaveStrategyDefinition(context.Background(), store.StrategyDefinition{StrategyID: "s1", StrategyName: "serial", DSLYaml: dsl, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.CreateInstance(context.Background(), store.StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", InputBindingsJSON: json.RawMessage(`{"source_view_id":"source","frequency":"1m"}`), Enabled: true, SessionID: &session, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	loader := &blockingModernLoader{started: make(chan struct{}), secondStarted: make(chan struct{}), release: make(chan struct{})}
	p := &Processor{Store: repo, Loader: loader, Now: func() time.Time { return now }}
	firstPeriod := now.Add(-2 * time.Minute)
	secondPeriod := now.Add(-time.Minute)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- p.Handle(context.Background(), PeriodReady{MessageID: "m1", EventName: "factor.ready", SpaceID: "space", ViewID: "source", Frequency: "1m", PeriodTime: firstPeriod})
	}()
	select {
	case <-loader.started:
	case <-time.After(time.Second):
		t.Fatal("first evaluation did not reach loader")
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- p.Handle(context.Background(), PeriodReady{MessageID: "m2", EventName: "factor.ready", SpaceID: "space", ViewID: "source", Frequency: "1m", PeriodTime: secondPeriod})
	}()
	select {
	case <-loader.secondStarted:
		t.Fatal("second evaluation entered loader before first evaluation released")
	case <-time.After(50 * time.Millisecond):
	}
	close(loader.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	loader.mu.Lock()
	calls := loader.calls
	loader.mu.Unlock()
	if calls != 2 {
		t.Fatalf("loader calls = %d, want 2 serialized evaluations", calls)
	}
}
