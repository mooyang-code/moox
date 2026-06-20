package access

import (
	"errors"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func timeSeriesRowToDataRow(row *pb.TimeSeriesRow) (*pb.DataRow, error) {
	if row == nil {
		return nil, errors.New("row is required")
	}
	key := row.GetKey()
	if err := validateTimeSeriesKey(key, true); err != nil {
		return nil, err
	}
	return &pb.DataRow{
		Key: &pb.DataKey{
			Scope: &pb.DataScope{
				SpaceId:    key.GetSpaceId(),
				DatasetId:  key.GetDatasetId(),
				SubjectId:  key.GetSubjectId(),
				Freq:       key.GetFreq(),
				Dimensions: cloneStringMap(key.GetDimensions()),
			},
			DataTime: key.GetDataTime(),
			RowId:    key.GetDataTime(),
		},
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
	}, nil
}

func dataRowToTimeSeriesRow(row *pb.DataRow) *pb.TimeSeriesRow {
	key := row.GetKey()
	scope := key.GetScope()
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:    scope.GetSpaceId(),
			DatasetId:  scope.GetDatasetId(),
			SubjectId:  scope.GetSubjectId(),
			Freq:       scope.GetFreq(),
			Dimensions: cloneStringMap(scope.GetDimensions()),
			DataTime:   key.GetDataTime(),
		},
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
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
		normalized, err := factkey.NormalizeTimeVersion(timeRange.GetStartTime())
		if err != nil {
			return errors.New("start_time must be RFC3339/RFC3339Nano")
		}
		start = normalized
	}
	if timeRange.GetEndTime() != "" {
		normalized, err := factkey.NormalizeTimeVersion(timeRange.GetEndTime())
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
		if _, err := factkey.NormalizeTimeVersion(key.GetDataTime()); err != nil {
			return errors.New("data_time must be RFC3339/RFC3339Nano")
		}
	}
	return nil
}

func objectRowToDataRow(row *pb.ObjectRow) (*pb.DataRow, error) {
	if row == nil {
		return nil, errors.New("row is required")
	}
	key := row.GetKey()
	if err := validateObjectKeyTemplate(key); err != nil {
		return nil, err
	}
	return &pb.DataRow{
		Key: &pb.DataKey{
			Scope: &pb.DataScope{
				SpaceId:   key.GetSpaceId(),
				DatasetId: key.GetDatasetId(),
			},
			DataTime: key.GetVersion(),
			RowId:    key.GetObjectId(),
		},
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
	}, nil
}

func dataRowToObjectRow(row *pb.DataRow) *pb.ObjectRow {
	key := row.GetKey()
	scope := key.GetScope()
	objectID := key.GetRowId()
	if objectID == "" {
		objectID = scope.GetSubjectId()
	}
	return &pb.ObjectRow{
		Key: &pb.ObjectKey{
			SpaceId:   scope.GetSpaceId(),
			DatasetId: scope.GetDatasetId(),
			ObjectId:  objectID,
			Version:   key.GetDataTime(),
		},
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
	}
}

func validateObjectKeyTemplate(key *pb.ObjectKey) error {
	if key == nil {
		return errors.New("key is required")
	}
	if strings.TrimSpace(key.GetSpaceId()) == "" {
		return errors.New("space_id is required")
	}
	if strings.TrimSpace(key.GetDatasetId()) == "" {
		return errors.New("dataset_id is required")
	}
	if strings.TrimSpace(key.GetObjectId()) == "" {
		return errors.New("object_id is required")
	}
	return nil
}

func dataRowMatchesObjectKey(row *pb.DataRow, key *pb.ObjectKey) bool {
	rowKey := row.GetKey()
	scope := rowKey.GetScope()
	if scope.GetFreq() != "" {
		return false
	}
	if scope.GetSpaceId() != key.GetSpaceId() || scope.GetDatasetId() != key.GetDatasetId() {
		return false
	}
	objectID := rowKey.GetRowId()
	if objectID == "" {
		objectID = scope.GetSubjectId()
	}
	return objectID == key.GetObjectId()
}

func objectVersionMatches(version string, exact string, versionRange *pb.VersionRange) bool {
	value := factkey.NormalizeVersion(version)
	if exact != "" && value != factkey.NormalizeVersion(exact) {
		return false
	}
	if versionRange == nil {
		return true
	}
	if start := versionRange.GetStartVersion(); start != "" && value < factkey.NormalizeVersion(start) {
		return false
	}
	if end := versionRange.GetEndVersion(); end != "" && value > factkey.NormalizeVersion(end) {
		return false
	}
	return true
}

func reverseTimeSeriesRows(rows []*pb.TimeSeriesRow) {
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
}

func reverseObjectRows(rows []*pb.ObjectRow) {
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
		return factkey.NormalizeVersion(left.GetDataTime()) < factkey.NormalizeVersion(right.GetDataTime())
	})
}

func sortObjectRows(rows []*pb.ObjectRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i].GetKey()
		right := rows[j].GetKey()
		if left.GetSpaceId() != right.GetSpaceId() {
			return left.GetSpaceId() < right.GetSpaceId()
		}
		if left.GetDatasetId() != right.GetDatasetId() {
			return left.GetDatasetId() < right.GetDatasetId()
		}
		if left.GetObjectId() != right.GetObjectId() {
			return left.GetObjectId() < right.GetObjectId()
		}
		return factkey.NormalizeVersion(left.GetVersion()) < factkey.NormalizeVersion(right.GetVersion())
	})
}

func pageTimeSeriesRows(rows []*pb.TimeSeriesRow, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(rows) {
		start = len(rows)
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], &pb.PageResult{
		Page:    pageNo,
		Size:    size,
		Total:   uint64(len(rows)),
		HasMore: end < len(rows),
	}
}

func pageObjectRows(rows []*pb.ObjectRow, page *pb.Page) ([]*pb.ObjectRow, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(rows) {
		start = len(rows)
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], &pb.PageResult{
		Page:    pageNo,
		Size:    size,
		Total:   uint64(len(rows)),
		HasMore: end < len(rows),
	}
}

func cloneColumns(columns []*pb.ColumnValue) []*pb.ColumnValue {
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, column := range columns {
		out = append(out, proto.Clone(column).(*pb.ColumnValue))
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
