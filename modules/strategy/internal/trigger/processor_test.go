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
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

type fakeInputLoader struct {
	value input.EvaluationInput
	err   error
}

func (f fakeInputLoader) Load(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time) (input.EvaluationInput, error) {
	return f.value, f.err
}

type fakeIndexedInputLoader struct{ fakeInputLoader }

func (f fakeIndexedInputLoader) LoadAt(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time, map[string]string) (input.EvaluationInput, error) {
	return f.value, f.err
}

func TestProcessorCommitsFullWeightEvaluationAndDeduplicatesEvent(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompiledStrategy{APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space", SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"}, Schedule: compiler.CompiledSchedule{Every: "1m"}, Readiness: "strict", Long: &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}}, Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor-view"}}}
	compiled.CompiledJSON, _ = json.Marshal(compiled)
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "strategy", Name: "strategy", Kind: config.Kind, ManifestYAML: "manifest", CompiledJSON: compiled.CompiledJSON, SourceHash: "hash", CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	period := time.UnixMilli(60_000).UTC()
	loader := fakeInputLoader{value: input.EvaluationInput{SpaceID: "space", StrategyID: "strategy", PeriodEnd: period.Format(time.RFC3339Nano), SourceViewID: "source", DataFrequency: "1m", Items: []input.InstrumentInput{{PoolItem: input.PoolItem{InstrumentID: "BTC", SubjectID: "btc", Market: "spot"}, Values: map[string]quant.Decimal{"bias": quant.Must("1")}}, {PoolItem: input.PoolItem{InstrumentID: "ETH", SubjectID: "eth", Market: "spot"}, Values: map[string]quant.Decimal{"bias": quant.Must("2")}}}}}
	processor := &Processor{Store: repo, Loader: loader, Now: func() time.Time { return period.Add(time.Second) }}
	event := PeriodReady{MessageID: "ready-1", EventName: "event.storage.view.factor_period.ready", SpaceID: "space", ViewID: "factor-view", PeriodTime: period, ReadyViewIDs: []string{"factor-view"}}
	if err := processor.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	result, err := repo.GetResult(context.Background(), resultID(event.MessageID, "runner", period))
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domain.ActionRebalance || result.CommandSequence == nil || *result.CommandSequence != 1 {
		t.Fatalf("result=%+v", result)
	}
	var targets []domain.InstrumentTarget
	if err := json.Unmarshal(result.TargetsJSON, &targets); err != nil || len(targets) != 1 || targets[0].InstrumentID != "BTC" || targets[0].TargetWeight != "1" {
		t.Fatalf("targets=%s err=%v", result.TargetsJSON, err)
	}
	if err := processor.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	processed, err := repo.IsProcessed(context.Background(), event.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected event to be recorded in inbox")
	}
}

func TestAugmentCompiledBindingsPreservesCompiledProgramAndAddsFactorViews(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		SourceView:  compiler.CompiledView{ID: "catalog-source", Frequency: "1m", Status: "active"},
		Rules:       []compiler.CompiledRule{{Name: "rank"}},
		InputFields: map[string]reflect.Type{"bias": reflect.TypeOf(float64(0))},
	}
	augmentCompiledBindings(&compiled, json.RawMessage(`{"source_view_id":"bound-source","factors":[{"factor_id":"bias","binding_id":"b1","result_view_id":"factor-view","column_name":"bias"}]}`))
	if compiled.SourceView.ID != "bound-source" || compiled.SourceView.Frequency != "1m" || len(compiled.Rules) != 1 || len(compiled.InputFields) != 1 {
		t.Fatalf("compiled binding augmentation lost catalog program: %+v", compiled)
	}
	if len(compiled.Factors) != 1 || compiled.Dependencies.FactorResultViewIDs[0] != "factor-view" {
		t.Fatalf("factor binding not added: %+v", compiled)
	}
}

func TestProcessorAcksTerminalStrictIncompleteReady(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompiledStrategy{
		APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space",
		SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"},
		Schedule:   compiler.CompiledSchedule{Every: "1m"}, Readiness: "strict",
		Factors:      []compiler.CompiledFactor{{FactorID: "bias", BindingID: "binding", ResultViewID: "factor-view", Frequency: "1m", ColumnName: "bias"}},
		Long:         &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}},
		Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor-view"}},
	}
	compiled.CompiledJSON, _ = json.Marshal(compiled)
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "strategy", Name: "strategy", Kind: config.Kind, ManifestYAML: "manifest", CompiledJSON: compiled.CompiledJSON, SourceHash: "hash", CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	period := time.UnixMilli(60_000).UTC()
	processor := &Processor{Store: repo, Loader: fakeInputLoader{err: input.ErrStrictIncomplete}, Now: func() time.Time { return period.Add(time.Second) }}
	event := PeriodReady{MessageID: "degraded-ready", EventName: "event.storage.view.factor_period.ready", SpaceID: "space", ViewID: "factor-view", PeriodTime: period, Status: "degraded", BindingStatuses: map[string]string{"binding": "degraded"}}
	if err := processor.Handle(context.Background(), event); err != nil {
		t.Fatalf("terminal degraded event should be ACKable: %v", err)
	}
	processed, err := repo.IsProcessed(context.Background(), event.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected terminal degraded event to be recorded in inbox")
	}
}

func TestProcessorAcksLegacyReadyWithoutIndexProvenance(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompiledStrategy{
		APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space",
		SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"},
		Schedule:   compiler.CompiledSchedule{Every: "1m"}, Readiness: "strict",
		Factors:      []compiler.CompiledFactor{{FactorID: "bias", BindingID: "binding", ResultViewID: "factor-view", Frequency: "1m", ColumnName: "bias"}},
		Long:         &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}},
		Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor-view"}},
	}
	compiled.CompiledJSON, _ = json.Marshal(compiled)
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "strategy", Name: "strategy", Kind: config.Kind, ManifestYAML: "manifest", CompiledJSON: compiled.CompiledJSON, SourceHash: "hash", CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	period := time.UnixMilli(60_000).UTC()
	loader := fakeIndexedInputLoader{fakeInputLoader{value: input.EvaluationInput{SpaceID: "space", StrategyID: "strategy", PeriodEnd: period.Format(time.RFC3339Nano), SourceViewID: "source", DataFrequency: "1m"}}}
	processor := &Processor{Store: repo, Loader: loader, Now: func() time.Time { return period.Add(time.Second) }}
	// This marker intentionally omits SourceIndexID/ResultIndexID. The indexed
	// loader cannot reconstruct the immutable generation, so the delivery must
	// be acknowledged after recording the runner failure rather than retried
	// forever by the unlimited consumer.
	event := PeriodReady{MessageID: "legacy-ready", EventName: "event.storage.view.factor_period.ready", SpaceID: "space", ViewID: "factor-view", PeriodTime: period}
	if err := processor.Handle(context.Background(), event); err != nil {
		t.Fatalf("legacy ready marker should be terminal: %v", err)
	}
	processed, err := repo.IsProcessed(context.Background(), event.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected legacy event to be recorded in inbox")
	}
	if _, err := repo.GetResult(context.Background(), resultID(event.MessageID, "runner", period)); err == nil {
		t.Fatal("legacy event must not commit an evaluation")
	}
}

func TestProcessorRetriesTransientDependencyVerificationFailure(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompiledStrategy{
		APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space",
		SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"},
		Schedule:   compiler.CompiledSchedule{Every: "1m"}, Readiness: "strict",
		Long:         &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}},
		Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor-view"}},
	}
	compiled.CompiledJSON, _ = json.Marshal(compiled)
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "strategy", Name: "strategy", Kind: config.Kind, ManifestYAML: "manifest", CompiledJSON: compiled.CompiledJSON, SourceHash: "hash", CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	transient := errors.New("factor RPC unavailable")
	period := time.UnixMilli(60_000).UTC()
	processor := &Processor{
		Store: repo, Loader: fakeInputLoader{},
		VerifyDependencies: func(context.Context, compiler.CompiledStrategy) error { return transient },
		Now:                func() time.Time { return period.Add(time.Second) },
	}
	event := PeriodReady{MessageID: "transient-ready", EventName: "event.storage.view.factor_period.ready", SpaceID: "space", ViewID: "factor-view", PeriodTime: period, ReadyViewIDs: []string{"factor-view"}}
	if err := processor.Handle(context.Background(), event); !errors.Is(err, transient) {
		t.Fatalf("Handle() error = %v, want transient dependency error", err)
	}
	processed, err := repo.IsProcessed(context.Background(), event.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("transient dependency failure must leave inbox message retryable")
	}
}

func TestProcessorAcksPermanentDependencyVerificationFailure(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompiledStrategy{
		APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space",
		SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"},
		Schedule:   compiler.CompiledSchedule{Every: "1m"}, Readiness: "strict",
		Long:         &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}},
		Dependencies: compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor-view"}},
	}
	compiled.CompiledJSON, _ = json.Marshal(compiled)
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "strategy", Name: "strategy", Kind: config.Kind, ManifestYAML: "manifest", CompiledJSON: compiled.CompiledJSON, SourceHash: "hash", CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	period := time.UnixMilli(60_000).UTC()
	processor := &Processor{
		Store: repo, Loader: fakeInputLoader{},
		VerifyDependencies: func(context.Context, compiler.CompiledStrategy) error {
			return compiler.DependencyMismatchError(errors.New("factor FACTOR_NOT_FOUND"))
		},
		Now: func() time.Time { return period.Add(time.Second) },
	}
	event := PeriodReady{MessageID: "permanent-ready", EventName: "event.storage.view.factor_period.ready", SpaceID: "space", ViewID: "factor-view", PeriodTime: period, ReadyViewIDs: []string{"factor-view"}}
	if err := processor.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() permanent dependency failure should ACK: %v", err)
	}
	processed, err := repo.IsProcessed(context.Background(), event.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("permanent dependency failure must be recorded in inbox")
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

func TestBindingEventSourceMismatchRejectsStaleReadyEvent(t *testing.T) {
	compiled := compiler.CompiledStrategy{Factors: []compiler.CompiledFactor{{BindingID: "binding", ResultViewID: "factor-view", SourceHash: "hash-a"}}}
	event := PeriodReady{ViewID: "factor-view", BindingStates: map[string]BindingPeriodState{
		"binding": {Status: "complete", SourceHash: "hash-b"},
	}}
	if err := bindingEventSourceMismatch(compiled, event); err == nil {
		t.Fatal("expected stale factor-ready event to be rejected")
	}
	event.BindingStates["binding"] = BindingPeriodState{Status: "complete", SourceHash: "hash-a"}
	if err := bindingEventSourceMismatch(compiled, event); err != nil {
		t.Fatalf("matching factor source hash rejected: %v", err)
	}
}

func TestRequiredBindingsReadyUsesOnlyCompiledBindings(t *testing.T) {
	compiled := compiler.CompiledStrategy{InstrumentPool: config.InstrumentPoolRule{Include: []string{"BTC"}}, Factors: []compiler.CompiledFactor{{BindingID: "binding-required"}}}
	if !requiredBindingsReady(compiled, PeriodReady{Status: "degraded", BindingStatuses: map[string]string{
		"binding-required": "complete", "binding-unrelated": "degraded",
	}}) {
		t.Fatal("unrelated degraded binding should not block this strategy")
	}
	if requiredBindingsReady(compiled, PeriodReady{Status: "complete", BindingStatuses: map[string]string{
		"binding-required": "degraded",
	}}) {
		t.Fatal("required degraded binding should block evaluation")
	}
}

func TestRequiredBindingsReadyAllowsSkippedOnlyBindingState(t *testing.T) {
	compiled := compiler.CompiledStrategy{InstrumentPool: config.InstrumentPoolRule{Include: []string{"BTC"}}, Factors: []compiler.CompiledFactor{{BindingID: "binding-required"}}}
	event := PeriodReady{Status: "degraded", BindingStatuses: map[string]string{"binding-required": "degraded"}, BindingStates: map[string]BindingPeriodState{
		"binding-required": {Status: "degraded", SkippedSubjects: []string{"OTHER"}},
	}}
	if !requiredBindingsReady(compiled, event) {
		t.Fatal("skipped-only binding should remain eligible for pool-level readiness")
	}
	event.BindingStates["binding-required"] = BindingPeriodState{Status: "degraded", FailedSubjects: []string{"BTC"}}
	if requiredBindingsReady(compiled, event) {
		t.Fatal("failed subject in selected pool must block evaluation")
	}
	event.BindingStates["binding-required"] = BindingPeriodState{Status: "degraded", FailedSubjects: []string{"OTHER"}}
	if !requiredBindingsReady(compiled, event) {
		t.Fatal("failed subject outside selected pool should not block evaluation")
	}
}

func TestRequiredBindingsReadyMatchesLoadedPoolBySubjectOrInstrument(t *testing.T) {
	compiled := compiler.CompiledStrategy{Factors: []compiler.CompiledFactor{{BindingID: "binding-required"}}}
	event := PeriodReady{Status: "degraded", BindingStatuses: map[string]string{"binding-required": "degraded"}, BindingStates: map[string]BindingPeriodState{
		"binding-required": {Status: "degraded", FailedSubjects: []string{"BUSD-USDT"}},
	}}
	pool := []input.InstrumentInput{{PoolItem: input.PoolItem{InstrumentID: "BTC-USDT", SubjectID: "btc-binance"}}}
	if !requiredBindingsReady(compiled, event, pool) {
		t.Fatal("failure for a subject outside the loaded pool should not block evaluation")
	}
	event.BindingStates["binding-required"] = BindingPeriodState{Status: "degraded", FailedSubjects: []string{"btc-binance"}}
	if requiredBindingsReady(compiled, event, pool) {
		t.Fatal("failure for a selected subject should block evaluation")
	}
	event.BindingStates["binding-required"] = BindingPeriodState{Status: "degraded", SkippedSubjects: []string{"BTC-USDT"}}
	if requiredBindingsReady(compiled, event, pool) {
		t.Fatal("instrument ID reported by a factor must also match the selected pool")
	}
}

func TestRequiredBindingsReadyScopesStatusesToEventView(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		SourceView: compiler.CompiledView{ID: "source", Frequency: "1m"},
		Factors: []compiler.CompiledFactor{
			{BindingID: "binding-one", ResultViewID: "view-one", Frequency: "1m"},
			{BindingID: "binding-two", ResultViewID: "view-two", Frequency: "1m"},
		},
	}
	event := PeriodReady{ViewID: "view-one", Status: "degraded", BindingStatuses: map[string]string{
		"binding-one": "complete", "binding-two": "degraded",
	}}
	if !requiredBindingsReady(compiled, event) {
		t.Fatal("degraded binding on another result View should not block this event")
	}
	if dependsOnEvent(compiled, PeriodReady{ViewID: "view-one", Frequency: "5m"}) {
		t.Fatal("ready event with mismatched frequency should not trigger strategy")
	}
	if !dependsOnEvent(compiled, PeriodReady{ViewID: "view-one", Frequency: "1m"}) {
		t.Fatal("matching result View/frequency should trigger strategy")
	}
}

func TestTerminalReadyForRunnerIgnoresUnrelatedDegradedBinding(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		SourceView: compiler.CompiledView{ID: "source", Frequency: "1m"},
		Factors: []compiler.CompiledFactor{
			{BindingID: "binding-required", ResultViewID: "factor-view", Frequency: "1m"},
		},
	}
	event := PeriodReady{ViewID: "factor-view", Status: "degraded", BindingStatuses: map[string]string{
		"binding-required": "complete", "binding-unrelated": "degraded",
	}}
	if terminalReadyForRunner(compiled, event) {
		t.Fatal("unrelated degraded binding must not terminally ACK a strict-incomplete read")
	}
	event.BindingStatuses["binding-required"] = "degraded"
	if !terminalReadyForRunner(compiled, event) {
		t.Fatal("required degraded binding should be terminal")
	}
}

func TestTerminalReadyForRunnerIsConservativeForDynamicPool(t *testing.T) {
	compiled := compiler.CompiledStrategy{
		SourceView: compiler.CompiledView{ID: "source", Frequency: "1m"},
		Factors: []compiler.CompiledFactor{
			{BindingID: "binding-required", ResultViewID: "factor-view", Frequency: "1m"},
		},
	}
	event := PeriodReady{ViewID: "factor-view", Status: "degraded", BindingStatuses: map[string]string{
		"binding-required": "degraded",
	}, BindingStates: map[string]BindingPeriodState{
		"binding-required": {Status: "degraded", FailedSubjects: []string{"UNKNOWN"}},
	}}
	if terminalReadyForRunner(compiled, event) {
		t.Fatal("dynamic pool with explicit subject failure must remain retryable")
	}
	pool := []input.InstrumentInput{{PoolItem: input.PoolItem{InstrumentID: "BTC", SubjectID: "btc"}}}
	event.BindingStates["binding-required"] = BindingPeriodState{Status: "degraded", FailedSubjects: []string{"other"}}
	if terminalReadyForRunner(compiled, event, pool) {
		t.Fatal("dynamic pool failure outside resolved pool must remain retryable")
	}
	event.BindingStates["binding-required"] = BindingPeriodState{Status: "degraded", FailedSubjects: []string{"btc"}}
	if !terminalReadyForRunner(compiled, event, pool) {
		t.Fatal("resolved pool failure must be terminal")
	}
}
