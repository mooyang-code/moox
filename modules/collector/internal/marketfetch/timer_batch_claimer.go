package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	input.ValidateRequestJSON = func(batchID string, raw []byte) error {
		return validatePersistedTimerRequest(raw, input, batchID)
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

func validatePersistedTimerRequest(raw []byte, input store.TimerPeriodBatchClaimInput, batchID string) error {
	if len(raw) == 0 || len(raw) > maxTimerBatchResponseBytes {
		return fmt.Errorf("persisted timer request is empty or exceeds %d bytes", maxTimerBatchResponseBytes)
	}
	var request Request
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	if strings.TrimSpace(batchID) == "" || request.BatchID != batchID {
		return fmt.Errorf("persisted request batch_id does not match claimed batch")
	}
	if !request.RequirePeriodCommit {
		return fmt.Errorf("persisted timer request must require period commit")
	}
	if err := request.validate(); err != nil {
		return err
	}
	if request.SpaceID != input.SpaceID || request.FunctionName != input.FunctionName || request.GroupID != int(input.GroupID) ||
		request.GroupCount != int(input.GroupCount) || request.BindingHash != input.BindingHash {
		return fmt.Errorf("persisted request static identity does not match claim")
	}
	if request.RequestID != "" && request.RequestID != input.RequestID {
		return fmt.Errorf("persisted request ID does not match claim")
	}
	if len(request.Items) > MaxRealtimeItems {
		return fmt.Errorf("persisted timer request exceeds %d items", MaxRealtimeItems)
	}
	items := make(map[string]struct{}, len(request.Items))
	for index, item := range request.Items {
		if strings.TrimSpace(item.InstanceID) == "" || strings.TrimSpace(item.TargetDataTime) == "" || strings.TrimSpace(item.SeriesHash) == "" || item.ExpectedCount == 0 {
			return fmt.Errorf("persisted timer item %d has incomplete period identity", index)
		}
		if _, err := time.Parse(time.RFC3339Nano, item.TargetDataTime); err != nil {
			return fmt.Errorf("persisted timer item %d target_data_time is invalid: %w", index, err)
		}
		items[item.InstanceID] = struct{}{}
	}
	if len(request.Targets) == 0 {
		return fmt.Errorf("persisted timer request has no write targets")
	}
	for index, target := range request.Targets {
		if _, ok := items[target.InstanceID]; !ok || target.SeriesHash == "" || target.ExpectedCount == 0 || target.Frequency != request.Frequency || target.TargetDataTime == "" {
			return fmt.Errorf("persisted timer target %d has incomplete period identity", index)
		}
		if _, err := time.Parse(time.RFC3339Nano, target.TargetDataTime); err != nil {
			return fmt.Errorf("persisted timer target %d target_data_time is invalid: %w", index, err)
		}
	}
	return nil
}
