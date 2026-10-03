package catalog

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func newFactorResultActivationFixture(t *testing.T, role string) (*sqlite.Store, *Service, *pb.Dataset) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.InitSchema(ctx))
	_, err = store.UpsertSpace(ctx, &pb.Space{SpaceId: "factor-space", Name: "因子空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "factor-space", DataSourceId: "factor", Name: "因子计算", Kind: "internal"})
	require.NoError(t, err)
	_, err = store.RegisterDataNode(ctx, "factor-node", "ip://127.0.0.1:19090", "因子节点")
	require.NoError(t, err)
	dataset, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "factor-space", DatasetId: "dataset_factor_btc_1m", DataSourceId: "factor", DataNodeId: "factor-node",
		Name: "BTC 因子", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, KeepDuration: "720h",
		Attributes: map[string]string{"dataset_role": role, "owner_module": "factor"},
	})
	require.NoError(t, err)
	for _, column := range []struct{ name, display string }{{"close", "收盘"}, {"bias_20", "偏离"}} {
		_, err := store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
			SpaceId: "factor-space", DatasetId: dataset.GetDatasetId(), ColumnName: column.name,
			OriginId:   column.name,
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Attributes: map[string]string{"display_name": column.display},
		})
		require.NoError(t, err)
	}
	service, err := NewMetadataService(store, nil, Options{
		AuthSecret:       "secret",
		NodeStateChecker: &fakeNodeStateChecker{rsp: readyNodeState("factor-node")},
	})
	require.NoError(t, err)
	return store, service, dataset
}

func activateFactorResultFixture(t *testing.T, service *Service, dataset *pb.Dataset) *pb.ActivateDatasetRsp {
	t.Helper()
	rsp, err := service.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{
		SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), ExpectedRevision: dataset.GetRevision(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	return rsp
}

func TestActivateFactorResultDatasetCreatesDefaultView(t *testing.T) {
	store, service, dataset := newFactorResultActivationFixture(t, "factor_result")
	activateFactorResultFixture(t, service, dataset)

	view, err := store.GetView(context.Background(), dataset.GetSpaceId(), "view_factor_btc_1m")
	require.NoError(t, err)
	require.Equal(t, dataset.GetDatasetId(), view.GetDatasetId())
	require.Equal(t, dataset.GetName(), view.GetName())
	require.Equal(t, dataset.GetKeepDuration(), view.GetKeepDuration())
	require.Equal(t, []string{"subject_id", "freq", "data_time", "series_tag"}, view.GetGrainKeys())
	require.Equal(t, `{"freq":"1m"}`, view.GetFilterJson())
	require.Equal(t, "duckdb", view.GetEngine())
	require.Equal(t, "factor_result", view.GetAttributes()["primary_dataset_role"])

	columns, _, err := store.ListViewColumns(context.Background(), view.GetSpaceId(), view.GetViewId(), nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		dataset.GetDatasetId() + ".close", dataset.GetDatasetId() + ".bias_20",
	}, viewColumnNames(columns))
}

func TestActivateFactorResultDatasetIsIdempotent(t *testing.T) {
	store, service, dataset := newFactorResultActivationFixture(t, "factor_result")
	activateFactorResultFixture(t, service, dataset)
	activateFactorResultFixture(t, service, dataset)

	views, _, err := store.ListViews(context.Background(), dataset.GetSpaceId(), dataset.GetDatasetId(), "", nil)
	require.NoError(t, err)
	require.Len(t, views, 1)
}

func TestActivateRawCollectionDatasetDoesNotCreateView(t *testing.T) {
	store, service, dataset := newFactorResultActivationFixture(t, "raw_collection")
	activateFactorResultFixture(t, service, dataset)

	_, err := store.GetView(context.Background(), dataset.GetSpaceId(), "view_factor_btc_1m")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestNewFactorResultColumnExtendsDefaultViewDesiredSchema(t *testing.T) {
	ctx := context.Background()
	store, service, dataset := newFactorResultActivationFixture(t, "factor_result")
	activateFactorResultFixture(t, service, dataset)
	view, err := store.GetView(ctx, dataset.GetSpaceId(), "view_factor_btc_1m")
	require.NoError(t, err)
	beforeRevision := view.GetDesiredViewRevision()
	column := &pb.DatasetColumn{
		SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), ColumnName: "momentum_5",
		OriginId: "momentum_5", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "动量"},
	}
	_, err = store.UpsertDatasetColumn(ctx, column)
	require.NoError(t, err)
	view, err = store.GetView(ctx, dataset.GetSpaceId(), view.GetViewId())
	require.NoError(t, err)
	require.Equal(t, beforeRevision+1, view.GetDesiredViewRevision())
	require.Contains(t, viewColumnNames(view.GetColumns()), dataset.GetDatasetId()+".momentum_5")

	_, err = store.UpsertDatasetColumn(ctx, column)
	require.NoError(t, err)
	view, err = store.GetView(ctx, dataset.GetSpaceId(), view.GetViewId())
	require.NoError(t, err)
	require.Equal(t, beforeRevision+1, view.GetDesiredViewRevision(), "idempotent metadata upsert must not advance View revision")
}

func viewColumnNames(columns []*pb.ViewColumn) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			names = append(names, column.GetColumnName())
		}
	}
	return names
}
