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

type datasetPeriodReportClient interface {
	ReportPeriod(context.Context, *storagepb.PrimaryReportPeriodReq, ...client.Option) (*storagepb.PrimaryReportPeriodRsp, error)
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

// StorageDatasetPeriodReporter closes the derived Dataset period through the
// generic Storage-owned protocol. Successful subjects were already recorded
// atomically by CommitInput; only failed/missing subjects are reported here.
type StorageDatasetPeriodReporter struct {
	client  datasetPeriodReportClient
	auth    *commonpb.AuthInfo
	spaceID string
}

func NewStorageDatasetPeriodReporter(client datasetPeriodReportClient, auth *commonpb.AuthInfo, spaceID string) *StorageDatasetPeriodReporter {
	return &StorageDatasetPeriodReporter{client: client, auth: auth, spaceID: spaceID}
}

func (r *StorageDatasetPeriodReporter) Report(ctx context.Context, marker PeriodMarker) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("dataset period reporter is not initialized")
	}
	if len(marker.FailedSubjects) == 0 {
		return nil
	}
	items := make([]*storagepb.DatasetPeriodItem, 0, len(marker.FailedSubjects))
	for _, subjectID := range marker.FailedSubjects {
		items = append(items, &storagepb.DatasetPeriodItem{SubjectId: subjectID, State: "missing", Reason: "merge_deadline_exceeded"})
	}
	requestID := fmt.Sprintf("merge-period-report:%s:%s:%d", marker.DatasetID, marker.Frequency, marker.PeriodTime.Unix())
	rsp, err := r.client.ReportPeriod(ctx, &storagepb.PrimaryReportPeriodReq{AuthInfo: r.auth, ReportId: requestID, SpaceId: r.spaceID, DatasetId: marker.DatasetID, Frequency: marker.Frequency, PeriodTime: marker.PeriodTime.Unix(), Items: items})
	if err != nil {
		return fmt.Errorf("report Dataset period: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("report Dataset period: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}
