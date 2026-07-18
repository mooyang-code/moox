package primarystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/rowkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

const (
	reservedAttributePrefix       = "__moox_"
	timeSeriesDimensionsAttribute = reservedAttributePrefix + "time_series_dimensions"
	maxMultiKeyMergeRows          = 10000
)

type multiKeyPagePlan struct {
	pageNo    uint32
	size      uint32
	start     int
	fetchSize uint32
}

func newMultiKeyPagePlan(page *pb.Page, keyCount int, perKeyCap uint32) (*multiKeyPagePlan, error) {
	pageNo := uint32(1)
	size := uint32(1000)
	start := uint64(0)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
		if page.GetCursor() != "" {
			offset, err := strconv.ParseUint(page.GetCursor(), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid page cursor %q", page.GetCursor())
			}
			start = offset
		}
	}
	if start == 0 {
		start = uint64(pageNo-1) * uint64(size)
	}
	fetchSize := start + uint64(size) + 1
	if perKeyCap > 0 && fetchSize > uint64(perKeyCap) {
		fetchSize = uint64(perKeyCap)
	}
	mergeRows := fetchSize * uint64(keyCount)
	if keyCount <= 0 || mergeRows > uint64(maxMultiKeyMergeRows) {
		return nil, fmt.Errorf("multi-key page requires merging %d rows, limit is %d; reduce page, size, or key count", mergeRows, maxMultiKeyMergeRows)
	}
	return &multiKeyPagePlan{pageNo: pageNo, size: size, start: int(start), fetchSize: uint32(fetchSize)}, nil
}

func timeSeriesPerKeyPageCap(req *pb.ReadTimeSeriesRowsReq) uint32 {
	versionRange, err := timeRangeToVersionRange(req.GetTimeRange())
	if err != nil {
		return 0
	}
	if versionRange != nil {
		if versionRange.GetStartVersion() != "" && versionRange.GetStartVersion() == versionRange.GetEndVersion() {
			return 1
		}
		return 0
	}
	for _, key := range req.GetKeys() {
		if strings.TrimSpace(key.GetDataTime()) == "" {
			return 0
		}
	}
	return 1
}

func recordPerKeyPageCap(req *pb.ReadRecordRowsReq) uint32 {
	versionRange := req.GetVersionRange()
	if versionRange == nil {
		for _, key := range req.GetKeys() {
			if strings.TrimSpace(key.GetVersion()) == "" {
				return 0
			}
		}
		return 1
	}
	if strings.TrimSpace(versionRange.GetStartVersion()) == "" || strings.TrimSpace(versionRange.GetEndVersion()) == "" {
		return 0
	}
	start := rowkey.NormalizeVersion(versionRange.GetStartVersion())
	end := rowkey.NormalizeVersion(versionRange.GetEndVersion())
	if start != "" && start == end {
		return 1
	}
	return 0
}

func timeSeriesRowToShardRow(row *pb.TimeSeriesRow) (*pb.ShardRow, error) {
	if row == nil {
		return nil, errors.New("row is required")
	}
	key, err := timeSeriesKeyToShardKey(row.GetKey(), true)
	if err != nil {
		return nil, err
	}
	if err := validateUserAttributes(row.GetAttributes()); err != nil {
		return nil, err
	}
	attributes := cloneStringMap(row.GetAttributes())
	if len(row.GetKey().GetDimensions()) > 0 {
		raw, err := json.Marshal(row.GetKey().GetDimensions())
		if err != nil {
			return nil, err
		}
		if attributes == nil {
			attributes = make(map[string]string, 1)
		}
		attributes[timeSeriesDimensionsAttribute] = string(raw)
	}
	return &pb.ShardRow{
		Key:                key,
		Columns:            cloneColumns(row.GetColumns()),
		Attributes:         attributes,
		AttributesToDelete: append([]string(nil), row.GetAttributesToDelete()...),
		RemovedColumnNames: append([]string(nil), row.GetRemovedColumnNames()...),
		RemovedColumns:     cloneColumnRemovals(row.GetRemovedColumns()),
		SourceShardId:      row.GetSourceShardId(), SourceSequence: row.GetSourceSequence(),
	}, nil
}

func validateUserAttributes(values map[string]string) error {
	for key := range values {
		if strings.HasPrefix(key, reservedAttributePrefix) {
			return errors.New("invalid reserved attribute " + key)
		}
	}
	return nil
}

func timeSeriesKeyToShardKey(key *pb.TimeSeriesKey, requireDataTime bool) (*pb.ShardKey, error) {
	if err := validateTimeSeriesKey(key, requireDataTime); err != nil {
		return nil, err
	}
	storeKey := &pb.ShardKey{
		SpaceId:   key.GetSpaceId(),
		DatasetId: key.GetDatasetId(),
		DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
		Key:       rowkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions()),
	}
	if key.GetDataTime() != "" {
		normalized, err := rowkey.NormalizeTimeVersion(key.GetDataTime())
		if err != nil {
			return nil, errors.New("data_time must be RFC3339/RFC3339Nano")
		}
		storeKey.Version = normalized
	}
	return storeKey, nil
}

func primaryStoreRowToTimeSeriesRow(row *pb.ShardRow, template *pb.TimeSeriesKey) *pb.TimeSeriesRow {
	key := &pb.TimeSeriesKey{}
	if template != nil {
		key = proto.Clone(template).(*pb.TimeSeriesKey)
	}
	storeKey := row.GetKey()
	if key.GetSpaceId() == "" {
		key.SpaceId = storeKey.GetSpaceId()
	}
	if key.GetDatasetId() == "" {
		key.DatasetId = storeKey.GetDatasetId()
	}
	if key.GetSubjectId() == "" || key.GetFreq() == "" {
		subjectID, freq, _, err := rowkey.ParseTimeSeriesDataKey(storeKey.GetKey())
		if err == nil {
			if key.GetSubjectId() == "" {
				key.SubjectId = subjectID
			}
			if key.GetFreq() == "" {
				key.Freq = freq
			}
		}
	}
	if len(key.GetDimensions()) == 0 {
		if raw := row.GetAttributes()[timeSeriesDimensionsAttribute]; raw != "" {
			dimensions := map[string]string{}
			if err := json.Unmarshal([]byte(raw), &dimensions); err == nil && len(dimensions) > 0 {
				key.Dimensions = dimensions
			}
		}
	}
	key.DataTime = storeKey.GetVersion()
	return &pb.TimeSeriesRow{
		Key:                key,
		Columns:            cloneColumns(row.GetColumns()),
		Attributes:         cloneTimeSeriesAttributes(row.GetAttributes()),
		AttributesToDelete: append([]string(nil), row.GetAttributesToDelete()...),
		RemovedColumnNames: append([]string(nil), row.GetRemovedColumnNames()...),
		RemovedColumns:     cloneColumnRemovals(row.GetRemovedColumns()),
		SourceShardId:      row.GetSourceShardId(), SourceSequence: row.GetSourceSequence(),
	}
}

func validateTimeSeriesKeyTemplate(key *pb.TimeSeriesKey) error {
	return validateTimeSeriesKey(key, false)
}

func validateTimeRange(timeRange *pb.TimeRange) error {
	if timeRange == nil {
		return nil
	}
	var start string
	var end string
	if timeRange.GetStartTime() != "" {
		normalized, err := rowkey.NormalizeTimeVersion(timeRange.GetStartTime())
		if err != nil {
			return errors.New("start_time must be RFC3339/RFC3339Nano")
		}
		start = normalized
	}
	if timeRange.GetEndTime() != "" {
		normalized, err := rowkey.NormalizeTimeVersion(timeRange.GetEndTime())
		if err != nil {
			return errors.New("end_time must be RFC3339/RFC3339Nano")
		}
		end = normalized
	}
	if start != "" && end != "" && start > end {
		return errors.New("start_time must be less than or equal to end_time")
	}
	return nil
}

func timeRangeToVersionRange(timeRange *pb.TimeRange) (*pb.VersionRange, error) {
	if timeRange == nil {
		return nil, nil
	}
	out := &pb.VersionRange{}
	if timeRange.GetStartTime() != "" {
		normalized, err := rowkey.NormalizeTimeVersion(timeRange.GetStartTime())
		if err != nil {
			return nil, errors.New("start_time must be RFC3339/RFC3339Nano")
		}
		out.StartVersion = normalized
	}
	if timeRange.GetEndTime() != "" {
		normalized, err := rowkey.NormalizeTimeVersion(timeRange.GetEndTime())
		if err != nil {
			return nil, errors.New("end_time must be RFC3339/RFC3339Nano")
		}
		out.EndVersion = normalized
	}
	if out.GetStartVersion() == "" && out.GetEndVersion() == "" {
		return nil, nil
	}
	return out, nil
}

func validateTimeSeriesKey(key *pb.TimeSeriesKey, requireDataTime bool) error {
	if key == nil {
		return errors.New("key is required")
	}
	if strings.TrimSpace(key.GetSpaceId()) == "" {
		return errors.New("space_id is required")
	}
	if strings.TrimSpace(key.GetDatasetId()) == "" {
		return errors.New("dataset_id is required")
	}
	if strings.TrimSpace(key.GetSubjectId()) == "" {
		return errors.New("subject_id is required")
	}
	if strings.TrimSpace(key.GetFreq()) == "" {
		return errors.New("freq is required")
	}
	if requireDataTime && strings.TrimSpace(key.GetDataTime()) == "" {
		return errors.New("data_time is required")
	}
	if key.GetDataTime() != "" {
		if _, err := rowkey.NormalizeTimeVersion(key.GetDataTime()); err != nil {
			return errors.New("data_time must be RFC3339/RFC3339Nano")
		}
	}
	return nil
}

func recordRowToShardRow(row *pb.RecordRow) (*pb.ShardRow, error) {
	if row == nil {
		return nil, errors.New("row is required")
	}
	key, err := recordKeyToShardKey(row.GetKey(), true)
	if err != nil {
		return nil, err
	}
	return &pb.ShardRow{
		Key:                key,
		Columns:            cloneColumns(row.GetColumns()),
		Attributes:         cloneStringMap(row.GetAttributes()),
		AttributesToDelete: append([]string(nil), row.GetAttributesToDelete()...),
		RemovedColumnNames: append([]string(nil), row.GetRemovedColumnNames()...),
		RemovedColumns:     cloneColumnRemovals(row.GetRemovedColumns()),
		SourceShardId:      row.GetSourceShardId(), SourceSequence: row.GetSourceSequence(),
	}, nil
}

func recordKeyToShardKey(key *pb.RecordKey, requireRecordID bool) (*pb.ShardKey, error) {
	if err := validateRecordKey(key, requireRecordID); err != nil {
		return nil, err
	}
	recordKey, err := rowkey.BuildRecordDataKey(key.GetRecordId())
	if err != nil {
		return nil, err
	}
	return &pb.ShardKey{
		SpaceId:   key.GetSpaceId(),
		DatasetId: key.GetDatasetId(),
		DataKind:  pb.DataKind_DATA_KIND_RECORD,
		Key:       recordKey,
		Version:   rowkey.NormalizeVersion(key.GetVersion()),
	}, nil
}

func primaryStoreRowToRecordRow(row *pb.ShardRow, template *pb.RecordKey) *pb.RecordRow {
	key := &pb.RecordKey{}
	if template != nil {
		key = proto.Clone(template).(*pb.RecordKey)
	}
	storeKey := row.GetKey()
	if key.GetSpaceId() == "" {
		key.SpaceId = storeKey.GetSpaceId()
	}
	if key.GetDatasetId() == "" {
		key.DatasetId = storeKey.GetDatasetId()
	}
	if key.GetRecordId() == "" {
		key.RecordId = rowkey.ParseRecordDataKey(storeKey.GetKey())
	}
	key.Version = publicRecordVersion(storeKey.GetVersion(), template)
	return &pb.RecordRow{
		Key:                key,
		Columns:            cloneColumns(row.GetColumns()),
		Attributes:         cloneStringMap(row.GetAttributes()),
		AttributesToDelete: append([]string(nil), row.GetAttributesToDelete()...),
		RemovedColumnNames: append([]string(nil), row.GetRemovedColumnNames()...),
		RemovedColumns:     cloneColumnRemovals(row.GetRemovedColumns()),
		SourceShardId:      row.GetSourceShardId(), SourceSequence: row.GetSourceSequence(),
	}
}

func publicRecordVersion(version string, template *pb.RecordKey) string {
	if version == rowkey.EmptyVersion && (template == nil || strings.TrimSpace(template.GetVersion()) == "") {
		return ""
	}
	return version
}

func validateRecordKeyTemplate(key *pb.RecordKey) error {
	return validateRecordKey(key, true)
}

func validateRecordKey(key *pb.RecordKey, requireRecordID bool) error {
	if key == nil {
		return errors.New("key is required")
	}
	if strings.TrimSpace(key.GetSpaceId()) == "" {
		return errors.New("space_id is required")
	}
	if strings.TrimSpace(key.GetDatasetId()) == "" {
		return errors.New("dataset_id is required")
	}
	if requireRecordID && strings.TrimSpace(key.GetRecordId()) == "" {
		return errors.New("record_id is required")
	}
	return nil
}

func reverseTimeSeriesRows(rows []*pb.TimeSeriesRow) {
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
}

func reverseRecordRows(rows []*pb.RecordRow) {
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
}

func sortTimeSeriesRows(rows []*pb.TimeSeriesRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i].GetKey()
		right := rows[j].GetKey()
		if left.GetSpaceId() != right.GetSpaceId() {
			return left.GetSpaceId() < right.GetSpaceId()
		}
		if left.GetDatasetId() != right.GetDatasetId() {
			return left.GetDatasetId() < right.GetDatasetId()
		}
		if left.GetSubjectId() != right.GetSubjectId() {
			return left.GetSubjectId() < right.GetSubjectId()
		}
		if left.GetFreq() != right.GetFreq() {
			return left.GetFreq() < right.GetFreq()
		}
		return rowkey.NormalizeVersion(left.GetDataTime()) < rowkey.NormalizeVersion(right.GetDataTime())
	})
}

func sortRecordRows(rows []*pb.RecordRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i].GetKey()
		right := rows[j].GetKey()
		if left.GetSpaceId() != right.GetSpaceId() {
			return left.GetSpaceId() < right.GetSpaceId()
		}
		if left.GetDatasetId() != right.GetDatasetId() {
			return left.GetDatasetId() < right.GetDatasetId()
		}
		if left.GetRecordId() != right.GetRecordId() {
			return left.GetRecordId() < right.GetRecordId()
		}
		return rowkey.NormalizeVersion(left.GetVersion()) < rowkey.NormalizeVersion(right.GetVersion())
	})
}

func pageTimeSeriesRows(rows []*pb.TimeSeriesRow, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	start := 0
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
		if page.GetCursor() != "" {
			if offset, err := strconv.Atoi(page.GetCursor()); err == nil && offset > 0 {
				start = offset
			}
		}
	}
	if start == 0 {
		start = int((pageNo - 1) * size)
	}
	if start > len(rows) {
		start = len(rows)
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	nextCursor := ""
	hasMore := end < len(rows)
	if hasMore {
		nextCursor = strconv.Itoa(end)
	}
	return rows[start:end], &pb.PageResult{
		Page:       pageNo,
		Size:       size,
		Total:      uint32(len(rows)),
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}
}

func pageRecordRows(rows []*pb.RecordRow, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	start := 0
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
		if page.GetCursor() != "" {
			if offset, err := strconv.Atoi(page.GetCursor()); err == nil && offset > 0 {
				start = offset
			}
		}
	}
	if start == 0 {
		start = int((pageNo - 1) * size)
	}
	if start > len(rows) {
		start = len(rows)
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	nextCursor := ""
	hasMore := end < len(rows)
	if hasMore {
		nextCursor = strconv.Itoa(end)
	}
	return rows[start:end], &pb.PageResult{
		Page:       pageNo,
		Size:       size,
		Total:      uint32(len(rows)),
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}
}

func pageMergedTimeSeriesRows(rows []*pb.TimeSeriesRow, plan *multiKeyPagePlan, sourceHasMore bool) ([]*pb.TimeSeriesRow, *pb.PageResult) {
	start, end, result := mergedPageBounds(len(rows), plan, sourceHasMore)
	return rows[start:end], result
}

func pageMergedRecordRows(rows []*pb.RecordRow, plan *multiKeyPagePlan, sourceHasMore bool) ([]*pb.RecordRow, *pb.PageResult) {
	start, end, result := mergedPageBounds(len(rows), plan, sourceHasMore)
	return rows[start:end], result
}

func mergedPageBounds(rowCount int, plan *multiKeyPagePlan, sourceHasMore bool) (int, int, *pb.PageResult) {
	if plan == nil {
		plan = &multiKeyPagePlan{pageNo: 1, size: 1000}
	}
	start := min(plan.start, rowCount)
	end := min(start+int(plan.size), rowCount)
	hasMore := sourceHasMore || end < rowCount
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(end)
	}
	return start, end, &pb.PageResult{
		Page:       plan.pageNo,
		Size:       plan.size,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		TotalState: pb.TotalState_SKIPPED,
	}
}

func cloneColumns(columns []*pb.ColumnValue) []*pb.ColumnValue {
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, column := range columns {
		out = append(out, proto.Clone(column).(*pb.ColumnValue))
	}
	return out
}

func cloneColumnRemovals(values []*pb.ColumnRemoval) []*pb.ColumnRemoval {
	out := make([]*pb.ColumnRemoval, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, proto.Clone(value).(*pb.ColumnRemoval))
		}
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneTimeSeriesAttributes(values map[string]string) map[string]string {
	out := cloneStringMap(values)
	delete(out, timeSeriesDimensionsAttribute)
	if len(out) == 0 {
		return nil
	}
	return out
}

func primaryStoreRowID(row *pb.ShardRow) string {
	key := row.GetKey()
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetDataKind().String(),
		key.GetKey(),
		key.GetVersion(),
	}, "\x00")
}
