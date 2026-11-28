package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

type wiringStorageFake struct {
	subjects []string
	datasets map[string]storageio.DatasetInfo
	columns  map[string][]storageio.ColumnInfo
}

func (f *wiringStorageFake) GetDataset(_ context.Context, _, datasetID string) (storageio.DatasetInfo, error) {
	return f.datasets[datasetID], nil
}

func (f *wiringStorageFake) ListColumns(_ context.Context, _, datasetID string) ([]storageio.ColumnInfo, error) {
	return f.columns[datasetID], nil
}

func (f *wiringStorageFake) CreateResultDataset(_ context.Context, spec storageio.ResultDatasetSpec) error {
	attrs := make(map[string]string, len(spec.Attributes))
	for k, v := range spec.Attributes {
		attrs[k] = v
	}
	f.datasets[spec.DatasetID] = storageio.DatasetInfo{
		SpaceID: spec.SpaceID, DatasetID: spec.DatasetID, DataSourceID: spec.DataSourceID,
		Name: spec.Name, DataKind: spec.DataKind, Freq: spec.Frequency,
		Status: storageio.DatasetStatusActive, Attributes: attrs,
	}
	f.columns[spec.DatasetID] = append([]storageio.ColumnInfo(nil), spec.Columns...)
	return nil
}

func (f *wiringStorageFake) UpsertColumns(_ context.Context, _, datasetID string, cols []storageio.ColumnInfo) error {
	f.columns[datasetID] = append(f.columns[datasetID], cols...)
	return nil
}

func (f *wiringStorageFake) DeleteDataset(_ context.Context, _, datasetID string) error {
	delete(f.datasets, datasetID)
	delete(f.columns, datasetID)
	return nil
}

func (f *wiringStorageFake) SetDatasetSubjectTags(context.Context, string, string, []string) error {
	return nil
}

func (f *wiringStorageFake) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return f.subjects, nil
}

// 验证 factor-mgr 装配 catalog.Service 时已注入 SubjectSource，SubmitFactorParams 会拒绝不在源数据集里的覆盖标的。
func TestBootstrapCatalogServiceValidatesParamSubjects(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))

	storage := &wiringStorageFake{
		subjects: []string{"BTC-USDT", "ETH-USDT"},
		datasets: map[string]storageio.DatasetInfo{
			"dataset_prices": {
				SpaceID: "crypto", DatasetID: "dataset_prices", DataSourceID: "ds_prices",
				DataKind: storageio.DataKindTimeSeries, Freq: "1m", Status: storageio.DatasetStatusActive,
			},
		},
		columns: map[string][]storageio.ColumnInfo{
			"dataset_prices": {{ColumnName: "close", ValueType: storageio.ColumnTypeDouble}},
		},
	}
	svc := newCatalogService(db, storage, nil, nil)

	_, err = svc.CreateFactor(ctx, domain.FactorDef{
		FactorID: "ma", Name: "均线",
		Spec: "params:\n  window: {type: int, min: 2, max: 20, default: 5}\noutput: ts_mean(close, window)\n",
	})
	require.NoError(t, err)
	task, err := svc.CreateTask(ctx, domain.FactorTask{
		TaskID: "ftask_wire", SpaceID: "crypto", Name: "测试任务", SourceDatasetID: "dataset_prices", Freq: "1m",
	})
	require.NoError(t, err)
	member, err := svc.AddTaskMember(ctx, task.TaskID, "ma", domain.MemberKindVar, nil, domain.MemberStatusEnabled)
	require.NoError(t, err)
	_, err = svc.BindTask(ctx, task.TaskID, "inst-1")
	require.NoError(t, err)

	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rejected, err := svc.SubmitParams(ctx, "inst-1", task.TaskID, period, "req-bad", []catalog.MemberSubmission{{
		MemberKey: member.MemberKey,
		Snapshot: domain.ParamSnapshot{
			"window": {Default: 5, Overrides: map[string]float64{"UNKNOWN-USDT": 10}},
		},
	}})
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	require.Equal(t, store.SubmitRejected, rejected[0].Result)
	require.Contains(t, rejected[0].Reason, "UNKNOWN-USDT")

	accepted, err := svc.SubmitParams(ctx, "inst-1", task.TaskID, period, "req-ok", []catalog.MemberSubmission{{
		MemberKey: member.MemberKey,
		Snapshot: domain.ParamSnapshot{
			"window": {Default: 5, Overrides: map[string]float64{"BTC-USDT": 10}},
		},
	}})
	require.NoError(t, err)
	require.Len(t, accepted, 1)
	require.Equal(t, store.SubmitAccepted, accepted[0].Result)
}
