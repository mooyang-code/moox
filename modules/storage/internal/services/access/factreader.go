package access

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type recordAccessSnapshot struct {
	target   *pb.PrimaryStoreTarget
	datasets map[string]struct{}
}

// primaryFactReader 通过 PrimaryStore 客户端回读主存事实行。
type primaryFactReader struct {
	service *Service
}

func (s *Service) primaryFactReader() *primaryFactReader {
	return &primaryFactReader{service: s}
}

// FactReader exposes the local cursor reader without making the public Read RPC
// carry internal dataset-scan semantics.
func (s *Service) FactReader() *primaryFactReader {
	return s.primaryFactReader()
}

func (s *Service) scanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	req := &pb.ReadTimeSeriesRowsReq{
		Keys:        []*pb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: datasetID}},
		TimeRange:   timeRange,
		ColumnNames: columnNames,
		Page:        page,
	}
	if err := validateTimeRange(req.GetTimeRange()); err != nil {
		return nil, nil, err
	}
	if isTimeSeriesDatasetScan(req) {
		return s.scanTimeSeriesDatasetPageRows(ctx, req)
	}
	rsp, err := s.ReadTimeSeriesRows(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, nil, errText(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (s *Service) scanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	req := &pb.ReadRecordRowsReq{
		Keys:        []*pb.RecordKey{{SpaceId: spaceID, DatasetId: datasetID}},
		Mode:        pb.RecordReadMode_RECORD_READ_MODE_HISTORY,
		ColumnNames: columnNames,
		Page:        page,
	}
	_ = versionRange
	rsp, err := s.ReadRecordRows(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, nil, errText(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

// ScanTimeSeriesRows is the internal cursor RPC used by independently deployed
// ViewBuilder processes. Each request reads one bounded PrimaryStore page.
func (s *Service) ScanTimeSeriesRows(ctx context.Context, req *pb.ScanTimeSeriesRowsReq) (*pb.ScanTimeSeriesRowsRsp, error) {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ScanTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	rows, page, err := s.scanTimeSeriesRows(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetTimeRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return &pb.ScanTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

// ScanRecordRows is the record counterpart of ScanTimeSeriesRows.
func (s *Service) ScanRecordRows(ctx context.Context, req *pb.ScanRecordRowsReq) (*pb.ScanRecordRowsRsp, error) {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ScanRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	rows, page, err := s.scanRecordRows(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetVersionRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return &pb.ScanRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanRecordRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func (r *primaryFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.service.ReadTimeSeriesRows(ctx, req)
}

func (r *primaryFactReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return r.service.scanTimeSeriesRows(ctx, spaceID, datasetID, timeRange, columnNames, page)
}

func (r *primaryFactReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return r.service.ReadRecordRows(ctx, req)
}

func (r *primaryFactReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return r.service.scanRecordRows(ctx, spaceID, datasetID, versionRange, columnNames, page)
}

func (r *primaryFactReader) OpenRecordSnapshot(ctx context.Context, req *pb.OpenRecordAccessSnapshotReq) (*pb.OpenRecordAccessSnapshotRsp, error) {
	return r.service.OpenRecordAccessSnapshot(ctx, req)
}
func (r *primaryFactReader) ReadRecordSnapshot(ctx context.Context, req *pb.ReadRecordAccessSnapshotReq) (*pb.ReadRecordAccessSnapshotRsp, error) {
	return r.service.ReadRecordAccessSnapshot(ctx, req)
}
func (r *primaryFactReader) ScanRecordSnapshot(ctx context.Context, req *pb.ScanRecordAccessSnapshotReq) (*pb.ScanRecordAccessSnapshotRsp, error) {
	return r.service.ScanRecordAccessSnapshot(ctx, req)
}
func (r *primaryFactReader) RenewRecordSnapshot(ctx context.Context, snapshotID string) error {
	rsp, err := r.service.RenewRecordAccessSnapshot(ctx, &pb.RenewRecordAccessSnapshotReq{SnapshotId: snapshotID})
	if err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return errors.New(rsp.GetRetInfo().GetMsg())
	}
	return nil
}
func (r *primaryFactReader) CloseRecordSnapshot(ctx context.Context, snapshotID string) error {
	rsp, err := r.service.CloseRecordAccessSnapshot(ctx, &pb.CloseRecordAccessSnapshotReq{SnapshotId: snapshotID})
	if err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return errors.New(rsp.GetRetInfo().GetMsg())
	}
	return nil
}
func (r *primaryFactReader) RecordWatermark(ctx context.Context, scope *pb.RecordAccessScope) (string, uint64, error) {
	rsp, err := r.service.RecordAccessWatermark(ctx, &pb.RecordAccessWatermarkReq{Scope: scope})
	if err != nil {
		return "", 0, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return "", 0, errors.New(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetSourceId(), rsp.GetCommitSeq(), nil
}
func (r *primaryFactReader) ScanRecordJournal(ctx context.Context, scope *pb.RecordAccessScope, after, through uint64, page *pb.Page) ([]*pb.RecordRowsCommittedEvent, uint64, *pb.PageResult, error) {
	rsp, err := r.service.ScanRecordAccessJournal(ctx, &pb.ScanRecordAccessJournalReq{Scope: scope, AfterCommitSeq: after, ThroughCommitSeq: through, Page: page})
	if err != nil {
		return nil, 0, nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, 0, nil, errors.New(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetEvents(), rsp.GetScannedThroughCommitSeq(), rsp.GetPageResult(), nil
}

func (s *Service) OpenRecordAccessSnapshot(ctx context.Context, req *pb.OpenRecordAccessSnapshotReq) (*pb.OpenRecordAccessSnapshotRsp, error) {
	if req.GetScope().GetSpaceId() == "" || len(req.GetScope().GetDatasetIds()) == 0 {
		return &pb.OpenRecordAccessSnapshotRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record snapshot scope requires space_id and dataset_ids"))}, nil
	}
	targets, datasets, err := s.resolveRecordAccessScope(ctx, req.GetScope())
	if err != nil {
		return &pb.OpenRecordAccessSnapshotRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED, err)}, nil
	}
	rsp, err := s.primary.OpenRecordSnapshot(ctx, &pb.OpenRecordSnapshotReq{SourceTarget: targets[0], Mode: req.GetMode(), UpdatedTimeRange: req.GetUpdatedTimeRange()})
	if err != nil {
		return &pb.OpenRecordAccessSnapshotRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	s.recordAccessMu.Lock()
	if s.recordAccess == nil {
		s.recordAccess = make(map[string]*recordAccessSnapshot)
	}
	s.recordAccess[rsp.GetSnapshotId()] = &recordAccessSnapshot{target: targets[0], datasets: datasets}
	s.recordAccessMu.Unlock()
	return &pb.OpenRecordAccessSnapshotRsp{RetInfo: response.Success("success"), SnapshotId: rsp.GetSnapshotId(), SourceId: rsp.GetSourceId(), SnapshotCommitSeq: rsp.GetCommitSeq()}, nil
}

func (s *Service) ReadRecordAccessSnapshot(ctx context.Context, req *pb.ReadRecordAccessSnapshotReq) (*pb.ReadRecordAccessSnapshotRsp, error) {
	snapshot, target, err := s.recordAccessTarget(req.GetSnapshotId(), req.GetDatasetId())
	if err != nil {
		return &pb.ReadRecordAccessSnapshotRsp{RetInfo: response.Error(pb.ErrorCode_NOT_FOUND, err)}, nil
	}
	queryTarget := proto.Clone(target).(*pb.PrimaryStoreTarget)
	queryTarget.DatasetId = req.GetDatasetId()
	rsp, err := s.primary.ReadRecordSnapshot(ctx, &pb.ReadRecordSnapshotReq{SnapshotId: snapshot, Target: queryTarget, RecordIds: req.GetRecordIds()})
	if err != nil {
		return &pb.ReadRecordAccessSnapshotRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ReadRecordAccessSnapshotRsp{RetInfo: response.Success("success"), Rows: rsp.GetRows()}, nil
}

func (s *Service) ScanRecordAccessSnapshot(ctx context.Context, req *pb.ScanRecordAccessSnapshotReq) (*pb.ScanRecordAccessSnapshotRsp, error) {
	snapshot, target, err := s.recordAccessTarget(req.GetSnapshotId(), req.GetDatasetId())
	if err != nil {
		return &pb.ScanRecordAccessSnapshotRsp{RetInfo: response.Error(pb.ErrorCode_NOT_FOUND, err)}, nil
	}
	queryTarget := proto.Clone(target).(*pb.PrimaryStoreTarget)
	queryTarget.DatasetId = req.GetDatasetId()
	rsp, err := s.primary.ScanRecordSnapshot(ctx, &pb.ScanRecordSnapshotReq{SnapshotId: snapshot, Target: queryTarget, Page: req.GetPage()})
	if err != nil {
		return &pb.ScanRecordAccessSnapshotRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanRecordAccessSnapshotRsp{RetInfo: response.Success("success"), Rows: rsp.GetRows(), PageResult: rsp.GetPageResult()}, nil
}

func (s *Service) RenewRecordAccessSnapshot(ctx context.Context, req *pb.RenewRecordAccessSnapshotReq) (*pb.RenewRecordAccessSnapshotRsp, error) {
	if _, _, err := s.recordAccessTarget(req.GetSnapshotId(), ""); err != nil {
		return &pb.RenewRecordAccessSnapshotRsp{RetInfo: response.Error(pb.ErrorCode_NOT_FOUND, err)}, nil
	}
	if err := s.primary.RenewRecordSnapshot(ctx, &pb.RenewRecordSnapshotReq{SnapshotId: req.GetSnapshotId()}); err != nil {
		return &pb.RenewRecordAccessSnapshotRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.RenewRecordAccessSnapshotRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) CloseRecordAccessSnapshot(ctx context.Context, req *pb.CloseRecordAccessSnapshotReq) (*pb.CloseRecordAccessSnapshotRsp, error) {
	if _, _, err := s.recordAccessTarget(req.GetSnapshotId(), ""); err != nil {
		return &pb.CloseRecordAccessSnapshotRsp{RetInfo: response.Error(pb.ErrorCode_NOT_FOUND, err)}, nil
	}
	if err := s.primary.CloseRecordSnapshot(ctx, &pb.CloseRecordSnapshotReq{SnapshotId: req.GetSnapshotId()}); err != nil {
		return &pb.CloseRecordAccessSnapshotRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	s.recordAccessMu.Lock()
	delete(s.recordAccess, req.GetSnapshotId())
	s.recordAccessMu.Unlock()
	return &pb.CloseRecordAccessSnapshotRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) RecordAccessWatermark(ctx context.Context, req *pb.RecordAccessWatermarkReq) (*pb.RecordAccessWatermarkRsp, error) {
	targets, _, err := s.resolveRecordAccessScope(ctx, req.GetScope())
	if err != nil {
		return &pb.RecordAccessWatermarkRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED, err)}, nil
	}
	source, seq, err := s.primary.GetRecordWatermark(ctx, targets[0])
	if err != nil {
		return &pb.RecordAccessWatermarkRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.RecordAccessWatermarkRsp{RetInfo: response.Success("success"), SourceId: source, CommitSeq: seq}, nil
}

func (s *Service) ScanRecordAccessJournal(ctx context.Context, req *pb.ScanRecordAccessJournalReq) (*pb.ScanRecordAccessJournalRsp, error) {
	targets, _, err := s.resolveRecordAccessScope(ctx, req.GetScope())
	if err != nil {
		return &pb.ScanRecordAccessJournalRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED, err)}, nil
	}
	rsp, err := s.primary.ScanRecordJournal(ctx, &pb.ScanRecordJournalReq{Target: targets[0], AfterCommitSeq: req.GetAfterCommitSeq(), ThroughCommitSeq: req.GetThroughCommitSeq(), Page: req.GetPage()})
	if err != nil {
		return &pb.ScanRecordAccessJournalRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	allowed := make(map[string]struct{}, len(req.GetScope().GetDatasetIds()))
	for _, datasetID := range req.GetScope().GetDatasetIds() {
		allowed[datasetID] = struct{}{}
	}
	events := make([]*pb.RecordRowsCommittedEvent, 0, len(rsp.GetEvents()))
	for _, event := range rsp.GetEvents() {
		filtered := proto.Clone(event).(*pb.RecordRowsCommittedEvent)
		filtered.Rows = filtered.Rows[:0]
		for _, row := range event.GetRows() {
			if _, ok := allowed[row.GetKey().GetDatasetId()]; ok {
				filtered.Rows = append(filtered.Rows, proto.Clone(row).(*pb.RecordRow))
			}
		}
		if len(filtered.GetRows()) > 0 {
			events = append(events, filtered)
		}
	}
	return &pb.ScanRecordAccessJournalRsp{RetInfo: response.Success("success"), Events: events, ScannedThroughCommitSeq: rsp.GetScannedThroughCommitSeq(), PageResult: rsp.GetPageResult()}, nil
}

func (s *Service) resolveRecordAccessScope(ctx context.Context, scope *pb.RecordAccessScope) ([]*pb.PrimaryStoreTarget, map[string]struct{}, error) {
	if scope == nil || scope.GetSpaceId() == "" || len(scope.GetDatasetIds()) == 0 {
		return nil, nil, errors.New("record snapshot scope requires space_id and dataset_ids")
	}
	var targets []*pb.PrimaryStoreTarget
	datasets := make(map[string]struct{}, len(scope.GetDatasetIds()))
	for _, datasetID := range scope.GetDatasetIds() {
		resolved, err := s.router.ResolveDatasetTargets(ctx, scope.GetSpaceId(), datasetID)
		if err != nil {
			return nil, nil, err
		}
		targets = append(targets, resolved...)
		datasets[datasetID] = struct{}{}
	}
	if len(targets) == 0 {
		return nil, nil, errors.New("record snapshot scope has no routes")
	}
	if _, err := requireSingleRecordCommitSource(targets); err != nil {
		return nil, nil, err
	}
	return targets, datasets, nil
}

func (s *Service) recordAccessTarget(snapshotID, datasetID string) (string, *pb.PrimaryStoreTarget, error) {
	s.recordAccessMu.Lock()
	snapshot := s.recordAccess[snapshotID]
	s.recordAccessMu.Unlock()
	if snapshot == nil {
		return "", nil, errors.New("record access snapshot not found")
	}
	if datasetID != "" {
		if _, ok := snapshot.datasets[datasetID]; !ok {
			return "", nil, errors.New("dataset is outside record snapshot scope")
		}
	}
	return snapshotID, snapshot.target, nil
}
