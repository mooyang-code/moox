package access

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

const maxDatasetScanRows = 10000

const primaryDatasetScanPageSize = uint32(1000)

func (s *Service) WriteTimeSeriesRows(ctx context.Context, req *pb.WriteTimeSeriesRowsReq) (*pb.WriteTimeSeriesRowsRsp, error) {
	if err := s.validator.ValidateWriteTimeSeriesRows(ctx, req.GetRows()); err != nil {
		return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	groups, err := s.groupTimeSeriesRowsByPrimaryStoreTarget(ctx, req.GetRows())
	if err != nil {
		return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(groupRowsErrorCode(err), err)}, nil
	}
	var written []*pb.TimeSeriesKey
	for _, group := range groups {
		if err := s.primary.WriteRows(ctx, group.target, group.rows); err != nil {
			if publishErr := s.publishTimeSeriesRowsChanged(ctx, written); publishErr != nil {
				s.reportViewError(ctx, "time_series_rows_changed_event", publishErr)
			}
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		written = append(written, group.timeSeriesKeys...)
	}
	if err := s.publishTimeSeriesRowsChanged(ctx, written); err != nil {
		s.reportViewError(ctx, "time_series_rows_changed_event", err)
		return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("primary write committed but change event was not published: %w", err))}, nil
	}
	return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if err := validateTimeRange(req.GetTimeRange()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if isTimeSeriesDatasetScan(req) {
		return s.scanTimeSeriesDataset(ctx, req)
	}
	var mergePlan *multiKeyPagePlan
	if len(req.GetKeys()) > 1 {
		var err error
		mergePlan, err = newMultiKeyPagePlan(req.GetPage(), len(req.GetKeys()), timeSeriesPerKeyPageCap(req))
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	var out []*pb.TimeSeriesRow
	var sourceHasMore bool
	for _, key := range req.GetKeys() {
		if err := validateTimeSeriesKeyTemplate(key); err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		storeKey, err := timeSeriesKeyToPrimaryStoreKey(key, false)
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		if versionRange != nil {
			storeKey.Version = ""
		}
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetSubjectId())
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		page := req.GetPage()
		if mergePlan != nil {
			page = &pb.Page{Page: 1, Size: mergePlan.fetchSize}
		}
		rows, pageResult, err := s.primary.ReadRows(ctx, target, &pb.ReadPrimaryRowsReq{
			AuthInfo:     req.GetAuthInfo(),
			Target:       target,
			Keys:         []*pb.PrimaryStoreKey{storeKey},
			VersionRange: versionRange,
			Order:        req.GetOrder(),
			ColumnNames:  req.GetColumnNames(),
			Page:         page,
		})
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		sourceHasMore = sourceHasMore || pageResult.GetHasMore()
		for _, row := range rows {
			out = append(out, primaryStoreRowToTimeSeriesRow(row, key))
		}
		if len(req.GetKeys()) == 1 {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
		}
	}
	sortTimeSeriesRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseTimeSeriesRows(out)
	}
	out, pageResult := pageMergedTimeSeriesRows(out, mergePlan, sourceHasMore)
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func isTimeSeriesDatasetScan(req *pb.ReadTimeSeriesRowsReq) bool {
	keys := req.GetKeys()
	if len(keys) != 1 {
		return false
	}
	key := keys[0]
	return key != nil &&
		strings.TrimSpace(key.GetSubjectId()) == "" &&
		strings.TrimSpace(key.GetFreq()) == "" &&
		strings.TrimSpace(key.GetDataTime()) == "" &&
		len(key.GetDimensions()) == 0
}

func (s *Service) scanTimeSeriesDataset(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	key := req.GetKeys()[0]
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	var out []*pb.TimeSeriesRow
	seen := make(map[string]bool)
	for _, target := range targets {
		rows, err := s.scanAllPrimaryRows(ctx, req.GetAuthInfo(), target, pb.DataKind_DATA_KIND_TIME_SERIES, versionRange, req.GetColumnNames())
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		for _, row := range rows {
			id := primaryStoreRowID(row)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, primaryStoreRowToTimeSeriesRow(row, key))
		}
	}
	sortTimeSeriesRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseTimeSeriesRows(out)
	}
	out, pageResult := pageTimeSeriesRows(out, req.GetPage())
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func (s *Service) scanTimeSeriesDatasetPageRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	key := req.GetKeys()[0]
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return nil, nil, errText("space_id and dataset_id are required")
	}
	versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return nil, nil, err
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return nil, nil, err
	}
	rows, page, err := s.scanPrimaryRowsPage(ctx, req.GetAuthInfo(), targets, pb.DataKind_DATA_KIND_TIME_SERIES, versionRange, req.GetColumnNames(), req.GetPage())
	if err != nil {
		return nil, nil, err
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, primaryStoreRowToTimeSeriesRow(row, key))
	}
	return out, page, nil
}

func (s *Service) WriteRecordRows(ctx context.Context, req *pb.WriteRecordRowsReq) (*pb.WriteRecordRowsRsp, error) {
	rows := s.normalizeWriteRecordRows(req.GetRows())
	if err := s.validator.ValidateWriteRecordRows(ctx, rows); err != nil {
		return &pb.WriteRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	groups, err := s.groupRecordRowsByPrimaryStoreTarget(ctx, rows)
	if err != nil {
		return &pb.WriteRecordRowsRsp{RetInfo: response.Error(groupRowsErrorCode(err), err)}, nil
	}
	var written []*pb.RecordKey
	for _, group := range groups {
		if err := s.primary.WriteRows(ctx, group.target, group.rows); err != nil {
			if publishErr := s.publishRecordRowsChanged(ctx, written); publishErr != nil {
				s.reportViewError(ctx, "record_rows_changed_event", publishErr)
			}
			return &pb.WriteRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		written = append(written, group.recordKeys...)
	}
	if err := s.publishRecordRowsChanged(ctx, written); err != nil {
		s.reportViewError(ctx, "record_rows_changed_event", err)
		return &pb.WriteRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("primary write committed but change event was not published: %w", err)), Keys: cloneRecordKeys(written)}, nil
	}
	return &pb.WriteRecordRowsRsp{RetInfo: response.Success("success"), Keys: cloneRecordKeys(written)}, nil
}

func (s *Service) normalizeWriteRecordRows(rows []*pb.RecordRow) []*pb.RecordRow {
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			out = append(out, nil)
			continue
		}
		copied := proto.Clone(row).(*pb.RecordRow)
		if copied.Key != nil && strings.TrimSpace(copied.Key.GetVersion()) == "" {
			copied.Key.Version = s.nextRecordVersion().Format(factkey.TimeVersionLayout)
		}
		out = append(out, copied)
	}
	return out
}

func (s *Service) nextRecordVersion() time.Time {
	now := time.Now().UTC()
	if s == nil {
		return now
	}
	s.recordVersionMu.Lock()
	defer s.recordVersionMu.Unlock()
	if !now.After(s.lastRecordVersion) {
		now = s.lastRecordVersion.Add(time.Nanosecond)
	}
	s.lastRecordVersion = now
	return now
}

// UpsertRecordRows is the server-managed Record write path. The request ID is
// part of the idempotency contract and the owner assigns revision metadata.
func (s *Service) UpsertRecordRows(ctx context.Context, req *pb.UpsertRecordRowsReq) (*pb.UpsertRecordRowsRsp, error) {
	if strings.TrimSpace(req.GetRequestId()) == "" {
		return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("request_id is required"))}, nil
	}
	if len(req.GetMutations()) == 0 {
		return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("mutations are required"))}, nil
	}
	targets := make([]*pb.PrimaryStoreTarget, len(req.GetMutations()))
	seen := make(map[string]struct{}, len(targets))
	for index, mutation := range req.GetMutations() {
		if mutation == nil || mutation.GetKey() == nil {
			return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("mutation key is required"))}, nil
		}
		key := mutation.GetKey()
		identity := key.GetSpaceId() + "|" + key.GetDatasetId() + "|" + key.GetRecordId()
		if _, exists := seen[identity]; exists {
			return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("duplicate record identity"))}, nil
		}
		seen[identity] = struct{}{}
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetRecordId())
		if err != nil {
			return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		targets[index] = target
	}
	if _, err := requireSingleRecordCommitSource(targets); err != nil {
		return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED, err)}, nil
	}
	exists := make([]bool, len(req.GetMutations()))
	if s.validator != nil {
		var err error
		exists, err = s.currentRecordMutationExistence(ctx, targets, req.GetMutations())
		if err != nil {
			return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
	}
	for index, mutation := range req.GetMutations() {
		if s.validator != nil {
			if err := s.validator.ValidateRecordMutation(ctx, mutation, exists[index]); err != nil {
				return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
			}
		}
	}
	event, err := s.primary.ApplyRecordMutations(ctx, targets[0], req.GetRequestId(), req.GetMutations())
	if err != nil {
		code := primaryErrorCode(err)
		if strings.Contains(strings.ToLower(err.Error()), "revision conflict") {
			code = pb.ErrorCode_REVISION_CONFLICT
		}
		return &pb.UpsertRecordRowsRsp{RetInfo: response.Error(code, err)}, nil
	}
	if event != nil && s.events != nil {
		if publishErr := s.events.PublishRecordRowsCommitted(ctx, event); publishErr != nil {
			s.reportViewError(ctx, "record_rows_committed_event", publishErr)
		}
	}
	return &pb.UpsertRecordRowsRsp{RetInfo: response.Success("success"), Rows: event.GetRows()}, nil
}

func (s *Service) currentRecordMutationExistence(ctx context.Context, targets []*pb.PrimaryStoreTarget, mutations []*pb.RecordMutation) ([]bool, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	snapshot, err := s.primary.OpenRecordSnapshot(ctx, &pb.OpenRecordSnapshotReq{SourceTarget: targets[0], Mode: pb.RecordReadMode_RECORD_READ_MODE_CURRENT})
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = s.primary.CloseRecordSnapshot(ctx, &pb.CloseRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId()})
	}()
	exists := make([]bool, len(mutations))
	for index, mutation := range mutations {
		target := proto.Clone(targets[index]).(*pb.PrimaryStoreTarget)
		target.DatasetId = mutation.GetKey().GetDatasetId()
		rows, err := s.primary.ReadRecordSnapshot(ctx, &pb.ReadRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId(), Target: target, RecordIds: []string{mutation.GetKey().GetRecordId()}})
		if err != nil {
			return nil, err
		}
		exists[index] = len(rows.GetRows()) > 0
	}
	return exists, nil
}

func requireSingleRecordCommitSource(targets []*pb.PrimaryStoreTarget) (string, error) {
	if len(targets) == 0 {
		return "", errText("record route is empty")
	}
	identity := recordCommitSourceIdentity(targets[0])
	for _, target := range targets[1:] {
		if current := recordCommitSourceIdentity(target); current != identity {
			return "", fmt.Errorf("record batch spans physical sources %q and %q", identity, current)
		}
	}
	return identity, nil
}

func recordCommitSourceIdentity(target *pb.PrimaryStoreTarget) string {
	if target == nil {
		return "<nil>"
	}
	return strings.Join([]string{target.GetNodeId(), target.GetDeviceId(), target.GetEngine(), target.GetEndpoint()}, "|")
}

func (s *Service) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	legacyVersionQuery := req.GetVersionRange() != nil
	for _, key := range req.GetKeys() {
		legacyVersionQuery = legacyVersionQuery || key.GetVersion() != ""
	}
	if req.GetMode() != pb.RecordReadMode_RECORD_READ_MODE_UNSPECIFIED || req.GetRevisionRange() != nil || (!legacyVersionQuery && len(req.GetKeys()) <= 1) {
		return s.readRecordRowsRevision(ctx, req)
	}
	if isRecordDatasetScan(req) {
		return s.scanRecordDataset(ctx, req)
	}
	var mergePlan *multiKeyPagePlan
	if len(req.GetKeys()) > 1 {
		var err error
		mergePlan, err = newMultiKeyPagePlan(req.GetPage(), len(req.GetKeys()), recordPerKeyPageCap(req))
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	var out []*pb.RecordRow
	var sourceHasMore bool
	for _, key := range req.GetKeys() {
		if err := validateRecordKeyTemplate(key); err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		storeKey, err := recordKeyToPrimaryStoreKey(key, true)
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		versionRange := req.GetVersionRange()
		if versionRange != nil {
			storeKey.Version = ""
		}
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetRecordId())
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		page := req.GetPage()
		if mergePlan != nil {
			page = &pb.Page{Page: 1, Size: mergePlan.fetchSize}
		}
		rows, pageResult, err := s.primary.ReadRows(ctx, target, &pb.ReadPrimaryRowsReq{
			AuthInfo:     req.GetAuthInfo(),
			Target:       target,
			Keys:         []*pb.PrimaryStoreKey{storeKey},
			VersionRange: versionRange,
			Order:        req.GetOrder(),
			ColumnNames:  req.GetColumnNames(),
			Page:         page,
		})
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		sourceHasMore = sourceHasMore || pageResult.GetHasMore()
		for _, row := range rows {
			out = append(out, primaryStoreRowToRecordRow(row, key))
		}
		if len(req.GetKeys()) == 1 {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
		}
	}
	sortRecordRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseRecordRows(out)
	}
	out, pageResult := pageMergedRecordRows(out, mergePlan, sourceHasMore)
	return &pb.ReadRecordRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func (s *Service) readRecordRowsRevision(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	mode := req.GetMode()
	if mode == pb.RecordReadMode_RECORD_READ_MODE_UNSPECIFIED {
		mode = pb.RecordReadMode_RECORD_READ_MODE_CURRENT
	}
	if mode != pb.RecordReadMode_RECORD_READ_MODE_CURRENT && mode != pb.RecordReadMode_RECORD_READ_MODE_HISTORY {
		return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("invalid record read mode"))}, nil
	}
	for _, key := range req.GetKeys() {
		if err := validateRecordKey(key, false); err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		if key.GetVersion() != "" {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("record key.version is not accepted; use revision_range"))}, nil
		}
	}
	if len(req.GetKeys()) == 1 && req.GetKeys()[0].GetRecordId() == "" {
		return s.readRecordDatasetRevision(ctx, req, mode)
	}
	rows := make([]*pb.RecordRow, 0)
	for _, key := range req.GetKeys() {
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetRecordId())
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		snapshot, err := s.primary.OpenRecordSnapshot(ctx, &pb.OpenRecordSnapshotReq{SourceTarget: target, Mode: mode})
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		queryTarget := proto.Clone(target).(*pb.PrimaryStoreTarget)
		queryTarget.DatasetId = key.GetDatasetId()
		if mode == pb.RecordReadMode_RECORD_READ_MODE_CURRENT {
			read, readErr := s.primary.ReadRecordSnapshot(ctx, &pb.ReadRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId(), Target: queryTarget, RecordIds: []string{key.GetRecordId()}})
			_ = s.primary.CloseRecordSnapshot(ctx, &pb.CloseRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId()})
			if readErr != nil {
				return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(readErr), readErr)}, nil
			}
			for _, row := range read.GetRows() {
				if revisionInRange(row.GetRevision(), req.GetRevisionRange()) {
					rows = append(rows, row)
				}
			}
			continue
		}
		history, scanErr := s.scanRecordSnapshotAll(ctx, snapshot.GetSnapshotId(), queryTarget)
		_ = s.primary.CloseRecordSnapshot(ctx, &pb.CloseRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId()})
		if scanErr != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(scanErr), scanErr)}, nil
		}
		for _, row := range history {
			if row.GetKey().GetRecordId() == key.GetRecordId() && revisionInRange(row.GetRevision(), req.GetRevisionRange()) {
				rows = append(rows, row)
			}
		}
	}
	sortRecordRows(rows)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseRecordRows(rows)
	}
	rows, page := pageRecordRows(rows, req.GetPage())
	return &pb.ReadRecordRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) readRecordDatasetRevision(ctx context.Context, req *pb.ReadRecordRowsReq, mode pb.RecordReadMode) (*pb.ReadRecordRowsRsp, error) {
	key := req.GetKeys()[0]
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	rows := make([]*pb.RecordRow, 0)
	for _, target := range targets {
		snapshot, err := s.primary.OpenRecordSnapshot(ctx, &pb.OpenRecordSnapshotReq{SourceTarget: target, Mode: mode})
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		queryTarget := proto.Clone(target).(*pb.PrimaryStoreTarget)
		queryTarget.DatasetId = key.GetDatasetId()
		pageRows, scanErr := s.scanRecordSnapshotAll(ctx, snapshot.GetSnapshotId(), queryTarget)
		_ = s.primary.CloseRecordSnapshot(ctx, &pb.CloseRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId()})
		if scanErr != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(scanErr), scanErr)}, nil
		}
		for _, row := range pageRows {
			if revisionInRange(row.GetRevision(), req.GetRevisionRange()) {
				rows = append(rows, primaryStoreRecordClone(row, key))
			}
		}
	}
	sortRecordRows(rows)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseRecordRows(rows)
	}
	rows, page := pageRecordRows(rows, req.GetPage())
	return &pb.ReadRecordRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) scanRecordSnapshotAll(ctx context.Context, snapshotID string, target *pb.PrimaryStoreTarget) ([]*pb.RecordRow, error) {
	var rows []*pb.RecordRow
	page := &pb.Page{Size: primaryDatasetScanPageSize}
	for {
		response, err := s.primary.ScanRecordSnapshot(ctx, &pb.ScanRecordSnapshotReq{SnapshotId: snapshotID, Target: target, Page: page})
		if err != nil {
			return nil, err
		}
		rows = append(rows, response.GetRows()...)
		if response.GetPageResult() == nil || !response.GetPageResult().GetHasMore() {
			return rows, nil
		}
		page = &pb.Page{Size: primaryDatasetScanPageSize, Cursor: response.GetPageResult().GetNextCursor()}
	}
}

func revisionInRange(revision uint64, value *pb.RevisionRange) bool {
	if value == nil {
		return true
	}
	return (value.GetStartRevision() == 0 || revision >= value.GetStartRevision()) && (value.GetEndRevision() == 0 || revision <= value.GetEndRevision())
}

func primaryStoreRecordClone(row *pb.RecordRow, key *pb.RecordKey) *pb.RecordRow {
	clone := proto.Clone(row).(*pb.RecordRow)
	if clone.Key == nil {
		clone.Key = proto.Clone(key).(*pb.RecordKey)
	}
	return clone
}

func isRecordDatasetScan(req *pb.ReadRecordRowsReq) bool {
	keys := req.GetKeys()
	return len(keys) == 1 && strings.TrimSpace(keys[0].GetRecordId()) == ""
}

func (s *Service) scanRecordDataset(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	key := req.GetKeys()[0]
	if err := validateRecordKey(key, false); err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	var out []*pb.RecordRow
	seen := make(map[string]bool)
	for _, target := range targets {
		rows, err := s.scanAllPrimaryRows(ctx, req.GetAuthInfo(), target, pb.DataKind_DATA_KIND_RECORD, req.GetVersionRange(), req.GetColumnNames())
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		for _, row := range rows {
			id := primaryStoreRowID(row)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, primaryStoreRowToRecordRow(row, key))
		}
	}
	sortRecordRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseRecordRows(out)
	}
	out, pageResult := pageRecordRows(out, req.GetPage())
	return &pb.ReadRecordRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func (s *Service) scanRecordDatasetPageRows(ctx context.Context, req *pb.ReadRecordRowsReq) ([]*pb.RecordRow, *pb.PageResult, error) {
	key := req.GetKeys()[0]
	if err := validateRecordKey(key, false); err != nil {
		return nil, nil, err
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return nil, nil, err
	}
	rows, page, err := s.scanPrimaryRowsPage(ctx, req.GetAuthInfo(), targets, pb.DataKind_DATA_KIND_RECORD, req.GetVersionRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return nil, nil, err
	}
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, primaryStoreRowToRecordRow(row, key))
	}
	return out, page, nil
}

func (s *Service) scanAllPrimaryRows(ctx context.Context, auth *pb.AuthInfo, target *pb.PrimaryStoreTarget, kind pb.DataKind, versionRange *pb.VersionRange, columnNames []string) ([]*pb.PrimaryStoreRow, error) {
	var out []*pb.PrimaryStoreRow
	cursor := ""
	for {
		rows, page, err := s.primary.ScanRows(ctx, target, &pb.ScanPrimaryRowsReq{
			AuthInfo:     auth,
			Target:       target,
			DataKind:     kind,
			VersionRange: versionRange,
			ColumnNames:  columnNames,
			Page:         &pb.Page{Size: primaryDatasetScanPageSize, Cursor: cursor},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(out) > maxDatasetScanRows {
			return nil, fmt.Errorf("dataset scan exceeds safe limit %d rows; add subject/freq/time range or query a view", maxDatasetScanRows)
		}
		if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
			break
		}
		cursor = page.GetNextCursor()
	}
	return out, nil
}

func (s *Service) scanPrimaryRowsPage(ctx context.Context, auth *pb.AuthInfo, targets []*pb.PrimaryStoreTarget, kind pb.DataKind, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	if len(targets) == 0 {
		return nil, &pb.PageResult{}, nil
	}
	size := page.GetSize()
	if size == 0 || size > primaryDatasetScanPageSize {
		size = primaryDatasetScanPageSize
	}
	targetIndex, innerCursor, err := decodePrimaryScanCursor(page.GetCursor())
	if err != nil {
		return nil, nil, err
	}
	if targetIndex >= len(targets) {
		return nil, &pb.PageResult{Page: page.GetPage(), Size: size}, nil
	}
	var out []*pb.PrimaryStoreRow
	for targetIndex < len(targets) && uint32(len(out)) < size {
		remaining := size - uint32(len(out))
		rows, next, err := s.primary.ScanRows(ctx, targets[targetIndex], &pb.ScanPrimaryRowsReq{
			AuthInfo:     auth,
			Target:       targets[targetIndex],
			DataKind:     kind,
			VersionRange: versionRange,
			ColumnNames:  columnNames,
			Page:         &pb.Page{Size: remaining, Cursor: innerCursor},
		})
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rows...)
		if next != nil && next.GetHasMore() && next.GetNextCursor() != "" {
			return out, &pb.PageResult{Page: page.GetPage(), Size: size, HasMore: true, NextCursor: encodePrimaryScanCursor(targetIndex, next.GetNextCursor())}, nil
		}
		targetIndex++
		innerCursor = ""
	}
	if targetIndex < len(targets) {
		return out, &pb.PageResult{Page: page.GetPage(), Size: size, HasMore: true, NextCursor: encodePrimaryScanCursor(targetIndex, "")}, nil
	}
	return out, &pb.PageResult{Page: page.GetPage(), Size: size}, nil
}

func decodePrimaryScanCursor(raw string) (int, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", nil
	}
	target, cursor, ok := strings.Cut(raw, "|")
	if !ok {
		return 0, raw, nil
	}
	idx, err := strconv.Atoi(target)
	if err != nil || idx < 0 {
		return 0, "", fmt.Errorf("invalid scan cursor %q", raw)
	}
	return idx, cursor, nil
}

func encodePrimaryScanCursor(targetIndex int, innerCursor string) string {
	return fmt.Sprintf("%d|%s", targetIndex, innerCursor)
}

func primaryErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "engine_capability_unsupported") ||
		(strings.Contains(text, "unsupported") && strings.Contains(text, "engine")) {
		return pb.ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED
	}
	if strings.Contains(text, "invalid_param") ||
		strings.Contains(text, " is required") ||
		strings.Contains(text, "invalid ") {
		return pb.ErrorCode_INVALID_PARAM
	}
	return pb.ErrorCode_INNER_ERR
}

func groupRowsErrorCode(err error) pb.ErrorCode {
	if primaryErrorCode(err) == pb.ErrorCode_INVALID_PARAM {
		return pb.ErrorCode_INVALID_PARAM
	}
	return pb.ErrorCode_ROUTE_NOT_FOUND
}

// routedRows 保存路由到同一主存目标的一批写入行。
type routedRows struct {
	target         *pb.PrimaryStoreTarget
	rows           []*pb.PrimaryStoreRow
	timeSeriesKeys []*pb.TimeSeriesKey
	recordKeys     []*pb.RecordKey
}

func (s *Service) groupTimeSeriesRowsByPrimaryStoreTarget(ctx context.Context, rows []*pb.TimeSeriesRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	var order []string
	resolved := make(map[string]*pb.PrimaryStoreTarget)
	for _, row := range rows {
		converted, err := timeSeriesRowToPrimaryStoreRow(row)
		if err != nil {
			return nil, err
		}
		key := row.GetKey()
		routeKey := key.GetSpaceId() + "|" + key.GetDatasetId() + "|" + key.GetSubjectId()
		target, ok := resolved[routeKey]
		if !ok {
			var err error
			target, err = s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetSubjectId())
			if err != nil {
				return nil, err
			}
			resolved[routeKey] = target
		}
		groupKey := target.GetNodeId() + "|" + target.GetEngine() + "|" + target.GetDeviceTable()
		group := groups[groupKey]
		if group == nil {
			group = &routedRows{target: target}
			groups[groupKey] = group
			order = append(order, groupKey)
		}
		group.rows = append(group.rows, converted)
		group.timeSeriesKeys = append(group.timeSeriesKeys, proto.Clone(key).(*pb.TimeSeriesKey))
	}
	out := make([]*routedRows, 0, len(groups))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out, nil
}

func (s *Service) groupRecordRowsByPrimaryStoreTarget(ctx context.Context, rows []*pb.RecordRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	var order []string
	resolved := make(map[string]*pb.PrimaryStoreTarget)
	for _, row := range rows {
		converted, err := recordRowToPrimaryStoreRow(row)
		if err != nil {
			return nil, err
		}
		key := row.GetKey()
		routeKey := key.GetSpaceId() + "|" + key.GetDatasetId() + "|" + key.GetRecordId()
		target, ok := resolved[routeKey]
		if !ok {
			var err error
			target, err = s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetRecordId())
			if err != nil {
				return nil, err
			}
			resolved[routeKey] = target
		}
		groupKey := target.GetNodeId() + "|" + target.GetEngine() + "|" + target.GetDeviceTable()
		group := groups[groupKey]
		if group == nil {
			group = &routedRows{target: target}
			groups[groupKey] = group
			order = append(order, groupKey)
		}
		group.rows = append(group.rows, converted)
		group.recordKeys = append(group.recordKeys, proto.Clone(key).(*pb.RecordKey))
	}
	out := make([]*routedRows, 0, len(groups))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out, nil
}

func (s *Service) publishTimeSeriesRowsChanged(ctx context.Context, keys []*pb.TimeSeriesKey) error {
	if len(keys) == 0 || s.events == nil {
		return nil
	}
	return s.events.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId:   xid.New().String(),
		EventTime: time.Now().Format(time.RFC3339Nano),
		Keys:      cloneTimeSeriesKeys(keys),
	})
}

func (s *Service) publishRecordRowsChanged(ctx context.Context, keys []*pb.RecordKey) error {
	if len(keys) == 0 || s.events == nil {
		return nil
	}
	return s.events.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		EventId:   xid.New().String(),
		EventTime: time.Now().Format(time.RFC3339Nano),
		Keys:      cloneRecordKeys(keys),
	})
}

func (s *Service) reportViewError(ctx context.Context, stage string, err error) {
	if s == nil || s.report == nil || err == nil {
		return
	}
	s.report(ctx, stage, err)
}

func cloneTimeSeriesKeys(keys []*pb.TimeSeriesKey) []*pb.TimeSeriesKey {
	out := make([]*pb.TimeSeriesKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, proto.Clone(key).(*pb.TimeSeriesKey))
	}
	return out
}

func cloneRecordKeys(keys []*pb.RecordKey) []*pb.RecordKey {
	out := make([]*pb.RecordKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, proto.Clone(key).(*pb.RecordKey))
	}
	return out
}
