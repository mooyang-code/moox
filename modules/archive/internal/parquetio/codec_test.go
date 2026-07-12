package parquetio

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestWideParquetRoundTripPreservesTypesAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet")
	zero := int64(0)
	closed := false
	rows := []domain.ArchiveRow{{Partition: domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}, DataTime: time.Date(2026, 6, 1, 0, 0, 0, 123, time.UTC), DimensionsJSON: "{}", Attributes: map[string]string{"source": "binance"}, WrittenAt: time.Date(2026, 6, 1, 0, 0, 1, 0, time.UTC), Columns: map[string]domain.Scalar{"trade_num": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Int: &zero}, "closed": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Bool: &closed}}}}
	manifest, err := Write(path, rows, WriteOptions{Generation: 7, MaterializedAt: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC), RowGroupRows: 65536})
	if err != nil {
		t.Fatal(err)
	}
	got, columns, metadata, err := Read(path)
	if err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("Read() = %#v, %v, %v", got, metadata, err)
	}
	if columns["trade_num"] != storagepb.FieldValueType_FIELD_VALUE_TYPE_INT || manifest.RowCount != 1 || metadata["moox.archive.schema_version"] != "1" || metadata["moox.archive.generation"] != "7" {
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
	path := filepath.Join(t.TempDir(), "crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet")
	jsonValue := `{"bid":100.25,"tags":["a"]}`
	rows := []domain.ArchiveRow{{Partition: domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}, DataTime: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), DimensionsJSON: "{}", Columns: map[string]domain.Scalar{"payload": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON, JSON: &jsonValue}}}}
	if _, err := Write(path, rows, WriteOptions{Generation: 1}); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := Read(path)
	if err != nil || len(got) != 1 || got[0].Columns["payload"].JSON == nil || *got[0].Columns["payload"].JSON != jsonValue {
		t.Fatalf("JSON roundtrip failed: %#v err=%v", got, err)
	}
}
