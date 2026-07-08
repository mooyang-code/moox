package builder

import (
	"encoding/json"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func mergeTimeSeriesRowsLatestWins(rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	positions := make(map[string]int, len(rows))
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := timeSeriesMergeKey(row)
		if idx, ok := positions[key]; ok {
			out[idx] = mergeTimeSeriesPatch(out[idx], row)
			continue
		}
		positions[key] = len(out)
		out = append(out, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return out
}

func timeSeriesMergeKey(row *pb.TimeSeriesRow) string {
	key := row.GetKey()
	dimensions, _ := json.Marshal(key.GetDimensions())
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetSubjectId(),
		key.GetFreq(),
		string(dimensions),
		key.GetDataTime(),
	}, "\x00")
}

func mergeTimeSeriesPatch(base *pb.TimeSeriesRow, patch *pb.TimeSeriesRow) *pb.TimeSeriesRow {
	if base == nil {
		return proto.Clone(patch).(*pb.TimeSeriesRow)
	}
	merged := proto.Clone(base).(*pb.TimeSeriesRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.TimeSeriesKey)
	merged.Attributes = mergeStringMaps(merged.GetAttributes(), patch.GetAttributes())
	merged.Columns = mergeColumnValues(merged.GetColumns(), patch.GetColumns())
	return merged
}

func mergeRecordRowsLatestWins(rows []*pb.RecordRow) []*pb.RecordRow {
	positions := make(map[string]int, len(rows))
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := recordMergeKey(row)
		if idx, ok := positions[key]; ok {
			out[idx] = mergeRecordPatch(out[idx], row)
			continue
		}
		positions[key] = len(out)
		out = append(out, proto.Clone(row).(*pb.RecordRow))
	}
	return out
}

func recordMergeKey(row *pb.RecordRow) string {
	key := row.GetKey()
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetRecordId(),
		key.GetVersion(),
	}, "\x00")
}

func mergeRecordPatch(base *pb.RecordRow, patch *pb.RecordRow) *pb.RecordRow {
	if base == nil {
		return proto.Clone(patch).(*pb.RecordRow)
	}
	merged := proto.Clone(base).(*pb.RecordRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.RecordKey)
	merged.Attributes = mergeStringMaps(merged.GetAttributes(), patch.GetAttributes())
	merged.Columns = mergeColumnValues(merged.GetColumns(), patch.GetColumns())
	return merged
}

func mergeColumnValues(base []*pb.ColumnValue, patch []*pb.ColumnValue) []*pb.ColumnValue {
	out := make([]*pb.ColumnValue, 0, len(base)+len(patch))
	positions := make(map[string]int, len(base)+len(patch))
	for _, column := range base {
		if column == nil {
			continue
		}
		positions[column.GetColumnName()] = len(out)
		out = append(out, proto.Clone(column).(*pb.ColumnValue))
	}
	for _, column := range patch {
		if column == nil {
			continue
		}
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			out[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(out)
		out = append(out, copied)
	}
	return out
}

func mergeStringMaps(base map[string]string, patch map[string]string) map[string]string {
	if len(base) == 0 && len(patch) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(patch))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range patch {
		out[key] = value
	}
	return out
}
