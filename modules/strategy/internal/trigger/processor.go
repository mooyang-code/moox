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
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/selection"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

type PeriodReady struct {
	MessageID    string
	EventName    string
	SpaceID      string
	ViewID       string
	Frequency    string
	PeriodTime   time.Time
	Status       string
	ReadyViewIDs []string
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

type Processor struct {
	Inbox interface {
		IsProcessed(context.Context, string) (bool, error)
		MarkProcessed(context.Context, string, string, time.Time) error
	}
	Store  *store.Store
	Loader InputLoader
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
	processed, err := inbox.IsProcessed(ctx, event.MessageID)
	if err != nil || processed {
		return err
	}
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
			if resultErr == nil && event.PeriodTime.Before(last.PeriodTime) {
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
				evaluationInput, loadErr = indexed.LoadAt(ctx, runner, compiled, event.PeriodTime.UTC(), expected)
			}
		} else {
			evaluationInput, loadErr = p.Loader.Load(ctx, runner, compiled, event.PeriodTime.UTC())
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
		result := domain.StrategyResult{ID: resultID(event.MessageID, runner.ID, event.PeriodTime), RunnerID: runner.ID, StrategyID: runner.StrategyID, PeriodTime: event.PeriodTime.UTC(), InputHash: inputHash, Action: eval.Action, CreatedAt: p.now()}
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

func manifestFromCompiled(compiled compiler.CompiledStrategy) (manifest config.Manifest) {
	return config.Manifest{APIVersion: compiled.APIVersion, Kind: compiled.Kind, Input: config.ManifestInput{SourceViewID: compiled.SourceView.ID, DataFrequency: compiled.SourceView.Frequency}, InstrumentPool: compiled.InstrumentPool, Schedule: config.Schedule{Every: compiled.Schedule.Every}, Readiness: config.Readiness{Policy: compiled.Readiness}, Long: compiled.Long, Short: compiled.Short}
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

func resultID(messageID, runnerID string, period time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{messageID, runnerID, period.UTC().Format(time.RFC3339Nano)}, "\x00")))
	return "sr_" + hex.EncodeToString(sum[:16])
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
