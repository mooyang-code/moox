package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/rowkey"
	primary "github.com/mooyang-code/moox/modules/storage/internal/service/datashard"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

const maxDatasetScanRows = 10000

const primaryDatasetScanPageSize = uint32(1000)

func (s *Service) MergeTimeSeriesRows(ctx context.Context, req *pb.MergeTimeSeriesRowsReq) (*pb.MergeTimeSeriesRowsRsp, error) {
	if err := s.validator.ValidateMergeTimeSeriesRows(ctx, req.GetRows()); err != nil {
		return &pb.MergeTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	s.topologyMu.Lock()
	defer s.topologyMu.Unlock()
	groups, err := s.groupTimeSeriesRowsByShardTarget(ctx, req.GetRows())
	if err != nil {
		return &pb.MergeTimeSeriesRowsRsp{RetInfo: retinfo.Error(groupRowsErrorCode(err), err)}, nil
	}
	written := make([]string, 0, len(req.GetRows()))
	for _, group := range groups {
		if err := s.writeRoutedRows(ctx, group); err != nil {
			return &pb.MergeTimeSeriesRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err), WrittenKeys: written}, nil
		}
		written = append(written, timeSeriesWrittenKeys(group.timeSeriesKeys)...)
	}
	return &pb.MergeTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), WrittenKeys: written}, nil
}

func timeSeriesWrittenKeys(keys []*pb.TimeSeriesKey) []string {
	written := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		written = append(written, strings.Join([]string{
			rowkey.EscapePart(key.GetSpaceId()),
			rowkey.EscapePart(key.GetDatasetId()),
			rowkey.EscapePart(key.GetSubjectId()),
			rowkey.EscapePart(key.GetFreq()),
			rowkey.EscapePart(key.GetDataTime()),
		}, "|"))
	}
	return written
}

func (s *Service) DeleteTimeSeriesRows(ctx context.Context, req *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if s == nil || s.metadataReader == nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("metadata reader is unavailable"))}, nil
	}
	dataset, err := s.metadataReader.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	if dataset == nil || dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset must be time_series"))}, nil
	}
	if err := validateTimeRange(req.GetTimeRange()); err != nil || req.GetTimeRange().GetEndTime() == "" {
		if err == nil {
			err = errors.New("delete requires an end_time")
		}
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	rangeValue, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	page := req.GetPage()
	size := page.GetSize()
	pageNo := page.GetPage()
	if pageNo == 0 {
		pageNo = 1
	}
	if size == 0 {
		size = primaryDatasetScanPageSize
	}
	if size > primaryDatasetScanPageSize {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("delete batch size must be <= 1000"))}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	deleted := uint32(0)
	for _, target := range targets {
		rows, _, scanErr := s.primary.ScanRows(ctx, target, &pb.ScanRowsReq{Target: target, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, VersionRange: rangeValue, Order: pb.SortOrder_SORT_ORDER_ASC, Page: &pb.Page{Page: pageNo, Size: size}})
		if scanErr != nil {
			return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, scanErr)}, nil
		}
		keys := make([]*pb.ShardKey, 0, len(rows))
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
			return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("primary store deletion is unavailable"))}, nil
		}
		if err := deleter.DeleteRows(ctx, target, keys); err != nil {
			return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		deleted += uint32(len(keys))
	}
	return &pb.DeleteTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Deleted: deleted}, nil
}

func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if err := validateTimeRange(req.GetTimeRange()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
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
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	var out []*pb.TimeSeriesRow
	var sourceHasMore bool
	for _, key := range req.GetKeys() {
		if err := validateTimeSeriesKeyTemplate(key); err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		storeKey, err := timeSeriesKeyToShardKey(key, false)
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		if versionRange != nil {
			storeKey.Version = ""
		}
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetSubjectId())
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		page := req.GetPage()
		if mergePlan != nil {
			page = &pb.Page{Page: 1, Size: mergePlan.fetchSize}
		}
		rows, pageResult, err := s.primary.ReadRows(ctx, target, &pb.ReadRowsReq{
			AuthInfo:     req.GetAuthInfo(),
			Target:       target,
			Keys:         []*pb.ShardKey{storeKey},
			VersionRange: versionRange,
			Order:        req.GetOrder(),
			ColumnNames:  req.GetColumnNames(),
			Page:         page,
		})
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
		}
		sourceHasMore = sourceHasMore || pageResult.GetHasMore()
		for _, row := range rows {
			out = append(out, primaryStoreRowToTimeSeriesRow(row, key))
		}
		if len(req.GetKeys()) == 1 {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: pageResult}, nil
		}
	}
	sortTimeSeriesRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseTimeSeriesRows(out)
	}
	out, pageResult := pageMergedTimeSeriesRows(out, mergePlan, sourceHasMore)
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: pageResult}, nil
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
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	if err := validateTimeSeriesKeyTemplate(key); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetSubjectId())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
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
		remaining := uint32(int(size) - len(rows))
		primaryRows, primaryPage, scanErr := s.primary.ScanRows(ctx, target, &pb.ScanRowsReq{
			AuthInfo: req.GetAuthInfo(), Target: target, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
			VersionRange: versionRange, ColumnNames: req.GetColumnNames(), Order: req.GetOrder(),
			KeyPrefix: rowkey.EscapePart(key.GetSubjectId()) + "%7C" + rowkey.EscapePart(key.GetFreq()) + "%7C",
			Page:      &pb.Page{Size: remaining, Cursor: cursor},
		})
		if scanErr != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(scanErr), scanErr)}, nil
		}
		for _, row := range primaryRows {
			if row == nil || row.GetKey() == nil {
				continue
			}
			subjectID, freq, _, parseErr := rowkey.ParseTimeSeriesDataKey(row.GetKey().GetKey())
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
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows, PageResult: result}, nil
}

func (s *Service) scanTimeSeriesDataset(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	key := req.GetKeys()[0]
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	var out []*pb.TimeSeriesRow
	seen := make(map[string]bool)
	for _, target := range targets {
		rows, err := s.scanAllPrimaryRows(ctx, req.GetAuthInfo(), target, pb.DataKind_DATA_KIND_TIME_SERIES, versionRange, req.GetColumnNames())
		if err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
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
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: pageResult}, nil
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

func (s *Service) MergeRecordRows(ctx context.Context, req *pb.MergeRecordRowsReq) (*pb.MergeRecordRowsRsp, error) {
	rows := s.normalizeMergeRecordRows(req.GetRows())
	if err := s.validator.ValidateMergeRecordRows(ctx, rows); err != nil {
		return &pb.MergeRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	s.topologyMu.Lock()
	defer s.topologyMu.Unlock()
	groups, err := s.groupRecordRowsByShardTarget(ctx, rows)
	if err != nil {
		return &pb.MergeRecordRowsRsp{RetInfo: retinfo.Error(groupRowsErrorCode(err), err)}, nil
	}
	var written []*pb.RecordKey
	for _, group := range groups {
		if err := s.writeRoutedRows(ctx, group); err != nil {
			return &pb.MergeRecordRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err), Keys: cloneRecordKeys(written)}, nil
		}
		written = append(written, group.recordKeys...)
	}
	return &pb.MergeRecordRowsRsp{RetInfo: retinfo.Success("success"), Keys: cloneRecordKeys(written)}, nil
}

func (s *Service) normalizeMergeRecordRows(rows []*pb.RecordRow) []*pb.RecordRow {
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			out = append(out, nil)
			continue
		}
		copied := proto.Clone(row).(*pb.RecordRow)
		if copied.Key != nil && strings.TrimSpace(copied.Key.GetVersion()) == "" {
			copied.Key.Version = s.nextRecordVersion().Format(rowkey.TimeVersionLayout)
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
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	}
	var out []*pb.RecordRow
	var sourceHasMore bool
	for _, key := range req.GetKeys() {
		if err := validateRecordKeyTemplate(key); err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		storeKey, err := recordKeyToShardKey(key, true)
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		versionRange := req.GetVersionRange()
		if versionRange != nil {
			storeKey.Version = ""
		}
		target, err := s.router.Resolve(ctx, key.GetSpaceId(), key.GetDatasetId(), key.GetRecordId())
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
		}
		page := req.GetPage()
		if mergePlan != nil {
			page = &pb.Page{Page: 1, Size: mergePlan.fetchSize}
		}
		rows, pageResult, err := s.primary.ReadRows(ctx, target, &pb.ReadRowsReq{
			AuthInfo:     req.GetAuthInfo(),
			Target:       target,
			Keys:         []*pb.ShardKey{storeKey},
			VersionRange: versionRange,
			Order:        req.GetOrder(),
			ColumnNames:  req.GetColumnNames(),
			Page:         page,
		})
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
		}
		sourceHasMore = sourceHasMore || pageResult.GetHasMore()
		for _, row := range rows {
			out = append(out, primaryStoreRowToRecordRow(row, key))
		}
		if len(req.GetKeys()) == 1 {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: pageResult}, nil
		}
	}
	sortRecordRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseRecordRows(out)
	}
	out, pageResult := pageMergedRecordRows(out, mergePlan, sourceHasMore)
	return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func isRecordDatasetScan(req *pb.ReadRecordRowsReq) bool {
	keys := req.GetKeys()
	return len(keys) == 1 && strings.TrimSpace(keys[0].GetRecordId()) == ""
}

func (s *Service) scanRecordDataset(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	key := req.GetKeys()[0]
	if err := validateRecordKey(key, false); err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	var out []*pb.RecordRow
	seen := make(map[string]bool)
	for _, target := range targets {
		rows, err := s.scanAllPrimaryRows(ctx, req.GetAuthInfo(), target, pb.DataKind_DATA_KIND_RECORD, req.GetVersionRange(), req.GetColumnNames())
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
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
	return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: pageResult}, nil
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

func (s *Service) scanAllPrimaryRows(ctx context.Context, auth *pb.AuthInfo, target *pb.ShardTarget, kind pb.DataKind, versionRange *pb.VersionRange, columnNames []string) ([]*pb.ShardRow, error) {
	var out []*pb.ShardRow
	cursor := ""
	for {
		rows, page, err := s.primary.ScanRows(ctx, target, &pb.ScanRowsReq{
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

func (s *Service) scanPrimaryRowsPage(ctx context.Context, auth *pb.AuthInfo, targets []*pb.ShardTarget, kind pb.DataKind, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
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
	var out []*pb.ShardRow
	for targetIndex < len(targets) && uint32(len(out)) < size {
		remaining := size - uint32(len(out))
		rows, next, err := s.primary.ScanRows(ctx, targets[targetIndex], &pb.ScanRowsReq{
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
	target         *pb.ShardTarget
	rows           []*pb.ShardRow
	timeSeriesRows []*pb.TimeSeriesRow
	recordRows     []*pb.RecordRow
	timeSeriesKeys []*pb.TimeSeriesKey
	recordKeys     []*pb.RecordKey
}

func (s *Service) groupTimeSeriesRowsByShardTarget(ctx context.Context, rows []*pb.TimeSeriesRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	var order []string
	resolved := make(map[string]*pb.ShardTarget)
	for _, row := range rows {
		converted, err := timeSeriesRowToShardRow(row)
		if err != nil {
			return nil, err
		}
		key := row.GetKey()
		if err := s.lockDatasetTopology(ctx, key.GetSpaceId(), key.GetDatasetId()); err != nil {
			return nil, err
		}
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

func (s *Service) groupRecordRowsByShardTarget(ctx context.Context, rows []*pb.RecordRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	var order []string
	resolved := make(map[string]*pb.ShardTarget)
	for _, row := range rows {
		converted, err := recordRowToShardRow(row)
		if err != nil {
			return nil, err
		}
		key := row.GetKey()
		if err := s.lockDatasetTopology(ctx, key.GetSpaceId(), key.GetDatasetId()); err != nil {
			return nil, err
		}
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

func (s *Service) writeRoutedRows(ctx context.Context, group *routedRows) error {
	if err := s.primary.WriteRows(ctx, group.target, group.rows); err != nil {
		return err
	}
	if locker, ok := s.metadata.(interface {
		LockDatasetTopology(context.Context, string, string) error
	}); ok && group != nil && group.target != nil {
		// Keep this idempotent fallback for internal maintenance callers that
		// construct routedRows directly.
		if err := locker.LockDatasetTopology(ctx, group.target.GetSpaceId(), group.target.GetDatasetId()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) lockDatasetTopology(ctx context.Context, spaceID, datasetID string) error {
	if locker, ok := s.metadata.(interface {
		LockDatasetTopology(context.Context, string, string) error
	}); ok {
		return locker.LockDatasetTopology(ctx, spaceID, datasetID)
	}
	return nil
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
