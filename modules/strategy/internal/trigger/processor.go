package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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
)

type PeriodReady struct {
	MessageID    string
	EventName    string
	SpaceID      string
	ViewID       string
	SourceViewID string
	Frequency    string
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

// PooledPeriodInputLoader resolves pool UDFs after it has pinned the same
// View snapshot that supplies the evaluation rows. The callback returns each
// rule's resolved pool and the union used to narrow the Storage read.
type PooledPeriodInputLoader interface {
	LoadPeriodAtWithPool(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time, time.Time, map[string]string, func([]input.Subject, time.Time) (map[string][]string, []string, error)) (input.EvaluationInput, error)
}

// SubjectDirectoryLoader exposes the frozen subject catalog before row
// loading. Production storage implements it so a pool UDF can narrow the
// read set before completeness checks run; older embedders may omit it and
// retain the compatibility path.
type SubjectDirectoryLoader interface {
	ListSubjects(context.Context, string, string) ([]input.Subject, error)
}

type Processor struct {
	// evaluationMu serializes modern evaluations in this process. A schedule
	// callback and a View-ready callback can arrive concurrently; keeping the
	// whole evaluation path single-file preserves RuleState ordering and lets
	// the existing Result CAS remain a last-line guard rather than the normal
	// coordination mechanism. This is intentionally process-local for the
	// personal deployment model; cross-process delivery is fenced by session_id.
	evaluationMu sync.Mutex
	Inbox        interface {
		IsProcessed(context.Context, string) (bool, error)
		MarkProcessed(context.Context, string, string, time.Time) error
	}
	Store        *store.Store
	Loader       InputLoader
	PoolRegistry *input.UDFRegistry
	// Compile rebuilds executable expressions and the dependency snapshot for
	// a persisted DSL. Production supplies a compiler backed by the Factor and
	// Storage catalogs; tests and embedded callers may omit it and use the
	// lightweight DSL fallback below.
	Compile func(context.Context, config.DSL, string) (compiler.CompiledStrategy, error)
	// CompileWithBindings is the production path for instance-specific fields;
	// it prevents bars[0].factor from being rejected before the binding is
	// attached to the shared DSL artifact.
	CompileWithBindings func(context.Context, config.DSL, string, json.RawMessage) (compiler.CompiledStrategy, error)
	// VerifyDependencies is optional for embedded/test processors. Production
	// bootstrap supplies the compiler-backed verifier so a running instance is
	// not allowed to consume a Factor that changed after it was compiled.
	VerifyDependencies func(context.Context, compiler.CompiledStrategy) error
	// Diagnostic receives non-fatal per-event evaluation errors. The trigger
	// path still decides whether to retry or acknowledge the event; callers can
	// use this hook to make skipped evaluations observable without coupling the
	// processor to a logging implementation.
	Diagnostic func(error)
	// SessionGeneration is the session-aware variant used by modern instances.
	// It must validate instance_id and session_id together.
	SessionGeneration func(context.Context, string, string, string, string) (int64, error)
	Now               func() time.Time
}

func (p *Processor) reportDiagnostic(err error) {
	if p != nil && err != nil && p.Diagnostic != nil {
		p.Diagnostic(err)
	}
}

func (p *Processor) Handle(ctx context.Context, event PeriodReady) error {
	if p == nil || p.Store == nil || p.Loader == nil || event.MessageID == "" || event.SpaceID == "" || event.ViewID == "" || event.PeriodTime.IsZero() {
		return fmt.Errorf("strategy trigger is not configured")
	}
	inbox := p.Inbox
	if inbox == nil {
		inbox = p.Store
	}
	processed, err := inbox.IsProcessed(ctx, event.MessageID)
	if err != nil || processed {
		return err
	}
	if err := p.handleInstances(ctx, event); err != nil {
		return err
	}
	return inbox.MarkProcessed(ctx, event.MessageID, event.EventName, p.now())
}

func (p *Processor) handleInstances(ctx context.Context, event PeriodReady) error {
	// robfig/cron starts each job in its own goroutine, while ready events are
	// consumed independently. Serialize the modern path so stateful signal
	// rules cannot evaluate two periods from the same stale predecessor.
	p.evaluationMu.Lock()
	defer p.evaluationMu.Unlock()
	instances, err := p.Store.ListInstances(ctx, event.SpaceID, ptrBool(true))
	if err != nil || len(instances) == 0 {
		return err
	}
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
			p.reportDiagnostic(fmt.Errorf("instance %s parse definition: %w", instance.InstanceID, parseErr))
			continue
		}
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
				p.reportDiagnostic(fmt.Errorf("instance %s compile: %w", instance.InstanceID, err))
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
					p.reportDiagnostic(fmt.Errorf("instance %s compile: %w", instance.InstanceID, err))
					retryErr = err
				}
				continue
			}
		} else {
			compiled = compiler.CompiledStrategy{Name: dsl.Name, SpaceID: instance.SpaceID, Data: dsl.Data, Triggers: dsl.Triggers}
		}
		augmentCompiledBindings(&compiled, instance.InputBindingsJSON)
		if p.VerifyDependencies != nil {
			if verifyErr := p.VerifyDependencies(ctx, compiled); verifyErr != nil {
				p.reportDiagnostic(fmt.Errorf("instance %s dependency check: %w", instance.InstanceID, verifyErr))
				if retryErr == nil && !errors.Is(verifyErr, compiler.ErrDependencyMismatch) {
					retryErr = verifyErr
				}
				continue
			}
		}
		if event.EventName != "strategy.schedule" && (len(compiled.Factors) > 0 || len(compiled.Dependencies.FactorResultViewIDs) > 0) && !dependsOnEvent(compiled, event) {
			continue
		}
		if hasCompiledFactorBindings(compiled) && indexProvenanceIsPartial(event) {
			// A malformed in-process event must not allow Storage to pin one
			// View generation while silently using the other View's current
			// generation. Public event validation rejects this shape as well.
			if retryErr == nil {
				retryErr = fmt.Errorf("%w: factor ready index provenance is incomplete", input.ErrNotReady)
			}
			continue
		}
		if event.SourceViewID != "" && compiled.SourceView.ID != "" && event.SourceViewID != compiled.SourceView.ID {
			// A result-ready event from another source View must not be used to
			// satisfy this instance's input binding or index provenance.
			continue
		}
		// Only claim the delivery once this instance is routed to the same
		// source/factor View. An unrelated instance must not affect this delivery.
		if event.TargetInstanceID != "" && hasCompiledFactorBindings(compiled) {
			// A timer wake-up has no Factor source/result index provenance. Do not
			// combine a newly revised source row with an older factor row; the
			// factor.ready event will evaluate this instance once its generation is
			// published. Schedule-only factor strategies are rejected at enable time.
			if retryErr == nil {
				retryErr = fmt.Errorf("%w: factor-backed strategy requires factor.ready provenance", input.ErrNotReady)
			}
			continue
		}
		runner := instanceRunner(instance, compiled)
		period := instanceBarEnd.UTC()
		var previous map[string]domain.RuleState
		var latestResult store.StrategyResult
		hasLatestResult := false
		if latest, latestErr := p.Store.LatestResult(ctx, instance.InstanceID, valueOrEmpty(instance.SessionID)); latestErr == nil {
			latestResult = latest
			hasLatestResult = true
			if len(strings.TrimSpace(string(latest.RuleStatesJSON))) > 0 {
				if stateErr := json.Unmarshal(latest.RuleStatesJSON, &previous); stateErr != nil {
					stateErr = fmt.Errorf("instance %s rule state is corrupt: %w", instance.InstanceID, stateErr)
					p.reportDiagnostic(stateErr)
					if retryErr == nil {
						retryErr = stateErr
					}
					continue
				}
			}
		}
		if hasLatestResult && !period.After(latestResult.BarEndTime) {
			continue
		}
		// Keep the predecessor observed at the start of evaluation. Re-reading
		// LatestResult after loading/evaluating would allow a concurrent worker
		// to advance the state and make this stale computation overwrite it.
		expectedResultID := ""
		if hasLatestResult {
			expectedResultID = latestResult.ResultID
		}
		withoutInput := holdingPeriodNeedsNoInput(dsl, compiled, period, previous)
		inputDSL := inputDSLForPeriod(dsl, compiled, period, previous)
		loadCompiled := compiled
		// Rebuild the compatibility pool from only rules that need current
		// input. Holding rules on non-establishment bars are carried from
		// RuleState and must not make unrelated source/factor rows a readiness
		// prerequisite for ordinary rules.
		loadCompiled.InstrumentPool.Include = nil
		loadCompiled.InstrumentPool.IncludeSet = false
		loadCompiled.InstrumentPool.HistoricalInclude = nil
		trimCompiledInput(&loadCompiled, inputDSL)
		if mismatchErr := bindingEventSourceMismatch(loadCompiled, event); mismatchErr != nil {
			p.reportDiagnostic(fmt.Errorf("instance %s binding source: %w", instance.InstanceID, mismatchErr))
			continue
		}
		configureFixedPool(&loadCompiled, inputDSL)
		_, pooledLoader := p.Loader.(PooledPeriodInputLoader)
		if hasPoolUDF(inputDSL) && p.PoolRegistry != nil && !pooledLoader {
			directory, ok := p.Loader.(SubjectDirectoryLoader)
			if !ok {
				if retryErr == nil {
					retryErr = errors.New("strategy pool UDF requires a subject directory loader")
				}
				continue
			}
			subjects, listErr := directory.ListSubjects(ctx, instance.SpaceID, compiled.SourceView.ID)
			if listErr != nil {
				if retryErr == nil {
					retryErr = fmt.Errorf("list subjects for pool UDF: %w", listErr)
				}
				continue
			}
			resolved, poolIDs, resolveErr := resolveRulePools(ctx, inputDSL, p.PoolRegistry, subjects, period)
			if resolveErr != nil {
				p.reportDiagnostic(fmt.Errorf("instance %s resolve pools: %w", instance.InstanceID, resolveErr))
				if retryErr == nil && !errors.Is(resolveErr, input.ErrPoolUDFNotRegistered) && !errors.Is(resolveErr, input.ErrPoolUDFInvalidParams) {
					retryErr = resolveErr
				}
				continue
			}
			for name, rule := range resolved.Rules {
				dsl.Rules[name] = rule
				inputDSL.Rules[name] = rule
			}
			loadCompiled.InstrumentPool.Include = poolIDs
			loadCompiled.InstrumentPool.IncludeSet = true
		}
		includeRuleStateIDs(&loadCompiled, previous)
		var evaluated input.EvaluationInput
		expectedIndexesMap := expectedIndexes(compiled, event)
		if withoutInput {
			duration, durationErr := report.ParseDatasetFrequency(compiled.Data.Bar)
			if durationErr != nil || duration <= 0 {
				if retryErr == nil {
					retryErr = fmt.Errorf("holding period frequency %q: %w", compiled.Data.Bar, durationErr)
				}
				continue
			}
			barIndex := int64(-1)
			barEndAt := func(offset int) time.Time { return period.UTC().Add(time.Duration(offset) * duration) }
			if boundaries, boundaryErr := input.FromBarEnd(compiled.Data.Calendar, compiled.SourceView.Frequency, period); boundaryErr == nil {
				barIndex = boundaries.BarIndex
				barEndAt = func(offset int) time.Time {
					value, advanceErr := input.AdvanceBarEnd(compiled.Data.Calendar, compiled.SourceView.Frequency, period, offset)
					if advanceErr != nil {
						return period.UTC().Add(time.Duration(offset) * duration)
					}
					return value
				}
			}
			evaluated = input.EvaluationInput{SpaceID: instance.SpaceID, StrategyID: instance.StrategyID, PeriodEnd: period.UTC().Format(time.RFC3339Nano), SourceViewID: compiled.SourceView.ID, DataFrequency: compiled.SourceView.Frequency, BarIndex: barIndex, BarDuration: duration, BarEndAt: barEndAt}
		} else if pooled, ok := p.Loader.(PooledPeriodInputLoader); ok && hasPoolUDF(inputDSL) && p.PoolRegistry != nil {
			evaluated, err = pooled.LoadPeriodAtWithPool(ctx, runner, loadCompiled, period, instanceStorage.UTC(), expectedIndexesMap, func(subjects []input.Subject, at time.Time) (map[string][]string, []string, error) {
				resolved, poolIDs, resolveErr := resolveRulePools(ctx, inputDSL, p.PoolRegistry, subjects, at)
				if resolveErr != nil {
					return nil, nil, resolveErr
				}
				rulePools := make(map[string][]string, len(resolved.Rules))
				for name, rule := range resolved.Rules {
					inputDSL.Rules[name] = rule
					dsl.Rules[name] = rule
					rulePools[name] = append([]string(nil), rule.Pool.Fixed...)
				}
				return rulePools, poolIDs, nil
			})
		} else if indexed, ok := p.Loader.(IndexedPeriodInputLoader); ok {
			if event.TargetInstanceID == "" && len(expectedIndexesMap) == 0 {
				if retryErr == nil {
					retryErr = fmt.Errorf("%w: View-ready event has no index provenance", input.ErrLegacyProvenance)
				}
				continue
			}
			if event.TargetInstanceID != "" {
				expectedIndexesMap = nil // scheduled jobs intentionally use the active snapshot
			}
			evaluated, err = indexed.LoadPeriodAt(ctx, runner, loadCompiled, period, instanceStorage.UTC(), expectedIndexesMap)
		} else if periodLoader, ok := p.Loader.(PeriodInputLoader); ok {
			evaluated, err = periodLoader.LoadPeriod(ctx, runner, loadCompiled, period, instanceStorage.UTC())
		} else if indexed, ok := p.Loader.(IndexedInputLoader); ok {
			evaluated, err = indexed.LoadAt(ctx, runner, loadCompiled, instanceStorage.UTC(), expectedIndexesMap)
		} else {
			evaluated, err = p.Loader.Load(ctx, runner, loadCompiled, instanceStorage.UTC())
		}
		if err != nil {
			p.reportDiagnostic(fmt.Errorf("instance %s load period %s: %w", instance.InstanceID, period.Format(time.RFC3339), err))
			if errors.Is(err, input.ErrStrictIncomplete) {
				// A scheduled wake-up owns this bar and must keep retrying while
				// Storage/Factor finishes publishing it. Ready events carrying a
				// terminal degraded binding are acknowledged instead.
				if event.TargetInstanceID != "" || !terminalReadyForInstance(compiled, event) {
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
		for _, rule := range inputDSL.Rules {
			if rule.Pool.UDF != nil && p.PoolRegistry == nil {
				poolResolutionFailed = true
			}
		}
		if poolResolutionFailed {
			continue
		}
		if p.PoolRegistry != nil && hasPoolUDF(inputDSL) {
			subjects := make([]input.Subject, 0, len(evaluated.Items))
			for _, item := range evaluated.Items {
				subjects = append(subjects, input.Subject{SubjectID: item.SubjectID, InstrumentID: item.InstrumentID, Exchange: item.Exchange, Market: item.Market, QuoteAsset: item.QuoteAsset, SeriesTag: item.SeriesTag, Active: true})
			}
			for name, rule := range inputDSL.Rules {
				if rule.Pool.UDF == nil {
					continue
				}
				ids, resolveErr := p.PoolRegistry.Resolve(ctx, rule.Pool, subjects, period)
				if resolveErr != nil {
					p.reportDiagnostic(fmt.Errorf("instance %s resolve pool %s: %w", instance.InstanceID, name, resolveErr))
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
		if !requiredBindingsReady(loadCompiled, event, evaluated.Items) {
			// A required binding that is degraded for this period must not be
			// replaced by a stale/partial row that happens to remain readable.
			// The check is against the trimmed plan so a carried holding rule does
			// not make an unrelated Factor a prerequisite for this bar.
			continue
		}
		evaluation, evalErr := selection.Evaluate(dsl, evaluated, previous)
		if evalErr != nil {
			p.reportDiagnostic(fmt.Errorf("instance %s evaluate: %w", instance.InstanceID, evalErr))
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
		if instance.LogicalAccountID != nil && p.SessionGeneration != nil {
			var generationErr error
			// Modern targets are fenced by instance_id + session_id. The numeric
			// return value is not part of the event contract.
			_, generationErr = p.SessionGeneration(ctx, event.SpaceID, *instance.LogicalAccountID, instance.InstanceID, valueOrEmpty(instance.SessionID))
			if generationErr != nil {
				p.reportDiagnostic(fmt.Errorf("instance %s owner generation: %w", instance.InstanceID, generationErr))
				if retryErr == nil {
					retryErr = generationErr
				}
				continue
			}
		}
		validUntil, validUntilErr := input.AdvanceBarEnd(dsl.Data.Calendar, dsl.Data.Bar, period, 2)
		if validUntilErr != nil {
			p.reportDiagnostic(fmt.Errorf("instance %s validity: %w", instance.InstanceID, validUntilErr))
			if retryErr == nil {
				retryErr = validUntilErr
			}
			continue
		}
		validUntil = tightenValidUntil(validUntil, evaluation.RuleStates)
		sourceIndexID, resultIndexID := event.SourceIndexID, event.ResultIndexID
		sourceIndexRevision, resultIndexRevision := event.SourceIndexRevision, event.ResultIndexRevision
		if evaluated.SourceIndexID != "" {
			sourceIndexID = evaluated.SourceIndexID
		}
		if evaluated.ResultIndexID != "" {
			resultIndexID = evaluated.ResultIndexID
		}
		if evaluated.SourceIndexRevision != 0 {
			sourceIndexRevision = evaluated.SourceIndexRevision
		}
		if evaluated.ResultIndexRevision != 0 {
			resultIndexRevision = evaluated.ResultIndexRevision
		}
		snapshot, snapshotErr := json.Marshal(map[string]any{
			"strategy_id": instance.StrategyID, "dsl_yaml": definition.DSLYaml,
			"inputs": json.RawMessage(instance.InputBindingsJSON), "period_end": period,
			"source_index_id": sourceIndexID, "result_index_id": resultIndexID,
			"source_index_revision": sourceIndexRevision, "result_index_revision": resultIndexRevision,
		})
		if snapshotErr != nil {
			p.reportDiagnostic(fmt.Errorf("instance %s snapshot: %w", instance.InstanceID, snapshotErr))
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
		if _, _, commitErr := p.Store.CommitResult(ctx, store.CommitResultRequest{Result: result, ExpectedResultID: &expectedResultID, Now: p.now()}); commitErr != nil {
			p.reportDiagnostic(fmt.Errorf("instance %s commit result %s: %w", instance.InstanceID, result.ResultID, commitErr))
			if retryErr == nil && retryableCommitResultError(commitErr) {
				retryErr = commitErr
			}
		}
	}
	if retryErr != nil {
		return retryErr
	}
	return nil
}

func holdingPeriodNeedsNoInput(dsl config.DSL, compiled compiler.CompiledStrategy, period time.Time, previous map[string]domain.RuleState) bool {
	if len(dsl.Rules) == 0 {
		return false
	}
	for name, rule := range dsl.Rules {
		if ruleNeedsCurrentInput(name, rule, dsl, compiled, period, previous) {
			return false
		}
	}
	return true
}

// inputDSLForPeriod describes only the rules that need current source/factor
// rows for this bar. A holding rule on a non-offset bar is carried from its
// persisted batches and must not contribute its pool or UDF to the load plan.
func inputDSLForPeriod(dsl config.DSL, compiled compiler.CompiledStrategy, period time.Time, previous map[string]domain.RuleState) config.DSL {
	result := dsl
	result.Rules = make(map[string]config.Rule, len(dsl.Rules))
	for name, rule := range dsl.Rules {
		if ruleNeedsCurrentInput(name, rule, dsl, compiled, period, previous) {
			result.Rules[name] = rule
		}
	}
	return result
}

// trimCompiledInput keeps the Storage read plan limited to rules that need a
// fresh row for this period. Holding rules carried from RuleState must not
// pull in their independent Factor Views or bars[-1] history and make an
// otherwise evaluable period wait for unrelated data.
func trimCompiledInput(compiled *compiler.CompiledStrategy, dsl config.DSL) {
	if compiled == nil {
		return
	}
	wanted := make(map[string]struct{}, len(dsl.Rules))
	for name := range dsl.Rules {
		wanted[name] = struct{}{}
	}
	rules := make([]compiler.CompiledRule, 0, len(compiled.Rules))
	fields := make(map[string]struct{})
	for _, rule := range compiled.Rules {
		if _, ok := wanted[rule.Name]; !ok {
			continue
		}
		rules = append(rules, rule)
		for _, expression := range []*compiler.CompiledExpression{rule.FilterBefore, rule.Score, rule.SelectWhere, rule.SignalEntry, rule.SignalExit, rule.FilterAfter} {
			if expression == nil {
				continue
			}
			for _, field := range expression.Dependencies.Fields {
				fields[strings.TrimSpace(field)] = struct{}{}
			}
			for _, bars := range expression.Dependencies.Bars {
				for _, field := range bars {
					fields[strings.TrimSpace(field)] = struct{}{}
				}
			}
		}
	}
	compiled.Rules = rules
	if len(compiled.Factors) == 0 {
		compiled.Dependencies.FactorResultViewIDs = nil
		return
	}
	factors := make([]compiler.CompiledFactor, 0, len(compiled.Factors))
	views := make([]string, 0, len(compiled.Dependencies.FactorResultViewIDs))
	seenViews := make(map[string]struct{})
	for _, factor := range compiled.Factors {
		used := false
		for _, alias := range []string{factor.FactorID, factor.Output, factor.ColumnName} {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			if _, ok := fields[alias]; ok {
				used = true
				break
			}
			for field := range fields {
				if strings.EqualFold(field, alias) {
					used = true
					break
				}
			}
			if used {
				break
			}
		}
		if !used {
			continue
		}
		factors = append(factors, factor)
		if id := strings.TrimSpace(factor.ResultViewID); id != "" {
			if _, ok := seenViews[id]; !ok {
				seenViews[id] = struct{}{}
				views = append(views, id)
			}
		}
	}
	compiled.Factors = factors
	sort.Strings(views)
	compiled.Dependencies.FactorResultViewIDs = views
}

func ruleNeedsCurrentInput(name string, rule config.Rule, dsl config.DSL, compiled compiler.CompiledStrategy, period time.Time, previous map[string]domain.RuleState) bool {
	if rule.Holding == nil {
		return true
	}
	boundaries, err := input.FromBarEnd(dsl.Data.Calendar, compiled.SourceView.Frequency, period)
	if err != nil || rule.Holding.Bars <= 0 {
		return true
	}
	hit := boundaries.BarIndex
	if containsIntValue(rule.Holding.Offsets, int(hit%int64(rule.Holding.Bars))) {
		return true
	}
	// A malformed state should still take the normal loader path so the
	// evaluator can report it instead of silently treating it as empty.
	if state, ok := previous[name]; ok && len(state.Signals) > 0 {
		return true
	}
	return false
}

func containsIntValue(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ptrBool(v bool) *bool { return &v }

func hasPoolUDF(dsl config.DSL) bool {
	for _, rule := range dsl.Rules {
		if rule.Pool.UDF != nil {
			return true
		}
	}
	return false
}

func hasCompiledFactorBindings(compiled compiler.CompiledStrategy) bool {
	return len(compiled.Factors) > 0 || len(compiled.Dependencies.FactorResultViewIDs) > 0
}

func resolveRulePools(ctx context.Context, dsl config.DSL, registry *input.UDFRegistry, subjects []input.Subject, at time.Time) (config.DSL, []string, error) {
	if registry == nil {
		return config.DSL{}, nil, input.ErrPoolUDFRegistryUnavailable
	}
	resolved := dsl
	resolved.Rules = make(map[string]config.Rule, len(dsl.Rules))
	ids := make([]string, 0)
	for name, rule := range dsl.Rules {
		current := rule
		poolIDs, err := registry.Resolve(ctx, rule.Pool, subjects, at)
		if err != nil {
			return config.DSL{}, nil, fmt.Errorf("rule %s: %w", name, err)
		}
		current.Pool = config.Pool{Fixed: poolIDs}
		resolved.Rules[name] = current
		ids = append(ids, poolIDs...)
	}
	return resolved, uniquePoolIDs(ids), nil
}

func tightenValidUntil(defaultUntil time.Time, states map[string]domain.RuleState) time.Time {
	until := defaultUntil
	for _, state := range states {
		for _, batch := range state.Batches {
			if batch.ExpiresAt <= 0 {
				continue
			}
			expires := time.UnixMilli(batch.ExpiresAt).UTC()
			if expires.Before(until) {
				until = expires
			}
		}
	}
	return until
}

func includeRuleStateIDs(compiled *compiler.CompiledStrategy, states map[string]domain.RuleState) {
	if compiled == nil || len(states) == 0 {
		return
	}
	ids := append([]string(nil), compiled.InstrumentPool.Include...)
	historical := append([]string(nil), compiled.InstrumentPool.HistoricalInclude...)
	for _, state := range states {
		for _, signal := range state.Signals {
			ids = append(ids, signal.InstrumentID)
			historical = append(historical, signal.InstrumentID)
		}
	}
	compiled.InstrumentPool.Include = uniquePoolIDs(ids)
	compiled.InstrumentPool.HistoricalInclude = uniquePoolIDs(historical)
	compiled.InstrumentPool.IncludeSet = true
}

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
	case "viewsourceperiodready", "source.ready", "ready", "event.storage.view.source_period.ready":
		return "event.storage.view.source_period.ready"
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
}

// configureFixedPool keeps the Storage read set narrow for the common case
// where every rule names a literal pool. A dynamic UDF must see the complete
// subject directory, so mixed fixed/UDF strategies intentionally leave the
// Include filter unset.
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

func indexProvenanceIsPartial(event PeriodReady) bool {
	hasSource := event.SourceIndexID != "" || event.SourceIndexRevision != 0
	hasResult := event.ResultIndexID != "" || event.ResultIndexRevision != 0
	return hasSource != hasResult
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
	return errors.Is(err, input.ErrNotReady) || errors.Is(err, input.ErrStrictIncomplete) || errors.Is(err, store.ErrResultCASConflict) || errors.Is(err, context.DeadlineExceeded)
}

func retryableCommitResultError(err error) bool {
	if err == nil {
		return false
	}
	// These errors describe a terminal lifecycle decision for this delivery;
	// retrying it would only replay an inactive, expired, older, or malformed
	// target.
	if errors.Is(err, store.ErrResultInvalid) || errors.Is(err, store.ErrResultInstanceNotActive) || errors.Is(err, store.ErrResultExpired) || errors.Is(err, store.ErrResultOlder) {
		return false
	}
	// SQLite busy/I/O and other unknown persistence errors must remain
	// retryable. A successful calculation that was not durably committed must
	// never be acknowledged as if it had produced a target.
	return true
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
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: result.ResultID, InstanceId: result.InstanceID,
		LogicalAccountId: valueOrEmpty(instance.LogicalAccountID), SessionId: result.SessionID,
		StrategyId: instance.StrategyID,
		BarEndTime: timestamppb.New(result.BarEndTime), EffectiveAt: timestamppb.New(result.BarEndTime),
		ValidUntil: timestamppb.New(result.ValidUntil), Targets: payloadTargets,
	}
	return registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, payload, events.PublishOptions{EventID: result.ResultID, OccurredAt: result.CreatedAt, SpaceID: instance.SpaceID, SubjectID: valueOrEmpty(instance.LogicalAccountID)})
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

func terminalReadyForInstance(compiled compiler.CompiledStrategy, event PeriodReady, pools ...[]input.InstrumentInput) bool {
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
			if bindingStateAffectsPool(factor, state, pools...) {
				return true
			}
		} else if len(compiled.InstrumentPool.Include) > 0 {
			items := make([]input.InstrumentInput, 0, len(compiled.InstrumentPool.Include))
			for _, id := range compiled.InstrumentPool.Include {
				items = append(items, input.InstrumentInput{PoolItem: input.PoolItem{InstrumentID: id}})
			}
			if bindingStateAffectsPool(factor, state, items) {
				return true
			}
		}
	}
	return false
}

func requiredBindingsReady(compiled compiler.CompiledStrategy, event PeriodReady, pools ...[]input.InstrumentInput) bool {
	if len(event.BindingStatuses) == 0 {
		return event.Status == "" || event.Status == "complete"
	}
	scopedPools := pools
	if len(scopedPools) == 0 && len(compiled.InstrumentPool.Include) > 0 {
		items := make([]input.InstrumentInput, 0, len(compiled.InstrumentPool.Include))
		for _, id := range compiled.InstrumentPool.Include {
			items = append(items, input.InstrumentInput{PoolItem: input.PoolItem{InstrumentID: id}})
		}
		scopedPools = [][]input.InstrumentInput{items}
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
		if bindingStateAffectsPool(factor, state, scopedPools...) {
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

func bindingStateAffectsPool(factor compiler.CompiledFactor, state BindingPeriodState, pools ...[]input.InstrumentInput) bool {
	if len(pools) == 0 {
		return true
	}
	var scoped map[string]struct{}
	if strings.EqualFold(strings.TrimSpace(factor.SubjectMode), "include") {
		var values []string
		if json.Unmarshal([]byte(factor.SubjectsJSON), &values) != nil {
			return true
		}
		scoped = make(map[string]struct{}, len(values))
		for _, value := range values {
			scoped[strings.ToUpper(strings.TrimSpace(value))] = struct{}{}
		}
	}
	for _, value := range append(append([]string(nil), state.FailedSubjects...), state.SkippedSubjects...) {
		key := strings.ToUpper(strings.TrimSpace(value))
		for _, item := range pools[0] {
			subject := strings.ToUpper(strings.TrimSpace(item.SubjectID))
			instrument := strings.ToUpper(strings.TrimSpace(item.InstrumentID))
			if key != subject && key != instrument {
				continue
			}
			if len(scoped) == 0 {
				return true
			}
			if _, ok := scoped[key]; ok {
				return true
			}
			if _, ok := scoped[subject]; ok {
				return true
			}
			if _, ok := scoped[instrument]; ok {
				return true
			}
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

func resultID(messageID, instanceID string, period time.Time, sessionID ...string) string {
	parts := []string{messageID, instanceID, period.UTC().Format(time.RFC3339Nano)}
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
