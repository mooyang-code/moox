package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

var ErrNoExecutableBinding = errors.New("no executable factor binding for source View period")
var ErrBindingNotReady = errors.New("factor binding is not ready for source View period")

type PeriodBindingSource interface {
	ListExecutable(context.Context) ([]domain.FactorBinding, error)
}

type PeriodBindingReadinessSource interface {
	HasExecutableOrPending(context.Context, string, string, string) (bool, error)
}

type PeriodFactorSource interface {
	Get(context.Context, string) (*domain.FactorDef, error)
}

type CombinationTaskRunner interface {
	RunAll(context.Context, []taskrunner.Task) []taskrunner.Result
}

type PeriodStorage interface {
	FactorPeriodComputed(context.Context, string, string, string, int64) (bool, error)
	ReportFactorPeriodComputed(context.Context, string, *storagepb.FactorPeriodComputedMarker) error
	ClearFactorOutputs(context.Context, *engine.FactorTask) error
}

type ViewReadyRunner struct {
	bindings      PeriodBindingSource
	factors       PeriodFactorSource
	taskRunner    CombinationTaskRunner
	storage       PeriodStorage
	factorsDir    string
	operationGate *taskrunner.OperationGate
	periodMetrics *observability.PeriodMetrics
}

type Option func(*ViewReadyRunner)

func WithOperationGate(gate *taskrunner.OperationGate) Option {
	return func(r *ViewReadyRunner) {
		if gate != nil {
			r.operationGate = gate
		}
	}
}

func WithPeriodMetrics(metrics *observability.PeriodMetrics) Option {
	return func(r *ViewReadyRunner) { r.periodMetrics = metrics }
}

func NewViewReadyRunner(bindings PeriodBindingSource, factors PeriodFactorSource, runner CombinationTaskRunner, storage PeriodStorage, factorsDir string, opts ...Option) *ViewReadyRunner {
	r := &ViewReadyRunner{
		bindings: bindings, factors: factors, taskRunner: runner, storage: storage,
		factorsDir: factorsDir, operationGate: taskrunner.NewOperationGate(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

func (r *ViewReadyRunner) Execute(ctx context.Context, spaceID, triggerEventID string, ready *publicstoragepb.ViewSourcePeriodReady) error {
	err := r.ExecuteSelected(ctx, spaceID, triggerEventID, "", ready)
	if errors.Is(err, ErrNoExecutableBinding) {
		return nil
	}
	return err
}

func (r *ViewReadyRunner) ExecuteSelected(ctx context.Context, spaceID, triggerEventID, factorID string, ready *publicstoragepb.ViewSourcePeriodReady) error {
	return r.executeSelected(ctx, spaceID, triggerEventID, factorID, ready, true)
}

func (r *ViewReadyRunner) ExecuteSelectedWithGate(ctx context.Context, spaceID, triggerEventID, factorID string, ready *publicstoragepb.ViewSourcePeriodReady) error {
	return r.executeSelected(ctx, spaceID, triggerEventID, factorID, ready, false)
}

func (r *ViewReadyRunner) executeSelected(ctx context.Context, spaceID, triggerEventID, factorID string, ready *publicstoragepb.ViewSourcePeriodReady, acquireGate bool) error {
	if r == nil || r.bindings == nil || r.factors == nil || r.taskRunner == nil || r.storage == nil {
		return fmt.Errorf("factor View-ready runner dependencies are required")
	}
	if ready == nil || spaceID == "" || triggerEventID == "" || ready.GetSourceViewId() == "" || ready.GetFrequency() == "" || ready.GetPeriodTime() <= 0 {
		return fmt.Errorf("source-ready event identity is incomplete")
	}
	if acquireGate {
		releaseOperation, gateErr := r.operationGate.AcquireContext(ctx)
		if gateErr != nil {
			return gateErr
		}
		defer releaseOperation()
	}

	log.InfoContextf(ctx, "factor View-ready execution start event_id=%s space_id=%s view_id=%s period=%d", triggerEventID, spaceID, ready.GetSourceViewId(), ready.GetPeriodTime())
	r.periodMetrics.Begin(ready.GetSourceViewId(), ready.GetFrequency())
	defer r.periodMetrics.End(ready.GetSourceViewId(), ready.GetFrequency())
	if !strings.HasPrefix(triggerEventID, "recalc-") && ready.GetReadyAt() != nil && ready.GetReadyAt().IsValid() {
		r.periodMetrics.ObserveSourceReady(ready.GetSourceViewId(), ready.GetFrequency(), ready.GetReadyAt().AsTime())
	}

	bindings, err := r.bindings.ListExecutable(ctx)
	if err != nil {
		return fmt.Errorf("list executable factor bindings: %w", err)
	}
	selected := selectPeriodBindings(bindings, spaceID, ready)
	if len(selected) == 0 || (factorID != "" && !containsFactor(selected, factorID)) {
		if factorID == "" && len(selected) == 0 {
			if readiness, ok := r.bindings.(PeriodBindingReadinessSource); ok {
				waiting, readinessErr := readiness.HasExecutableOrPending(ctx, spaceID, ready.GetSourceViewId(), ready.GetFrequency())
				if readinessErr != nil {
					return fmt.Errorf("check pending factor bindings: %w", readinessErr)
				}
				if waiting {
					return ErrBindingNotReady
				}
			}
		}
		return ErrNoExecutableBinding
	}
	if factorID != "" {
		selected = selectFactorBindings(selected, factorID)
	}

	found, err := r.storage.FactorPeriodComputed(ctx, spaceID, ready.GetSourceViewId(), triggerEventID, ready.GetPeriodTime())
	if err != nil || found {
		return err
	}
	resultDatasetID := selected[0].ResultDatasetID
	for _, binding := range selected[1:] {
		if binding.ResultDatasetID != resultDatasetID {
			return fmt.Errorf("source view %s has multiple result datasets", ready.GetSourceViewId())
		}
	}

	period := time.Unix(ready.GetPeriodTime(), 0).UTC()
	periodEnd, err := domain.NextPeriod(period, ready.GetFrequency())
	if err != nil {
		return err
	}
	triggeredAt := time.Now().UTC()
	groupStatus := ready.GetStatus()
	if groupStatus == "" {
		groupStatus = "complete"
	}
	failedUpstream := failedSubjectSet(ready)
	subjects := sortedUnique(ready.GetPrimarySubjects())

	states := make(map[string]*storagepb.FactorBindingPeriodState, len(selected))
	factors := make(map[string]domain.FactorDef, len(selected))
	for _, binding := range selected {
		factor, loadErr := r.factors.Get(ctx, binding.FactorID)
		if loadErr != nil {
			return fmt.Errorf("load factor %s: %w", binding.FactorID, loadErr)
		}
		factors[binding.BindingID] = *factor
		status := "complete"
		if groupStatus == "degraded" {
			status = "degraded"
		}
		states[binding.BindingID] = &storagepb.FactorBindingPeriodState{
			BindingId: binding.BindingID, FactorId: binding.FactorID, Status: status,
		}
	}

	tasks := make([]taskrunner.Task, 0, len(subjects)*len(selected))
	for _, subjectID := range subjects {
		for _, binding := range selected {
			task, buildErr := taskrunner.BuildTask(taskrunner.TaskScope{
				BindingID: binding.BindingID, TriggerType: "view_ready", SpaceID: spaceID,
				SourceViewID: binding.SourceViewID, ResultDatasetID: binding.ResultDatasetID,
				SubjectID: subjectID, Freq: binding.Freq, PeriodTime: ready.GetPeriodTime(),
				TriggerEventID: triggerEventID, TriggeredAt: triggeredAt, StartTime: period, EndTime: periodEnd,
			}, factors[binding.BindingID], r.factorsDir)
			if buildErr != nil {
				return buildErr
			}
			task.TaskID = taskrunner.DeterministicTaskID(task)
			state := states[binding.BindingID]
			if !domain.BindingAllowsSubject(binding, subjectID) {
				if clearErr := r.clearOutputs(ctx, &task.FactorTask); clearErr != nil {
					return fmt.Errorf("clear excluded subject outputs: %w", clearErr)
				}
				state.SkippedSubjects = append(state.SkippedSubjects, subjectID)
				state.Status = "degraded"
				continue
			}
			if _, failed := failedUpstream[subjectID]; failed {
				if clearErr := r.clearOutputs(ctx, &task.FactorTask); clearErr != nil {
					return fmt.Errorf("clear failed input outputs: %w", clearErr)
				}
				state.FailedSubjects = append(state.FailedSubjects, subjectID)
				state.Status = "degraded"
				continue
			}
			tasks = append(tasks, task)
		}
	}

	results := r.taskRunner.RunAll(ctx, tasks)
	terminal := make(map[string]taskrunner.Result, len(results))
	for _, result := range results {
		terminal[combinationKey(result.Task.BindingID, result.Task.SubjectID)] = result
	}
	for _, task := range tasks {
		result, ok := terminal[combinationKey(task.BindingID, task.SubjectID)]
		if ok && result.Err == nil {
			continue
		}
		runErr := errors.New("task runner returned no terminal result")
		if ok {
			runErr = result.Err
		}
		log.ErrorContextf(ctx, "factor View-ready task failed event_id=%s binding_id=%s subject_id=%s: %v", triggerEventID, task.BindingID, task.SubjectID, runErr)
		if clearErr := r.clearOutputs(ctx, &task.FactorTask); clearErr != nil {
			return fmt.Errorf("clear failed factor outputs: %w", clearErr)
		}
		state := states[task.BindingID]
		state.FailedSubjects = append(state.FailedSubjects, task.SubjectID)
		state.Status = "degraded"
	}

	markerStates := make([]*storagepb.FactorBindingPeriodState, 0, len(selected))
	for _, binding := range selected {
		state := states[binding.BindingID]
		state.SkippedSubjects = sortedUnique(state.SkippedSubjects)
		state.FailedSubjects = sortedUnique(state.FailedSubjects)
		if state.GetStatus() == "degraded" {
			groupStatus = "degraded"
		}
		markerStates = append(markerStates, state)
	}
	if groupStatus == "degraded" {
		r.periodMetrics.ObserveDegraded(ready.GetSourceViewId(), ready.GetFrequency())
	}
	marker := &storagepb.FactorPeriodComputedMarker{
		SourceViewId: ready.GetSourceViewId(), ResultDatasetId: resultDatasetID,
		Frequency: ready.GetFrequency(), PeriodTime: ready.GetPeriodTime(), Status: groupStatus,
		Bindings: markerStates, ComputedAt: timestamppb.Now(), TriggerEventId: triggerEventID,
	}
	return r.storage.ReportFactorPeriodComputed(ctx, spaceID, marker)
}

func selectPeriodBindings(bindings []domain.FactorBinding, spaceID string, ready *publicstoragepb.ViewSourcePeriodReady) []domain.FactorBinding {
	selected := make([]domain.FactorBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.SpaceID != spaceID || binding.SourceViewID != ready.GetSourceViewId() || binding.Freq != ready.GetFrequency() {
			continue
		}
		selected = append(selected, binding)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].BindingID < selected[j].BindingID })
	return selected
}

func containsFactor(bindings []domain.FactorBinding, factorID string) bool {
	for _, binding := range bindings {
		if binding.FactorID == factorID {
			return true
		}
	}
	return false
}

func selectFactorBindings(bindings []domain.FactorBinding, factorID string) []domain.FactorBinding {
	selected := make([]domain.FactorBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.FactorID == factorID {
			selected = append(selected, binding)
		}
	}
	return selected
}

func failedSubjectSet(ready *publicstoragepb.ViewSourcePeriodReady) map[string]struct{} {
	failed := make(map[string]struct{})
	for _, dataset := range ready.GetDatasets() {
		for _, subjectID := range sortedUnique(dataset.GetFailedSubjects()) {
			failed[subjectID] = struct{}{}
		}
	}
	return failed
}

func combinationKey(bindingID, subjectID string) string { return bindingID + "\x00" + subjectID }

func (r *ViewReadyRunner) clearOutputs(ctx context.Context, task *engine.FactorTask) error {
	if err := r.storage.ClearFactorOutputs(ctx, task); err != nil {
		return err
	}
	r.periodMetrics.ObserveManifestClear(task.BindingID)
	return nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
