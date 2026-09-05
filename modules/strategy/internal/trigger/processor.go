package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/selection"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type PeriodReady struct {
	MessageID string
	EventName string
	SpaceID   string
	ViewID    string
	Frequency string
	// PeriodTime is retained as the legacy timestamp alias. New producers set
	// StoragePeriodTime to the Storage row key (bar start) and BarEndTime to the
	// public strategy boundary used for results and target validity.
	PeriodTime        time.Time
	StoragePeriodTime time.Time
	BarEndTime        time.Time
	Status            string
	ReadyViewIDs      []string
	// SourceIndexID and ResultIndexID form a causal generation fence emitted by
	// Storage. They let Strategy replay exactly the View generations used by
	// Factor instead of merely pinning whatever is active at trigger time.
	SourceIndexID       string
	ResultIndexID       string
	SourceIndexRevision uint64
	ResultIndexRevision uint64
	// BindingStatuses preserves the per-binding terminal state emitted by
	// Factor. A degraded aggregate may still be usable when every binding this
	// strategy actually references completed successfully.
	BindingStatuses map[string]string
	// BindingStates keeps the subject-level terminal details needed to
	// distinguish an include-scope skip from an actual computation failure.
	// A strategy may safely evaluate a pool that does not intersect skipped
	// subjects, while failed subjects must never be treated as ready.
	BindingStates map[string]BindingPeriodState
	// TargetInstanceID is set by the in-process timer so one scheduled job
	// cannot accidentally evaluate every enabled instance. Event-driven ready
	// notifications leave it empty and fan out to all matching instances.
	TargetInstanceID string
}

type BindingPeriodState struct {
	Status          string
	SkippedSubjects []string
	FailedSubjects  []string
	// SourceHash identifies the immutable Factor source used for this period.
	// It prevents a delayed readiness event from being evaluated with a newer
	// or rolled-back Factor version.
	SourceHash string
}

type InputLoader interface {
	Load(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time) (input.EvaluationInput, error)
}

// IndexedInputLoader is the stronger production loader contract. The
// expected index IDs come from View-ready provenance and are checked before
// any row is read. Older embedded loaders can continue implementing Load.
type IndexedInputLoader interface {
	LoadAt(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time, map[string]string) (input.EvaluationInput, error)
}

// PeriodInputLoader keeps the Storage bar-start convention at the adapter
// boundary. The evaluator always receives PeriodEnd while the reader uses the
// explicit start timestamp.
type PeriodInputLoader interface {
	LoadPeriod(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time, time.Time) (input.EvaluationInput, error)
}

type IndexedPeriodInputLoader interface {
	LoadPeriodAt(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time, time.Time, map[string]string) (input.EvaluationInput, error)
}

type Processor struct {
	Inbox interface {
		IsProcessed(context.Context, string) (bool, error)
		MarkProcessed(context.Context, string, string, time.Time) error
	}
	Store        *store.Store
	Loader       InputLoader
	PoolRegistry *input.UDFRegistry
	// Compile rebuilds executable expressions and the dependency snapshot for
	// a persisted DSL. Production supplies a compiler backed by the Factor and
	// Storage catalogs; tests and legacy embedders may omit it and use the
	// lightweight DSL fallback below.
	Compile func(context.Context, config.DSL, string) (compiler.CompiledStrategy, error)
	// CompileWithBindings is the production path for instance-specific fields;
	// it prevents bars[0].factor from being rejected before the binding is
	// attached to the shared DSL artifact.
	CompileWithBindings func(context.Context, config.DSL, string, json.RawMessage) (compiler.CompiledStrategy, error)
	// VerifyDependencies is optional for embedded/test processors. Production
	// bootstrap supplies the compiler-backed verifier so a running Runner is
	// not allowed to consume a Factor that changed after it was compiled.
	VerifyDependencies func(context.Context, compiler.CompiledStrategy) error
	// OwnerGeneration snapshots the Trade logical-account lifecycle token for
	// each executable runner and verifies that the account is still owned by
	// that runner. It is embedded in the target event so Trade can reject
	// delayed messages without comparing clocks across processes.
	OwnerGeneration func(context.Context, string, string, string) (int64, error)
	Now             func() time.Time
}

func (p *Processor) Handle(ctx context.Context, event PeriodReady) error {
	if p == nil || p.Store == nil || p.Loader == nil || event.MessageID == "" || event.SpaceID == "" || event.ViewID == "" || event.PeriodTime.IsZero() {
		return fmt.Errorf("strategy trigger is not configured")
	}
	inbox := p.Inbox
	if inbox == nil {
		inbox = p.Store
	}
	barEnd, storagePeriod := effectivePeriodTimes(event)
	processed, err := inbox.IsProcessed(ctx, event.MessageID)
	if err != nil || processed {
		return err
	}
	// New instances are evaluated from the persisted DSL on every process
	// start. Keep the legacy path below only as a source adapter for old
	// embedders; it is never used for a valid current DSL definition.
	if handled, modernErr := p.handleInstances(ctx, inbox, event); handled {
		return modernErr
	}
	// Degraded and legacy readiness markers are terminal for the compatibility
	// path. Modern instances have already checked only their referenced
	// bindings above, so an unrelated degraded binding cannot suppress them.
	// A source View degradation has no per-binding detail and is unusable.
	// Factor result Views do carry binding states, so defer the decision until
	// each compiled runner can check only the bindings it references.
	if event.Status != "" && event.Status != "complete" && len(event.BindingStatuses) == 0 {
		return inbox.MarkProcessed(ctx, event.MessageID, event.EventName, p.now())
	}
	runners, err := p.Store.ListRunners(ctx, store.RunnerFilter{SpaceID: event.SpaceID, Status: domain.RunnerStatusEnabled})
	if err != nil {
		return err
	}
	var retryErr error
	for _, runner := range runners {
		strategy, getErr := p.Store.GetStrategy(ctx, runner.StrategyID)
		if getErr != nil {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, getErr, p.now())
			if !errors.Is(getErr, gorm.ErrRecordNotFound) && retryErr == nil {
				retryErr = getErr
			}
			continue
		}
		var compiled compiler.CompiledStrategy
		if len(strategy.CompiledJSON) == 0 || json.Unmarshal(strategy.CompiledJSON, &compiled) != nil {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, fmt.Errorf("strategy %s has no compiled artifact", strategy.ID), p.now())
			continue
		}
		if !dependsOnEvent(compiled, event) {
			continue
		}
		if p.VerifyDependencies != nil {
			if verifyErr := p.VerifyDependencies(ctx, compiled); verifyErr != nil {
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, verifyErr, p.now())
				if !errors.Is(verifyErr, compiler.ErrDependencyMismatch) && retryErr == nil {
					retryErr = verifyErr
				}
				continue
			}
		}
		// The current runtime contract has one immutable Result View per
		// strategy. Older compiled artifacts may still contain several; do not
		// evaluate them with a partial generation map.
		if compiledResultViewCount(compiled) > 1 {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, fmt.Errorf("strategy %s references multiple factor result Views", strategy.ID), p.now())
			continue
		}
		if mismatchErr := bindingEventSourceMismatch(compiled, event); mismatchErr != nil {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, mismatchErr, p.now())
			continue
		}
		if runner.LastResultID != nil {
			last, resultErr := p.Store.GetResult(ctx, *runner.LastResultID)
			if resultErr == nil && barEnd.Before(last.PeriodTime) {
				continue
			}
		}
		if !aligned(event.PeriodTime, runner.Frequency, compiled.Schedule.Every) {
			continue
		}
		var evaluationInput input.EvaluationInput
		var loadErr error
		if indexed, ok := p.Loader.(IndexedInputLoader); ok {
			expected := map[string]string{}
			if event.SourceIndexID != "" {
				expected[compiled.SourceView.ID] = event.SourceIndexID
			}
			if event.ResultIndexID != "" {
				for _, factor := range compiled.Factors {
					if factor.ResultViewID != "" && factor.ResultViewID == event.ViewID {
						expected[factor.ResultViewID] = event.ResultIndexID
					}
				}
			}
			if event.SourceIndexRevision != 0 {
				expected[compiled.SourceView.ID] = expected[compiled.SourceView.ID] + "\x00" + fmt.Sprint(event.SourceIndexRevision)
			}
			if event.ResultIndexRevision != 0 {
				for _, factor := range compiled.Factors {
					if factor.ResultViewID != "" && factor.ResultViewID == event.ViewID {
						expected[factor.ResultViewID] = expected[factor.ResultViewID] + "\x00" + fmt.Sprint(event.ResultIndexRevision)
					}
				}
			}
			if len(expected) == 0 {
				// Compatibility markers from before index provenance can never
				// become valid by redelivery. Record the runner failure and ACK the
				// event instead of retrying it forever on an unlimited consumer.
				loadErr = fmt.Errorf("%w: View-ready event has no index provenance", input.ErrLegacyProvenance)
			} else {
				evaluationInput, loadErr = indexed.LoadAt(ctx, runner, compiled, storagePeriod.UTC(), expected)
			}
		} else {
			evaluationInput, loadErr = p.Loader.Load(ctx, runner, compiled, storagePeriod.UTC())
		}
		if loadErr != nil {
			if errors.Is(loadErr, input.ErrLegacyProvenance) {
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, loadErr, p.now())
				continue
			}
			if errors.Is(loadErr, compiler.ErrDependencyMismatch) {
				// A permanently removed or invalidated View/column cannot be
				// repaired by redelivery of this ready marker. ACK after recording
				// the runner failure rather than retrying forever.
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, loadErr, p.now())
				continue
			}
			if errors.Is(loadErr, input.ErrStaleViewSnapshot) {
				// A newer View generation supersedes this readiness delivery. ACK
				// instead of filling the unlimited-delivery consumer with a message
				// that can never satisfy its old generation fence.
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, loadErr, p.now())
				continue
			}
			if errors.Is(loadErr, input.ErrStrictIncomplete) {
				// A terminal degraded Factor-ready marker is authoritative: the
				// selected subject/column will not be repaired by another delivery.
				// ACK it after recording the failure instead of poisoning the
				// unlimited-delivery consumer forever. Complete markers remain
				// retryable because a View rebuild can still be catching up.
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, loadErr, p.now())
				var incomplete *input.StrictIncompleteError
				terminal := terminalReadyForRunner(compiled, event)
				if errors.As(loadErr, &incomplete) {
					resolvedPool := make([]input.InstrumentInput, len(incomplete.Pool.Items))
					for i, item := range incomplete.Pool.Items {
						resolvedPool[i].PoolItem = item
					}
					terminal = terminalReadyForRunner(compiled, event, resolvedPool)
				}
				if terminal {
					continue
				}
				if retryErr == nil {
					retryErr = loadErr
				}
				continue
			}
			if errors.Is(loadErr, input.ErrNotReady) {
				if retryErr == nil {
					retryErr = loadErr
				}
			}
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, loadErr, p.now())
			continue
		}
		// Verify again after the View read to narrow the metadata/data TOCTOU
		// window. A changed Factor/Binding is recorded and this period is not
		// evaluated under a potentially stale compiled artifact.
		if p.VerifyDependencies != nil {
			if verifyErr := p.VerifyDependencies(ctx, compiled); verifyErr != nil {
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, verifyErr, p.now())
				if !errors.Is(verifyErr, compiler.ErrDependencyMismatch) && retryErr == nil {
					retryErr = verifyErr
				}
				continue
			}
		}
		if !requiredBindingsReady(compiled, event, evaluationInput.Items) {
			// The event is terminal for this period. Do not retry forever because
			// an unrelated binding or subject in the same result View was degraded.
			continue
		}
		inputHash, hashErr := input.Hash(evaluationInput)
		if hashErr != nil {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, hashErr, p.now())
			continue
		}
		evaluation, evalErr := selection.Evaluate(manifestFromCompiled(compiled), evaluationInput)
		if evalErr != nil {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, evalErr, p.now())
			continue
		}
		// Recheck after the pure evaluation as well. This closes the remaining
		// practical window between reading the View and committing a target when
		// an administrator replaces a Factor/Binding during a long ranking run.
		if p.VerifyDependencies != nil {
			if verifyErr := p.VerifyDependencies(ctx, compiled); verifyErr != nil {
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, verifyErr, p.now())
				if !errors.Is(verifyErr, compiler.ErrDependencyMismatch) && retryErr == nil {
					retryErr = verifyErr
				}
				continue
			}
		}
		eval := domain.Evaluation{Action: domain.ActionRebalance, DebugInfo: map[string]any{"selection": evaluation.Debug}}
		var ownerGeneration int64
		if runner.LogicalAccountID != nil && p.OwnerGeneration != nil {
			generation, generationErr := p.OwnerGeneration(ctx, event.SpaceID, *runner.LogicalAccountID, runner.ID)
			if generationErr != nil {
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, generationErr, p.now())
				if retryErr == nil {
					retryErr = generationErr
				}
				continue
			}
			if generation <= 0 {
				generationErr = fmt.Errorf("logical account %s has no active owner generation", *runner.LogicalAccountID)
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, generationErr, p.now())
				if retryErr == nil {
					retryErr = generationErr
				}
				continue
			}
			ownerGeneration = generation
			eval.DebugInfo["owner_generation"] = generation
		}
		if len(evaluationInput.Ineligible) > 0 {
			eval.DebugInfo["ineligible"] = evaluationInput.Ineligible
		}
		for _, target := range evaluation.Targets {
			eval.Targets = append(eval.Targets, domain.InstrumentTarget{InstrumentID: target.InstrumentID, TargetWeight: target.TargetWeight})
		}
		if runner.LastResultID != nil && sameTargets(runner.CurrentTargetsJSON, eval.Targets) {
			eval.Action = domain.ActionHold
		}
		result := domain.StrategyResult{ID: resultID(event.MessageID, runner.ID, barEnd), RunnerID: runner.ID, StrategyID: runner.StrategyID, PeriodTime: barEnd.UTC(), InputHash: inputHash, Action: eval.Action, CreatedAt: p.now()}
		outcome, commitErr := p.Store.CommitEvaluation(ctx, store.CommitEvaluationRequest{Result: result, Evaluation: eval, OwnerGeneration: ownerGeneration})
		if commitErr != nil {
			_ = p.Store.RecordRunnerFailure(ctx, runner.ID, commitErr, p.now())
			if !errors.Is(commitErr, store.ErrRunnerNotEnabled) && !errors.Is(commitErr, store.ErrLogicalResultConflict) && retryErr == nil {
				retryErr = commitErr
			}
			continue
		}
		if ownerGeneration > 0 && p.OwnerGeneration != nil && runner.LogicalAccountID != nil {
			currentGeneration, generationErr := p.OwnerGeneration(ctx, event.SpaceID, *runner.LogicalAccountID, runner.ID)
			if generationErr != nil || currentGeneration != ownerGeneration {
				invalidateErr := p.Store.InvalidateEvaluation(ctx, runner.ID, outcome.Result.ID, p.now())
				if generationErr == nil {
					generationErr = fmt.Errorf("logical account %s owner generation changed during evaluation", *runner.LogicalAccountID)
				}
				if invalidateErr != nil {
					generationErr = errors.Join(generationErr, fmt.Errorf("invalidate stale evaluation: %w", invalidateErr))
				}
				_ = p.Store.RecordRunnerFailure(ctx, runner.ID, generationErr, p.now())
				if retryErr == nil {
					retryErr = generationErr
				}
			}
		}
	}
	if retryErr != nil {
		return retryErr
	}
	return inbox.MarkProcessed(ctx, event.MessageID, event.EventName, p.now())
}

func (p *Processor) handleInstances(ctx context.Context, inbox interface {
	IsProcessed(context.Context, string) (bool, error)
	MarkProcessed(context.Context, string, string, time.Time) error
}, event PeriodReady) (bool, error) {
	instances, err := p.Store.ListInstances(ctx, event.SpaceID, ptrBool(true))
	if err != nil || len(instances) == 0 {
		return false, err
	}
	modern := false
	var retryErr error
	barEnd, storagePeriod := effectivePeriodTimes(event)
	for _, instance := range instances {
		if event.TargetInstanceID != "" && instance.InstanceID != event.TargetInstanceID {
			continue
		}
		definition, getErr := p.Store.GetStrategyDefinition(ctx, instance.StrategyID)
		if getErr != nil {
			if retryErr == nil {
				retryErr = getErr
			}
			continue
		}
		dsl, parseErr := config.Parse([]byte(definition.DSLYaml))
		if parseErr != nil {
			// A row containing a legacy manifest is handled by the compatibility
			// processor below, so do not ACK it as a modern evaluation.
			continue
		}
		modern = true
		// Ready events carry the Storage BarStart but not the strategy calendar.
		// Normalize it with the instance DSL so cn_stock events use the same
		// exchange-close boundary as scheduled triggers.
		instanceBarEnd, instanceStorage := barEnd, storagePeriod
		if event.TargetInstanceID == "" {
			if normalized, normalizeErr := input.FromStorageStart(dsl.Data.Calendar, dsl.Data.Bar, storagePeriod); normalizeErr != nil {
				if retryErr == nil && errors.Is(normalizeErr, input.ErrNotReady) {
					retryErr = normalizeErr
				}
				continue
			} else {
				instanceBarEnd, instanceStorage = normalized.BarEnd, normalized.StorageStart
			}
		}
		if !triggerMatchesDSL(dsl, event) {
			continue
		}
		// Expression compilation is performed when the instance is enabled
		// against the bound Factor/View catalog. The stateless trigger keeps the
		// DSL authoritative and lets the evaluator resolve values from this
		// frozen input snapshot.
		var compiled compiler.CompiledStrategy
		if p.CompileWithBindings != nil {
			compiled, err = p.CompileWithBindings(ctx, dsl, instance.SpaceID, instance.InputBindingsJSON)
			if err != nil {
				// A persisted binding must never fall through to a zero-value
				// artifact. Keep the delivery retryable so an operator can repair
				// the binding and rerun the same period deterministically.
				if retryErr == nil {
					retryErr = err
				}
				continue
			}
		} else if p.Compile != nil {
			compiled, err = p.Compile(ctx, dsl, instance.SpaceID)
			if err != nil {
				if retryErr == nil && retryableModernError(err) {
					retryErr = err
				}
				continue
			}
		} else {
			compiled = compiler.CompiledStrategy{Name: dsl.Name, SpaceID: instance.SpaceID, Data: dsl.Data, Triggers: dsl.Triggers}
		}
		augmentCompiledBindings(&compiled, instance.InputBindingsJSON)
		configureFixedPool(&compiled, dsl)
		if p.VerifyDependencies != nil {
			if verifyErr := p.VerifyDependencies(ctx, compiled); verifyErr != nil {
				if retryErr == nil && !errors.Is(verifyErr, compiler.ErrDependencyMismatch) {
					retryErr = verifyErr
				}
				continue
			}
		}
		if event.EventName != "strategy.schedule" && (len(compiled.Factors) > 0 || len(compiled.Dependencies.FactorResultViewIDs) > 0) && !dependsOnEvent(compiled, event) {
			continue
		}
		runner := instanceRunner(instance, compiled)
		period := instanceBarEnd.UTC()
		if latest, latestErr := p.Store.LatestResult(ctx, instance.InstanceID, valueOrEmpty(instance.SessionID)); latestErr == nil && !period.After(latest.BarEndTime) {
			continue
		}
		var evaluated input.EvaluationInput
		expectedIndexesMap := expectedIndexes(compiled, event)
		if indexed, ok := p.Loader.(IndexedPeriodInputLoader); ok {
			if event.TargetInstanceID == "" && len(expectedIndexesMap) == 0 {
				if retryErr == nil {
					retryErr = fmt.Errorf("%w: View-ready event has no index provenance", input.ErrLegacyProvenance)
				}
				continue
			}
			if event.TargetInstanceID != "" {
				expectedIndexesMap = nil // scheduled jobs intentionally use the active snapshot
			}
			evaluated, err = indexed.LoadPeriodAt(ctx, runner, compiled, period, instanceStorage.UTC(), expectedIndexesMap)
		} else if periodLoader, ok := p.Loader.(PeriodInputLoader); ok {
			evaluated, err = periodLoader.LoadPeriod(ctx, runner, compiled, period, instanceStorage.UTC())
		} else if indexed, ok := p.Loader.(IndexedInputLoader); ok {
			evaluated, err = indexed.LoadAt(ctx, runner, compiled, instanceStorage.UTC(), expectedIndexesMap)
		} else {
			evaluated, err = p.Loader.Load(ctx, runner, compiled, instanceStorage.UTC())
		}
		if err != nil {
			if errors.Is(err, input.ErrStrictIncomplete) {
				// A scheduled wake-up owns this bar and must keep retrying while
				// Storage/Factor finishes publishing it. Ready events carrying a
				// terminal degraded binding are acknowledged instead.
				if event.TargetInstanceID != "" || !terminalReadyForRunner(compiled, event) {
					if retryErr == nil {
						retryErr = err
					}
				}
			} else if retryErr == nil && retryableModernError(err) {
				retryErr = err
			}
			continue
		}
		poolResolutionFailed := false
		for _, rule := range dsl.Rules {
			if rule.Pool.UDF != nil && p.PoolRegistry == nil {
				poolResolutionFailed = true
			}
		}
		if poolResolutionFailed {
			continue
		}
		if p.PoolRegistry != nil {
			subjects := make([]input.Subject, 0, len(evaluated.Items))
			for _, item := range evaluated.Items {
				subjects = append(subjects, input.Subject{SubjectID: item.SubjectID, InstrumentID: item.InstrumentID, Exchange: item.Exchange, Market: item.Market, QuoteAsset: item.QuoteAsset, SeriesTag: item.SeriesTag, Active: true})
			}
			for name, rule := range dsl.Rules {
				if rule.Pool.UDF == nil {
					continue
				}
				ids, resolveErr := p.PoolRegistry.Resolve(ctx, rule.Pool, subjects, period)
				if resolveErr != nil {
					if retryErr == nil {
						if !errors.Is(resolveErr, input.ErrPoolUDFNotRegistered) && !errors.Is(resolveErr, input.ErrPoolUDFInvalidParams) {
							retryErr = resolveErr
						}
					}
					poolResolutionFailed = true
					continue
				}
				rule.Pool = config.Pool{Fixed: ids}
				dsl.Rules[name] = rule
			}
		}
		if poolResolutionFailed {
			continue
		}
		var previous map[string]domain.RuleState
		if latest, latestErr := p.Store.LatestResult(ctx, instance.InstanceID, valueOrEmpty(instance.SessionID)); latestErr == nil {
			_ = json.Unmarshal(latest.RuleStatesJSON, &previous)
		}
		evaluation, evalErr := selection.Evaluate(dsl, evaluated, previous)
		if evalErr != nil {
			if retryErr == nil {
				if retryableModernError(evalErr) || (event.TargetInstanceID != "" && errors.Is(evalErr, input.ErrStrictIncomplete)) {
					retryErr = evalErr
				}
			}
			continue
		}
		targets, _ := json.Marshal(evaluation.Targets)
		states, _ := json.Marshal(evaluation.RuleStates)
		if len(targets) == 0 {
			targets = json.RawMessage(`[]`)
		}
		if len(states) == 0 {
			states = json.RawMessage(`{}`)
		}
		validUntil, validUntilErr := input.AdvanceBarEnd(dsl.Data.Calendar, dsl.Data.Bar, period, 2)
		if validUntilErr != nil {
			if retryErr == nil {
				retryErr = validUntilErr
			}
			continue
		}
		snapshot, snapshotErr := json.Marshal(map[string]any{
			"strategy_id": instance.StrategyID, "dsl_yaml": definition.DSLYaml,
			"inputs": json.RawMessage(instance.InputBindingsJSON), "period_end": period,
			"source_index_id": event.SourceIndexID, "result_index_id": event.ResultIndexID,
			"source_index_revision": event.SourceIndexRevision, "result_index_revision": event.ResultIndexRevision,
		})
		if snapshotErr != nil {
			if retryErr == nil {
				if retryableModernError(snapshotErr) {
					retryErr = snapshotErr
				}
			}
			continue
		}
		result := store.StrategyResult{ResultID: resultID(event.MessageID, instance.InstanceID, period, valueOrEmpty(instance.SessionID)), InstanceID: instance.InstanceID, SessionID: valueOrEmpty(instance.SessionID), BarEndTime: period, ValidUntil: validUntil, SnapshotJSON: snapshot, TargetsJSON: targets, RuleStatesJSON: states, PublishStatus: store.PublishNone, CreatedAt: p.now()}
		if instance.LogicalAccountID != nil {
			result.PublishStatus = store.PublishPending
			result.EventData, err = marshalTargetEvent(instance, result, evaluation.Targets)
			if err != nil {
				if retryErr == nil {
					if retryableModernError(err) {
						retryErr = err
					}
				}
				continue
			}
		}
		var expected *string
		if latest, latestErr := p.Store.LatestResult(ctx, instance.InstanceID, valueOrEmpty(instance.SessionID)); latestErr == nil {
			expected = &latest.ResultID
		}
		if _, _, commitErr := p.Store.CommitResult(ctx, store.CommitResultRequest{Result: result, ExpectedResultID: expected, Now: p.now()}); commitErr != nil {
			if retryErr == nil && retryableModernError(commitErr) {
				retryErr = commitErr
			}
		}
	}
	if modern {
		if retryErr != nil {
			return true, retryErr
		}
		return true, inbox.MarkProcessed(ctx, event.MessageID, event.EventName, p.now())
	}
	return false, nil
}

func ptrBool(v bool) *bool { return &v }

func triggerMatchesDSL(dsl config.DSL, event PeriodReady) bool {
	// Timer wakeups carry an explicit source marker; they must not be
	// accidentally triggered by an unrelated ready event with the same bar.
	if event.EventName == "strategy.schedule" {
		return dsl.Triggers.Schedule != nil
	}
	if dsl.Triggers.Event != nil {
		configured := canonicalEventName(dsl.Triggers.Event.Name)
		actual := canonicalEventName(event.EventName)
		return configured != "" && configured == actual
	}
	return false
}

func canonicalEventName(name string) string {
	name = strings.TrimSpace(name)
	switch strings.ToLower(name) {
	case "viewfactorperiodready", "factor.ready", "event.storage.view.factor_period.ready":
		return "event.storage.view.factor_period.ready"
	default:
		return name
	}
}

func augmentCompiledBindings(compiled *compiler.CompiledStrategy, raw json.RawMessage) {
	var b struct {
		SourceViewID string `json:"source_view_id"`
		Frequency    string `json:"frequency"`
		ViewID       string `json:"view_id"`
		Factors      []struct {
			FactorID        string   `json:"factor_id"`
			BindingID       string   `json:"binding_id"`
			SourceHash      string   `json:"source_hash"`
			InputColumns    []string `json:"input_columns"`
			ParamsJSON      string   `json:"params_json"`
			LookbackPeriods int      `json:"lookback_periods"`
			Frequency       string   `json:"frequency"`
			ResultDatasetID string   `json:"result_dataset_id"`
			ResultViewID    string   `json:"result_view_id"`
			Output          string   `json:"output"`
			ColumnName      string   `json:"column_name"`
			SubjectMode     string   `json:"subject_mode"`
			SubjectsJSON    string   `json:"subjects_json"`
		} `json:"factors"`
		FactorViews []string `json:"factor_view_ids"`
	}
	_ = json.Unmarshal(raw, &b)
	if b.SourceViewID == "" {
		b.SourceViewID = b.ViewID
	}
	// Input bindings provide the concrete source View selected by an instance.
	// Do not erase catalog metadata (including factor Views and input fields)
	// produced by the compiler when a binding only contains a source ID.
	if strings.TrimSpace(b.SourceViewID) != "" {
		compiled.SourceView.ID = strings.TrimSpace(b.SourceViewID)
	}
	if strings.TrimSpace(b.Frequency) != "" {
		compiled.SourceView.Frequency = strings.TrimSpace(b.Frequency)
	}
	if compiled.SourceView.Status == "" {
		compiled.SourceView.Status = "active"
	}
	if compiled.SourceView.Frequency == "" {
		compiled.SourceView.Frequency = compiled.Data.Bar
	}
	if compiled.Schedule.Every == "" {
		compiled.Schedule.Every = compiled.Data.Bar
	}
	// Bindings are persisted with an instance rather than the shared DSL. Keep
	// the concrete factor result Views and columns on the in-memory artifact so
	// the Storage loader reads the same dependency set after a restart.
	for _, factor := range b.Factors {
		if strings.TrimSpace(factor.FactorID) == "" || strings.TrimSpace(factor.ResultViewID) == "" {
			continue
		}
		found := false
		for i := range compiled.Factors {
			if compiled.Factors[i].FactorID == factor.FactorID {
				found = true
				break
			}
		}
		if !found {
			compiled.Factors = append(compiled.Factors, compiler.CompiledFactor{
				FactorID: factor.FactorID, BindingID: factor.BindingID, SourceHash: factor.SourceHash,
				InputColumns: append([]string(nil), factor.InputColumns...), ParamsJSON: factor.ParamsJSON,
				LookbackPeriods: factor.LookbackPeriods, Frequency: factor.Frequency,
				ResultDatasetID: factor.ResultDatasetID, ResultViewID: factor.ResultViewID,
				Output: factor.Output, ColumnName: factor.ColumnName, SubjectMode: factor.SubjectMode, SubjectsJSON: factor.SubjectsJSON,
			})
		}
	}
	for _, viewID := range b.FactorViews {
		viewID = strings.TrimSpace(viewID)
		if viewID == "" {
			continue
		}
		if !containsString(compiled.Dependencies.FactorResultViewIDs, viewID) {
			compiled.Dependencies.FactorResultViewIDs = append(compiled.Dependencies.FactorResultViewIDs, viewID)
		}
	}
	for _, factor := range compiled.Factors {
		if factor.ResultViewID != "" && !containsString(compiled.Dependencies.FactorResultViewIDs, factor.ResultViewID) {
			compiled.Dependencies.FactorResultViewIDs = append(compiled.Dependencies.FactorResultViewIDs, factor.ResultViewID)
		}
	}
	// The storage adapter still accepts the old pool shape. Literal pools from
	// the legacy compiled artifact are copied directly; modern DSL rules keep
	// their per-rule pool out of this global metadata filter so a UDF rule can
	// still see the complete subject directory when mixed with fixed pools.
	if compiled.Name == "" {
		for _, rule := range compiled.Rules {
			if len(rule.Definition.Pool.Fixed) > 0 {
				compiled.InstrumentPool.Include = append(compiled.InstrumentPool.Include, rule.Definition.Pool.Fixed...)
			}
		}
	}
}

// configureFixedPool keeps the Storage read set narrow for the common case
// where every rule names a literal pool. A dynamic UDF must see the complete
// subject directory, so mixed fixed/UDF strategies intentionally leave the
// compatibility Include filter unset.
func configureFixedPool(compiled *compiler.CompiledStrategy, dsl config.DSL) {
	if compiled == nil || len(dsl.Rules) == 0 {
		return
	}
	ids := make([]string, 0)
	for _, rule := range dsl.Rules {
		if rule.Pool.UDF != nil {
			return
		}
		if rule.Pool.Fixed == nil {
			return
		}
		ids = append(ids, rule.Pool.Fixed...)
	}
	compiled.InstrumentPool.Include = uniquePoolIDs(ids)
	compiled.InstrumentPool.IncludeSet = true
}

func uniquePoolIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToUpper(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func expectedIndexes(compiled compiler.CompiledStrategy, event PeriodReady) map[string]string {
	expected := map[string]string{}
	if event.SourceIndexID != "" && compiled.SourceView.ID != "" {
		expected[compiled.SourceView.ID] = event.SourceIndexID
	}
	if event.ResultIndexID != "" {
		for _, factor := range compiled.Factors {
			if factor.ResultViewID != "" && (event.ViewID == "" || factor.ResultViewID == event.ViewID) {
				expected[factor.ResultViewID] = event.ResultIndexID
			}
		}
	}
	if event.SourceIndexRevision != 0 && compiled.SourceView.ID != "" {
		expected[compiled.SourceView.ID] = expected[compiled.SourceView.ID] + "\x00" + fmt.Sprint(event.SourceIndexRevision)
	}
	if event.ResultIndexRevision != 0 {
		for _, factor := range compiled.Factors {
			if factor.ResultViewID != "" && (event.ViewID == "" || factor.ResultViewID == event.ViewID) {
				expected[factor.ResultViewID] = expected[factor.ResultViewID] + "\x00" + fmt.Sprint(event.ResultIndexRevision)
			}
		}
	}
	return expected
}

func instanceRunner(instance store.StrategyInstance, compiled compiler.CompiledStrategy) domain.StrategyRunner {
	status := domain.RunnerStatusDisabled
	if instance.Enabled {
		status = domain.RunnerStatusEnabled
	}
	return domain.StrategyRunner{ID: instance.InstanceID, StrategyID: instance.StrategyID, SpaceID: instance.SpaceID, SourceViewID: compiled.SourceView.ID, Frequency: compiled.Data.Bar, LogicalAccountID: instance.LogicalAccountID, Status: status, CreatedAt: instance.CreatedAt, UpdatedAt: instance.UpdatedAt}
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func effectivePeriodTimes(event PeriodReady) (barEnd, storagePeriod time.Time) {
	if !event.BarEndTime.IsZero() {
		barEnd = event.BarEndTime.UTC()
	} else {
		barEnd = event.PeriodTime.UTC()
	}
	if !event.StoragePeriodTime.IsZero() {
		storagePeriod = event.StoragePeriodTime.UTC()
		return barEnd, storagePeriod
	}
	if !event.BarEndTime.IsZero() {
		if duration, err := report.ParseDatasetFrequency(event.Frequency); err == nil && duration > 0 {
			return barEnd, barEnd.Add(-duration)
		}
	}
	return barEnd, barEnd
}

// Most modern errors are deterministic DSL/input failures and should be
// acknowledged after recording diagnostics. Only readiness and transport
// style errors need broker redelivery.
func retryableModernError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, input.ErrNotReady) || errors.Is(err, context.DeadlineExceeded)
}

func marshalTargetEvent(instance store.StrategyInstance, result store.StrategyResult, targets []selection.TargetWeight) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payloadTargets := make([]*tradeeventpb.InstrumentWeightTarget, 0, len(targets))
	for _, target := range targets {
		payloadTargets = append(payloadTargets, &tradeeventpb.InstrumentWeightTarget{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight})
	}
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{TargetId: result.ResultID, InstanceId: result.InstanceID, LogicalAccountId: valueOrEmpty(instance.LogicalAccountID), SessionId: result.SessionID, StrategyId: instance.StrategyID, BarEndTime: timestamppb.New(result.BarEndTime), EffectiveAt: timestamppb.New(result.BarEndTime), ValidUntil: timestamppb.New(result.ValidUntil), Targets: payloadTargets}
	return registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, payload, events.PublishOptions{EventID: result.ResultID, OccurredAt: result.CreatedAt, SpaceID: instance.SpaceID, SubjectID: valueOrEmpty(instance.LogicalAccountID)})
}

func compiledResultViewCount(compiled compiler.CompiledStrategy) int {
	views := make(map[string]struct{}, len(compiled.Dependencies.FactorResultViewIDs))
	for _, viewID := range compiled.Dependencies.FactorResultViewIDs {
		if value := strings.TrimSpace(viewID); value != "" {
			views[value] = struct{}{}
		}
	}
	for _, factor := range compiled.Factors {
		if value := strings.TrimSpace(factor.ResultViewID); value != "" {
			views[value] = struct{}{}
		}
	}
	return len(views)
}

func bindingEventSourceMismatch(compiled compiler.CompiledStrategy, event PeriodReady) error {
	for _, factor := range compiled.Factors {
		if factor.BindingID == "" || (event.ViewID != "" && factor.ResultViewID != event.ViewID) {
			continue
		}
		state, ok := event.BindingStates[factor.BindingID]
		if strings.TrimSpace(factor.SourceHash) == "" {
			continue
		}
		if !ok || strings.TrimSpace(state.SourceHash) == "" {
			return fmt.Errorf("factor binding %s ready event has no source hash", factor.BindingID)
		}
		if state.SourceHash != factor.SourceHash {
			return fmt.Errorf("factor binding %s source hash changed: compiled=%s event=%s", factor.BindingID, factor.SourceHash, state.SourceHash)
		}
	}
	return nil
}

func terminalReadyForRunner(compiled compiler.CompiledStrategy, event PeriodReady, pools ...[]input.InstrumentInput) bool {
	if status := strings.TrimSpace(strings.ToLower(event.Status)); status != "" && status != "complete" && len(event.BindingStatuses) == 0 {
		return true
	}
	for _, factor := range compiled.Factors {
		if factor.BindingID == "" || (event.ViewID != "" && factor.ResultViewID != event.ViewID) {
			continue
		}
		status, ok := event.BindingStatuses[factor.BindingID]
		if !ok || strings.EqualFold(strings.TrimSpace(status), "complete") {
			continue
		}
		state, hasState := event.BindingStates[factor.BindingID]
		if !hasState || (len(state.FailedSubjects) == 0 && len(state.SkippedSubjects) == 0) {
			return true
		}
		// With a dynamic pool there is no authoritative membership information
		// until the loader has resolved it. Do not treat an explicit failed/skipped
		// subject as terminally irrelevant; the caller will retry with the loaded
		// pool instead. An explicit include list can safely scope the event here.
		if len(pools) > 0 {
			if bindingStateIntersectsPool(compiled, state, pools...) {
				return true
			}
		} else if len(compiled.InstrumentPool.Include) > 0 && bindingStateIntersectsPool(compiled, state) {
			return true
		}
	}
	return false
}

func requiredBindingsReady(compiled compiler.CompiledStrategy, event PeriodReady, pools ...[]input.InstrumentInput) bool {
	if len(event.BindingStatuses) == 0 {
		return event.Status == "" || event.Status == "complete"
	}
	required := 0
	for _, factor := range compiled.Factors {
		if factor.BindingID == "" {
			continue
		}
		if event.ViewID != "" && factor.ResultViewID != event.ViewID {
			continue
		}
		required++
		status, ok := event.BindingStatuses[factor.BindingID]
		if !ok {
			return false
		}
		if status == "complete" {
			continue
		}
		state, hasState := event.BindingStates[factor.BindingID]
		// Subject-level terminal details are scoped against an explicit
		// instrument include list when one exists. A failed/skipped subject
		// outside that list cannot affect this run; without an include list we
		// retain the conservative aggregate gate because the loader may otherwise
		// see a stale row for the failed subject.
		if !hasState || (len(state.FailedSubjects) == 0 && len(state.SkippedSubjects) == 0) {
			return false
		}
		if bindingStateIntersectsPool(compiled, state, pools...) {
			return false
		}
	}
	if required == 0 {
		// A degraded result View that this strategy does not reference must not
		// veto evaluation. Source View events are different: they carry the
		// actual market-row readiness for the whole input and remain strict.
		if event.ViewID != "" && event.ViewID != compiled.SourceView.ID {
			return true
		}
		return event.Status == "" || event.Status == "complete"
	}
	return true
}

func bindingStateIntersectsPool(compiled compiler.CompiledStrategy, state BindingPeriodState, pools ...[]input.InstrumentInput) bool {
	// The event carries storage subject IDs, while a manifest may select by
	// instrument, venue, market, quote asset, or history. The loaded pool is
	// therefore the only authoritative scope for this period. Match both IDs
	// because factor implementations may publish either representation.
	selected := make(map[string]struct{})
	if len(pools) > 0 {
		for _, item := range pools[0] {
			if value := strings.TrimSpace(item.SubjectID); value != "" {
				selected[strings.ToUpper(value)] = struct{}{}
			}
			if value := strings.TrimSpace(item.InstrumentID); value != "" {
				selected[strings.ToUpper(value)] = struct{}{}
			}
		}
	} else {
		// Keep the helper useful for direct callers and legacy tests. Without a
		// loaded pool, an explicit include list is the best available scope;
		// otherwise stay conservative.
		if len(compiled.InstrumentPool.Include) == 0 {
			return true
		}
		for _, value := range compiled.InstrumentPool.Include {
			selected[strings.ToUpper(strings.TrimSpace(value))] = struct{}{}
		}
	}
	for _, value := range append(append([]string(nil), state.FailedSubjects...), state.SkippedSubjects...) {
		if _, ok := selected[strings.ToUpper(strings.TrimSpace(value))]; ok {
			return true
		}
	}
	return false
}

func dependsOnEvent(compiled compiler.CompiledStrategy, event PeriodReady) bool {
	if event.ViewID == compiled.SourceView.ID && frequencyMatches(event.Frequency, compiled.SourceView.Frequency) {
		return true
	}
	for _, factor := range compiled.Factors {
		if factor.ResultViewID == event.ViewID && frequencyMatches(event.Frequency, factor.Frequency) {
			return true
		}
	}
	// Keep compatibility with hand-built compiled fixtures and older stored
	// artifacts that only recorded the dependency View IDs. New compilations
	// always carry per-factor result View/frequency metadata above.
	for _, viewID := range compiled.Dependencies.FactorResultViewIDs {
		if viewID == event.ViewID && strings.TrimSpace(event.Frequency) == "" {
			return true
		}
	}
	return false
}

func frequencyMatches(eventFrequency, expected string) bool {
	if strings.TrimSpace(eventFrequency) == "" || strings.TrimSpace(expected) == "" {
		return true
	}
	eventValue, eventErr := report.NormalizeDatasetFrequency(eventFrequency)
	expectedValue, expectedErr := report.NormalizeDatasetFrequency(expected)
	return eventErr == nil && expectedErr == nil && eventValue == expectedValue
}

func manifestFromCompiled(compiled compiler.CompiledStrategy) config.DSL {
	dsl := config.DSL{Name: "legacy", Data: config.Data{Bar: compiled.SourceView.Frequency, Calendar: "crypto_24x7"}, Rules: map[string]config.Rule{}}
	if dsl.Data.Bar == "" {
		dsl.Data.Bar = compiled.Schedule.Every
	}
	for name, side := range map[string]*config.Side{"long": compiled.Long, "short": compiled.Short} {
		if side == nil || side.SideWeight == "" {
			continue
		}
		rule := config.Rule{Pool: config.Pool{Fixed: append([]string(nil), compiled.InstrumentPool.Include...)}, Weight: side.SideWeight, Side: name}
		if len(side.Scores) > 0 {
			rule.Score = side.Scores[0].FactorID
		}
		if side.Selection.Mode == "count" {
			n := 0
			switch value := side.Selection.Value.(type) {
			case int:
				n = value
			case int64:
				n = int(value)
			case float64:
				n = int(value)
			case string:
				if parsed, parseErr := strconv.Atoi(value); parseErr == nil {
					n = parsed
				}
			}
			if n > 0 {
				selectRule := &config.Select{Top: &n}
				if len(side.Scores) > 0 && strings.EqualFold(side.Scores[0].Direction, "ascending") {
					selectRule.Top = nil
					selectRule.Tail = &n
				}
				rule.Select = selectRule
			}
		}
		dsl.Rules[name] = rule
	}
	return dsl
}

func aligned(period time.Time, runnerFrequency, every string) bool {
	frequency := every
	if frequency == "" {
		frequency = runnerFrequency
	}
	duration, err := report.ParseDatasetFrequency(frequency)
	if err != nil || duration <= 0 {
		return false
	}
	return period.UnixNano()%duration.Nanoseconds() == 0
}

func sameTargets(raw json.RawMessage, targets []domain.InstrumentTarget) bool {
	var current []domain.InstrumentTarget
	if len(raw) > 0 && json.Unmarshal(raw, &current) == nil {
		return targetJSON(current) == targetJSON(targets)
	}
	return len(targets) == 0 && (len(raw) == 0 || string(raw) == "[]")
}

func targetJSON(targets []domain.InstrumentTarget) string {
	copyTargets := append([]domain.InstrumentTarget(nil), targets...)
	sort.Slice(copyTargets, func(i, j int) bool { return copyTargets[i].InstrumentID < copyTargets[j].InstrumentID })
	raw, _ := json.Marshal(copyTargets)
	return string(raw)
}

func resultID(messageID, runnerID string, period time.Time, sessionID ...string) string {
	parts := []string{messageID, runnerID, period.UTC().Format(time.RFC3339Nano)}
	if len(sessionID) > 0 {
		parts = append(parts, sessionID[0])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sr_" + hex.EncodeToString(sum[:16])
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
