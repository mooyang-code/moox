package viewindex

import (
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestRowKeyValidateRequiresExactlyOneKey(t *testing.T) {
	timeSeriesKey := &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-18T00:00:00Z"}
	recordKey := &pb.RecordKey{SpaceId: "space", DatasetId: "news", RecordId: "n-1", Version: "v1"}

	tests := []struct {
		name string
		key  RowKey
		want string
	}{
		{name: "neither", key: RowKey{}, want: "exactly one"},
		{name: "both", key: RowKey{TimeSeriesKey: timeSeriesKey, RecordKey: recordKey}, want: "exactly one"},
		{name: "time series", key: RowKey{TimeSeriesKey: timeSeriesKey}},
		{name: "record", key: RowKey{RecordKey: recordKey}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.key.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestRowWriteValidateRejectsUnspecifiedOperation(t *testing.T) {
	write := RowWrite{Key: RowKey{RecordKey: &pb.RecordKey{RecordId: "n-1"}}}
	if err := write.Validate(); err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("Validate() error = %v, want operation error", err)
	}
}

func TestRowWriteValidateRejectsDeleteContent(t *testing.T) {
	key := RowKey{RecordKey: &pb.RecordKey{RecordId: "n-1"}}

	for _, tt := range []struct {
		name  string
		write RowWrite
	}{
		{
			name: "columns",
			write: RowWrite{
				Operation: RowWriteOperationDelete,
				Key:       key,
				Columns:   []*pb.ColumnValue{{ColumnName: "title"}},
			},
		},
		{
			name: "attributes",
			write: RowWrite{
				Operation:  RowWriteOperationDelete,
				Key:        key,
				Attributes: map[string]string{"source": "news"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.write.Validate(); err == nil || !strings.Contains(err.Error(), "DELETE") {
				t.Fatalf("Validate() error = %v, want DELETE content error", err)
			}
		})
	}
}

func TestViewIndexApplyBatchValidateRejectsDuplicateRowKeys(t *testing.T) {
	key := &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-18T00:00:00Z"}
	batch := ViewIndexApplyBatch{
		RowWrites: []RowWrite{
			{Operation: RowWriteOperationMerge, Key: RowKey{TimeSeriesKey: key}},
			{Operation: RowWriteOperationReplace, Key: RowKey{TimeSeriesKey: &pb.TimeSeriesKey{
				SpaceId: "space", DatasetId: "prices", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-18T00:00:00Z",
			}}},
		},
	}

	if err := batch.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Validate() error = %v, want duplicate row key error", err)
	}
}

func TestViewIndexApplyBatchValidateRejectsIncompleteReplace(t *testing.T) {
	batch := ViewIndexApplyBatch{
		RequiredColumnNames: []string{"close", "momentum"},
		RowWrites: []RowWrite{{
			Operation: RowWriteOperationReplace,
			Key:       RowKey{RecordKey: &pb.RecordKey{SpaceId: "space", DatasetId: "prices", RecordId: "n-1"}},
			Columns:   []*pb.ColumnValue{{ColumnName: "close"}},
		}},
	}
	if err := batch.Validate(); err == nil || !strings.Contains(err.Error(), "momentum") {
		t.Fatalf("Validate() error = %v, want missing column error", err)
	}
}

func TestViewIndexApplyBatchValidateRejectsCheckpointRegression(t *testing.T) {
	batch := ViewIndexApplyBatch{
		CheckpointUpdates: []ShardCheckpointUpdate{{
			ShardID:                     "shard-1",
			ExpectedLastAppliedSequence: 10,
			LastAppliedSequence:         9,
		}},
	}

	if err := batch.Validate(); err == nil || !strings.Contains(err.Error(), "Expected") {
		t.Fatalf("Validate() error = %v, want checkpoint bound error", err)
	}
}

func TestViewIndexApplyBatchValidateAcceptsCheckpointPrefix(t *testing.T) {
	batch := ViewIndexApplyBatch{CheckpointUpdates: []ShardCheckpointUpdate{{
		ShardID: "shard-1", ExpectedLastAppliedSequence: 10, LastAppliedSequence: 12,
	}}}
	if err := batch.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestViewIndexApplyBatchValidateRejectsEmptyApply(t *testing.T) {
	if err := (ViewIndexApplyBatch{}).Validate(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Validate() error = %v, want empty apply error", err)
	}
}

func TestViewIndexApplyBatchValidateAcceptsEachApplyPart(t *testing.T) {
	key := RowKey{RecordKey: &pb.RecordKey{SpaceId: "space", DatasetId: "news", RecordId: "n-1", Version: "v1"}}
	tests := []struct {
		name  string
		batch ViewIndexApplyBatch
	}{
		{
			name: "row writes",
			batch: ViewIndexApplyBatch{RowWrites: []RowWrite{{
				Operation: RowWriteOperationMerge,
				Key:       key,
				Columns:   []*pb.ColumnValue{{ColumnName: "title"}},
			}}},
		},
		{
			name: "checkpoint updates",
			batch: ViewIndexApplyBatch{CheckpointUpdates: []ShardCheckpointUpdate{{
				ShardID: "shard-1", ExpectedLastAppliedSequence: 10, LastAppliedSequence: 11,
			}}},
		},
		{
			name:  "range update",
			batch: ViewIndexApplyBatch{IndexRangeUpdate: &IndexRangeUpdate{IndexedTo: stringPtr("2026-07-18T00:00:00Z")}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.batch.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }
