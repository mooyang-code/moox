package registry

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidateEnabledBindingAcceptsActiveTimeSeriesViewProjection(t *testing.T) {
	client := validBindingContractClient()
	syncer := NewMetadataSync(client, nil)

	err := syncer.ValidateEnabledBinding(context.Background(), contractBinding(), contractFactor())
	require.NoError(t, err)
	require.Equal(t, 1, client.getDatasetCalls)
	require.Equal(t, 1, client.listViewsCalls)
}

func TestValidateCandidateBindingSetRejectsSameBatchCycle(t *testing.T) {
	bindings := []domain.FactorBinding{
		{
			BindingID: "a-to-b", FactorID: "factor", SpaceID: "crypto",
			SourceDataset: "a", TargetDataset: "b", Freq: "1h",
			Status: domain.BindingStatusEnabled,
		},
		{
			BindingID: "b-to-a", FactorID: "factor", SpaceID: "crypto",
			SourceDataset: "b", TargetDataset: "a", Freq: "1h",
			Status: domain.BindingStatusEnabled,
		},
	}

	err := validateCandidateBindingSet(bindings)
	require.ErrorContains(t, err, "source dataset")
	require.ErrorContains(t, err, "also targeted")
}

func TestValidateEnabledBindingRejectsSecondaryOnlyView(t *testing.T) {
	client := validBindingContractClient()
	client.views = []*storagepb.View{{
		SpaceId: "crypto", ViewId: "joined", Status: "active",
		PrimaryDatasetId: "orders", DatasetIds: []string{"spot_kline"},
		ActiveIndexId: "index-a",
		ActiveColumns: []*storagepb.ViewColumn{
			{ColumnName: "spot_kline.close", OriginId: "spot_kline.close"},
			{ColumnName: "spot_kline.volume", OriginId: "spot_kline.volume"},
		},
	}}

	err := NewMetadataSync(client, nil).ValidateEnabledBinding(
		context.Background(), contractBinding(), contractFactor(),
	)
	require.ErrorContains(t, err, "has no active primary view")
}

func TestValidateAllEnabledBindingsRejectsPersistedConflict(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "factor.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	factors := store.NewFactorRepository(db)
	bindings := store.NewBindingRepository(db)
	require.NoError(t, factors.Create(ctx, domain.FactorDef{
		FactorID: "factor", Name: "Factor", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close", "volume"}, Outputs: []string{"value"},
		ParamsJSON: `{}`, LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	for _, binding := range []domain.FactorBinding{
		{
			BindingID: "a-to-b", FactorID: "factor", SpaceID: "crypto",
			SourceDataset: "a", TargetDataset: "b", Freq: "1m",
			Status: domain.BindingStatusEnabled,
		},
		{
			BindingID: "b-to-a", FactorID: "factor", SpaceID: "crypto",
			SourceDataset: "b", TargetDataset: "a", Freq: "1m",
			Status: domain.BindingStatusEnabled,
		},
	} {
		require.NoError(t, bindings.Upsert(ctx, binding))
	}
	svc := NewService(
		factors,
		NewMetadataSync(validBindingContractClient(), nil),
		Options{FactorsDir: t.TempDir()},
	).WithBindings(bindings)

	err = svc.ValidateAllEnabledBindings(ctx)
	require.ErrorContains(t, err, "also targeted")
}

func TestValidateEnabledBindingRejectsInvalidLocalContractWithoutRemoteCall(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*domain.FactorBinding)
		want   string
	}{
		"frequency": {
			mutate: func(binding *domain.FactorBinding) { binding.Freq = "0s" },
			want:   "frequency",
		},
		"self target": {
			mutate: func(binding *domain.FactorBinding) { binding.TargetDataset = binding.SourceDataset },
			want:   "must differ",
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := validBindingContractClient()
			binding := contractBinding()
			tc.mutate(&binding)

			err := NewMetadataSync(client, nil).ValidateEnabledBinding(
				context.Background(), binding, contractFactor(),
			)
			require.ErrorContains(t, err, tc.want)
			require.Zero(t, client.getDatasetCalls)
			require.Zero(t, client.listViewsCalls)
		})
	}
}

func TestValidateEnabledBindingRejectsInvalidStorageContract(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*bindingContractFake)
		want   string
	}{
		"missing dataset": {
			mutate: func(client *bindingContractFake) { client.dataset = nil },
			want:   "source dataset",
		},
		"inactive dataset": {
			mutate: func(client *bindingContractFake) { client.dataset.Status = "disabled" },
			want:   "must be active",
		},
		"record dataset": {
			mutate: func(client *bindingContractFake) {
				client.dataset.DataKind = storagepb.DataKind_DATA_KIND_RECORD
			},
			want: "time-series",
		},
		"factor result source": {
			mutate: func(client *bindingContractFake) {
				client.dataset.Attributes["dataset_role"] = "factor_result"
			},
			want: "factor_result",
		},
		"no active view": {
			mutate: func(client *bindingContractFake) { client.views = nil },
			want:   "active primary view",
		},
		"missing projected input": {
			mutate: func(client *bindingContractFake) {
				client.views[0].ActiveColumns = client.views[0].ActiveColumns[:1]
			},
			want: "volume",
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := validBindingContractClient()
			tc.mutate(client)
			err := NewMetadataSync(client, nil).ValidateEnabledBinding(
				context.Background(), contractBinding(), contractFactor(),
			)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func contractBinding() domain.FactorBinding {
	return domain.FactorBinding{
		BindingID: "binding", FactorID: "factor", SpaceID: "crypto",
		SourceDataset: "spot_kline", TargetDataset: "spot_kline_factor",
		Freq: "1m", Status: domain.BindingStatusEnabled,
	}
}

func contractFactor() domain.FactorDef {
	return domain.FactorDef{FactorID: "factor", InputColumns: []string{"close", "volume"}}
}

type bindingContractFake struct {
	dataset         *storagepb.Dataset
	views           []*storagepb.View
	getDatasetCalls int
	listViewsCalls  int
}

func validBindingContractClient() *bindingContractFake {
	return &bindingContractFake{
		dataset: &storagepb.Dataset{
			SpaceId: "crypto", DatasetId: "spot_kline",
			DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active",
			Attributes: map[string]string{},
		},
		views: []*storagepb.View{{
			SpaceId: "crypto", ViewId: "spot_kline_view", Status: "active",
			PrimaryDatasetId: "spot_kline", ActiveIndexId: "index-a",
			ActiveColumns: []*storagepb.ViewColumn{
				{ColumnName: "spot_kline.close", OriginId: "spot_kline.close"},
				{ColumnName: "spot_kline.volume", OriginId: "spot_kline.volume"},
			},
		}},
	}
}

func (f *bindingContractFake) GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	f.getDatasetCalls++
	if f.dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: contractSuccess(), Dataset: f.dataset}, nil
}

func (f *bindingContractFake) ListViews(context.Context, *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error) {
	f.listViewsCalls++
	return &storagepb.ListViewsRsp{RetInfo: contractSuccess(), Views: f.views}, nil
}

func contractSuccess() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
}

func (*bindingContractFake) CreateFactor(context.Context, *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) UpdateFactor(context.Context, *storagepb.UpdateFactorReq) (*storagepb.UpdateFactorRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) GetFactor(context.Context, *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) UpdateDataset(context.Context, *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	panic("unexpected call")
}
func (*bindingContractFake) UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	panic("unexpected call")
}
