package marketfetch

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

const maxTimerBatchResponseBytes = 256 * 1024

type TimerBatchClaimer struct {
	Batches           *store.TimerPeriodBatchRepository
	CompletionTimeout time.Duration
}

type TimerBatchClaimResponse struct {
	Claimed          bool
	RequestJSON      []byte
	PeriodDeadlineAt time.Time
	BatchID          string
}

// Claim exposes only the frozen JSON saved with the manifest. Caller input
// contains routing identity and a request token, never items or targets.
func (c *TimerBatchClaimer) Claim(ctx context.Context, input store.TimerPeriodBatchClaimInput) (TimerBatchClaimResponse, error) {
	if c == nil || c.Batches == nil {
		return TimerBatchClaimResponse{}, fmt.Errorf("timer batch claimer is not initialized")
	}
	completionTimeout := c.CompletionTimeout
	if completionTimeout <= 0 {
		completionTimeout = 70 * time.Second
	}
	input.CompletionTimeout = completionTimeout
	input.ValidateRequestJSON = func(manifest *domain.TimerPeriodBatch, batch *domain.BatchInvocation, batchInstanceIDs []string, instances []domain.TaskInstance, targets []domain.WriteTarget, snapshotEntries []domain.PeriodSeriesSnapshotEntry) error {
		if batch == nil {
			return fmt.Errorf("persisted timer batch is missing")
		}
		return validatePersistedTimerRequest([]byte(batch.RequestJSON), input, manifest, batch, batchInstanceIDs, instances, targets, snapshotEntries)
	}
	claimed, err := c.Batches.Claim(ctx, input)
	if err != nil {
		return TimerBatchClaimResponse{}, err
	}
	if !claimed.Claimed {
		return TimerBatchClaimResponse{}, nil
	}
	if len(claimed.RequestJSON) == 0 || len(claimed.RequestJSON) > maxTimerBatchResponseBytes {
		return TimerBatchClaimResponse{}, fmt.Errorf("persisted timer batch response is empty or exceeds %d bytes", maxTimerBatchResponseBytes)
	}
	return TimerBatchClaimResponse{
		Claimed: true, RequestJSON: append([]byte(nil), claimed.RequestJSON...),
		PeriodDeadlineAt: claimed.PeriodDeadlineAt.UTC(), BatchID: claimed.BatchID,
	}, nil
}

func validatePersistedTimerRequest(raw []byte, input store.TimerPeriodBatchClaimInput, manifest *domain.TimerPeriodBatch, batch *domain.BatchInvocation, batchInstanceIDs []string, persistedInstances []domain.TaskInstance, persistedTargets []domain.WriteTarget, snapshotEntries []domain.PeriodSeriesSnapshotEntry) error {
	if len(raw) == 0 || len(raw) > maxTimerBatchResponseBytes {
		return fmt.Errorf("persisted timer request is empty or exceeds %d bytes", maxTimerBatchResponseBytes)
	}
	if manifest == nil || batch == nil {
		return fmt.Errorf("persisted timer manifest or batch is missing")
	}
	request, err := decodeClaimedTimerRequest(raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(batch.BatchID) == "" || batch.BatchID != manifest.BatchID || request.BatchID != batch.BatchID {
		return fmt.Errorf("persisted request batch_id does not match claimed batch")
	}
	periodTime := manifest.PeriodTime.UTC().Format(time.RFC3339Nano)
	shardIndex := strconv.Itoa(int(manifest.ShardIndex))
	expectedBatchID := stableID(manifest.SpaceID, manifest.DatasetID, manifest.Frequency, periodTime, shardIndex, "timer-initial")
	expectedSyncPointID := stableID(manifest.SpaceID, manifest.DatasetID, manifest.Frequency, periodTime, shardIndex, "timer-sync-point")
	expectedScheduleID := "timer:" + stableID(manifest.SpaceID, manifest.DatasetID, manifest.Frequency, periodTime, shardIndex)
	if request.SyncPointID != expectedSyncPointID {
		return fmt.Errorf("persisted request period identity does not match manifest")
	}
	if batch.BatchID != expectedBatchID || batch.ScheduleID != expectedScheduleID || request.ScheduleID != expectedScheduleID ||
		batch.ParentBatchID != "" || batch.Attempt != 1 || batch.InstanceID != "" ||
		batch.WriteTargetID != "" || batch.RetryScope != "" ||
		batch.SpaceID != manifest.SpaceID || batch.ScheduleID != request.ScheduleID ||
		batch.BatchKind != domain.BatchKindRealtime || request.BatchKind != batch.BatchKind ||
		batch.ShardIndex != int(manifest.ShardIndex) || request.ShardIndex != batch.ShardIndex ||
		batch.Frequency != manifest.Frequency || request.Frequency != batch.Frequency ||
		batch.NodeID != manifest.NodeID || request.NodeID != batch.NodeID ||
		batch.Region != manifest.Region || request.Region != batch.Region ||
		batch.FunctionName != manifest.FunctionName || request.FunctionName != batch.FunctionName ||
		batch.PlannedCount <= 0 || batch.PlannedCount != len(request.Items) {
		return fmt.Errorf("persisted request batch identity or planned membership does not match manifest")
	}
	if batch.Status == domain.BatchStatusPlanned {
		if manifest.ClaimRequestID != "" || manifest.ClaimedAt != nil || batch.RequestID != "" || request.RequestID != "" || batch.DispatchedAt != nil {
			return fmt.Errorf("persisted timer claim state does not match planned batch")
		}
	} else if (batch.Status != domain.BatchStatusDispatched && !batch.Status.Terminal()) ||
		manifest.ClaimRequestID != input.RequestID || manifest.ClaimedAt == nil || batch.RequestID != input.RequestID ||
		request.RequestID != input.RequestID || batch.DispatchedAt == nil || !manifest.ClaimedAt.Equal(*batch.DispatchedAt) {
		return fmt.Errorf("persisted timer claim state does not match replay")
	}
	if !request.RequirePeriodCommit {
		return fmt.Errorf("persisted timer request must require period commit")
	}
	if request.SpaceID != manifest.SpaceID || request.DatasetID != manifest.DatasetID || request.Frequency != manifest.Frequency ||
		request.FunctionName != manifest.FunctionName || request.NodeID != manifest.NodeID || request.Region != manifest.Region ||
		request.GroupID != int(manifest.GroupID) || request.GroupCount != int(manifest.GroupCount) ||
		request.ShardIndex != int(manifest.ShardIndex) || request.BindingHash != manifest.BindingHash || request.RouteVersion != manifest.RouteVersion ||
		request.SpaceID != input.SpaceID || request.FunctionName != input.FunctionName || request.GroupID != int(input.GroupID) ||
		request.GroupCount != int(input.GroupCount) || request.BindingHash != input.BindingHash {
		return fmt.Errorf("persisted request static identity does not match claim")
	}
	if len(snapshotEntries) != int(manifest.ExpectedCount) {
		return fmt.Errorf("persisted timer snapshot is missing")
	}
	snapshotByIndex := make(map[uint32]domain.PeriodSeriesSnapshotEntry, len(snapshotEntries))
	for _, entry := range snapshotEntries {
		if entry.SpaceID != manifest.SpaceID || entry.DatasetID != manifest.DatasetID || entry.Frequency != manifest.Frequency ||
			!entry.PeriodTime.UTC().Equal(manifest.PeriodTime.UTC()) || entry.SeriesHash != manifest.SeriesHash ||
			uint32(entry.ExpectedCount) != manifest.ExpectedCount || entry.SeriesIndex >= manifest.ExpectedCount {
			return fmt.Errorf("persisted timer snapshot differs from manifest")
		}
		if _, exists := snapshotByIndex[entry.SeriesIndex]; exists {
			return fmt.Errorf("persisted timer snapshot contains duplicate series index")
		}
		snapshotByIndex[entry.SeriesIndex] = entry
	}
	routeProvider := request.RouteProvider
	if strings.TrimSpace(routeProvider) == "" {
		return fmt.Errorf("persisted request route provider is missing")
	}
	outputFields := append([]string(nil), request.Items[0].OutputFields...)
	for _, item := range request.Items[1:] {
		if !slices.Equal(item.OutputFields, outputFields) {
			return fmt.Errorf("persisted timer request items differ in output-field binding")
		}
	}
	assignment := NodeAssignment{
		RouteProvider: routeProvider, Provider: request.Provider, SourceID: request.SourceID, RouteVersion: request.RouteVersion,
		GroupID: request.GroupID, GroupCount: request.GroupCount, MarketType: request.MarketType, MarketID: request.MarketID,
		InstrumentType: request.InstrumentType, DatasetID: request.DatasetID, Frequency: request.Frequency,
		OutputFields: outputFields, NodeID: request.NodeID, FunctionName: request.FunctionName, Region: request.Region,
	}
	if timerPeriodBindingHash(assignment) != manifest.BindingHash {
		return fmt.Errorf("persisted request period identity does not match frozen binding")
	}
	if len(request.Items) > MaxRealtimeItems {
		return fmt.Errorf("persisted timer request exceeds %d items", MaxRealtimeItems)
	}
	if len(batchInstanceIDs) != batch.PlannedCount {
		return fmt.Errorf("persisted timer batch-item membership does not match planned_count")
	}
	if len(persistedInstances) != len(batchInstanceIDs) {
		return fmt.Errorf("persisted timer task-instance membership differs from frozen batch membership")
	}
	batchItems := make(map[string]struct{}, len(batchInstanceIDs))
	for _, instanceID := range batchInstanceIDs {
		if strings.TrimSpace(instanceID) == "" {
			return fmt.Errorf("persisted timer batch-item membership contains an empty instance ID")
		}
		if _, exists := batchItems[instanceID]; exists {
			return fmt.Errorf("persisted timer batch-item membership contains a duplicate instance ID")
		}
		batchItems[instanceID] = struct{}{}
	}
	instances := make(map[string]domain.TaskInstance, len(persistedInstances))
	for _, instance := range persistedInstances {
		if _, exists := instances[instance.InstanceID]; exists {
			return fmt.Errorf("persisted timer task-instance membership contains duplicate IDs")
		}
		instances[instance.InstanceID] = instance
	}
	items := make(map[string]domain.CollectionItem, len(request.Items))
	seriesIndexes := make(map[uint32]struct{}, len(request.Items))
	for index, item := range request.Items {
		period, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.TargetDataTime))
		if item.Provider != request.Provider || item.SourceID != request.SourceID {
			return fmt.Errorf("persisted timer item %d differs from frozen route binding", index)
		}
		if item.CandidateIndex != 0 || item.RateBudgetRatio != 0 || strings.TrimSpace(item.SourceEventID) != "" {
			return fmt.Errorf("persisted timer item %d contains retry execution state", index)
		}
		if strings.TrimSpace(item.InstanceID) == "" || parseErr != nil || !period.UTC().Equal(manifest.PeriodTime.UTC()) ||
			item.DatasetID != manifest.DatasetID || item.Frequency != manifest.Frequency || item.SeriesHash != manifest.SeriesHash ||
			item.ExpectedCount != manifest.ExpectedCount || item.SeriesIndex >= manifest.ExpectedCount {
			return fmt.Errorf("persisted timer item %d differs from manifest period identity", index)
		}
		snapshotEntry, snapshotExists := snapshotByIndex[item.SeriesIndex]
		if !snapshotExists || snapshotEntry.SubjectID != item.SubjectID || snapshotEntry.ProviderSymbol != item.Symbol ||
			snapshotEntry.MarketType != item.MarketType || snapshotEntry.SeriesHash != manifest.SeriesHash ||
			uint32(snapshotEntry.ExpectedCount) != manifest.ExpectedCount {
			return fmt.Errorf("persisted timer item %d differs from frozen snapshot", index)
		}
		if _, exists := items[item.InstanceID]; exists {
			return fmt.Errorf("persisted timer request contains duplicate item instance identity")
		}
		if _, exists := batchItems[item.InstanceID]; !exists {
			return fmt.Errorf("persisted timer request item membership differs from frozen batch membership")
		}
		instance, instanceExists := instances[item.InstanceID]
		instancePeriodMatches := instance.TargetDataTime != nil && instance.TargetDataTime.UTC().Equal(manifest.PeriodTime.UTC())
		logicalItem := item
		logicalItem.Provider = snapshotEntry.Provider
		logicalItem.SourceID = snapshotEntry.SourceID
		expectedRequestKey := sharedCollectionItemKey(logicalItem, request.Frequency, manifest.PeriodTime)
		if !instanceExists || instance.SpaceID != manifest.SpaceID || instance.SubjectID != item.SubjectID ||
			instance.ProviderSymbol != item.Symbol || instance.MarketType != item.MarketType || instance.DataType != item.DataType ||
			instance.Frequency != item.Frequency || !instancePeriodMatches || instance.TaskParams != collectionItemRequestParams(item) ||
			instance.RunID != manifest.FirstRunID || instance.Provider != snapshotEntry.Provider || instance.SeriesTag != snapshotEntry.SeriesTag ||
			instance.RequestKey != expectedRequestKey {
			return fmt.Errorf("persisted timer item %d differs from frozen task instance", index)
		}
		if _, exists := seriesIndexes[item.SeriesIndex]; exists {
			return fmt.Errorf("persisted timer request contains duplicate series index")
		}
		seriesIndexes[item.SeriesIndex] = struct{}{}
		items[item.InstanceID] = item
	}
	if len(items) != len(batchItems) {
		return fmt.Errorf("persisted timer request item membership differs from frozen batch membership")
	}
	if len(request.Targets) == 0 || len(request.Targets) != len(items) {
		return fmt.Errorf("persisted timer request has no write targets")
	}
	if len(persistedTargets) != len(request.Targets) {
		return fmt.Errorf("persisted timer write-target membership differs from frozen batch membership")
	}
	persistedTargetsByID := make(map[string]domain.WriteTarget, len(persistedTargets))
	for _, persistedTarget := range persistedTargets {
		if persistedTarget.ID == "" {
			return fmt.Errorf("persisted timer write target has no ID")
		}
		if _, exists := persistedTargetsByID[persistedTarget.ID]; exists {
			return fmt.Errorf("persisted timer write-target membership contains duplicate IDs")
		}
		persistedTargetsByID[persistedTarget.ID] = persistedTarget
	}
	targets := make(map[string]struct{}, len(request.Targets))
	for index, target := range request.Targets {
		item, exists := items[target.InstanceID]
		persistedTarget, targetExists := persistedTargetsByID[target.ID]
		period, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(target.TargetDataTime))
		if !exists || !targetExists || parseErr != nil || !period.UTC().Equal(manifest.PeriodTime.UTC()) || target.SpaceID != manifest.SpaceID ||
			target.TaskID != manifest.TaskID || target.DatasetID != manifest.DatasetID || target.Frequency != manifest.Frequency ||
			target.SeriesIndex != item.SeriesIndex || target.SeriesHash != manifest.SeriesHash || target.ExpectedCount != manifest.ExpectedCount ||
			persistedTarget.SpaceID != target.SpaceID || persistedTarget.InstanceID != target.InstanceID || persistedTarget.TaskID != target.TaskID ||
			persistedTarget.DatasetID != target.DatasetID || persistedTarget.SeriesIndex != target.SeriesIndex ||
			persistedTarget.ViewID != target.ViewID || persistedTarget.OutputFields != target.OutputFields ||
			persistedTarget.SeriesHash != target.SeriesHash || persistedTarget.ExpectedCount != target.ExpectedCount {
			return fmt.Errorf("persisted timer target %d differs from manifest period identity", index)
		}
		if _, exists := targets[target.InstanceID]; exists {
			return fmt.Errorf("persisted timer request contains duplicate target instance identity")
		}
		targets[target.InstanceID] = struct{}{}
	}
	for instanceID := range items {
		if _, exists := targets[instanceID]; !exists {
			return fmt.Errorf("persisted timer item has no matching target")
		}
	}
	return nil
}
