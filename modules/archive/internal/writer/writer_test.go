package writer

import (
	"context"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestWriterMergesPartialUpdateWithoutAddingDuplicateRow(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	store, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}
	open := 100.0
	closeValue := 101.0
	_, err = store.Append(context.Background(), domain.EventBatch{MessageID: "initial", Rows: []domain.RowPatch{{Partition: key, DataTime: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Attributes: map[string]string{"provider": "binance"}, WrittenAt: time.Date(2026, 6, 1, 0, 0, 1, 0, time.UTC), Columns: map[string]domain.Scalar{"open": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &open}, "close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeValue}}}}})
	if err != nil {
		t.Fatal(err)
	}
	w := New(store, root, 65536)
	if _, err := w.WritePartition(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	newClose := 102.0
	_, err = store.Append(context.Background(), domain.EventBatch{MessageID: "revision", Rows: []domain.RowPatch{{Partition: key, DataTime: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Attributes: map[string]string{"revision": "2"}, WrittenAt: time.Date(2026, 6, 1, 0, 1, 0, 0, time.UTC), Columns: map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &newClose}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WritePartition(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	path, _ := key.AbsolutePath(root)
	rows, _, _, err := parquetio.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Columns["open"].Double == nil || *rows[0].Columns["open"].Double != open || *rows[0].Columns["close"].Double != newClose || rows[0].Attributes["provider"] != "binance" || rows[0].Attributes["revision"] != "2" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestWriterClearsAndRestoresTypedColumn(t *testing.T) {
	root := t.TempDir()
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "factor", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}
	dataTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	first := 1.25
	appendPatch := func(messageID string, value domain.Scalar) {
		t.Helper()
		_, err := store.Append(context.Background(), domain.EventBatch{
			MessageID: messageID,
			Rows: []domain.RowPatch{{
				Partition: key, DataTime: dataTime, WrittenAt: time.Now().UTC(),
				Columns: map[string]domain.Scalar{"factor": value},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	w := New(store, root, 65536)
	appendPatch("initial", domain.Scalar{Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &first})
	if _, err := w.WritePartition(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	appendPatch("clear", domain.Scalar{Null: true})
	if _, err := w.WritePartition(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	path, _ := key.AbsolutePath(root)
	rows, schema, _, err := parquetio.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if _, exists := rows[0].Columns["factor"]; exists {
		t.Fatalf("cleared factor remains in row: %#v", rows[0])
	}
	if schema["factor"] != storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE {
		t.Fatalf("factor schema = %v", schema["factor"])
	}

	second := 2.5
	appendPatch("restore", domain.Scalar{Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &second})
	if _, err := w.WritePartition(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err = parquetio.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Columns["factor"].Double; got == nil || *got != second {
		t.Fatalf("restored factor = %#v", rows[0].Columns["factor"])
	}
}

func TestTempFileHelpers(t *testing.T) {
	assert.True(t, isTempFile(".part.tmp-123.parquet"))
	assert.False(t, isTempFile("202601.parquet"))
	assert.True(t, containsTempMarker("dir/.tmp-123"))
	assert.False(t, containsTempMarker("clean.parquet"))
}
