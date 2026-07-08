package access

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

func (s *Service) WriteTimeSeriesRows(ctx context.Context, req *pb.WriteTimeSeriesRowsReq) (*pb.WriteTimeSeriesRowsRsp, error) {
	if err := s.validator.ValidateWriteTimeSeriesRows(ctx, req.GetRows()); err != nil {
		return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	groups, err := s.groupTimeSeriesRowsByPrimaryStoreTarget(ctx, req.GetRows())
	if err != nil {
		return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(groupRowsErrorCode(err), err)}, nil
	}
	for _, group := range groups {
		if err := s.primary.WriteRows(ctx, group.target, group.rows); err != nil {
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		if err := s.publishTimeSeriesRowsUpdated(ctx, group.timeSeriesRows); err != nil {
			s.reportViewError(ctx, "time_series_rows_updated_journal", err)
		}
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
	var out []*pb.TimeSeriesRow
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
		if len(req.GetKeys()) > 1 {
			page = nil
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
	out, pageResult := pageTimeSeriesRows(out, req.GetPage())
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
			return &pb.WriteRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		written = append(written, group.recordKeys...)
		if err := s.publishRecordRowsUpdated(ctx, group.recordRows); err != nil {
			s.reportViewError(ctx, "record_rows_updated_journal", err)
		}
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

func (s *Service) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	if isRecordDatasetScan(req) {
		return s.scanRecordDataset(ctx, req)
	}
	var out []*pb.RecordRow
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
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), "")
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		page := req.GetPage()
		if len(req.GetKeys()) > 1 {
			page = nil
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
	out, pageResult := pageRecordRows(out, req.GetPage())
	return &pb.ReadRecordRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
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

func (s *Service) scanAllPrimaryRows(ctx context.Context, auth *pb.AuthInfo, target *pb.PrimaryStoreTarget, kind pb.DataKind, versionRange *pb.VersionRange, columnNames []string) ([]*pb.PrimaryStoreRow, error) {
	const pageSize = uint32(1000)
	var out []*pb.PrimaryStoreRow
	cursor := ""
	for {
		rows, page, err := s.primary.ScanRows(ctx, target, &pb.ScanPrimaryRowsReq{
			AuthInfo:     auth,
			Target:       target,
			DataKind:     kind,
			VersionRange: versionRange,
			ColumnNames:  columnNames,
			Page:         &pb.Page{Size: pageSize, Cursor: cursor},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
			break
		}
		cursor = page.GetNextCursor()
	}
	return out, nil
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
	timeSeriesRows []*pb.TimeSeriesRow
	recordKeys     []*pb.RecordKey
	recordRows     []*pb.RecordRow
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
		group.timeSeriesRows = append(group.timeSeriesRows, proto.Clone(row).(*pb.TimeSeriesRow))
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
		routeKey := key.GetSpaceId() + "|" + key.GetDatasetId()
		target, ok := resolved[routeKey]
		if !ok {
			var err error
			target, err = s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), "")
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
		group.recordRows = append(group.recordRows, proto.Clone(row).(*pb.RecordRow))
	}
	out := make([]*routedRows, 0, len(groups))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out, nil
}

func (s *Service) publishTimeSeriesRowsUpdated(ctx context.Context, rows []*pb.TimeSeriesRow) error {
	if len(rows) == 0 || s.events == nil {
		return nil
	}
	for _, group := range groupTimeSeriesRowsForJournal(rows) {
		if err := s.events.PublishTimeSeriesRowsUpdated(ctx, &pb.TimeSeriesRowsUpdated{
			MessageId: xid.New().String(),
			WrittenAt: time.Now().UTC().Format(time.RFC3339Nano),
			SpaceId:   group.spaceID,
			DatasetId: group.datasetID,
			Rows:      cloneTimeSeriesRows(group.rows),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) publishRecordRowsUpdated(ctx context.Context, rows []*pb.RecordRow) error {
	if len(rows) == 0 || s.events == nil {
		return nil
	}
	for _, group := range groupRecordRowsForJournal(rows) {
		if err := s.events.PublishRecordRowsUpdated(ctx, &pb.RecordRowsUpdated{
			MessageId: xid.New().String(),
			WrittenAt: time.Now().UTC().Format(time.RFC3339Nano),
			SpaceId:   group.spaceID,
			DatasetId: group.datasetID,
			Rows:      cloneRecordRows(group.rows),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) currentTimeSeriesRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	reader := s.timeSeriesFactReaderOrDefault()
	var out []*pb.TimeSeriesRow
	for _, key := range keys {
		rsp, err := reader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{key}})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return nil, errText(rsp.GetRetInfo().GetMsg())
		}
		out = append(out, rsp.GetRows()...)
	}
	return out, nil
}

func (s *Service) waitForAsyncJobs() {
	s.asyncWG.Wait()
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

type timeSeriesJournalGroup struct {
	spaceID   string
	datasetID string
	rows      []*pb.TimeSeriesRow
}

func groupTimeSeriesRowsForJournal(rows []*pb.TimeSeriesRow) []timeSeriesJournalGroup {
	positions := make(map[string]int)
	groups := make([]timeSeriesJournalGroup, 0)
	for _, row := range rows {
		key := row.GetKey()
		if key == nil {
			continue
		}
		groupKey := key.GetSpaceId() + "|" + key.GetDatasetId()
		idx, ok := positions[groupKey]
		if !ok {
			idx = len(groups)
			positions[groupKey] = idx
			groups = append(groups, timeSeriesJournalGroup{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()})
		}
		groups[idx].rows = append(groups[idx].rows, row)
	}
	return groups
}

type recordJournalGroup struct {
	spaceID   string
	datasetID string
	rows      []*pb.RecordRow
}

func groupRecordRowsForJournal(rows []*pb.RecordRow) []recordJournalGroup {
	positions := make(map[string]int)
	groups := make([]recordJournalGroup, 0)
	for _, row := range rows {
		key := row.GetKey()
		if key == nil {
			continue
		}
		groupKey := key.GetSpaceId() + "|" + key.GetDatasetId()
		idx, ok := positions[groupKey]
		if !ok {
			idx = len(groups)
			positions[groupKey] = idx
			groups = append(groups, recordJournalGroup{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()})
		}
		groups[idx].rows = append(groups[idx].rows, row)
	}
	return groups
}

func cloneTimeSeriesRows(rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		out = append(out, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return out
}

func cloneRecordRows(rows []*pb.RecordRow) []*pb.RecordRow {
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		out = append(out, proto.Clone(row).(*pb.RecordRow))
	}
	return out
}
