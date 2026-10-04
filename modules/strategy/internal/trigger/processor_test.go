package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type fakeInputLoader struct {
	value input.EvaluationInput
	err   error
}

func (f fakeInputLoader) Load(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time) (input.EvaluationInput, error) {
	return f.value, f.err
}

type legacyRunnerProbeLoader struct{ calls int }

func (l *legacyRunnerProbeLoader) Load(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time) (input.EvaluationInput, error) {
	l.calls++
	return input.EvaluationInput{}, errors.New("legacy runner execution must be disabled")
}

func TestProcessorDoesNotExecuteLegacyRunner(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompiledStrategy{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		SpaceID:    "space",
		SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"},
		Schedule:   compiler.CompiledSchedule{Every: "1m"},
	}
	raw, err := json.Marshal(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "legacy-strategy", Name: "legacy", Kind: config.Kind, CompiledJSON: raw, CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "legacy-runner", StrategyID: "legacy-strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusEnabled,
		CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	loader := &legacyRunnerProbeLoader{}
	processor := &Processor{Store: repo, Loader: loader, Now: func() time.Time { return time.UnixMilli(2_000) }}
	event := PeriodReady{MessageID: "legacy-runner-ready", EventName: "event.storage.view.data.ready", SpaceID: "space", ViewID: "source", PeriodTime: time.UnixMilli(60_000)}
	if err := processor.Handle(context.Background(), event); err != nil {
		t.Fatalf("legacy runner event should be acknowledged without execution: %v", err)
	}
	if loader.calls != 0 {
		t.Fatalf("legacy runner entered the evaluator %d times", loader.calls)
	}
	processed, err := repo.IsProcessed(context.Background(), event.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("legacy runner event was not acknowledged")
	}
}

func TestIndexProvenanceIsPartial(t *testing.T) {
	tests := []struct {
		name    string
		event   PeriodReady
		partial bool
	}{
		{name: "none", event: PeriodReady{}, partial: false},
		{name: "paired", event: PeriodReady{SourceIndexID: "source", SourceIndexRevision: 1, ResultIndexID: "result", ResultIndexRevision: 2}, partial: false},
		{name: "source only", event: PeriodReady{SourceIndexID: "source", SourceIndexRevision: 1}, partial: true},
		{name: "result only", event: PeriodReady{ResultIndexID: "result", ResultIndexRevision: 2}, partial: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := indexProvenanceIsPartial(tt.event); got != tt.partial {
				t.Fatalf("indexProvenanceIsPartial() = %v, want %v", got, tt.partial)
			}
		})
	}
}

func TestMarshalTargetEventUsesSessionFenceWithoutOwnerGeneration(t *testing.T) {
	account := "logical-1"
	now := time.UnixMilli(1000).UTC()
	raw, err := marshalTargetEvent(store.StrategyInstance{InstanceID: "instance-1", StrategyID: "strategy-1", SpaceID: "space-1", LogicalAccountID: &account}, store.StrategyResult{ResultID: "result-1", InstanceID: "instance-1", SessionID: "session-1", BarEndTime: now, ValidUntil: now.Add(time.Hour), CreatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, err := registry.UnmarshalMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	payload := new(tradeeventpb.LogicalAccountTargetWeightRequested)
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	if payload.GetTargetId() != "result-1" || payload.GetInstanceId() != "instance-1" || payload.GetSessionId() != "session-1" || payload.GetStrategyId() != "strategy-1" || payload.GetLogicalAccountId() != account {
		t.Fatalf("modern target identity = %+v", payload)
	}
	if !payload.GetBarEndTime().AsTime().Equal(now) || !payload.GetEffectiveAt().AsTime().Equal(now) || !payload.GetValidUntil().AsTime().Equal(now.Add(time.Hour)) {
		t.Fatalf("modern target times = %+v", payload)
	}
	for _, name := range []protoreflect.Name{"command_sequence", "signal_time", "owner_generation", "runner_id"} {
		if payload.ProtoReflect().Descriptor().Fields().ByName(name) != nil {
			t.Errorf("modern target descriptor still exposes %s", name)
		}
	}
}

func TestCanonicalEventNameMapsViewDataReady(t *testing.T) {
	if got := canonicalEventName("ViewDataReady"); got != "event.storage.view.data.ready" {
		t.Fatalf("ViewDataReady canonical name = %q", got)
	}
	if got := canonicalEventName("ready"); got != "event.storage.view.data.ready" {
		t.Fatalf("ready canonical name = %q", got)
	}
}

func TestAugmentCompiledBindingsPreservesCompiledProgramAndAddsFactorViews(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		SourceView:  compiler.CompiledView{ID: "catalog-source", Frequency: "1m", Status: "active"},
		Rules:       []compiler.CompiledRule{{Name: "rank"}},
		InputFields: map[string]reflect.Type{"bias": reflect.TypeOf(float64(0))},
	}
	augmentCompiledBindings(&compiled, json.RawMessage(`{"source_view_id":"bound-source","factors":[{"factor_id":"bias","set_id":"set-1","result_view_id":"factor-view","column_name":"bias"}]}`))
	if compiled.SourceView.ID != "bound-source" || compiled.SourceView.Frequency != "1m" || len(compiled.Rules) != 1 || len(compiled.InputFields) != 1 {
		t.Fatalf("compiled binding augmentation lost catalog program: %+v", compiled)
	}
	if len(compiled.Factors) != 1 || compiled.Dependencies.FactorResultViewIDs[0] != "factor-view" {
		t.Fatalf("factor selection not added: %+v", compiled)
	}
}

func TestDependsOnEventOnlyMatchesDeclaredViews(t *testing.T) {
	compiled := compiler.CompiledStrategy{SourceView: compiler.CompiledView{ID: "source"}, Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor"}}}
	if dependsOnEvent(compiled, PeriodReady{ViewID: "unrelated"}) {
		t.Fatal("unrelated ready view triggered strategy")
	}
	if !dependsOnEvent(compiled, PeriodReady{ViewID: "factor"}) || !dependsOnEvent(compiled, PeriodReady{ViewID: "source"}) {
		t.Fatal("declared ready view did not trigger strategy")
	}
}

func TestProcessorIgnoresNonFactorCompletionKinds(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		Factors:      []compiler.CompiledFactor{{FactorID: "momentum", ResultViewID: "factor-view"}},
		Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor-view"}},
	}
	if acceptsFactorResultReady(compiled, PeriodReady{ViewID: "factor-view", CompletionKind: "event.storage.collector.period.completed"}) {
		t.Fatal("a non-factor completion kind must not run a factor-backed strategy")
	}
	if !acceptsFactorResultReady(compiled, PeriodReady{ViewID: "factor-view", CompletionKind: events.FactorPeriodComputed.Name()}) {
		t.Fatal("factor-period completion must run the strategy")
	}
}

func TestProcessorReadsFactorStatesFromViewDataReady(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		SourceView:     compiler.CompiledView{ID: "source"},
		InstrumentPool: config.InstrumentPoolRule{Include: []string{"BTC"}},
		Factors:        []compiler.CompiledFactor{{FactorID: "momentum", ResultViewID: "factor-view"}},
	}
	event := PeriodReady{
		ViewID: "factor-view", Status: "degraded",
		FactorStates: map[string]FactorPeriodState{
			"momentum": {Status: "degraded", FailedSubjects: []string{"ETH"}},
			"other":    {Status: "degraded", FailedSubjects: []string{"BTC"}},
		},
	}
	pool := []input.InstrumentInput{{PoolItem: input.PoolItem{InstrumentID: "BTC", SubjectID: "BTC"}}}
	if !requiredFactorsReady(compiled, event, pool) {
		t.Fatal("a target factor failure outside the selected pool should follow the existing degradation policy")
	}
	event.FactorStates["momentum"] = FactorPeriodState{Status: "degraded", FailedSubjects: []string{"BTC"}}
	if requiredFactorsReady(compiled, event, pool) {
		t.Fatal("a target factor failure in the selected pool must block evaluation")
	}
}

func TestTrimCompiledInputDropsHoldingRulesAndUnrelatedFactors(t *testing.T) {
	active := compiler.CompiledRule{
		Name:  "active",
		Score: &compiler.CompiledExpression{Dependencies: compiler.ExpressionDependencies{Fields: []string{"momentum"}}},
	}
	holding := compiler.CompiledRule{
		Name:  "holding",
		Score: &compiler.CompiledExpression{Dependencies: compiler.ExpressionDependencies{Fields: []string{"slow"}}},
	}
	compiled := compiler.CompiledStrategy{
		Rules: []compiler.CompiledRule{active, holding},
		Factors: []compiler.CompiledFactor{
			{FactorID: "momentum", Output: "momentum", ColumnName: "momentum", ResultViewID: "view-fast"},
			{FactorID: "slow", Output: "slow", ColumnName: "slow", ResultViewID: "view-slow"},
		},
		Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"view-fast", "view-slow"}},
	}
	trimCompiledInput(&compiled, config.DSL{Rules: map[string]config.Rule{"active": {}}})
	if len(compiled.Rules) != 1 || compiled.Rules[0].Name != "active" {
		t.Fatalf("rules=%v, want only active", compiled.Rules)
	}
	if len(compiled.Factors) != 1 || compiled.Factors[0].FactorID != "momentum" {
		t.Fatalf("factors=%v, want only momentum", compiled.Factors)
	}
	if len(compiled.Dependencies.FactorResultViewIDs) != 1 || compiled.Dependencies.FactorResultViewIDs[0] != "view-fast" {
		t.Fatalf("views=%v, want only view-fast", compiled.Dependencies.FactorResultViewIDs)
	}
}
