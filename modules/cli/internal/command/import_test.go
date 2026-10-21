package command

import (
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesformat"
	"github.com/mooyang-code/moox/packages/seriesimport/runner"
	"github.com/stretchr/testify/require"
)

func TestImportTargetsAndStep(t *testing.T) {
	s, f, err := importTargets([]string{"storage", "factor"})
	require.NoError(t, err)
	require.True(t, s && f)
	s, f, err = importTargets([]string{"factor"})
	require.NoError(t, err)
	require.False(t, s)
	require.True(t, f)
	_, _, err = importTargets([]string{"nowhere"})
	require.ErrorContains(t, err, "未知的写入目标")
	_, _, err = importTargets(nil)
	require.ErrorContains(t, err, "至少要有一个写入目标")

	step, err := importStep("5m")
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, step)
	_, err = importStep("3m")
	require.ErrorContains(t, err, "不支持的频率")
}

func TestImportFieldsFromColumnsSkipsSystemAndReserved(t *testing.T) {
	columns := []*pb.DatasetColumn{
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		{ColumnName: "trades", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
		{ColumnName: "meta", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON, Status: "active"},
		{ColumnName: "subject_id", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "sys", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM},
		{ColumnName: "off", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "disabled"},
		{ColumnName: "gone", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "deleted"},
	}
	fields, types, err := importFieldsFromColumns(columns)
	require.NoError(t, err)
	require.Equal(t, []seriesformat.Field{
		{Name: "close", Type: seriesformat.TypeDouble}, {Name: "meta", Type: seriesformat.TypeJSON},
		{Name: "trades", Type: seriesformat.TypeInt}, {Name: "volume", Type: seriesformat.TypeDouble},
	}, fields)
	require.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_INT, types["trades"])
	_, _, err = importFieldsFromColumns(nil)
	require.Error(t, err)
}

func TestImportUpsertsBuildTypedRowsAndSkipNulls(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fields := []seriesformat.Field{
		{Name: "close", Type: seriesformat.TypeDouble}, {Name: "trades", Type: seriesformat.TypeInt},
		{Name: "closed", Type: seriesformat.TypeBool}, {Name: "ts", Type: seriesformat.TypeTime}, {Name: "meta", Type: seriesformat.TypeJSON},
	}
	b := seriesformat.NewBatch(fields, 2)
	b.Times = []time.Time{t0, t0.Add(time.Minute)}
	b.Tags = []string{"venue:okx", "venue:okx"}
	b.Cols = []seriesformat.Column{
		{Null: []bool{false, true}, F: []float64{1.5, 0}},
		{Null: []bool{false, false}, I: []int64{7, 8}},
		{Null: []bool{false, false}, I: []int64{1, 0}},
		{Null: []bool{false, false}, S: []string{"2026-10-01T00:00:00Z", "2026-10-01T00:01:00Z"}},
		{Null: []bool{false, false}, S: []string{`{"a":1}`, `{}`}},
	}
	rows, err := importUpserts(runner.Chunk{Space: "crypto", Dataset: "spot", Freq: "1m", Subject: "BTC", Fields: fields, Batch: b}, nil)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	key := rows[0].GetKey().GetTimeSeries()
	require.Equal(t, "BTC", key.GetSubjectId())
	require.Equal(t, "2026-10-01T00:00:00Z", key.GetDataTime())
	require.Equal(t, "venue:okx", key.GetSeriesTag())
	require.Len(t, rows[0].GetFields(), 5)
	require.Equal(t, 1.5, rows[0].GetFields()[0].GetValue().GetDoubleValue())
	require.True(t, rows[0].GetFields()[2].GetValue().GetBoolValue())
	require.Equal(t, "2026-10-01T00:00:00Z", rows[0].GetFields()[3].GetValue().GetTimeValue())
	require.Len(t, rows[1].GetFields(), 4, "空值不写：字段保持原值")

	b.Cols[4].S[0] = "{broken"
	_, err = importUpserts(runner.Chunk{Space: "crypto", Dataset: "spot", Freq: "1m", Subject: "BTC", Fields: fields, Batch: b}, nil)
	require.ErrorContains(t, err, "invalid json")
}

func TestImportSubjectBound(t *testing.T) {
	subjects := []*pb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "deleted"}}
	require.True(t, storageImportSubjectBound(subjects, "BTC"))
	require.False(t, storageImportSubjectBound(subjects, "ETH"))
	require.False(t, storageImportSubjectBound(subjects, "SOL"))
}
