package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

var ErrNoExecutableBinding = errors.New("no executable factor binding for source View period")
var ErrBindingNotReady = errors.New("factor binding is not ready for source View period")
var ErrMissingBindingInputs = errors.New("source view is missing required factor inputs")

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

type PeriodStorageRevision interface {
	ActiveViewRevision(context.Context, string, string, string, string) (uint64, error)
}

type PeriodStorageRevisionAt interface {
	ActiveViewRevisionAt(context.Context, string, string, string, string, string) (uint64, error)
}

type ViewColumnSource interface {
	Columns(context.Context, string, string) ([]string, error)
}

type ViewReadyRunner struct {
	bindings             PeriodBindingSource
	factors              PeriodFactorSource
	taskRunner           CombinationTaskRunner
	storage              PeriodStorage
	viewColumns          ViewColumnSource
	factorsDir           string
	operationGate        *taskrunner.OperationGate
	periodMetrics        *observability.PeriodMetrics
	executionUnitTimeout time.Duration
	executionParallelism int
	batchExecution       bool
	barrier              *PeriodBarrier
	catalog              *store.Store
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

func WithExecutionUnitTimeout(timeout time.Duration) Option {
	return func(r *ViewReadyRunner) {
		if timeout > 0 {
			r.executionUnitTimeout = timeout
		}
	}
}

func WithExecutionParallelism(workers int) Option {
	return func(r *ViewReadyRunner) {
		if workers > 0 {
			r.executionParallelism = workers
		}
	}
}

func WithBatchExecution(enabled bool) Option {
	return func(r *ViewReadyRunner) { r.batchExecution = enabled }
}

func WithViewColumns(source ViewColumnSource) Option {
	return func(r *ViewReadyRunner) { r.viewColumns = source }
}

func WithPeriodBarrier(barrier *PeriodBarrier) Option {
	return func(r *ViewReadyRunner) { r.barrier = barrier }
}

func WithCatalogStore(db *store.Store) Option {
	return func(r *ViewReadyRunner) { r.catalog = db }
}

func NewViewReadyRunner(bindings PeriodBindingSource, factors PeriodFactorSource, runner CombinationTaskRunner, storage PeriodStorage, factorsDir string, opts ...Option) *ViewReadyRunner {
	r := &ViewReadyRunner{
		bindings: bindings, factors: factors, taskRunner: runner, storage: storage,
		factorsDir: factorsDir, operationGate: taskrunner.NewOperationGate(), executionUnitTimeout: 30 * time.Second, executionParallelism: 1,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// ExecutionBudget returns a conservative upper bound for one source period.
// Factor batches execute each selected factor sequentially per subject, so a
// fixed outer deadline must scale with the number of executable bindings.
func (r *ViewReadyRunner) ExecutionBudget(ctx context.Context, spaceID string, ready *publicstoragepb.ViewDataReady) (time.Duration, error) {
	if r == nil || r.bindings == nil || ready == nil {
		return 0, fmt.Errorf("factor execution budget inputs are required")
	}
	if !acceptsCrossSectionReady("", ready) {
		return 0, nil
	}
	bindings, err := r.bindings.ListExecutable(ctx)
	if err != nil {
		return 0, fmt.Errorf("list executable factor bindings for budget: %w", err)
	}
	selected := selectPeriodBindings(bindings, spaceID, ready)
	unit := r.executionUnitTimeout
	if unit <= 0 {
		unit = 30 * time.Second
	}
	// Every subject-factor pair can consume one full execution unit. Account for
	// the worker pool so a large universe cannot be cancelled by a budget that
	// only considered the number of factor definitions.
	subjectCount := max(1, len(periodSubjectUniverse(selected, ready)))
	executionUnits := len(selected) * subjectCount
	if r.batchExecution {
		// The task runner groups every selected factor for one subject into one
		// Python batch. The worker's legal batch timeout is one unit per factor,
		// while batches themselves run in subject waves across the worker pool.
		waves := (subjectCount + max(1, r.executionParallelism) - 1) / max(1, r.executionParallelism)
		executionUnits = waves * max(1, len(selected))
		return time.Duration(executionUnits)*unit + 2*time.Minute, nil
	}
	executionUnits = max(1, executionUnits)
	parallelism := max(1, r.executionParallelism)
	waves := (executionUnits + parallelism - 1) / parallelism
	// Storage reads, marker writes and scheduler overhead get a fixed margin.
	return time.Duration(waves)*unit + 2*time.Minute, nil
}

func (r *ViewReadyRunner) Execute(ctx context.Context, spaceID, triggerEventID string, ready *publicstoragepb.ViewDataReady) error {
	err := r.ExecuteSelected(ctx, spaceID, triggerEventID, "", ready)
	if errors.Is(err, ErrNoExecutableBinding) {
		return nil
	}
	return err
}

func (r *ViewReadyRunner) ExecuteSelected(ctx context.Context, spaceID, triggerEventID, factorID string, ready *publicstoragepb.ViewDataReady) error {
	return r.executeSelected(ctx, spaceID, triggerEventID, factorID, ready, true)
}

func (r *ViewReadyRunner) ExecuteSelectedWithGate(ctx context.Context, spaceID, triggerEventID, factorID string, ready *publicstoragepb.ViewDataReady) error {
	return r.executeSelected(ctx, spaceID, triggerEventID, factorID, ready, false)
}

func (r *ViewReadyRunner) executeSelected(ctx context.Context, spaceID, triggerEventID, factorID string, ready *publicstoragepb.ViewDataReady, acquireGate bool) error {
	if r == nil || r.bindings == nil || r.factors == nil || r.taskRunner == nil || r.storage == nil {
		return fmt.Errorf("factor View-ready runner dependencies are required")
	}
	if ready == nil || spaceID == "" || triggerEventID == "" || ready.GetViewId() == "" || ready.GetFrequency() == "" || ready.GetPeriodTime() <= 0 {
		return fmt.Errorf("source-ready event identity is incomplete")
	}
	if !acceptsCrossSectionReady(triggerEventID, ready) {
		return nil
	}
	if acquireGate {
		releaseOperation, gateErr := r.operationGate.AcquireContext(ctx)
		if gateErr != nil {
			return gateErr
		}
		defer releaseOperation()
	}

	started := time.Now()
	log.InfoContextf(ctx, "factor View-ready execution start event_id=%s space_id=%s view_id=%s period=%d", triggerEventID, spaceID, ready.GetViewId(), ready.GetPeriodTime())
	r.periodMetrics.Begin(ready.GetViewId(), ready.GetFrequency())
	defer r.periodMetrics.End(ready.GetViewId(), ready.GetFrequency())
	if !strings.HasPrefix(triggerEventID, "recalc-") && ready.GetReadyAt() != nil && ready.GetReadyAt().IsValid() {
		r.periodMetrics.ObserveSourceReady(ready.GetViewId(), ready.GetFrequency(), ready.GetReadyAt().AsTime())
	}

	bindings, err := r.bindings.ListExecutable(ctx)
	if err != nil {
		return fmt.Errorf("list executable factor bindings: %w", err)
	}
	selected := selectPeriodBindings(bindings, spaceID, ready)
	if err := r.freezePeriod(ctx, spaceID, triggerEventID, ready, mergePeriodBindings(selected, selectDatasetBindings(bindings, spaceID, ready.GetDatasetId()))); err != nil {
		return err
	}
	if len(selected) == 0 || (factorID != "" && !containsFactor(selected, factorID)) {
		if factorID == "" && len(selected) == 0 {
			if readiness, ok := r.bindings.(PeriodBindingReadinessSource); ok {
				waiting, readinessErr := readiness.HasExecutableOrPending(ctx, spaceID, ready.GetViewId(), ready.GetFrequency())
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
	states := make(map[string]*storagepb.FactorBindingPeriodState, len(selected))
	factors := make(map[string]domain.FactorDef, len(selected))
	cross := make([]domain.FactorBinding, 0, len(selected))
	for _, binding := range selected {
		factor, loadErr := r.factors.Get(ctx, binding.FactorID)
		if loadErr != nil {
			return fmt.Errorf("load factor %s: %w", binding.FactorID, loadErr)
		}
		if factor.FactorType != domain.FactorTypeCrossSection {
			continue
		}
		factors[binding.BindingID] = *factor
		cross = append(cross, binding)
	}
	selected = cross
	if len(selected) == 0 || (factorID != "" && !containsFactor(selected, factorID)) {
		return ErrNoExecutableBinding
	}
	resultDatasetID := selected[0].ResultDatasetID
	for _, binding := range selected[1:] {
		if binding.ResultDatasetID != resultDatasetID {
			return fmt.Errorf("source view %s has multiple result datasets", ready.GetViewId())
		}
	}
	found, err := r.storage.FactorPeriodComputed(ctx, spaceID, resultDatasetID, triggerEventID, ready.GetPeriodTime())
	if err != nil || found {
		return err
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
	subjects := periodSubjectUniverse(selected, ready)
	viewColumns, err := r.loadViewColumns(ctx, spaceID, ready.GetViewId())
	if err != nil {
		return err
	}

	for _, binding := range selected {
		status := "complete"
		if groupStatus == "degraded" {
			status = "degraded"
		}
		states[binding.BindingID] = &storagepb.FactorBindingPeriodState{
			BindingId: binding.BindingID, FactorId: binding.FactorID, Status: status,
			SourceHash: factors[binding.BindingID].SourceHash,
		}
	}
	expectedActiveIndexRevision := uint64(0)
	if strings.HasPrefix(triggerEventID, "recalc-") {
		var revision uint64
		var revisionErr error
		if revisionReader, ok := r.storage.(PeriodStorageRevisionAt); ok {
			revision, revisionErr = revisionReader.ActiveViewRevisionAt(ctx, spaceID, ready.GetViewId(), firstSubject(subjects), ready.GetFrequency(), "")
		} else if _, ok := r.storage.(PeriodStorageRevision); ok {
			return fmt.Errorf("recalc requires fenced storage View revision reader")
		} else {
			return fmt.Errorf("recalc requires storage View revision reader")
		}
		if revisionErr != nil {
			return fmt.Errorf("resolve source View revision for recalc: %w", revisionErr)
		}
		if revision == 0 {
			return fmt.Errorf("source View %s has no active revision", ready.GetViewId())
		}
		expectedActiveIndexRevision = revision
	}

	tasks := make([]taskrunner.Task, 0, len(selected))
	for _, binding := range selected {
		factor := factors[binding.BindingID]
		state := states[binding.BindingID]
		if missing := missingViewInputs(viewColumns, factor.InputColumns); len(missing) > 0 {
			return fmt.Errorf("%w: %s", ErrMissingBindingInputs, strings.Join(missing, ","))
		}
		allowed, skipped := partitionBindingSubjects(binding, subjects)
		state.SkippedSubjects = append(state.SkippedSubjects, skipped...)
		available, missing := splitAvailableSubjects(allowed, failedUpstream)
		allowDegraded := domain.FactorAllowsDegraded(factor)
		if groupStatus == "degraded" && !allowDegraded {
			state.SkippedSubjects = append(state.SkippedSubjects, allowed...)
			state.Status = "degraded"
			if clearErr := r.clearPanelOutputs(ctx, spaceID, triggerEventID, triggeredAt, period, periodEnd, expectedActiveIndexRevision, ready, binding, factor, allowed); clearErr != nil {
				return clearErr
			}
			continue
		}
		if len(available) == 0 {
			state.SkippedSubjects = append(state.SkippedSubjects, allowed...)
			state.Status = "degraded"
			if clearErr := r.clearPanelOutputs(ctx, spaceID, triggerEventID, triggeredAt, period, periodEnd, expectedActiveIndexRevision, ready, binding, factor, allowed); clearErr != nil {
				return clearErr
			}
			continue
		}
		if len(missing) > 0 {
			state.FailedSubjects = append(state.FailedSubjects, missing...)
			state.Status = "degraded"
			if clearErr := r.clearPanelOutputs(ctx, spaceID, triggerEventID, triggeredAt, period, periodEnd, expectedActiveIndexRevision, ready, binding, factor, missing); clearErr != nil {
				return clearErr
			}
		}
		inputStatus := groupStatus
		if inputStatus == "" {
			inputStatus = "complete"
		}
		task, buildErr := taskrunner.BuildTask(taskrunner.TaskScope{
			BindingID: binding.BindingID, TriggerType: "view_ready", SpaceID: spaceID,
			BindingGeneration: binding.BindingGeneration,
			SourceViewID:      binding.SourceViewID, ResultDatasetID: binding.ResultDatasetID,
			Freq: binding.Freq, PeriodTime: ready.GetPeriodTime(),
			TriggerEventID: triggerEventID, TriggeredAt: triggeredAt, StartTime: period, EndTime: periodEnd,
			ExpectedActiveIndexRevision: expectedActiveIndexRevision,
			ConfigSnapshotID:            firstNonEmpty(ready.GetViewConfigId()),
			ExpectedSubjects:            append([]string(nil), allowed...),
			AvailableSubjects:           append([]string(nil), available...),
			MissingSubjects:             append([]string(nil), missing...),
			InputStatus:                 inputStatus,
		}, factor, r.factorsDir)
		if buildErr != nil {
			return buildErr
		}
		task.TaskID = taskrunner.DeterministicTaskID(task)
		tasks = append(tasks, task)
	}

	batchCount := uniqueTaskSubjects(tasks)
	r.periodMetrics.BeginBatches(ready.GetViewId(), ready.GetFrequency(), batchCount)
	var results []taskrunner.Result
	defer func() {
		bySubject := make(map[string]struct {
			factors int
			failed  bool
		}, batchCount)
		for index, task := range tasks {
			state := bySubject[task.SubjectID]
			state.factors++
			if index >= len(results) || results[index].Err != nil {
				state.failed = true
			}
			bySubject[task.SubjectID] = state
		}
		for _, state := range bySubject {
			status := "complete"
			if state.failed {
				status = "degraded"
			}
			r.periodMetrics.ObserveBatch(ready.GetViewId(), ready.GetFrequency(), status, 1, state.factors, time.Since(started))
		}
	}()
	results = r.taskRunner.RunAll(ctx, tasks)
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
		if task.SubjectID != "" {
			state.FailedSubjects = append(state.FailedSubjects, task.SubjectID)
		} else {
			state.FailedSubjects = append(state.FailedSubjects, task.AvailableSubjects...)
		}
		state.Status = "degraded"
	}
	if strings.HasPrefix(triggerEventID, "recalc-") {
		for _, task := range tasks {
			if result, ok := terminal[combinationKey(task.BindingID, task.SubjectID)]; !ok || result.Err != nil {
				if ok {
					return fmt.Errorf("recalc factor task failed: %w", result.Err)
				}
				return fmt.Errorf("recalc factor task returned no terminal result")
			}
		}
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
		r.periodMetrics.ObserveDegraded(ready.GetViewId(), ready.GetFrequency())
	}
	if err := r.recordCrossSectionOutcomes(ctx, spaceID, triggerEventID, ready, selected, subjects, failedUpstream, tasks, terminal); err != nil {
		return err
	}
	if r.barrier != nil && !strings.HasPrefix(triggerEventID, "recalc-") {
		log.InfoContextf(ctx, "factor_view_ready_done event_id=%s space_id=%s source_view_id=%s result_dataset_id=%s freq=%s period_time=%d binding_count=%d task_count=%d batch_count=%d subject_count=%d status=%s elapsed_ms=%d",
			triggerEventID, spaceID, ready.GetViewId(), resultDatasetID, ready.GetFrequency(), ready.GetPeriodTime(), len(selected), len(tasks), batchCount, len(subjects), groupStatus, time.Since(started).Milliseconds())
		return nil
	}
	marker := &storagepb.FactorPeriodComputedMarker{
		DatasetId: resultDatasetID, Frequency: ready.GetFrequency(), PeriodTime: ready.GetPeriodTime(), Status: groupStatus,
		BatchId: triggerEventID, ConfigSnapshotId: firstNonEmpty(ready.GetViewConfigId(), "factor"),
		ExpectedScopeRef:   firstNonEmpty(ready.GetVisibleScope(), fmt.Sprintf("%s:%s:%d", resultDatasetID, ready.GetFrequency(), ready.GetPeriodTime())),
		ExpectedSubjectIds: append([]string(nil), subjects...), Bindings: markerStates,
		CommittedPositions: cloneEventPositions(ready.GetCommittedPositions()),
		ComputedAt:         timestamppb.Now(), TriggerEventId: triggerEventID,
	}
	if err := r.storage.ReportFactorPeriodComputed(ctx, spaceID, marker); err != nil {
		log.ErrorContextf(ctx, "factor_view_ready_report_failed event_id=%s space_id=%s source_view_id=%s result_dataset_id=%s freq=%s period_time=%d binding_count=%d subject_count=%d error=%q",
			triggerEventID, spaceID, ready.GetViewId(), resultDatasetID, ready.GetFrequency(), ready.GetPeriodTime(), len(selected), len(subjects), err.Error())
		return err
	}
	log.InfoContextf(ctx, "factor_view_ready_done event_id=%s space_id=%s source_view_id=%s result_dataset_id=%s freq=%s period_time=%d binding_count=%d task_count=%d batch_count=%d subject_count=%d status=%s elapsed_ms=%d",
		triggerEventID, spaceID, ready.GetViewId(), resultDatasetID, ready.GetFrequency(), ready.GetPeriodTime(), len(selected), len(tasks), batchCount, len(subjects), groupStatus, time.Since(started).Milliseconds())
	return nil
}

func uniqueTaskSubjects(tasks []taskrunner.Task) int {
	seen := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if subjectID := strings.TrimSpace(task.SubjectID); subjectID != "" {
			seen[subjectID] = struct{}{}
			continue
		}
		for _, subjectID := range task.ExpectedSubjects {
			if subjectID = strings.TrimSpace(subjectID); subjectID != "" {
				seen[subjectID] = struct{}{}
			}
		}
	}
	return len(seen)
}

func acceptsCrossSectionReady(triggerEventID string, ready *publicstoragepb.ViewDataReady) bool {
	if strings.HasPrefix(triggerEventID, "recalc-") {
		return true
	}
	return ready != nil && ready.GetCompletionKind() == events.MergePeriodCompleted.Name()
}

func (r *ViewReadyRunner) loadViewColumns(ctx context.Context, spaceID, viewID string) ([]string, error) {
	if r == nil || r.viewColumns == nil {
		return nil, nil
	}
	columns, err := r.viewColumns.Columns(ctx, spaceID, viewID)
	if err != nil {
		return nil, fmt.Errorf("load source view columns: %w", err)
	}
	return columns, nil
}

func missingViewInputs(columns, inputs []string) []string {
	if columns == nil {
		return nil
	}
	available := make(map[string]struct{}, len(columns)*2)
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if column == "" {
			continue
		}
		available[column] = struct{}{}
		if _, suffix, ok := strings.Cut(column, "."); ok {
			available[suffix] = struct{}{}
		}
		if _, suffix, ok := strings.Cut(column, "__"); ok {
			available[suffix] = struct{}{}
		}
	}
	var missing []string
	for _, input := range inputs {
		if _, ok := available[input]; !ok {
			missing = append(missing, input)
		}
	}
	sort.Strings(missing)
	return missing
}

func partitionBindingSubjects(binding domain.FactorBinding, subjects []string) (allowed, skipped []string) {
	for _, subjectID := range subjects {
		if domain.BindingAllowsSubject(binding, subjectID) {
			allowed = append(allowed, subjectID)
			continue
		}
		skipped = append(skipped, subjectID)
	}
	return allowed, skipped
}

func splitAvailableSubjects(subjects []string, failed map[string]struct{}) (available, missing []string) {
	for _, subjectID := range subjects {
		if _, ok := failed[subjectID]; ok {
			missing = append(missing, subjectID)
			continue
		}
		available = append(available, subjectID)
	}
	return available, missing
}

func (r *ViewReadyRunner) clearPanelOutputs(
	ctx context.Context,
	spaceID, triggerEventID string,
	triggeredAt, period, periodEnd time.Time,
	revision uint64,
	ready *publicstoragepb.ViewDataReady,
	binding domain.FactorBinding,
	factor domain.FactorDef,
	subjects []string,
) error {
	for _, subjectID := range subjects {
		task, err := taskrunner.BuildTask(taskrunner.TaskScope{
			BindingID: binding.BindingID, TriggerType: "view_ready", SpaceID: spaceID,
			BindingGeneration: binding.BindingGeneration,
			SourceViewID:      binding.SourceViewID, ResultDatasetID: binding.ResultDatasetID,
			SubjectID: subjectID, Freq: binding.Freq, PeriodTime: ready.GetPeriodTime(),
			TriggerEventID: triggerEventID, TriggeredAt: triggeredAt, StartTime: period, EndTime: periodEnd,
			ExpectedActiveIndexRevision: revision,
		}, factor, r.factorsDir)
		if err != nil {
			return err
		}
		if clearErr := r.clearOutputs(ctx, &task.FactorTask); clearErr != nil {
			return fmt.Errorf("clear factor outputs: %w", clearErr)
		}
	}
	return nil
}

func selectPeriodBindings(bindings []domain.FactorBinding, spaceID string, ready *publicstoragepb.ViewDataReady) []domain.FactorBinding {
	selected := make([]domain.FactorBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.SpaceID != spaceID || binding.SourceViewID != ready.GetViewId() || binding.Freq != ready.GetFrequency() {
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

func failedSubjectSet(ready *publicstoragepb.ViewDataReady) map[string]struct{} {
	failed := make(map[string]struct{})
	for _, subjectID := range splitScopeRef(ready.GetFailedScopeRef()) {
		failed[subjectID] = struct{}{}
	}
	return failed
}

func periodSubjectUniverse(bindings []domain.FactorBinding, ready *publicstoragepb.ViewDataReady) []string {
	if ready != nil {
		if subjectID := strings.TrimSpace(strings.TrimPrefix(ready.GetVisibleScope(), "subject:")); strings.HasPrefix(ready.GetVisibleScope(), "subject:") && subjectID != "" {
			return []string{subjectID}
		}
	}
	var subjects []string
	for _, binding := range bindings {
		if binding.SubjectMode != domain.SubjectModeInclude {
			continue
		}
		var listed []string
		if err := json.Unmarshal([]byte(binding.SubjectsJSON), &listed); err != nil {
			continue
		}
		subjects = append(subjects, listed...)
	}
	if ready != nil {
		subjects = append(subjects, splitScopeRef(ready.GetFailedScopeRef())...)
	}
	return sortedUnique(subjects)
}

func splitScopeRef(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" && part != "degraded" {
			out = append(out, part)
		}
	}
	return out
}

func cloneEventPositions(values []*publicstoragepb.CommittedPosition) []*storagepb.CommittedPosition {
	out := make([]*storagepb.CommittedPosition, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		out = append(out, &storagepb.CommittedPosition{NodeId: value.GetNodeId(), StoreId: value.GetStoreId(), Sequence: value.GetSequence()})
	}
	if len(out) == 0 {
		out = append(out, &storagepb.CommittedPosition{NodeId: "factor", StoreId: "local", Sequence: 1})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

func firstSubject(values []string) string {
	values = sortedUnique(values)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func mergePeriodBindings(parts ...[]domain.FactorBinding) []domain.FactorBinding {
	seen := make(map[string]struct{})
	out := make([]domain.FactorBinding, 0)
	for _, part := range parts {
		for _, binding := range part {
			if _, ok := seen[binding.BindingID]; ok {
				continue
			}
			seen[binding.BindingID] = struct{}{}
			out = append(out, binding)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BindingID < out[j].BindingID })
	return out
}

func (r *ViewReadyRunner) freezePeriod(ctx context.Context, spaceID, triggerEventID string, ready *publicstoragepb.ViewDataReady, bindings []domain.FactorBinding) error {
	if r == nil || r.barrier == nil || ready == nil || strings.HasPrefix(triggerEventID, "recalc-") {
		return nil
	}
	datasetID := strings.TrimSpace(ready.GetDatasetId())
	if datasetID == "" && len(bindings) > 0 {
		datasetID = bindingDatasetID(bindings[0])
	}
	if datasetID == "" {
		return nil
	}
	snapshotID := r.configSnapshotID(ctx, datasetID)
	if snapshotID == "" {
		snapshotID = firstNonEmpty(ready.GetViewConfigId(), "factor")
	}
	frozen := make([]FrozenBinding, 0, len(bindings))
	subjects := periodSubjectUniverse(bindings, ready)
	for _, binding := range bindings {
		factor, err := r.factors.Get(ctx, binding.FactorID)
		if err != nil || factor == nil {
			continue
		}
		allowed, _ := partitionBindingSubjects(binding, subjects)
		if len(allowed) == 0 {
			allowed = append([]string(nil), subjects...)
		}
		frozen = append(frozen, FrozenBinding{
			BindingID: binding.BindingID, FactorID: binding.FactorID, FactorType: factor.FactorType,
			SourceHash: factor.SourceHash, Subjects: allowed,
		})
	}
	return r.barrier.Freeze(ctx, FreezeSpec{
		Key: PeriodKey{
			SpaceID: spaceID, DatasetID: datasetID, SnapshotID: snapshotID,
			Frequency: ready.GetFrequency(), PeriodTime: ready.GetPeriodTime(),
		},
		ExpectedSubjects: subjects,
		FailedSubjects:   splitScopeRef(ready.GetFailedScopeRef()),
		Bindings:         frozen,
		BatchID:          firstNonEmpty(ready.GetCompletionEventId(), triggerEventID),
		ScopeRef:         firstNonEmpty(ready.GetVisibleScope(), fmt.Sprintf("%s:%s:%d", datasetID, ready.GetFrequency(), ready.GetPeriodTime())),
	})
}

func (r *ViewReadyRunner) recordCrossSectionOutcomes(ctx context.Context, spaceID, triggerEventID string, ready *publicstoragepb.ViewDataReady, selected []domain.FactorBinding, subjects []string, failedUpstream map[string]struct{}, tasks []taskrunner.Task, terminal map[string]taskrunner.Result) error {
	if r == nil || r.barrier == nil || ready == nil || strings.HasPrefix(triggerEventID, "recalc-") {
		return nil
	}
	datasetID := strings.TrimSpace(ready.GetDatasetId())
	snapshotID := r.configSnapshotID(ctx, datasetID)
	if snapshotID == "" {
		snapshotID = firstNonEmpty(ready.GetViewConfigId(), "factor")
	}
	key := PeriodKey{
		SpaceID: spaceID, DatasetID: datasetID, SnapshotID: snapshotID,
		Frequency: ready.GetFrequency(), PeriodTime: ready.GetPeriodTime(),
	}
	ran := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		ran[task.BindingID] = struct{}{}
		result, ok := terminal[combinationKey(task.BindingID, task.SubjectID)]
		status := PairComplete
		if !ok || result.Err != nil {
			status = PairFailed
		}
		ids := task.AvailableSubjects
		if task.SubjectID != "" {
			ids = []string{task.SubjectID}
		}
		for _, subjectID := range ids {
			outcome := PairOutcome{BindingID: task.BindingID, SubjectID: subjectID, Status: status}
			if status == PairComplete {
				if patch, ok := collapseFactorWrite(result.Write); ok {
					outcome.Receipt = patch
				}
			}
			if err := r.barrier.Record(ctx, key, outcome); err != nil {
				return err
			}
		}
		for _, subjectID := range task.MissingSubjects {
			if err := r.barrier.Record(ctx, key, PairOutcome{BindingID: task.BindingID, SubjectID: subjectID, Status: PairMissingInput}); err != nil {
				return err
			}
		}
	}
	for _, binding := range selected {
		if _, ok := ran[binding.BindingID]; ok {
			continue
		}
		allowed, skipped := partitionBindingSubjects(binding, subjects)
		for _, subjectID := range skipped {
			if err := r.barrier.Record(ctx, key, PairOutcome{BindingID: binding.BindingID, SubjectID: subjectID, Status: PairSkipped}); err != nil {
				return err
			}
		}
		for _, subjectID := range allowed {
			status := PairSkipped
			if _, failed := failedUpstream[subjectID]; failed {
				status = PairMissingInput
			}
			if err := r.barrier.Record(ctx, key, PairOutcome{BindingID: binding.BindingID, SubjectID: subjectID, Status: status}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *ViewReadyRunner) configSnapshotID(ctx context.Context, datasetID string) string {
	if r == nil || r.catalog == nil || r.catalog.MergedDatasets() == nil || strings.TrimSpace(datasetID) == "" {
		return ""
	}
	def, err := r.catalog.MergedDatasets().Get(ctx, datasetID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(def.ConfigSnapshotID)
}
