package parquetio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/parquet-go/parquet-go"
)

func TestReadRejectsV1Parquet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.parquet")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	schema := parquet.NewSchema("moox_archive_v1", parquet.Group{
		colCandleTime:     parquet.Timestamp(parquet.Nanosecond),
		colSpace:          parquet.String(),
		colDataset:        parquet.String(),
		colSubject:        parquet.String(),
		colFreq:           parquet.String(),
		"dimensions_json": parquet.String(),
		colAttributes:     parquet.String(),
		colWrittenAt:      parquet.Timestamp(parquet.Nanosecond),
	})
	writer := parquet.NewGenericWriter[map[string]any](file, schema)
	writer.SetKeyValueMetadata("moox.archive.schema_version", "1")
	_, err = writer.Write([]map[string]any{{
		colCandleTime: time.Now().UTC(), colSpace: "crypto", colDataset: "kline",
		colSubject: "BTC", colFreq: "1m", "dimensions_json": "{}",
		colAttributes: "{}", colWrittenAt: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Read(path); err == nil {
		t.Fatal("expected v1 parquet rejection")
	}
}

func TestWideParquetRoundTripPreservesTypesAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crypto__spot_kline_1h__BTC-USDT__1h__series_tag=venue%3Abinance__202606.parquet")
	zero := int64(0)
	closed := false
	rows := []domain.ArchiveRow{{Partition: domain.PartitionKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}, DataTime: time.Date(2026, 6, 1, 0, 0, 0, 123, time.UTC), Attributes: map[string]string{"source": "binance"}, WrittenAt: time.Date(2026, 6, 1, 0, 0, 1, 0, time.UTC), Columns: map[string]domain.Scalar{"trade_num": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Int: &zero}, "closed": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Bool: &closed}}}}
	manifest, err := Write(path, rows, WriteOptions{Generation: 7, MaterializedAt: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC), RowGroupRows: 65536})
	if err != nil {
		t.Fatal(err)
	}
	got, columns, metadata, err := Read(path)
	if err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("Read() = %#v, %v, %v", got, metadata, err)
	}
	if columns["trade_num"] != storagepb.FieldValueType_FIELD_VALUE_TYPE_INT || manifest.RowCount != 1 || metadata["moox.archive.schema_version"] != "2" || metadata["moox.archive.generation"] != "7" || metadata["moox.archive.series_tag"] != "venue:binance" {
		t.Fatalf("manifest=%#v columns=%v metadata=%v", manifest, columns, metadata)
	}
}

func TestBuildSchemaRejectsConflictingColumnType(t *testing.T) {
	_, err := BuildSchema(map[string]storagepb.FieldValueType{"close": storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED})
	if err == nil {
		t.Fatal("BuildSchema() accepted unspecified type")
	}
}

func TestWideParquetRoundTripPreservesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crypto__spot_kline_1h__BTC-USDT__1h__series_tag=__202606.parquet")
	jsonValue := `{"bid":100.25,"tags":["a"]}`
	rows := []domain.ArchiveRow{{Partition: domain.PartitionKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", Month: "202606"}, DataTime: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Columns: map[string]domain.Scalar{"payload": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON, JSON: &jsonValue}}}}
	if _, err := Write(path, rows, WriteOptions{Generation: 1}); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := Read(path)
	if err != nil || len(got) != 1 || got[0].Columns["payload"].JSON == nil || *got[0].Columns["payload"].JSON != jsonValue {
		t.Fatalf("JSON roundtrip failed: %#v err=%v", got, err)
	}
}
