package access

import (
	"context"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
)

func TestCreateSpaceRejectsMissingFields(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	rsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCreateSpacePersistsGeneratedID(t *testing.T) {
	store := &stubMetadataStore{}
	svc := &Service{metadata: store}
	rsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{Name: "Crypto"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetSpace().GetSpaceId())
	assert.Equal(t, rsp.GetSpace().GetSpaceId(), store.lastSpace.GetSpaceId())
}

func TestGetSpaceReturnsStoredValue(t *testing.T) {
	store := &stubMetadataStore{
		spaces: map[string]*pb.Space{"crypto": {SpaceId: "crypto", Name: "Crypto"}},
	}
	svc := &Service{metadata: store}
	rsp, err := svc.GetSpace(context.Background(), &pb.GetSpaceReq{SpaceId: "crypto"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "Crypto", rsp.GetSpace().GetName())
}

func TestStorageRootUsesEnvOverride(t *testing.T) {
	t.Setenv("MOOX_STORAGE_HOME", "/tmp/moox-storage")
	assert.Equal(t, "/tmp/moox-storage", storageRoot(""))
	assert.Equal(t, "/custom", storageRoot("/custom"))
}

type stubMetadataStore struct {
	spaces    map[string]*pb.Space
	devices   []*pb.Device
	lastSpace *pb.Space
	lastNode  *pb.PrimaryStoreNode
}

func (s *stubMetadataStore) UpsertSpace(_ context.Context, space *pb.Space) (*pb.Space, error) {
	if s.spaces == nil {
		s.spaces = map[string]*pb.Space{}
	}
	copied := proto.Clone(space).(*pb.Space)
	s.spaces[copied.GetSpaceId()] = copied
	s.lastSpace = copied
	return copied, nil
}

func (s *stubMetadataStore) GetSpace(_ context.Context, spaceID string) (*pb.Space, error) {
	if space, ok := s.spaces[spaceID]; ok {
		return proto.Clone(space).(*pb.Space), nil
	}
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) Close() error { return nil }

func (s *stubMetadataStore) InitSchema(context.Context) error { return nil }

func (s *stubMetadataStore) TableNames(context.Context) ([]string, error) { return nil, nil }

func (s *stubMetadataStore) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) GetView(context.Context, string, string) (*pb.View, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListViews(context.Context, string, string, string, *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) ListViewsByDataset(context.Context, string, string) ([]*pb.View, error) {
	return nil, nil
}

func (s *stubMetadataStore) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertView(context.Context, *pb.View) (*pb.View, error) { return nil, nil }

func (s *stubMetadataStore) UpsertViewColumn(context.Context, *pb.ViewColumn) (*pb.ViewColumn, error) {
	return nil, nil
}

func (s *stubMetadataStore) ClaimViewIndexBuild(context.Context, *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	return nil, false, nil
}

func (s *stubMetadataStore) UpdateViewIndexBuild(context.Context, *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	return nil, nil
}

func (s *stubMetadataStore) ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq) (*pb.View, error) {
	return nil, nil
}

func (s *stubMetadataStore) FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetDataSource(context.Context, string, string) (*pb.DataSource, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListDataSources(context.Context, string, string, string, *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDataSource(context.Context, *pb.DataSource) (*pb.DataSource, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetSubject(context.Context, string, string) (*pb.Subject, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListSubjects(context.Context, string, string, string, []string, *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) ListSubjectSymbols(context.Context, string, string, string, string, *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertSubject(context.Context, *pb.Subject) (*pb.Subject, error) {
	return nil, nil
}

func (s *stubMetadataStore) UpsertSubjectSymbol(context.Context, *pb.SubjectSymbol) (*pb.SubjectSymbol, error) {
	return nil, nil
}

func (s *stubMetadataStore) RegisterDataSubject(context.Context, *pb.Subject, *pb.SubjectSymbol, []*pb.DatasetSubject) (*pb.Subject, []*pb.DatasetSubject, error) {
	return nil, nil, nil
}

func (s *stubMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListDatasets(context.Context, string, string, pb.DataKind, string, *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) ListDatasetSubjects(context.Context, string, string, string, *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDataset(context.Context, *pb.Dataset) (*pb.Dataset, error) {
	return nil, nil
}

func (s *stubMetadataStore) BindDatasetSubject(context.Context, *pb.DatasetSubject) (*pb.DatasetSubject, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetFieldGroup(context.Context, string, string) (*pb.FieldGroup, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListFieldGroups(context.Context, string, string, *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertFieldGroup(context.Context, *pb.FieldGroup) (*pb.FieldGroup, error) {
	return nil, nil
}

func (s *stubMetadataStore) CreateFieldGroup(context.Context, *pb.FieldGroup) (*pb.FieldGroup, error) {
	return nil, nil
}

func (s *stubMetadataStore) UpdateFieldGroup(context.Context, *pb.FieldGroup) (*pb.FieldGroup, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetField(context.Context, string, string) (*pb.Field, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListFields(context.Context, metadata.FieldQuery) ([]*pb.Field, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) CountFieldsByGroup(context.Context, string) (metadata.FieldGroupCounts, error) {
	return metadata.FieldGroupCounts{ByGroup: map[string]uint64{}}, nil
}

func (s *stubMetadataStore) UpsertField(context.Context, *pb.Field) (*pb.Field, error) {
	return nil, nil
}

func (s *stubMetadataStore) CreateField(context.Context, *pb.Field) (*pb.Field, error) {
	return nil, nil
}

func (s *stubMetadataStore) UpdateField(context.Context, *pb.Field) (*pb.Field, error) {
	return nil, nil
}

func (s *stubMetadataStore) BatchUpdateFields(context.Context, string, []string, string, string) (uint32, error) {
	return 0, nil
}

func (s *stubMetadataStore) DeleteFieldGroup(context.Context, string, string) error {
	return nil
}

func (s *stubMetadataStore) GetFactor(context.Context, string, string) (*pb.Factor, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListFactors(context.Context, string, string, *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertFactor(context.Context, *pb.Factor) (*pb.Factor, error) {
	return nil, nil
}

func (s *stubMetadataStore) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDatasetColumn(context.Context, *pb.DatasetColumn) (*pb.DatasetColumn, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetPrimaryStoreNode(context.Context, string) (*pb.PrimaryStoreNode, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListPrimaryStoreNodes(context.Context, *pb.Page) ([]*pb.PrimaryStoreNode, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertPrimaryStoreNode(_ context.Context, item *pb.PrimaryStoreNode) (*pb.PrimaryStoreNode, error) {
	copied := proto.Clone(item).(*pb.PrimaryStoreNode)
	s.lastNode = copied
	return copied, nil
}

func (s *stubMetadataStore) GetDevice(_ context.Context, deviceID string) (*pb.Device, error) {
	for _, device := range s.devices {
		if device.GetDeviceId() == deviceID {
			return proto.Clone(device).(*pb.Device), nil
		}
	}
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListDevices(_ context.Context, nodeID string, _ string, _ *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	var out []*pb.Device
	for _, device := range s.devices {
		if nodeID == "" || device.GetNodeId() == nodeID {
			out = append(out, proto.Clone(device).(*pb.Device))
		}
	}
	return out, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDevice(_ context.Context, item *pb.Device) (*pb.Device, error) {
	copied := proto.Clone(item).(*pb.Device)
	s.devices = append(s.devices, copied)
	return copied, nil
}

func (s *stubMetadataStore) GetPrimaryStoreRoute(context.Context, string, string) (*pb.PrimaryStoreRoute, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertPrimaryStoreRoute(context.Context, *pb.PrimaryStoreRoute) (*pb.PrimaryStoreRoute, error) {
	return nil, nil
}

func (s *stubMetadataStore) ListArchiveFiles(context.Context, string, string, *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) RegisterArchiveFile(context.Context, *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	return nil, nil
}

var _ metadata.Store = (*stubMetadataStore)(nil)

func TestSpaceAndViewCRUDFlow(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto", Name: "Crypto", Status: "active",
	}})
	mustRetOK(t, spaceRsp, err)

	dataSourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active",
	}})
	mustRetOK(t, dataSourceRsp, err)

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance",
		Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active",
		Freqs: []string{"1m"},
	}})
	mustRetOK(t, datasetRsp, err)

	viewRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId: "crypto", ViewId: "kline_view", Name: "K线视图",
		DatasetIds: []string{"kline"}, Engine: "duckdb", Status: "active",
		FilterJson: `{"freq":"1m"}`,
	}})
	mustRetOK(t, viewRsp, err)

	getViewRsp, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "crypto", ViewId: "kline_view"})
	mustRetOK(t, getViewRsp, err)

	viewRsp.GetView().Name = "K线视图更新"
	updateViewRsp, err := svc.UpdateView(ctx, &pb.UpdateViewReq{View: viewRsp.GetView()})
	mustRetOK(t, updateViewRsp, err)

	listViewsRsp, err := svc.ListViews(ctx, &pb.ListViewsReq{SpaceId: "crypto"})
	mustRetOK(t, listViewsRsp, err)
	require.NotEmpty(t, listViewsRsp.GetViews())

	colRsp, err := svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "kline_view", ColumnName: "close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "收盘价"},
	}})
	mustRetOK(t, colRsp, err)

	listColsRsp, err := svc.ListViewColumns(ctx, &pb.ListViewColumnsReq{SpaceId: "crypto", ViewId: "kline_view"})
	mustRetOK(t, listColsRsp, err)

	listSpacesRsp, err := svc.ListSpaces(ctx, &pb.ListSpacesReq{})
	mustRetOK(t, listSpacesRsp, err)

	updateSpaceRsp, err := svc.UpdateSpace(ctx, &pb.UpdateSpaceReq{Space: spaceRsp.GetSpace()})
	mustRetOK(t, updateSpaceRsp, err)
}

func TestCreateViewRejectsMissingFields(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	rsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
