package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

type TimerPeriodPlan struct {
	Task           domain.CollectionTask
	RunID          string
	RunCutoff      time.Time
	TaskModifyTime time.Time
	Snapshot       domain.PeriodSeriesSnapshot
	Assignment     NodeAssignment
	Request        Request
	Instances      []domain.TaskInstance
	Targets        []domain.WriteTarget
	EnsureStorage  func(context.Context, domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error)
}

type TimerPeriodPlanner struct {
	Snapshots           *store.PeriodSeriesSnapshotRepository
	States              *store.PeriodStorageStateRepository
	Batches             *store.TimerPeriodBatchRepository
	EnsureStorage       func(context.Context, domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error)
	ValidTargetDataTime func(frequency string, target time.Time) bool
	Now                 func() time.Time
}

// Plan freezes the period snapshot before Storage Ensure, then creates the
// initial claimable batch only after Storage confirms the same snapshot and
// its canonical deadline. A duplicate period returns without mutating owner
// membership, even when a later Run supplies different instances or targets.
func (p *TimerPeriodPlanner) Plan(ctx context.Context, plan TimerPeriodPlan) (bool, error) {
	created, err := p.PlanMany(ctx, []TimerPeriodPlan{plan})
	return created > 0, err
}

// PlanMany commits one complete period's group set. Validation and persisted
// request sizing happen before storage side effects; the repository then
// writes every manifest, owner, target, batch, and item in one transaction.
func (p *TimerPeriodPlanner) PlanMany(ctx context.Context, plans []TimerPeriodPlan) (int, error) {
	if p == nil || p.Snapshots == nil || p.States == nil || p.Batches == nil {
		return 0, fmt.Errorf("timer period planner is not initialized")
	}
	if len(plans) == 0 {
		return 0, nil
	}
	firstPlan := plans[0]
	if p.EnsureStorage == nil && firstPlan.EnsureStorage == nil {
		return 0, fmt.Errorf("timer period planner is not initialized")
	}
	if err := validateTimerPeriodPlanSet(plans); err != nil {
		return 0, err
	}
	period := firstPlan.Snapshot.Key.PeriodTime.UTC()
	if p.ValidTargetDataTime != nil && !p.ValidTargetDataTime(firstPlan.Snapshot.Key.Frequency, period) {
		return 0, nil
	}
	snapshot, _, err := p.Snapshots.CreatePeriodSeriesSnapshotIfAbsent(ctx, firstPlan.Snapshot)
	if err != nil {
		return 0, fmt.Errorf("freeze timer period series snapshot: %w", err)
	}
	ensureStorage := firstPlan.EnsureStorage
	if ensureStorage == nil {
		ensureStorage = p.EnsureStorage
	}
	state, err := ensureStorage(ctx, snapshot)
	if err != nil {
		return 0, fmt.Errorf("ensure timer period in Storage: %w", err)
	}
	if err := validateTimerPeriodStorageState(snapshot, state); err != nil {
		return 0, err
	}
	if err := p.States.ObservePeriodStorageState(ctx, state); err != nil {
		return 0, fmt.Errorf("persist timer period Storage state: %w", err)
	}
	afterEnsure := p.currentTime()
	if state.Status != domain.PeriodStatusWaiting || !state.DeadlineAt.After(afterEnsure) {
		return 0, nil
	}

	batchPlans := make([]store.TimerPeriodBatchPlan, 0, len(plans))
	for _, plan := range plans {
		batchPlan, buildErr := buildTimerPeriodBatchPlan(plan, snapshot, state, afterEnsure)
		if buildErr != nil {
			return 0, buildErr
		}
		batchPlans = append(batchPlans, batchPlan)
	}
	// Storage Ensure can consume the remaining dispatch window. Check again
	// immediately before the transaction and timestamp every frozen artifact
	// with the same post-Ensure instant.
	persistAt := p.currentTime()
	if !state.DeadlineAt.After(persistAt) {
		return 0, nil
	}
	for index := range batchPlans {
		batchPlans[index].Manifest.CreateTime = persistAt
		batchPlans[index].Batch.PlannedAt = &persistAt
		batchPlans[index].Batch.CreateTime = persistAt
	}
	created, err := p.Batches.CreateMany(ctx, batchPlans)
	if err != nil {
		return 0, fmt.Errorf("persist timer period batch set: %w", err)
	}
	return created, nil
}

func (p *TimerPeriodPlanner) currentTime() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func buildTimerPeriodBatchPlan(plan TimerPeriodPlan, snapshot domain.PeriodSeriesSnapshot, state domain.PeriodStorageState, now time.Time) (store.TimerPeriodBatchPlan, error) {
	period := snapshot.Key.PeriodTime.UTC()
	periodKey := snapshot.Key
	assignment := plan.Assignment
	shard := plan.Request.ShardIndex
	bindingHash := timerPeriodBindingHash(assignment)
	batchID := stableID(periodKey.SpaceID, periodKey.DatasetID, periodKey.Frequency, period.Format(time.RFC3339Nano), strconv.Itoa(shard), "timer-initial")
	syncPointID := stableID(periodKey.SpaceID, periodKey.DatasetID, periodKey.Frequency, period.Format(time.RFC3339Nano), strconv.Itoa(shard), "timer-sync-point")
	scheduleID := "timer:" + stableID(periodKey.SpaceID, periodKey.DatasetID, periodKey.Frequency, period.Format(time.RFC3339Nano), strconv.Itoa(shard))
	request := plan.Request
	request.BatchID = batchID
	request.SyncPointID = syncPointID
	request.ScheduleID = scheduleID
	request.BatchKind = domain.BatchKindRealtime
	request.SpaceID = periodKey.SpaceID
	request.DatasetID = periodKey.DatasetID
	request.Frequency = periodKey.Frequency
	request.RequirePeriodCommit = true
	for index := range request.Items {
		request.Items[index].RequirePeriodCommit = true
	}
	request.Provider = firstNonEmpty(assignment.Provider, assignment.RouteProvider)
	request.RouteProvider = firstNonEmpty(assignment.RouteProvider, assignment.Provider)
	request.SourceID = assignment.SourceID
	request.MarketID = assignment.MarketID
	request.InstrumentType = assignment.InstrumentType
	request.MarketType = assignment.MarketType
	request.NodeID = assignment.NodeID
	request.Region = assignment.Region
	request.FunctionName = assignment.FunctionName
	request.GroupID = assignment.GroupID
	request.GroupCount = assignment.GroupCount
	request.BindingHash = bindingHash
	request.RouteVersion = assignment.RouteVersion
	request.RequestID = ""
	request.RunID = plan.RunID
	request.Targets = timerPeriodTargets(plan.Targets, request.Items, period, periodKey.Frequency, snapshot)
	if len(request.Targets) == 0 {
		return store.TimerPeriodBatchPlan{}, fmt.Errorf("timer period plan has no frozen write targets")
	}
	if err := request.validate(); err != nil {
		return store.TimerPeriodBatchPlan{}, fmt.Errorf("validate timer period request: %w", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return store.TimerPeriodBatchPlan{}, err
	}
	if len(raw) > 256*1024 {
		return store.TimerPeriodBatchPlan{}, fmt.Errorf("timer period request exceeds maximum persisted size")
	}
	manifest := &domain.TimerPeriodBatch{
		Key:     stableID(periodKey.SpaceID, periodKey.DatasetID, periodKey.Frequency, period.Format(time.RFC3339Nano), strconv.Itoa(shard), "timer-manifest"),
		SpaceID: periodKey.SpaceID, DatasetID: periodKey.DatasetID, Frequency: periodKey.Frequency, PeriodTime: period,
		TaskID: plan.Task.TaskID, FirstRunID: plan.RunID, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		GroupID: uint32(assignment.GroupID), GroupCount: uint32(assignment.GroupCount), ShardIndex: uint32(shard),
		BindingHash: bindingHash, RouteVersion: assignment.RouteVersion, BatchID: batchID, FunctionName: assignment.FunctionName,
		NodeID: assignment.NodeID, Region: assignment.Region, DeadlineAt: state.DeadlineAt.UTC(), CreateTime: now,
	}
	batch := &domain.BatchInvocation{
		SpaceID: periodKey.SpaceID, BatchID: batchID, ScheduleID: scheduleID, BatchKind: domain.BatchKindRealtime,
		ShardIndex: shard, Frequency: periodKey.Frequency, Region: assignment.Region, NodeID: assignment.NodeID,
		FunctionName: assignment.FunctionName, Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw),
		PlannedCount: len(request.Items), PlannedAt: &now, DeadlineAt: timePtr(state.DeadlineAt.UTC()),
	}
	instances := append([]domain.TaskInstance(nil), plan.Instances...)
	taskModifyTime := plan.TaskModifyTime
	if taskModifyTime.IsZero() {
		taskModifyTime = plan.Task.ModifyTime
	}
	requestItemsByID := make(map[string]domain.CollectionItem, len(request.Items))
	for _, item := range request.Items {
		requestItemsByID[item.InstanceID] = item
	}
	snapshotEntriesByIndex := make(map[uint32]domain.PeriodSeriesSnapshotEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		snapshotEntriesByIndex[entry.SeriesIndex] = entry
	}
	for index := range instances {
		item, itemExists := requestItemsByID[instances[index].InstanceID]
		entry, entryExists := snapshotEntriesByIndex[item.SeriesIndex]
		if !itemExists || !entryExists {
			return store.TimerPeriodBatchPlan{}, fmt.Errorf("timer task instance %s has no frozen request or snapshot entry", instances[index].InstanceID)
		}
		logicalItem := item
		logicalItem.Provider = entry.Provider
		logicalItem.SourceID = entry.SourceID
		instances[index].RunID = plan.RunID
		instances[index].FunctionName = assignment.FunctionName
		instances[index].Provider = entry.Provider
		instances[index].SourceID = entry.SourceID
		instances[index].SeriesTag = entry.SeriesTag
		instances[index].RequestKey = sharedCollectionItemKey(logicalItem, periodKey.Frequency, period)
		instances[index].TaskParams = collectionItemRequestParams(item)
	}
	return store.TimerPeriodBatchPlan{Manifest: manifest, Batch: batch, Instances: instances, Targets: request.Targets,
		OwnerRunCutoff: plan.RunCutoff, OwnerTaskModifyTime: taskModifyTime}, nil
}

func validateTimerPeriodPlanSet(plans []TimerPeriodPlan) error {
	if len(plans) == 0 {
		return nil
	}
	first := plans[0]
	series := make(map[uint32]struct{}, first.Snapshot.ExpectedCount)
	groups := make(map[int]struct{}, len(plans))
	for _, plan := range plans {
		if err := validateTimerPeriodPlan(plan); err != nil {
			return err
		}
		if plan.Task.SpaceID != first.Task.SpaceID || plan.Task.TaskID != first.Task.TaskID || plan.RunID != first.RunID ||
			!samePeriodKey(plan.Snapshot.Key, first.Snapshot.Key) || plan.Snapshot.SeriesHash != first.Snapshot.SeriesHash ||
			plan.Snapshot.ExpectedCount != first.Snapshot.ExpectedCount || plan.Assignment.GroupCount != first.Assignment.GroupCount ||
			plan.Assignment.RouteVersion != first.Assignment.RouteVersion || !plan.RunCutoff.Equal(first.RunCutoff) ||
			!plan.TaskModifyTime.Equal(first.TaskModifyTime) {
			return fmt.Errorf("timer period batch set mixes period, owner, or route identity")
		}
		if !samePeriodSeriesEntries(plan.Snapshot.Entries, first.Snapshot.Entries) {
			return fmt.Errorf("timer period batch set mixes frozen series snapshots")
		}
		if _, exists := groups[plan.Assignment.GroupID]; exists {
			return fmt.Errorf("timer period batch set has duplicate group_id %d", plan.Assignment.GroupID)
		}
		groups[plan.Assignment.GroupID] = struct{}{}
		for _, target := range plan.Targets {
			if _, exists := series[target.SeriesIndex]; exists {
				return fmt.Errorf("timer period batch set assigns series_index %d more than once", target.SeriesIndex)
			}
			series[target.SeriesIndex] = struct{}{}
		}
	}
	if len(series) != int(first.Snapshot.ExpectedCount) {
		return fmt.Errorf("timer period batch set does not cover every frozen series exactly once")
	}
	for index := uint32(0); index < first.Snapshot.ExpectedCount; index++ {
		if _, ok := series[index]; !ok {
			return fmt.Errorf("timer period batch set is missing frozen series_index %d", index)
		}
	}
	return nil
}

func samePeriodKey(left, right domain.PeriodKey) bool {
	return left.SpaceID == right.SpaceID && left.DatasetID == right.DatasetID && left.Frequency == right.Frequency && left.PeriodTime.Equal(right.PeriodTime)
}

func samePeriodSeriesEntries(left, right []domain.PeriodSeriesSnapshotEntry) bool {
	if len(left) != len(right) {
		return false
	}
	byIndex := make(map[uint32]domain.PeriodSeriesSnapshotEntry, len(left))
	for _, entry := range left {
		if _, exists := byIndex[entry.SeriesIndex]; exists {
			return false
		}
		byIndex[entry.SeriesIndex] = entry
	}
	for _, entry := range right {
		other, ok := byIndex[entry.SeriesIndex]
		if !ok || entry.SeriesKey != other.SeriesKey || entry.SubjectID != other.SubjectID || entry.Provider != other.Provider ||
			entry.SourceID != other.SourceID || entry.MarketType != other.MarketType || entry.ProviderSymbol != other.ProviderSymbol ||
			entry.SeriesTag != other.SeriesTag || entry.SeriesHash != other.SeriesHash || entry.ExpectedCount != other.ExpectedCount {
			return false
		}
	}
	return true
}

func validateTimerPeriodPlan(plan TimerPeriodPlan) error {
	task := plan.Task
	snapshot := plan.Snapshot
	assignment := plan.Assignment
	if strings.TrimSpace(task.SpaceID) == "" || strings.TrimSpace(task.TaskID) == "" || strings.TrimSpace(plan.RunID) == "" ||
		strings.TrimSpace(snapshot.Key.SpaceID) == "" || snapshot.Key.SpaceID != task.SpaceID || snapshot.Key.DatasetID == "" ||
		snapshot.Key.Frequency == "" || snapshot.Key.PeriodTime.IsZero() || snapshot.ExpectedCount == 0 || strings.TrimSpace(snapshot.SeriesHash) == "" {
		return fmt.Errorf("timer period task, run, or snapshot identity is incomplete")
	}
	if !plan.RunCutoff.IsZero() && ((!task.CreateTime.IsZero() && task.CreateTime.After(plan.RunCutoff)) ||
		(!task.ModifyTime.IsZero() && task.ModifyTime.After(plan.RunCutoff)) ||
		(!plan.TaskModifyTime.IsZero() && plan.TaskModifyTime.After(plan.RunCutoff))) {
		return fmt.Errorf("timer period owner task is newer than its Run cutoff")
	}
	if assignment.GroupID < 0 || assignment.GroupCount <= 0 || assignment.GroupID >= assignment.GroupCount ||
		strings.TrimSpace(assignment.NodeID) == "" || strings.TrimSpace(assignment.FunctionName) == "" || strings.TrimSpace(assignment.Region) == "" ||
		strings.TrimSpace(assignment.RouteVersion) == "" || strings.TrimSpace(assignment.DatasetID) != snapshot.Key.DatasetID || strings.TrimSpace(assignment.Frequency) != snapshot.Key.Frequency {
		return fmt.Errorf("timer period assignment identity is incomplete or does not match the snapshot")
	}
	if len(plan.Request.Items) == 0 || len(plan.Request.Items) > MaxRealtimeItems || len(plan.Instances) != len(plan.Request.Items) || len(plan.Targets) == 0 {
		return fmt.Errorf("timer period batch membership is empty, exceeds item limit, or lacks instances/targets")
	}
	if plan.Request.ShardIndex < 0 {
		return fmt.Errorf("timer period shard index must not be negative")
	}
	if len(plan.Snapshot.Entries) != int(plan.Snapshot.ExpectedCount) {
		return fmt.Errorf("timer period snapshot entries do not match expected_count")
	}
	entries := make(map[uint32]domain.PeriodSeriesSnapshotEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.SeriesIndex] = entry
	}
	seen := make(map[string]struct{}, len(plan.Request.Items))
	for _, item := range plan.Request.Items {
		entry, ok := entries[item.SeriesIndex]
		if !ok || entry.SubjectID != item.SubjectID || entry.SeriesHash != snapshot.SeriesHash || uint32(entry.ExpectedCount) != snapshot.ExpectedCount ||
			item.SeriesHash != snapshot.SeriesHash || item.ExpectedCount != snapshot.ExpectedCount || item.DatasetID != snapshot.Key.DatasetID ||
			item.Frequency != snapshot.Key.Frequency || item.TargetDataTime == "" || strings.TrimSpace(item.InstanceID) == "" {
			return fmt.Errorf("timer request item does not match the frozen period snapshot")
		}
		itemPeriod, err := time.Parse(time.RFC3339Nano, item.TargetDataTime)
		if err != nil || !itemPeriod.UTC().Equal(snapshot.Key.PeriodTime.UTC()) {
			return fmt.Errorf("timer request item target_data_time does not match period")
		}
		if item.Provider != assignment.Provider && item.Provider != assignment.RouteProvider || item.SourceID != assignment.SourceID || item.MarketType != assignment.MarketType {
			return fmt.Errorf("timer request item crosses its static Provider/Source binding")
		}
		if _, ok := seen[item.InstanceID]; ok {
			return fmt.Errorf("timer request contains duplicate instance_id")
		}
		seen[item.InstanceID] = struct{}{}
	}
	instanceIDs := make(map[string]struct{}, len(plan.Instances))
	for _, instance := range plan.Instances {
		if instance.SpaceID != task.SpaceID || instance.RunID != plan.RunID || instance.InstanceID == "" {
			return fmt.Errorf("timer instance is not owned by the initial Run")
		}
		instanceIDs[instance.InstanceID] = struct{}{}
	}
	for instanceID := range seen {
		if _, ok := instanceIDs[instanceID]; !ok {
			return fmt.Errorf("timer instance membership does not match request items")
		}
	}
	for _, target := range plan.Targets {
		if target.SpaceID != task.SpaceID || target.TaskID != task.TaskID || target.DatasetID != snapshot.Key.DatasetID || target.InstanceID == "" {
			return fmt.Errorf("timer write target does not match task and Dataset owner")
		}
		if _, ok := seen[target.InstanceID]; !ok {
			return fmt.Errorf("timer write target is not attached to a request item")
		}
	}
	return nil
}

func validateTimerPeriodStorageState(snapshot domain.PeriodSeriesSnapshot, state domain.PeriodStorageState) error {
	if state.Key.SpaceID != snapshot.Key.SpaceID || state.Key.DatasetID != snapshot.Key.DatasetID || state.Key.Frequency != snapshot.Key.Frequency ||
		!state.Key.PeriodTime.Equal(snapshot.Key.PeriodTime) || state.SeriesHash != snapshot.SeriesHash || state.ExpectedCount != snapshot.ExpectedCount ||
		state.DeadlineAt.IsZero() || state.ConfirmedAt.IsZero() || (state.Status != domain.PeriodStatusWaiting && state.Status != domain.PeriodStatusComplete && state.Status != domain.PeriodStatusDegraded) {
		return fmt.Errorf("Storage returned period state that does not match the frozen snapshot")
	}
	return nil
}

func timerPeriodTargets(input []domain.WriteTarget, items []domain.CollectionItem, period time.Time, frequency string, snapshot domain.PeriodSeriesSnapshot) []domain.WriteTarget {
	itemByID := make(map[string]domain.CollectionItem, len(items))
	for _, item := range items {
		itemByID[item.InstanceID] = item
	}
	targets := append([]domain.WriteTarget(nil), input...)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].InstanceID != targets[j].InstanceID {
			return targets[i].InstanceID < targets[j].InstanceID
		}
		return targets[i].TaskID < targets[j].TaskID
	})
	for i := range targets {
		item, ok := itemByID[targets[i].InstanceID]
		if !ok {
			return nil
		}
		targets[i].Frequency = frequency
		targets[i].TargetDataTime = period.UTC().Format(time.RFC3339Nano)
		targets[i].SeriesIndex = item.SeriesIndex
		targets[i].SeriesHash = snapshot.SeriesHash
		targets[i].ExpectedCount = snapshot.ExpectedCount
	}
	return targets
}

func timerPeriodBindingHash(assignment NodeAssignment) string {
	routeProvider := strings.ToLower(strings.TrimSpace(firstNonEmpty(assignment.RouteProvider, assignment.Provider)))
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(assignment.Provider, routeProvider)))
	return AssignmentHash(
		routeProvider, provider,
		strings.ToLower(strings.TrimSpace(assignment.SourceID)), strings.TrimSpace(assignment.RouteVersion),
		strconv.Itoa(assignment.GroupID), strconv.Itoa(assignment.GroupCount), strings.ToLower(strings.TrimSpace(assignment.MarketType)),
		strings.ToLower(strings.TrimSpace(assignment.MarketID)), strings.ToLower(strings.TrimSpace(assignment.InstrumentType)),
		strings.TrimSpace(assignment.DatasetID), strings.TrimSpace(assignment.Frequency), strings.Join(assignment.OutputFields, ","),
		strings.TrimSpace(assignment.NodeID), strings.TrimSpace(assignment.FunctionName), strings.TrimSpace(assignment.Region),
	)
}
