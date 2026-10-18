package primarystore

import (
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func primaryPeriodRowAt(period time.Time, subject, tag string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "bars", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, SeriesTag: tag, Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
}
