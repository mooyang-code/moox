package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type datasetMetadataStore struct {
	metadata.Store
	dataset     *pb.Dataset
	datasetErr  error
	upsertCalls int
}

type createSpaceMetadataStore struct {
	metadata.Store
	space      *pb.Space
	createCall int
	readErr    error
}

func (s *createSpaceMetadataStore) CreateSpace(_ context.Context, item *pb.Space) (*pb.Space, error) {
	s.createCall++
	s.space = item
	return item, nil
}

func (s *createSpaceMetadataStore) GetSpace(context.Context, string) (*pb.Space, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.space, nil
}

func (s *datasetMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	if s.datasetErr != nil {
		return nil, s.datasetErr
	}
	if s.dataset == nil {
		return nil, sql.ErrNoRows
	}
	return s.dataset, nil
}

func (s *datasetMetadataStore) UpsertDataset(_ context.Context, item *pb.Dataset, _ ...*pb.DatasetColumn) (*pb.Dataset, error) {
	s.upsertCalls++
	updated := proto.Clone(item).(*pb.Dataset)
	updated.Revision++
	s.dataset = updated
	return updated, nil
}

func newDatasetService(t *testing.T, dataset *pb.Dataset) (*Service, *datasetMetadataStore) {
	t.Helper()
	store := &datasetMetadataStore{dataset: dataset}
	svc, err := NewMetadataService(store, nil, Options{})
	require.NoError(t, err)
	return svc, store
}

func testDataset(status string) *pb.Dataset {
	return &pb.Dataset{SpaceId: "space-a", DatasetId: "dataset_a", Name: "数据集", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: status, Revision: 7}
}

// 激活已无作用：检查总是就绪，激活对启用的数据集不写库，对停用的数据集重新启用；响应总带 binding_locked。
func TestDatasetActivationIsANoOpForActiveDatasets(t *testing.T) {
	svc, store := newDatasetService(t, testDataset("active"))
	check, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, check.GetRetInfo().GetCode())
	require.True(t, check.GetReady())
	require.Equal(t, uint64(7), check.GetDatasetRevision())

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 1})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.GetDataset().GetBindingLocked())
	require.Equal(t, uint64(7), rsp.GetDataset().GetRevision())
	require.Zero(t, store.upsertCalls)
}

func TestActivateDatasetReenablesDisabledDataset(t *testing.T) {
	svc, store := newDatasetService(t, testDataset("disabled"))
	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "active", rsp.GetDataset().GetStatus())
	require.Equal(t, 1, store.upsertCalls)
}

func TestCreateSpaceSucceedsWhenCacheRefreshFailsAfterCommit(t *testing.T) {
	store := &createSpaceMetadataStore{}
	service, err := NewMetadataService(store, &metacache.Store{}, Options{})
	require.NoError(t, err)

	rsp, err := service.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "space-create", Name: "Create", Owner: "test",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, store.createCall)
	require.Equal(t, "space-create", rsp.GetSpace().GetSpaceId())
}

func TestGetSpaceDistinguishesMissingFromStoreFailure(t *testing.T) {
	store := &createSpaceMetadataStore{readErr: errors.New("sqlite read failed")}
	service, err := NewMetadataService(store, nil, Options{})
	require.NoError(t, err)

	failed, err := service.GetSpace(context.Background(), &pb.GetSpaceReq{SpaceId: "space"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, failed.GetRetInfo().GetCode())

	store.readErr = fmt.Errorf("missing: %w", sql.ErrNoRows)
	missing, err := service.GetSpace(context.Background(), &pb.GetSpaceReq{SpaceId: "space"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SPACE_NOT_FOUND, missing.GetRetInfo().GetCode())
}

func TestDatasetActivationReadErrorsAreSafeAndDistinguishedFromMissing(t *testing.T) {
	svc, store := newDatasetService(t, testDataset("disabled"))
	store.datasetErr = fmt.Errorf("wrapped metadata read: %w", errors.New("sqlite: secret=do-not-leak"))

	checkRsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, checkRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset metadata could not be read", checkRsp.GetRetInfo().GetMsg())

	activateRsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, activateRsp.GetRetInfo().GetCode())
	require.NotContains(t, activateRsp.GetRetInfo().GetMsg(), "secret")

	store.datasetErr = fmt.Errorf("wrapped missing Dataset: %w", sql.ErrNoRows)
	missingRsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, missingRsp.GetRetInfo().GetCode())

	activateMissingRsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, activateMissingRsp.GetRetInfo().GetCode())
}

func TestTimeSeriesViewGrainIncludesSeriesTag(t *testing.T) {
	require.Equal(t,
		[]string{"subject_id", "freq", "data_time", "series_tag"},
		defaultViewGrainKeys(pb.DataKind_DATA_KIND_TIME_SERIES),
	)
}

func TestReservedTimeSeriesSystemColumnNamesAreRejected(t *testing.T) {
	svc := &Service{}
	for _, name := range []string{
		"subject_id", "freq", "data_time", "series_tag",
		"Subject_ID", "FREQ", "Data_Time", "Series_Tag",
	} {
		t.Run("field/"+name, func(t *testing.T) {
			field := &pb.Field{SpaceId: "space", GroupId: "group", FieldId: name}
			createRsp, err := svc.CreateField(context.Background(), &pb.CreateFieldReq{Field: field})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, createRsp.GetRetInfo().GetCode())
			require.Contains(t, createRsp.GetRetInfo().GetMsg(), "reserved system column")

			updateRsp, err := svc.UpdateField(context.Background(), &pb.UpdateFieldReq{Field: field})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, updateRsp.GetRetInfo().GetCode())
			require.Contains(t, updateRsp.GetRetInfo().GetMsg(), "reserved system column")
		})
		t.Run("view_column/"+name, func(t *testing.T) {
			column := &pb.ViewColumn{
				SpaceId:    "mooxsys",
				ViewId:     "view_test",
				ColumnName: name,
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION,
				Attributes: map[string]string{"display_name": name},
			}
			rsp, err := svc.UpsertViewColumn(context.Background(), &pb.UpsertViewColumnReq{Column: column})
			require.NoError(t, err)
			require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
			require.Contains(t, rsp.GetRetInfo().GetMsg(), "reserved system column")
		})
	}
}

func TestCreateAndUpdateViewRejectEmbeddedReservedColumns(t *testing.T) {
	svc := &Service{}
	newView := func() *pb.View {
		return &pb.View{
			SpaceId:   "space",
			ViewId:    "view_test",
			Name:      "测试视图",
			DatasetId: "dataset",
			Columns: []*pb.ViewColumn{
				nil,
				{ColumnName: "Series_Tag", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION},
			},
		}
	}

	createRsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{View: newView()})
	require.NoError(t, err)
	require.NotEqual(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	require.Contains(t, createRsp.GetRetInfo().GetMsg(), "reserved system column")

	updateRsp, err := svc.UpdateView(context.Background(), &pb.UpdateViewReq{View: newView()})
	require.NoError(t, err)
	require.NotEqual(t, pb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
	require.Contains(t, updateRsp.GetRetInfo().GetMsg(), "reserved system column")
}

func TestDatasetMetadataHasNoSeriesTagRegistry(t *testing.T) {
	fields := (&pb.Dataset{}).ProtoReflect().Descriptor().Fields()
	for _, forbidden := range []string{"series_tag_" + "name", "series_tag_allowed_values", "series_tag_registry"} {
		require.Nil(t, fields.ByName(protoreflect.Name(forbidden)), forbidden)
	}
}

type deleteDatasetMetadataStore struct {
	metadata.Store
	dataset *pb.Dataset
	deleted int
}

func (s *deleteDatasetMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	if s.dataset == nil {
		return nil, sql.ErrNoRows
	}
	return s.dataset, nil
}

func (s *deleteDatasetMetadataStore) DeleteDataset(context.Context, string, string) error {
	if s.dataset == nil {
		return sql.ErrNoRows
	}
	s.deleted++
	s.dataset = nil
	return nil
}

// recordingRows 记录删行请求，并记下删行时元数据里是否还有这个数据集。
type recordingRows struct {
	store       *deleteDatasetMetadataStore
	deleted     []string
	sawMetadata []bool
}

func (r *recordingRows) DeleteDatasetRows(_ context.Context, spaceID, datasetID string) (uint64, error) {
	r.deleted = append(r.deleted, spaceID+"/"+datasetID)
	r.sawMetadata = append(r.sawMetadata, r.store.dataset != nil)
	return 1, nil
}

// 删除数据集先删元数据、再删行；元数据已经不在时照样删行，返回“不存在”。
func TestDeleteDatasetRemovesMetadataThenRows(t *testing.T) {
	store := &deleteDatasetMetadataStore{dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "ordinary", DataSourceId: "binance"}}
	rows := &recordingRows{store: store}
	svc, err := NewMetadataService(store, nil, Options{Rows: rows})
	require.NoError(t, err)
	rsp, err := svc.DeleteDataset(context.Background(), &pb.DeleteDatasetReq{SpaceId: "crypto", DatasetId: "ordinary"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, store.deleted)
	require.Equal(t, []string{"crypto/ordinary"}, rows.deleted)
	require.Equal(t, []bool{false}, rows.sawMetadata, "删行时元数据必须已经删掉")

	retry, err := svc.DeleteDataset(context.Background(), &pb.DeleteDatasetReq{SpaceId: "crypto", DatasetId: "ordinary"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, retry.GetRetInfo().GetCode())
	require.Len(t, rows.deleted, 2, "重试删除仍要删行")
}

// 列只能追加：数据集列被禁用后 View 里的这一列要留着，更新 View 时只校验新列。
func TestNewViewColumnsSkipsDeclaredAndBuiltColumns(t *testing.T) {
	view := &pb.View{
		Columns:       []*pb.ViewColumn{{ColumnName: "declared"}},
		ActiveColumns: []*pb.ViewColumn{{ColumnName: "built"}},
	}
	added := newViewColumns(view, []*pb.ViewColumn{{ColumnName: "declared"}, {ColumnName: "built"}, {ColumnName: "new"}})
	require.Len(t, added, 1)
	require.Equal(t, "new", added[0].GetColumnName())
	require.Len(t, newViewColumns(nil, []*pb.ViewColumn{{ColumnName: "new"}}), 1, "新建 View 时所有列都是新列")
}

func TestVisibleViewColumnsHidesLogicallyDeletedColumns(t *testing.T) {
	columns := []*pb.ViewColumn{{ColumnName: "close"}, {ColumnName: "old"}}
	visible := visibleViewColumns(columns, []string{"old"})
	require.Len(t, visible, 1)
	require.Equal(t, "close", visible[0].GetColumnName())
	require.Len(t, visibleViewColumns(columns, nil), 2)
}

// GetDataset 只在数据集确实不存在时返回 DATASET_NOT_FOUND：读取出错是内部错误，调用方不能把它当成“已经删掉”。
func TestGetDatasetDistinguishesMissingFromReadErrors(t *testing.T) {
	svc, store := newDatasetService(t, testDataset("active"))
	store.datasetErr = errors.New("sqlite: database is locked")
	rsp, err := svc.GetDataset(context.Background(), &pb.GetDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.NotContains(t, rsp.GetRetInfo().GetMsg(), "sqlite")

	store.datasetErr = fmt.Errorf("metadata row not found: %w", sql.ErrNoRows)
	rsp, err = svc.GetDataset(context.Background(), &pb.GetDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, rsp.GetRetInfo().GetCode())
}
