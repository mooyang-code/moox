package catalog

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func newFactorResultActivationFixture(t *testing.T, role string) (*sqlite.Store, *Service, *pb.Dataset) {
	return newFactorResultActivationFixtureWithSchema(t, role, pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"})
}

func newFactorResultActivationFixtureWithSchema(t *testing.T, role string, kind pb.DataKind, freqs []string) (*sqlite.Store, *Service, *pb.Dataset) {
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
	keepDuration := "720h"
	if kind == pb.DataKind_DATA_KIND_RECORD {
		keepDuration = "0"
	}
	dataset, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "factor-space", DatasetId: "dataset_factor_btc_1m", DataSourceId: "factor", DataNodeId: "factor-node",
		Name: "BTC 因子", DataKind: kind, Freqs: freqs, KeepDuration: keepDuration,
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

func TestFactorResultColumnUsesOriginFactorIDWithoutFactorEntity(t *testing.T) {
	_, service, dataset := newFactorResultActivationFixture(t, "factor_result")
	column := &pb.DatasetColumn{
		SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), ColumnName: "bias_10",
		OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR,
		OriginId:   "bias",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{
			"display_name": "bias_10", "origin_factor_id": "bias", "factor_output": "bias_10",
		},
	}
	rsp, err := service.UpsertDatasetColumn(context.Background(), &pb.UpsertDatasetColumnReq{Column: column})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, "bias", rsp.GetColumn().GetOriginId())
}

func TestActivateFactorResultDatasetRejectsInvalidSchemaBeforeCommit(t *testing.T) {
	cases := []struct {
		name  string
		kind  pb.DataKind
		freqs []string
	}{
		{name: "record dataset", kind: pb.DataKind_DATA_KIND_RECORD, freqs: []string{"1m"}},
		{name: "no frequency", kind: pb.DataKind_DATA_KIND_TIME_SERIES},
		{name: "multiple frequencies", kind: pb.DataKind_DATA_KIND_TIME_SERIES, freqs: []string{"1m", "5m"}},
		{name: "blank frequency", kind: pb.DataKind_DATA_KIND_TIME_SERIES, freqs: []string{" "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, service, dataset := newFactorResultActivationFixtureWithSchema(t, "factor_result", tc.kind, tc.freqs)
			rsp, err := service.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{
				SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), ExpectedRevision: dataset.GetRevision(),
			})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			persisted, err := store.GetDataset(context.Background(), dataset.GetSpaceId(), dataset.GetDatasetId())
			require.NoError(t, err)
			require.Equal(t, "disabled", persisted.GetStatus())
			require.False(t, persisted.GetBindingLocked())
		})
	}
}

func TestEnsureFactorResultDefaultViewRejectsManagedContractConflicts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*pb.View)
	}{
		{name: "owner", mutate: func(view *pb.View) { view.Attributes["owner_module"] = "collector" }},
		{name: "view role", mutate: func(view *pb.View) { view.Attributes["view_role"] = "raw_collection" }},
		{name: "managed by", mutate: func(view *pb.View) { delete(view.Attributes, "managed_by") }},
		{name: "primary role", mutate: func(view *pb.View) { view.Attributes["primary_dataset_role"] = "raw_collection" }},
		{name: "engine", mutate: func(view *pb.View) { view.Engine = "bleve" }},
		{name: "status", mutate: func(view *pb.View) { view.Status = "disabled" }},
		{name: "grain", mutate: func(view *pb.View) { view.GrainKeys = []string{"subject_id"} }},
		{name: "frequency", mutate: func(view *pb.View) { view.FilterJson = `{"freq":"5m"}` }},
		{name: "retention", mutate: func(view *pb.View) { view.KeepDuration = "24h" }},
		{name: "columns", mutate: func(view *pb.View) { view.Columns[0].ValueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, service, dataset := newFactorResultActivationFixture(t, "factor_result")
			activateFactorResultFixture(t, service, dataset)
			view, err := store.GetView(context.Background(), dataset.GetSpaceId(), "view_factor_btc_1m")
			require.NoError(t, err)
			view = proto.Clone(view).(*pb.View)
			tc.mutate(view)
			_, err = store.UpsertView(context.Background(), view)
			require.NoError(t, err)

			err = service.ensureFactorResultDefaultView(context.Background(), dataset)
			require.Error(t, err, "an existing same-ID View must match the managed factor-result contract")
		})
	}
}

type factorResultViewCreateRaceStore struct {
	metadatastore.Store
	existing  *pb.View
	getCalls  int
	createErr error
}

func (s *factorResultViewCreateRaceStore) GetView(context.Context, string, string) (*pb.View, error) {
	s.getCalls++
	if s.getCalls == 1 {
		return nil, sql.ErrNoRows
	}
	return proto.Clone(s.existing).(*pb.View), nil
}

func (s *factorResultViewCreateRaceStore) CreateView(context.Context, *pb.View) (*pb.View, error) {
	return nil, s.createErr
}

func TestEnsureFactorResultDefaultViewRejectsErrViewExistsRaceConflict(t *testing.T) {
	store, service, dataset := newFactorResultActivationFixture(t, "factor_result")
	activateFactorResultFixture(t, service, dataset)
	existing, err := store.GetView(context.Background(), dataset.GetSpaceId(), "view_factor_btc_1m")
	require.NoError(t, err)
	existing.Attributes["owner_module"] = "collector"
	raceStore := &factorResultViewCreateRaceStore{Store: store, existing: existing, createErr: metadatastore.ErrViewExists}
	service.metadata = raceStore

	err = service.ensureFactorResultDefaultView(context.Background(), dataset)
	require.Error(t, err, "the ErrViewExists re-read must validate the same managed contract")
	require.Equal(t, 2, raceStore.getCalls)
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
