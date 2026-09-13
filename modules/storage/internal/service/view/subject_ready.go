package view

import (
	"context"
	"fmt"
	"strconv"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type rowOrigin struct {
	message  *eventpb.EventMessage
	nodeID   string
	storeID  string
	sequence uint64
}

// The caller holds runtime.mu across the active commit and publication. A
// publication failure keeps the original durable delivery pending for replay.
func (s *Service) publishSubjectReady(ctx context.Context, view viewRef, indexID, datasetID string, rows []*pb.RowFieldUpsert, origins map[*pb.RowFieldUpsert]rowOrigin) error {
	if len(origins) == 0 {
		return nil
	}
	if !publishesSourceSubjectReady(view) {
		return nil
	}
	s.mu.RLock()
	schema := s.schemas[indexID]
	publisher := s.readyPublisher
	s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, row := range rows {
		key := row.GetKey().GetTimeSeries()
		if key == nil {
			continue
		}
		position := origins[row]
		origin := position.message
		if origin == nil || origin.GetEventId() == "" || position.nodeID == "" || position.storeID == "" || position.sequence == 0 || schema.SchemaHash == "" {
			return fmt.Errorf("source subject readiness provenance is incomplete")
		}
		if publisher == nil {
			return fmt.Errorf("source subject readiness publisher is unavailable")
		}
		period, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
		if err != nil {
			return fmt.Errorf("source subject readiness time: %w", err)
		}
		contract := schema.SchemaHash + ":" + strconv.FormatUint(schema.ViewVersion, 10)
		eventID := stableViewEventID("source-subject-ready", view.spaceID, view.viewID, contract, datasetID, position.nodeID, position.storeID, strconv.FormatUint(position.sequence, 10), origin.GetEventId(), key.GetSubjectId(), key.GetFreq(), key.GetDataTime(), key.GetSeriesTag())
		if _, exists := seen[eventID]; exists {
			continue
		}
		seen[eventID] = struct{}{}
		readyAt := timestamppb.Now()
		ready := &storagepb.ViewSourceSubjectReady{
			SourceViewId: view.viewID, SourceDatasetId: datasetID, SubjectId: key.GetSubjectId(),
			Frequency: key.GetFreq(), PeriodTime: period.Unix(), SeriesTag: key.GetSeriesTag(),
			InputContractVersion: contract, SourceEventId: origin.GetEventId(), ReadyAt: readyAt,
			SourceNodeId: position.nodeID, SourceSequence: position.sequence,
			SourceStoreId: position.storeID,
		}
		if _, err := publisher.Publish(ctx, events.ViewSourceSubjectReady, ready, events.PublishOptions{
			EventID: eventID, OccurredAt: readyAt.AsTime(), SpaceID: view.spaceID, SubjectID: view.viewID,
		}); err != nil {
			return err
		}
	}
	return nil
}
