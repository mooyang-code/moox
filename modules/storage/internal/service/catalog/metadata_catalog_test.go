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
	"google.golang.org/protobuf/reflect/protoreflect"
)

type activationMetadataStore struct {
	metadata.Store
	dataset      *pb.Dataset
	node         *pb.DataNode
	datasetErr   error
	commitCalls  int
	commitErr    error
	lastExpected uint64
	beforeCommit func()
}

type registerDataSubjectMetadataStore struct {
	metadata.Store
	registered bool
}

type registerArchiveFileMetadataStore struct {
	metadata.Store
	registered bool
}

type manualRebuildMetadataStore struct {
	metadata.Store
	view       *pb.View
	requestCnt int
}

func (s *manualRebuildMetadataStore) GetView(context.Context, string, string) (*pb.View, error) {
	if s.view == nil {
		return nil, sql.ErrNoRows
	}
	return s.view, nil
}

func (s *manualRebuildMetadataStore) RequestViewRebuild(context.Context, string, string) (*pb.View, error) {
	s.requestCnt++
	s.view.DesiredViewRevision++
	return s.view, nil
}

func (s *registerDataSubjectMetadataStore) RegisterDataSubject(
	_ context.Context,
	subject *pb.Subject,
	_ *pb.SubjectSymbol,
	bindings []*pb.DatasetSubject,
) (*pb.Subject, []*pb.DatasetSubject, error) {
	s.registered = true
	return subject, bindings, nil
}

func (s *registerArchiveFileMetadataStore) RegisterArchiveFile(_ context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	s.registered = true
	return item, nil
}

func (s *activationMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	if s.datasetErr != nil {
		return nil, s.datasetErr
	}
	if s.dataset == nil {
		return nil, sql.ErrNoRows
	}
	return s.dataset, nil
}

func (s *activationMetadataStore) GetDataNode(context.Context, string) (*pb.DataNode, error) {
	if s.node == nil {
		return nil, errors.New("data node not found")
	}
	return s.node, nil
}

func (s *activationMetadataStore) CommitDatasetActivation(_ context.Context, _, _ string, expected uint64) (*pb.Dataset, error) {
	s.commitCalls++
	s.lastExpected = expected
	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	if s.dataset.GetRevision() != expected {
		return nil, errors.New("dataset revision conflict")
	}
	s.dataset.Status = "active"
	s.dataset.BindingLocked = true
	s.dataset.Revision++
	return s.dataset, nil
}

func newActivationService(t *testing.T, dataset *pb.Dataset, node *pb.DataNode, runtime *fakeNodeStateChecker) (*Service, *activationMetadataStore) {
	t.Helper()
	store := &activationMetadataStore{dataset: dataset, node: node}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret", NodeStateChecker: runtime})
	require.NoError(t, err)
	return svc, store
}

func TestCheckDatasetActivationIsReadOnly(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.GetReady())
	require.Equal(t, uint64(7), rsp.GetDatasetRevision())
	require.Zero(t, store.commitCalls)
	require.Equal(t, activationCheckIDs, checkIDs(rsp.GetChecks()))
}

func TestRequestViewRebuildReturnsImmediatelyAndPreservesActiveView(t *testing.T) {
	store := &manualRebuildMetadataStore{view: &pb.View{
		SpaceId: "space-a", ViewId: "view-a", Name: "视图", PrimaryDatasetId: "dataset-a",
		Status:        "active",
		ActiveIndexId: "index-a", ActiveViewRevision: 4, DesiredViewRevision: 4,
	}}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	rsp, err := svc.RequestViewRebuild(context.Background(), &pb.RequestViewRebuildReq{
		AuthInfo: &pb.AuthInfo{AppId: "admin-gateway", AppKey: serviceAuthKey("secret", "admin-gateway")},
		SpaceId:  "space-a", ViewId: "view-a",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, store.requestCnt)
	require.Equal(t, uint64(5), rsp.GetView().GetDesiredViewRevision())
	require.Equal(t, "index-a", rsp.GetView().GetActiveIndexId())
	require.Equal(t, uint64(4), rsp.GetView().GetActiveViewRevision())
}

func TestRequestViewRebuildRejectsNonAdminIdentity(t *testing.T) {
	store := &manualRebuildMetadataStore{view: &pb.View{SpaceId: "space-a", ViewId: "view-a", Status: "active"}}
	svc, err := NewMetadataService(store, nil, Options{})
	require.NoError(t, err)
	rsp, err := svc.RequestViewRebuild(context.Background(), &pb.RequestViewRebuildReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, SpaceId: "space-a", ViewId: "view-a",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode())
	require.Zero(t, store.requestCnt)
}

func TestRequestViewRebuildRejectsMissingOrForgedIdentity(t *testing.T) {
	store := &manualRebuildMetadataStore{view: &pb.View{SpaceId: "space-a", ViewId: "view-a", Status: "active"}}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret"})
	require.NoError(t, err)
	for _, auth := range []*pb.AuthInfo{
		nil,
		{AppId: "admin-gateway", AppKey: "wrong"},
		{AppId: "collector", AppKey: serviceAuthKey("secret", "collector")},
	} {
		rsp, callErr := svc.RequestViewRebuild(context.Background(), &pb.RequestViewRebuildReq{AuthInfo: auth, SpaceId: "space-a", ViewId: "view-a"})
		require.NoError(t, callErr)
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode())
	}
}

func TestActivateDatasetCommitsWithExpectedRevision(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(7), store.lastExpected)
	require.Equal(t, "active", rsp.GetDataset().GetStatus())
	require.True(t, rsp.GetDataset().GetBindingLocked())
	require.Equal(t, uint64(8), rsp.GetDataset().GetRevision())
}

func TestActivateDatasetReportsCommittedPublicationFailureAndRetryIsIdempotent(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	store := &activationMetadataStore{dataset: dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}}
	svc, err := NewMetadataService(store, &metacache.Store{}, Options{AuthSecret: "secret", NodeStateChecker: runtime})
	require.NoError(t, err)

	first, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, first.GetRetInfo().GetCode())
	require.Contains(t, first.GetRetInfo().GetMsg(), "publication is pending")
	require.Equal(t, "active", first.GetDataset().GetStatus())
	require.True(t, first.GetDataset().GetBindingLocked())
	require.Equal(t, uint64(8), first.GetDataset().GetRevision())

	retry, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, retry.GetRetInfo().GetCode())
	require.Equal(t, uint64(8), retry.GetDataset().GetRevision())
	require.Equal(t, 1, store.commitCalls)
}

func TestRegisterDataSubjectSucceedsWhenCacheRefreshIsAlreadyRunning(t *testing.T) {
	store := &registerDataSubjectMetadataStore{}
	service, err := NewMetadataService(store, &metacache.Store{}, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	rsp, err := service.RegisterDataSubject(context.Background(), &pb.RegisterDataSubjectReq{
		SpaceId:        "crypto",
		DataSourceId:   "binance",
		ExternalSymbol: "BTCUSDT",
		Subject:        &pb.Subject{SubjectId: "BTC-USDT", SubjectType: "crypto_pair"},
		DatasetBindings: []*pb.DatasetSubject{{
			DatasetId: "symbols",
		}},
	})
	require.NoError(t, err)
	require.True(t, store.registered)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestRegisterArchiveFileSucceedsWhenCachePublicationIsUnavailable(t *testing.T) {
	store := &registerArchiveFileMetadataStore{}
	service, err := NewMetadataService(store, &metacache.Store{}, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	rsp, err := service.RegisterArchiveFile(context.Background(), &pb.RegisterArchiveFileReq{
		ArchiveFile: &pb.ArchiveFile{
			SpaceId: "crypto", DatasetId: "spot_kline_1h", DeviceId: "parquet-local", FileUri: "file:///tmp/spot.parquet",
		},
	})
	require.NoError(t, err)
	require.True(t, store.registered)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestActivateDatasetCASConflictDoesNotChangeState(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)
	store.beforeCommit = func() { store.dataset.Revision++ }

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
	require.Equal(t, "disabled", store.dataset.GetStatus())
	require.False(t, store.dataset.GetBindingLocked())
	require.Equal(t, uint64(8), store.dataset.GetRevision(), "the concurrent metadata mutation is preserved")
}

func TestActivateDatasetActiveLockedRetryIgnoresStaleRevision(t *testing.T) {
	dataset := newActivationReader("active", "ip://127.0.0.1:19090").dataset
	dataset.BindingLocked = true
	dataset.Revision = 11
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "disabled", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 1})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(11), rsp.GetDataset().GetRevision())
	require.Zero(t, store.commitCalls)
	require.Zero(t, runtime.calls, "idempotent retry must not rerun readiness")
}

func TestDatasetActivationReadErrorsAreSafeAndDistinguishedFromMissing(t *testing.T) {
	internalErr := errors.New("sqlite: secret=do-not-leak")
	svc, _ := newActivationService(t, newActivationReader("disabled", "ip://127.0.0.1:19090").dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, &fakeNodeStateChecker{rsp: readyNodeState("node-a")})
	store := svc.metadata.(*activationMetadataStore)
	store.datasetErr = fmt.Errorf("wrapped metadata read: %w", internalErr)

	checkRsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, checkRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset metadata could not be read", checkRsp.GetRetInfo().GetMsg())
	require.NotContains(t, checkRsp.GetRetInfo().GetMsg(), "secret")

	activateRsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, activateRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset metadata could not be read", activateRsp.GetRetInfo().GetMsg())

	store.datasetErr = fmt.Errorf("wrapped missing Dataset: %w", sql.ErrNoRows)
	missingRsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, missingRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset not found", missingRsp.GetRetInfo().GetMsg())

	activateMissingRsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, activateMissingRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset not found", activateMissingRsp.GetRetInfo().GetMsg())
}

func TestActivateDatasetLockedDisabledCanReactivate(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	dataset.BindingLocked = true
	dataset.Revision = 11
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 11})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(12), rsp.GetDataset().GetRevision())
	require.True(t, rsp.GetDataset().GetBindingLocked())
	require.Equal(t, 1, store.commitCalls)
}

func TestActivateDatasetReadinessFailureDoesNotWrite(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: &pb.GetNodeStateRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_PERMISSION}, Status: "READY"}}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Zero(t, store.commitCalls)
	require.Equal(t, "disabled", store.dataset.GetStatus())
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
				SpaceId:    "moox_system",
				ViewId:     "view",
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
			SpaceId:          "space",
			ViewId:           "view",
			Name:             "测试视图",
			PrimaryDatasetId: "dataset",
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
