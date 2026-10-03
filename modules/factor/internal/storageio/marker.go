package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (c *Client) ReportComputed(ctx context.Context, marker PeriodMarker) error {
	if err := c.primaryReady("report factor period computed"); err != nil {
		return err
	}
	if strings.TrimSpace(marker.SpaceID) == "" || strings.TrimSpace(marker.ResultDatasetID) == "" ||
		strings.TrimSpace(marker.SourceDatasetID) == "" || strings.TrimSpace(marker.Frequency) == "" ||
		strings.TrimSpace(marker.TriggerEventID) == "" || marker.PeriodTime <= 0 {
		return errors.New("space, result/source dataset, frequency, positive period_time and trigger_event_id are required")
	}
	protoMarker := markerToProto(marker)
	rsp, err := c.primary.ReportFactorPeriodComputed(ctx, &storagepb.ReportFactorPeriodComputedReq{
		AuthInfo: c.auth, SpaceId: marker.SpaceID, Marker: protoMarker,
	})
	if err != nil {
		return rpcError("report factor period computed", err)
	}
	if rsp == nil {
		return fmt.Errorf("%w: report factor period computed returned an empty response", ErrInfra)
	}
	if err := responseError("report factor period computed", rsp.GetRetInfo()); err != nil {
		return err
	}
	expected := computedEventID(marker.SpaceID, protoMarker)
	if rsp.GetEventId() != expected {
		return fmt.Errorf("reported factor period event_id %q does not match deterministic ID %q", rsp.GetEventId(), expected)
	}
	return nil
}

func (c *Client) ComputedExists(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (bool, error) {
	if err := c.primaryReady("get factor period computed"); err != nil {
		return false, err
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(triggerEventID) == "" || periodTime <= 0 {
		return false, errors.New("space_id, dataset_id, trigger_event_id and positive period_time are required")
	}
	rsp, err := c.primary.GetFactorPeriodComputed(ctx, &storagepb.GetFactorPeriodComputedReq{
		AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID, TriggerEventId: triggerEventID, PeriodTime: periodTime,
	})
	if err != nil {
		return false, rpcError("get factor period computed", err)
	}
	if rsp == nil {
		return false, fmt.Errorf("%w: get factor period computed returned an empty response", ErrInfra)
	}
	ret := rsp.GetRetInfo()
	if ret != nil && ret.GetCode() == commonpb.ErrorCode_NOT_FOUND {
		return false, nil
	}
	if err := responseError("get factor period computed", ret); err != nil {
		return false, err
	}
	return rsp.GetFound(), nil
}

func markerToProto(marker PeriodMarker) *storagepb.FactorPeriodComputedMarker {
	factors := append([]FactorState(nil), marker.Factors...)
	sort.Slice(factors, func(i, j int) bool { return factors[i].FactorID < factors[j].FactorID })
	protoFactors := make([]*storagepb.FactorPeriodState, 0, len(factors))
	for _, factor := range factors {
		protoFactors = append(protoFactors, &storagepb.FactorPeriodState{
			FactorId: factor.FactorID, Status: factor.Status,
			FailedSubjects: normalizedSorted(factor.FailedSubjects), SourceHash: factor.SourceHash,
		})
	}
	return &storagepb.FactorPeriodComputedMarker{
		DatasetId: marker.ResultDatasetID, SourceDatasetId: marker.SourceDatasetID, Frequency: marker.Frequency,
		PeriodTime: marker.PeriodTime, Status: marker.Status,
		UniverseSubjectIds: normalizedSorted(marker.UniverseSubjects), FailedSubjects: normalizedSorted(marker.FailedSubjects),
		Factors: protoFactors, TriggerEventId: marker.TriggerEventID, ComputedAt: timestamppb.New(marker.ComputedAt.UTC()),
	}
}

func normalizedSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func computedEventID(spaceID string, marker *storagepb.FactorPeriodComputedMarker) string {
	parts := []string{
		"factor-period", spaceID, marker.GetDatasetId(), marker.GetTriggerEventId(), strconv.FormatInt(marker.GetPeriodTime(), 10),
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "storage-marker-" + hex.EncodeToString(hash[:16])
}
