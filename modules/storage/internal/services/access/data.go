package access

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	if req.GetWriteMode() == pb.RowWriteMode_ROW_WRITE_MODE_REPLACE {
		for _, group := range groups {
			for _, row := range group.rows {
				row.WriteMode = pb.RowWriteMode_ROW_WRITE_MODE_REPLACE
			}
		}
	}
	for _, group := range groups {
		message, event, err := timeSeriesUpdateMessage(group)
		if err != nil {
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		if err := s.writeRoutedRows(ctx, group, message); err != nil {
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		if event != nil && !s.hasMessageWriter() && s.events != nil {
			if err := s.events.PublishTimeSeriesRowsUpdated(ctx, event); err != nil {
				s.reportViewError(ctx, "time_series_rows_updated", err)
			}
		}
	}
	return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) DeleteTimeSeriesRows(ctx context.Context, req *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if s == nil || s.metadataReader == nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("metadata reader is unavailable"))}, nil
	}
	dataset, err := s.metadataReader.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	if dataset == nil || dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset must be time_series"))}, nil
	}
	if err := validateTimeRange(req.GetTimeRange()); err != nil || req.GetTimeRange().GetEndTime() == "" {
		if err == nil {
			err = errors.New("delete requires an end_time")
		}
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rangeValue, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	page := req.GetPage()
	size := page.GetSize()
	if size == 0 {
		size = primaryDatasetScanPageSize
	}
	if size > primaryDatasetScanPageSize {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("delete batch size must be <= 1000"))}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	deleted := uint32(0)
	for _, target := range targets {
		rows, _, scanErr := s.primary.ScanRows(ctx, target, &pb.ScanPrimaryRowsReq{Target: target, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, VersionRange: rangeValue, Order: pb.SortOrder_SORT_ORDER_ASC, Page: &pb.Page{Page: 1, Size: size}})
		if scanErr != nil {
			return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, scanErr)}, nil
		}
		keys := make([]*pb.PrimaryStoreKey, 0, len(rows))
		for _, row := range rows {
			if row != nil {
				keys = append(keys, row.GetKey())
			}
		}
		if len(keys) == 0 {
			continue
		}
		deleter, ok := s.primary.(primary.Deleter)
		if !ok {
			return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, errors.New("primary store deletion is unavailable"))}, nil
		}
		if err := deleter.DeleteRows(ctx, target, keys); err != nil {
			return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		deleted += uint32(len(keys))
	}
	return &pb.DeleteTimeSeriesRowsRsp{RetInfo: response.Success("success"), Deleted: deleted}, nil
}

func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if err := validateTimeRange(req.GetTimeRange()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if isTimeSeriesSubjectScan(req) {
		return s.scanTimeSeriesSubject(ctx, req)
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

func isTimeSeriesSubjectScan(req *pb.ReadTimeSeriesRowsReq) bool {
	keys := req.GetKeys()
	if len(keys) != 1 {
		return false
	}
	key := keys[0]
	return key != nil &&
		strings.TrimSpace(key.GetSubjectId()) != "" &&
		strings.TrimSpace(key.GetFreq()) != "" &&
		strings.TrimSpace(key.GetDataTime()) == "" &&
		len(key.GetDimensions()) == 0
}

func (s *Service) scanTimeSeriesSubject(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	key := req.GetKeys()[0]
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	if err := validateTimeSeriesKeyTemplate(key); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetSubjectId())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	page := req.GetPage()
	size := page.GetSize()
	if size == 0 || size > primaryDatasetScanPageSize {
		size = primaryDatasetScanPageSize
	}
	cursor := page.GetCursor()
	rows := make([]*pb.TimeSeriesRow, 0, size)
	var hasMore bool
	var nextCursor string
	for len(rows) < int(size) {
		primaryRows, primaryPage, scanErr := s.primary.ScanRows(ctx, target, &pb.ScanPrimaryRowsReq{
			AuthInfo: req.GetAuthInfo(), Target: target, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
			VersionRange: versionRange, ColumnNames: req.GetColumnNames(), Order: req.GetOrder(),
			KeyPrefix: factkey.EscapePart(key.GetSubjectId()) + "%7C" + factkey.EscapePart(key.GetFreq()) + "%7C",
			Page:      &pb.Page{Size: primaryDatasetScanPageSize, Cursor: cursor},
		})
		if scanErr != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(scanErr), scanErr)}, nil
		}
		for _, row := range primaryRows {
			if row == nil || row.GetKey() == nil {
				continue
			}
			subjectID, freq, _, parseErr := factkey.ParseTimeSeriesDataKey(row.GetKey().GetKey())
			if parseErr != nil || subjectID != key.GetSubjectId() || freq != key.GetFreq() {
				continue
			}
			rows = append(rows, primaryStoreRowToTimeSeriesRow(row, key))
			if len(rows) == int(size) {
				break
			}
		}
		if primaryPage == nil || !primaryPage.GetHasMore() || primaryPage.GetNextCursor() == "" {
			break
		}
		hasMore = true
		nextCursor = primaryPage.GetNextCursor()
		cursor = nextCursor
	}
	result := &pb.PageResult{Page: page.GetPage(), Size: size, HasMore: hasMore, NextCursor: nextCursor}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: result}, nil
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
	if req.GetWriteMode() == pb.RowWriteMode_ROW_WRITE_MODE_REPLACE {
		for _, group := range groups {
			for _, row := range group.rows {
				row.WriteMode = pb.RowWriteMode_ROW_WRITE_MODE_REPLACE
			}
		}
	}
	for _, group := range groups {
		message, event, err := recordUpdateMessage(group)
		if err != nil {
			return &pb.WriteRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		if err := s.writeRoutedRows(ctx, group, message); err != nil {
			return &pb.WriteRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		if event != nil && !s.hasMessageWriter() && s.events != nil {
			if err := s.events.PublishRecordRowsUpdated(ctx, event); err != nil {
				s.reportViewError(ctx, "record_rows_updated", err)
			}
		}
	}
	var written []*pb.RecordKey
	for _, group := range groups {
		written = append(written, group.recordKeys...)
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
	timeSeriesRows []*pb.TimeSeriesRow
	recordRows     []*pb.RecordRow
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
		group.timeSeriesRows = append(group.timeSeriesRows, proto.Clone(row).(*pb.TimeSeriesRow))
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
		group.recordRows = append(group.recordRows, proto.Clone(row).(*pb.RecordRow))
		group.recordKeys = append(group.recordKeys, proto.Clone(key).(*pb.RecordKey))
	}
	out := make([]*routedRows, 0, len(groups))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out, nil
}

func (s *Service) hasMessageWriter() bool { _, ok := s.primary.(primary.MessageWriter); return ok }

func (s *Service) writeRoutedRows(ctx context.Context, group *routedRows, message []byte) error {
	if writer, ok := s.primary.(primary.MessageWriter); ok {
		return writer.WriteRowsWithMessage(ctx, group.target, group.rows, message)
	}
	return s.primary.WriteRows(ctx, group.target, group.rows)
}

func timeSeriesUpdateMessage(group *routedRows) ([]byte, *pb.TimeSeriesRowsUpdated, error) {
	if group == nil || len(group.timeSeriesRows) == 0 {
		return nil, nil, errors.New("time-series rows are required")
	}
	id := xid.New().String()
	now := time.Now().UTC()
	event := &pb.TimeSeriesRowsUpdated{MessageId: id, WrittenAt: now.Format(time.RFC3339Nano), SpaceId: group.target.GetSpaceId(), DatasetId: group.timeSeriesRows[0].GetKey().GetDatasetId(), Rows: cloneTimeSeriesRows(group.timeSeriesRows)}
	raw, err := marshalUpdateMessage(id, "moox.storage.time_series.rows_updated.v1", event, group.target.GetSpaceId(), group.target.GetNodeId(), now)
	return raw, event, err
}

func recordUpdateMessage(group *routedRows) ([]byte, *pb.RecordRowsUpdated, error) {
	if group == nil || len(group.recordRows) == 0 {
		return nil, nil, errors.New("record rows are required")
	}
	id := xid.New().String()
	now := time.Now().UTC()
	event := &pb.RecordRowsUpdated{MessageId: id, WrittenAt: now.Format(time.RFC3339Nano), SpaceId: group.target.GetSpaceId(), DatasetId: group.recordRows[0].GetKey().GetDatasetId(), Rows: cloneRecordRows(group.recordRows)}
	raw, err := marshalUpdateMessage(id, "moox.storage.record.rows_updated.v1", event, group.target.GetSpaceId(), group.target.GetNodeId(), now)
	return raw, event, err
}

func marshalUpdateMessage(id, topic string, payload proto.Message, spaceID, nodeID string, now time.Time) ([]byte, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, err
	}
	msg := &messagepb.MooxMessage{ProtocolVersion: jetstream.ProtocolVersion, MessageId: id, Topic: topic, Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, Producer: &messagepb.Producer{ServiceName: "moox-storage", InstanceId: firstNonEmpty(nodeID, "storage")}, SpaceId: spaceID, OccurredAt: timestamppb.New(now), PublishedAt: timestamppb.New(now), ContentType: "application/protobuf", Payload: data}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func cloneTimeSeriesRows(rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, proto.Clone(row).(*pb.TimeSeriesRow))
		}
	}
	return out
}
func cloneRecordRows(rows []*pb.RecordRow) []*pb.RecordRow {
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, proto.Clone(row).(*pb.RecordRow))
		}
	}
	return out
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
