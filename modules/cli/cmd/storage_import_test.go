package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestStorageImportCommandExposesFormatBasedImport(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"storage", "import"})
	if err != nil || cmd == nil {
		t.Fatalf("storage import command not registered: %v", err)
	}
	for _, name := range []string{
		"format",
		"file",
		"access-url",
		"metadata-url",
		"space",
		"view",
		"dataset",
		"subject",
		"data-source",
		"freq",
		"time-column",
		"batch-size",
		"dry-run",
	} {
		if flag := cmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("storage import missing --%s", name)
		}
	}
}

func TestInferStorageImportFormat(t *testing.T) {
	got, err := inferStorageImportFormat("auto", "/tmp/ARB-USDT.csv")
	if err != nil {
		t.Fatalf("inferStorageImportFormat returned error: %v", err)
	}
	if got != "csv" {
		t.Fatalf("format = %q, want csv", got)
	}
	if _, err := inferStorageImportFormat("auto", "/tmp/ARB-USDT.unknown"); err == nil {
		t.Fatalf("inferStorageImportFormat should reject unknown extension")
	}
	if got, err := inferStorageImportFormat("CSV", "/tmp/no-extension"); err != nil || got != "csv" {
		t.Fatalf("explicit CSV format = %q err=%v, want csv nil", got, err)
	}
}

func TestCSVImporterRejectsUnregisteredHeader(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close,unregistered\n2024-01-01 00:00:00,1.23,oops\n")
	importer := csvStorageFileImporter{}
	_, err := importer.ReadTimeSeriesRows(csvPath, storageImportContext{
		Options: validStorageImportOptions(csvPath),
		Columns: map[string]*pb.DatasetColumn{
			"close": datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
		},
	})
	if err == nil {
		t.Fatalf("ReadTimeSeriesRows should reject unregistered header")
	}
	if !strings.Contains(err.Error(), "unregistered") {
		t.Fatalf("error = %q, want unregistered column detail", err.Error())
	}
}

func TestCSVImporterValidatesTypesAndBuildsRows(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close,trade_num,tradable,note\n2024-01-01 00:00:00,1.23,7,true,ok\n")
	importer := csvStorageFileImporter{}
	result, err := importer.ReadTimeSeriesRows(csvPath, storageImportContext{
		Options: validStorageImportOptions(csvPath),
		Columns: map[string]*pb.DatasetColumn{
			"close":     datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
			"trade_num": datasetColumn("trade_num", pb.FieldValueType_FIELD_VALUE_TYPE_INT),
			"tradable":  datasetColumn("tradable", pb.FieldValueType_FIELD_VALUE_TYPE_BOOL),
			"note":      datasetColumn("note", pb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		},
	})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows returned error: %v", err)
	}
	if result.Stats.ValidatedRows != 1 || len(result.Rows) != 1 {
		t.Fatalf("validated rows = %d len(rows)=%d, want 1", result.Stats.ValidatedRows, len(result.Rows))
	}
	row := result.Rows[0]
	if row.GetKey().GetDataTime() != "2024-01-01T00:00:00Z" {
		t.Fatalf("data_time = %q, want RFC3339 UTC", row.GetKey().GetDataTime())
	}
	if row.GetKey().GetDatasetId() != "binance_spot_kline" || row.GetKey().GetSubjectId() != "ARB-USDT" {
		t.Fatalf("key = %+v", row.GetKey())
	}
	if len(row.GetColumns()) != 4 {
		t.Fatalf("columns = %d, want 4", len(row.GetColumns()))
	}
}

func TestCSVImporterSkipsBannerBeforeHeader(t *testing.T) {
	csvPath := writeTempCSV(t, "downloaded from exchange\ncandle_begin_time,close\n2024-01-01 00:00:00,1.23\n")
	importer := csvStorageFileImporter{}
	result, err := importer.ReadTimeSeriesRows(csvPath, storageImportContext{
		Options: validStorageImportOptions(csvPath),
		Columns: map[string]*pb.DatasetColumn{
			"close": datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
		},
	})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows returned error: %v", err)
	}
	if result.Stats.ValidatedRows != 1 {
		t.Fatalf("validated rows = %d, want 1", result.Stats.ValidatedRows)
	}
}

func TestCSVImporterRejectsInvalidDimension(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,1.23\n")
	importer := csvStorageFileImporter{}
	opts := validStorageImportOptions(csvPath)
	opts.Dimensions = []string{"market"}
	_, err := importer.ReadTimeSeriesRows(csvPath, storageImportContext{
		Options: opts,
		Columns: map[string]*pb.DatasetColumn{
			"close": datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
		},
	})
	if err == nil {
		t.Fatalf("ReadTimeSeriesRows should reject invalid dimension")
	}
	if !strings.Contains(err.Error(), "dimension") || !strings.Contains(err.Error(), "name=value") {
		t.Fatalf("error = %q, want dimension format detail", err.Error())
	}
}

func TestCSVImporterRejectsBadTypedValueBeforeWrite(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,not-a-number\n")
	importer := csvStorageFileImporter{}
	_, err := importer.ReadTimeSeriesRows(csvPath, storageImportContext{
		Options: validStorageImportOptions(csvPath),
		Columns: map[string]*pb.DatasetColumn{
			"close": datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
		},
	})
	if err == nil {
		t.Fatalf("ReadTimeSeriesRows should reject invalid double")
	}
	if !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), "close") {
		t.Fatalf("error = %q, want row and column detail", err.Error())
	}
}

func TestRunStorageImportBindsSubjectAndWritesBatches(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,1.23\n2024-01-01 00:01:00,1.24\n")
	meta := &fakeStorageImportMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "binance_spot_kline", DataSourceId: "binance", Freqs: []string{"1m"}},
		view:    &pb.View{SpaceId: "crypto", ViewId: "swap_spot_kline_view", DatasetIds: []string{"binance_spot_kline"}},
		columns: []*pb.DatasetColumn{
			datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
		},
	}
	writer := &fakeStorageDataWriter{}
	opts := validStorageImportOptions(csvPath)
	opts.ViewID = "swap_spot_kline_view"
	opts.BatchSize = 1

	summary, err := runStorageImport(context.Background(), opts, meta, writer)
	if err != nil {
		t.Fatalf("runStorageImport returned error: %v", err)
	}
	if !summary.BoundSubject {
		t.Fatalf("BoundSubject = false, want true")
	}
	if len(meta.binds) != 1 || meta.binds[0].GetSubjectId() != "ARB-USDT" {
		t.Fatalf("binds = %+v, want ARB-USDT bind", meta.binds)
	}
	if len(writer.requests) != 2 {
		t.Fatalf("write batches = %d, want 2", len(writer.requests))
	}
	if summary.WrittenRows != 2 || summary.Batches != 2 {
		t.Fatalf("summary = %+v, want written rows=2 batches=2", summary)
	}
	if summary.Status != "imported" {
		t.Fatalf("status = %q, want imported", summary.Status)
	}
}

func TestRunStorageImportDryRunDoesNotBindOrWrite(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,1.23\n")
	meta := &fakeStorageImportMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "binance_spot_kline", DataSourceId: "binance", Freqs: []string{"1m"}},
		columns: []*pb.DatasetColumn{datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)},
	}
	writer := &fakeStorageDataWriter{}
	opts := validStorageImportOptions(csvPath)
	opts.DryRun = true

	summary, err := runStorageImport(context.Background(), opts, meta, writer)
	if err != nil {
		t.Fatalf("runStorageImport returned error: %v", err)
	}
	if summary.Status != "dry_run" || !summary.WouldBindSubject || summary.WouldWriteTimeSeriesRows != 1 {
		t.Fatalf("summary = %+v, want dry-run bind/write plan", summary)
	}
	if len(meta.binds) != 0 || len(writer.requests) != 0 {
		t.Fatalf("dry-run should not bind/write, binds=%d writes=%d", len(meta.binds), len(writer.requests))
	}
}

func TestRunStorageImportRejectsMissingSubject(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,1.23\n")
	meta := &fakeStorageImportMetadata{
		subjectMissing: true,
		dataset:        &pb.Dataset{SpaceId: "crypto", DatasetId: "binance_spot_kline", DataSourceId: "binance", Freqs: []string{"1m"}},
		columns:        []*pb.DatasetColumn{datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)},
	}

	_, err := runStorageImport(context.Background(), validStorageImportOptions(csvPath), meta, &fakeStorageDataWriter{})
	if err == nil {
		t.Fatalf("runStorageImport should reject missing subject")
	}
	if !strings.Contains(err.Error(), "subject") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want subject not found detail", err.Error())
	}
}

func TestRunStorageImportRetriesWriteAfterMetadataCacheLag(t *testing.T) {
	oldWindow := storageImportRetryWindow
	oldDelay := storageImportRetryDelay
	storageImportRetryWindow = 50 * time.Millisecond
	storageImportRetryDelay = time.Millisecond
	defer func() {
		storageImportRetryWindow = oldWindow
		storageImportRetryDelay = oldDelay
	}()

	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,1.23\n")
	meta := &fakeStorageImportMetadata{
		dataset:  &pb.Dataset{SpaceId: "crypto", DatasetId: "binance_spot_kline", DataSourceId: "binance", Freqs: []string{"1m"}},
		columns:  []*pb.DatasetColumn{datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)},
		subjects: []*pb.DatasetSubject{{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "ARB-USDT", Status: "active"}},
	}
	writer := &fakeStorageDataWriter{errors: []error{errors.New("dataset metadata is not loaded yet")}}

	summary, err := runStorageImport(context.Background(), validStorageImportOptions(csvPath), meta, writer)
	if err != nil {
		t.Fatalf("runStorageImport returned error: %v", err)
	}
	if summary.BoundSubject {
		t.Fatalf("BoundSubject = true, want false; this test covers retry without a fresh bind")
	}
	if len(writer.requests) != 2 {
		t.Fatalf("write attempts = %d, want 2", len(writer.requests))
	}
	if summary.WrittenRows != 1 {
		t.Fatalf("WrittenRows = %d, want 1", summary.WrittenRows)
	}
}

func TestRunStorageImportRejectsViewDatasetMismatch(t *testing.T) {
	csvPath := writeTempCSV(t, "candle_begin_time,close\n2024-01-01 00:00:00,1.23\n")
	meta := &fakeStorageImportMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "binance_spot_kline", DataSourceId: "binance", Freqs: []string{"1m"}},
		view:    &pb.View{SpaceId: "crypto", ViewId: "other_view", DatasetIds: []string{"other_dataset"}},
		columns: []*pb.DatasetColumn{datasetColumn("close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)},
	}
	opts := validStorageImportOptions(csvPath)
	opts.ViewID = "other_view"

	_, err := runStorageImport(context.Background(), opts, meta, &fakeStorageDataWriter{})
	if err == nil {
		t.Fatalf("runStorageImport should reject view/dataset mismatch")
	}
	if !strings.Contains(err.Error(), "view") || !strings.Contains(err.Error(), "dataset") {
		t.Fatalf("error = %q, want view/dataset detail", err.Error())
	}
}

func validStorageImportOptions(file string) storageImportOptions {
	return storageImportOptions{
		Format:       "csv",
		File:         file,
		AccessURL:    "http://127.0.0.1:20201",
		MetadataURL:  "http://127.0.0.1:20200",
		SpaceID:      "crypto",
		DatasetID:    "binance_spot_kline",
		SubjectID:    "ARB-USDT",
		DataSourceID: "binance",
		Freq:         "1m",
		TimeColumn:   "candle_begin_time",
		BatchSize:    1000,
	}
}

func datasetColumn(name string, valueType pb.FieldValueType) *pb.DatasetColumn {
	return &pb.DatasetColumn{
		SpaceId:    "crypto",
		DatasetId:  "binance_spot_kline",
		ColumnName: name,
		ValueType:  valueType,
		Status:     "active",
	}
}

func writeTempCSV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write csv failed: %v", err)
	}
	return path
}

// fakeStorageImportMetadata 是数据导入测试使用的元数据桩。
type fakeStorageImportMetadata struct {
	dataset        *pb.Dataset
	view           *pb.View
	columns        []*pb.DatasetColumn
	subjects       []*pb.DatasetSubject
	binds          []*pb.DatasetSubject
	subject        *pb.Subject
	subjectMissing bool
}

func (f *fakeStorageImportMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return f.dataset, nil
}

func (f *fakeStorageImportMetadata) GetView(context.Context, string, string) (*pb.View, error) {
	return f.view, nil
}

func (f *fakeStorageImportMetadata) GetSubject(context.Context, string, string) (*pb.Subject, error) {
	if f.subjectMissing {
		return nil, nil
	}
	if f.subject != nil {
		return f.subject, nil
	}
	return &pb.Subject{SpaceId: "crypto", SubjectId: "ARB-USDT", Status: "active"}, nil
}

func (f *fakeStorageImportMetadata) ListDatasetColumns(context.Context, string, string) ([]*pb.DatasetColumn, error) {
	return f.columns, nil
}

func (f *fakeStorageImportMetadata) ListDatasetSubjects(context.Context, string, string, string) ([]*pb.DatasetSubject, error) {
	return f.subjects, nil
}

func (f *fakeStorageImportMetadata) BindDatasetSubject(_ context.Context, item *pb.DatasetSubject) error {
	f.binds = append(f.binds, item)
	return nil
}

// fakeStorageDataWriter 是数据导入测试使用的写入桩。
type fakeStorageDataWriter struct {
	requests []*pb.WriteTimeSeriesRowsReq
	errors   []error
}

func (f *fakeStorageDataWriter) WriteTimeSeriesRows(_ context.Context, req *pb.WriteTimeSeriesRowsReq) error {
	f.requests = append(f.requests, req)
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		return err
	}
	return nil
}
