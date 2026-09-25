package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
)

type mergeMarkerClient interface {
	ReportMergePeriodCompleted(context.Context, *storagepb.ReportMergePeriodCompletedReq, ...client.Option) (*storagepb.ReportMergePeriodCompletedRsp, error)
}

// StoragePeriodReporter publishes MergePeriodCompleted after receipts are persisted.
type StoragePeriodReporter struct {
	client  mergeMarkerClient
	auth    *commonpb.AuthInfo
	spaceID string
}

func NewStoragePeriodReporter(client mergeMarkerClient, auth *commonpb.AuthInfo, spaceID string) *StoragePeriodReporter {
	return &StoragePeriodReporter{client: client, auth: auth, spaceID: spaceID}
}

func (r *StoragePeriodReporter) Report(ctx context.Context, marker PeriodMarker) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("merge period reporter is not initialized")
	}
	positions := make([]*storagepb.CommittedPosition, 0, len(marker.Positions))
	for _, pos := range marker.Positions {
		positions = append(positions, &storagepb.CommittedPosition{NodeId: pos.NodeID, StoreId: pos.StoreID, Sequence: pos.Sequence})
	}
	rsp, err := r.client.ReportMergePeriodCompleted(ctx, &storagepb.ReportMergePeriodCompletedReq{
		AuthInfo: r.auth, SpaceId: r.spaceID,
		Marker: &storagepb.MergePeriodCompletedMarker{
			DatasetId: marker.DatasetID, Frequency: marker.Frequency, PeriodTime: marker.PeriodTime.UTC().Unix(),
			Status: marker.Status, BatchId: marker.BatchID, ConfigSnapshotId: marker.SnapshotID,
			ExpectedScopeRef: marker.ScopeRef, UniverseSubjectIds: append([]string(nil), marker.UniverseSubjectIDs...),
			FailedSubjects: append([]string(nil), marker.FailedSubjects...), CommittedPositions: positions,
			CompletedAt: timestamppb.New(time.Now().UTC()),
		},
	})
	if err != nil {
		return fmt.Errorf("report merge period completed: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		msg := strings.TrimSpace(rsp.GetRetInfo().GetMsg())
		if msg == "" {
			msg = rsp.GetRetInfo().GetCode().String()
		}
		return fmt.Errorf("report merge period completed: %s", msg)
	}
	return nil
}
