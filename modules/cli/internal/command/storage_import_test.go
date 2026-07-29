package command

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferStorageImportFormat(t *testing.T) {
	format, err := inferStorageImportFormat("auto", "/tmp/data.csv")
	require.NoError(t, err)
	assert.Equal(t, "csv", format)
	_, err = inferStorageImportFormat("auto", "/tmp/data.json")
	require.Error(t, err)
	_, err = inferStorageImportFormat("xml", "/tmp/data.csv")
	require.Error(t, err)
}

func TestStorageImporterForFormat(t *testing.T) {
	importer, err := storageImporterForFormat("csv")
	require.NoError(t, err)
	assert.Equal(t, "csv", importer.Format())
	_, err = storageImporterForFormat("xml")
	require.Error(t, err)
}

func TestValidateStorageImportOptions(t *testing.T) {
	err := validateStorageImportOptions(storageImportOptions{
		File: "a.csv", MetadataURL: "http://meta", SpaceID: "s", DatasetID: "d",
		SubjectID: "sub", TimeColumn: "t", AccessURL: "http://access", Freq: "1m",
	})
	require.NoError(t, err)
	err = validateStorageImportOptions(storageImportOptions{File: "a.csv"})
	require.Error(t, err)
	err = validateStorageImportOptions(storageImportOptions{
		File: "a.csv", MetadataURL: "http://meta", SpaceID: "s", DatasetID: "d",
		SubjectID: "sub", TimeColumn: "t", DryRun: true, Freq: "1m",
	})
	require.NoError(t, err)
}

func TestValidateStorageImportFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.csv")
	require.NoError(t, os.WriteFile(path, []byte("a,b\n1,2\n"), 0o600))
	require.NoError(t, validateStorageImportFile(path))
	require.Error(t, validateStorageImportFile("/etc/passwd"))
}

func TestValidateStorageImportFreq(t *testing.T) {
	dataset := &pb.Dataset{DatasetId: "d1", Freqs: []string{"1m", "1h"}}
	require.NoError(t, validateStorageImportFreq(storageImportOptions{Freq: "1m"}, dataset))
	require.Error(t, validateStorageImportFreq(storageImportOptions{Freq: "1d"}, dataset))
	require.NoError(t, validateStorageImportFreq(storageImportOptions{}, dataset))
}

func TestNormalizedStorageImportBatchSize(t *testing.T) {
	assert.Equal(t, defaultStorageImportBatchSize, normalizedStorageImportBatchSize(0))
	assert.Equal(t, 200, normalizedStorageImportBatchSize(200))
}

func TestRetryableStorageImportWriteError(t *testing.T) {
	assert.True(t, retryableStorageImportWriteError(assertErr("subject not bound")))
	assert.True(t, retryableStorageImportWriteError(assertErr("路由未注册")))
	assert.False(t, retryableStorageImportWriteError(assertErr("permission denied")))
	assert.False(t, retryableStorageImportWriteError(nil))
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestEmptyCSVRecord(t *testing.T) {
	assert.True(t, emptyCSVRecord([]string{"", "  "}))
	assert.False(t, emptyCSVRecord([]string{"", "x"}))
}

func TestNormalizeStorageImportTime(t *testing.T) {
	got, err := normalizeStorageImportTime("2026-01-02T03:04:05Z")
	require.NoError(t, err)
	assert.Contains(t, got, "2026-01-02T03:04:05")
	got, err = normalizeStorageImportTime("2026-01-02 03:04:05")
	require.NoError(t, err)
	assert.Contains(t, got, "2026-01-02")
	_, err = normalizeStorageImportTime("not-a-time")
	require.Error(t, err)
}

func TestStorageImportTypedValue(t *testing.T) {
	v, err := storageImportTypedValue("1.5", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)
	require.NoError(t, err)
	assert.InDelta(t, 1.5, v.GetDoubleValue(), 0.001)
	v, err = storageImportTypedValue("true", pb.FieldValueType_FIELD_VALUE_TYPE_BOOL)
	require.NoError(t, err)
	assert.True(t, v.GetBoolValue())
	_, err = storageImportTypedValue("x", pb.FieldValueType_FIELD_VALUE_TYPE_INT)
	require.Error(t, err)
}

func TestStorageImportTypedValueMoreTypes(t *testing.T) {
	v, err := storageImportTypedValue("hello", pb.FieldValueType_FIELD_VALUE_TYPE_STRING)
	require.NoError(t, err)
	assert.Equal(t, "hello", v.GetStringValue())
	v, err = storageImportTypedValue("42", pb.FieldValueType_FIELD_VALUE_TYPE_INT)
	require.NoError(t, err)
	assert.Equal(t, int64(42), v.GetIntValue())
	v, err = storageImportTypedValue(`{"a":1}`, pb.FieldValueType_FIELD_VALUE_TYPE_JSON)
	require.NoError(t, err)
	assert.Contains(t, v.GetJsonValue(), `"a"`)
	v, err = storageImportTypedValue("raw", pb.FieldValueType_FIELD_VALUE_TYPE_BYTES)
	require.NoError(t, err)
	assert.Equal(t, []byte("raw"), v.GetBytesValue())
	_, err = storageImportTypedValue("bad", pb.FieldValueType(999))
	require.Error(t, err)
}

func TestStorageImportValueTypeName(t *testing.T) {
	assert.Equal(t, "double", storageImportValueTypeName(pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE))
}

func TestWriteStorageImportSummary(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	err = writeStorageImportSummary(storageImportSummary{Status: "imported", WrittenRows: 3})
	w.Close()
	os.Stdout = old
	require.NoError(t, err)
	raw, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"written_rows": 3`)
}

func TestWriteStorageImportRowsImmediateSuccess(t *testing.T) {
	writer := fakeStorageWriter{}
	require.NoError(t, writeStorageImportRows(context.Background(), writer, &pb.PrimaryUpsertFieldsReq{}, false))
}

type retryOnceWriter struct{ calls int }

func (w *retryOnceWriter) UpsertFields(context.Context, *pb.PrimaryUpsertFieldsReq) error {
	w.calls++
	if w.calls == 1 {
		return assertErr("subject not bound")
	}
	return nil
}

func TestWriteStorageImportRowsRetriesRetryableError(t *testing.T) {
	writer := &retryOnceWriter{}
	require.NoError(t, writeStorageImportRows(context.Background(), writer, &pb.PrimaryUpsertFieldsReq{}, true))
	assert.Equal(t, 2, writer.calls)
}

func TestRunStorageImportWritePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.csv")
	content := "meta\nignore\nopen_time,close\n2026-01-02T03:04:05Z,1.25\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	meta := fakeStorageImportMetaFull{
		fakeStorageImportMeta: fakeStorageImportMeta{
			dataset: &pb.Dataset{DatasetId: "kline", Freqs: []string{"1m"}, Status: "active"},
			subject: &pb.Subject{SubjectId: "BTC", Status: "active"},
			columns: []*pb.DatasetColumn{
				{ColumnName: "open_time", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME, Required: true, Status: "active"},
				{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Required: true, Status: "active"},
			},
		},
		subjects: []*pb.DatasetSubject{},
	}
	writer := &trackingStorageWriter{}
	summary, err := runStorageImport(context.Background(), storageImportOptions{
		Format: "csv", File: path, MetadataURL: "http://meta", AccessURL: "http://access",
		SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", TimeColumn: "open_time",
		BatchSize: 100,
	}, &meta, writer)
	require.NoError(t, err)
	assert.Equal(t, "imported", summary.Status)
	assert.Equal(t, 1, summary.WrittenRows)
	assert.True(t, summary.BoundSubject)
	assert.Equal(t, 1, writer.writes)
}

type fakeStorageImportMetaFull struct {
	fakeStorageImportMeta
	subjects []*pb.DatasetSubject
	bound    bool
}

func (f *fakeStorageImportMetaFull) ListDatasetSubjects(context.Context, string, string, string) ([]*pb.DatasetSubject, error) {
	return f.subjects, nil
}

func (f *fakeStorageImportMetaFull) BindDatasetSubject(context.Context, *pb.DatasetSubject) error {
	f.bound = true
	return nil
}

type trackingStorageWriter struct{ writes int }

func (w *trackingStorageWriter) UpsertFields(context.Context, *pb.PrimaryUpsertFieldsReq) error {
	w.writes++
	return nil
}

func TestStringSliceContains(t *testing.T) {
	assert.True(t, stringSliceContains([]string{"a", "b"}, "b"))
	assert.False(t, stringSliceContains([]string{"a"}, "c"))
}

func TestStorageImportColumnMap(t *testing.T) {
	_, err := storageImportColumnMap(nil)
	require.Error(t, err)
	m, err := storageImportColumnMap([]*pb.DatasetColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "deleted", Status: "deleted"},
	})
	require.NoError(t, err)
	assert.Contains(t, m, "close")
}

func TestStorageImportSubjectBound(t *testing.T) {
	subjects := []*pb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "deleted"}}
	assert.True(t, storageImportSubjectBound(subjects, "BTC"))
	assert.False(t, storageImportSubjectBound(subjects, "ETH"))
}

func TestValidateStorageCSVHeader(t *testing.T) {
	ctx := storageImportContext{
		Options: storageImportOptions{TimeColumn: "time", DatasetID: "d1"},
		Columns: map[string]*pb.DatasetColumn{
			"time":  {ColumnName: "time", Required: true},
			"close": {ColumnName: "close", Required: true},
		},
	}
	header, idx, err := validateStorageCSVHeader([]string{"time", "close"}, ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, idx)
	assert.Equal(t, []string{"time", "close"}, header)
	_, _, err = validateStorageCSVHeader([]string{"close"}, ctx)
	require.Error(t, err)
}

func TestCSVStorageFileImporterReadsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.csv")
	content := "meta\nignore\nopen_time,close\n2026-01-02T03:04:05Z,1.25\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	ctx := storageImportContext{
		Options: storageImportOptions{
			SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m",
			TimeColumn: "open_time",
		},
		Columns: map[string]*pb.DatasetColumn{
			"open_time": {ColumnName: "open_time", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME, Required: true},
			"close":     {ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Required: true},
		},
	}
	result, err := csvStorageFileImporter{}.ReadTimeSeriesRows(path, ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Stats.ValidatedRows)
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "BTC", result.Rows[0].GetKey().GetSubjectId())
}

func TestCSVStorageFileImporterUsesScalarSeriesTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.csv")
	content := "data_time,series_tag,close\n2026-01-02T03:04:05Z,venue:binance,1.25\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	ctx := storageImportContext{
		Options: storageImportOptions{
			SpaceID: "crypto", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h",
			TimeColumn: "data_time", SeriesTag: "venue:binance",
		},
		Columns: map[string]*pb.DatasetColumn{
			"close": {ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Required: true},
		},
	}
	result, err := csvStorageFileImporter{}.ReadTimeSeriesRows(path, ctx)
	require.NoError(t, err)
	require.Equal(t, "venue:binance", result.Rows[0].GetKey().GetSeriesTag())
	upserts := storageImportUpserts(result.Rows)
	require.Equal(t, "venue:binance", upserts[0].GetKey().GetTimeSeries().GetSeriesTag())
}
