package access

import (
	"errors"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func timeSeriesRowToPrimaryStoreRow(row *pb.TimeSeriesRow) (*pb.PrimaryStoreRow, error) {
	if row == nil {
		return nil, errors.New("row is required")
	}
	key, err := timeSeriesKeyToPrimaryStoreKey(row.GetKey(), true)
	if err != nil {
		return nil, err
	}
	return &pb.PrimaryStoreRow{
		Key:        key,
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
	}, nil
}

func timeSeriesKeyToPrimaryStoreKey(key *pb.TimeSeriesKey, requireDataTime bool) (*pb.PrimaryStoreKey, error) {
	if err := validateTimeSeriesKey(key, requireDataTime); err != nil {
		return nil, err
	}
	storeKey := &pb.PrimaryStoreKey{
		SpaceId:   key.GetSpaceId(),
		DatasetId: key.GetDatasetId(),
		DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
		Key:       factkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions()),
	}
	if key.GetDataTime() != "" {
		normalized, err := factkey.NormalizeTimeVersion(key.GetDataTime())
		if err != nil {
			return nil, errors.New("data_time must be RFC3339/RFC3339Nano")
		}
		storeKey.Version = normalized
	}
	return storeKey, nil
}

func primaryStoreRowToTimeSeriesRow(row *pb.PrimaryStoreRow, template *pb.TimeSeriesKey) *pb.TimeSeriesRow {
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
		subjectID, freq, _, err := factkey.ParseTimeSeriesDataKey(storeKey.GetKey())
		if err == nil {
			if key.GetSubjectId() == "" {
				key.SubjectId = subjectID
			}
			if key.GetFreq() == "" {
				key.Freq = freq
			}
		}
	}
	key.DataTime = storeKey.GetVersion()
	return &pb.TimeSeriesRow{
		Key:        key,
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

func timeRangeToVersionRange(timeRange *pb.TimeRange) (*pb.VersionRange, error) {
	if timeRange == nil {
		return nil, nil
	}
	out := &pb.VersionRange{}
	if timeRange.GetStartTime() != "" {
		normalized, err := factkey.NormalizeTimeVersion(timeRange.GetStartTime())
		if err != nil {
			return nil, errors.New("start_time must be RFC3339/RFC3339Nano")
		}
		out.StartVersion = normalized
	}
	if timeRange.GetEndTime() != "" {
		normalized, err := factkey.NormalizeTimeVersion(timeRange.GetEndTime())
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
		if _, err := factkey.NormalizeTimeVersion(key.GetDataTime()); err != nil {
			return errors.New("data_time must be RFC3339/RFC3339Nano")
		}
	}
	return nil
}

func recordRowToPrimaryStoreRow(row *pb.RecordRow) (*pb.PrimaryStoreRow, error) {
	if row == nil {
		return nil, errors.New("row is required")
	}
	key, err := recordKeyToPrimaryStoreKey(row.GetKey(), true)
	if err != nil {
		return nil, err
	}
	return &pb.PrimaryStoreRow{
		Key:        key,
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
	}, nil
}

func recordKeyToPrimaryStoreKey(key *pb.RecordKey, requireRecordID bool) (*pb.PrimaryStoreKey, error) {
	if err := validateRecordKey(key, requireRecordID); err != nil {
		return nil, err
	}
	recordKey, err := factkey.BuildRecordDataKey(key.GetRecordId())
	if err != nil {
		return nil, err
	}
	return &pb.PrimaryStoreKey{
		SpaceId:   key.GetSpaceId(),
		DatasetId: key.GetDatasetId(),
		DataKind:  pb.DataKind_DATA_KIND_RECORD,
		Key:       recordKey,
		Version:   factkey.NormalizeVersion(key.GetVersion()),
	}, nil
}

func primaryStoreRowToRecordRow(row *pb.PrimaryStoreRow, template *pb.RecordKey) *pb.RecordRow {
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
		key.RecordId = factkey.ParseRecordDataKey(storeKey.GetKey())
	}
	key.Version = publicRecordVersion(storeKey.GetVersion(), template)
	return &pb.RecordRow{
		Key:        key,
		Columns:    cloneColumns(row.GetColumns()),
		Attributes: cloneStringMap(row.GetAttributes()),
	}
}

func publicRecordVersion(version string, template *pb.RecordKey) string {
	if version == factkey.EmptyVersion && (template == nil || strings.TrimSpace(template.GetVersion()) == "") {
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
		return factkey.NormalizeVersion(left.GetDataTime()) < factkey.NormalizeVersion(right.GetDataTime())
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
		Total:   uint32(len(rows)),
		HasMore: end < len(rows),
	}
}

func pageRecordRows(rows []*pb.RecordRow, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult) {
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
		Total:   uint32(len(rows)),
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
