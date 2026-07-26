//go:build cgo

package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

const (
	dataNodeManagementSecret = "datanode-management-e2e-secret"
	deployerAppID            = "storage-deployer"
	primaryAppID             = "storage-primary"
)

// TestDataNodeManagementLifecycle exercises the public metadata lifecycle and
// the PrimaryStore -> Dataset -> DataNode path against real SQLite and Pebble
// stores. The runtime phase is deliberately protected by countingPanicStore:
// a request that falls back to SQLite after snapshot publication fails loudly.
func TestDataNodeManagementLifecycle(t *testing.T) {
	ctx := context.Background()
	store := openLifecycleStore(t, ctx)
	defer store.Close()
	seedLifecycleParents(t, ctx, store)

	nodeA := newLifecycleNode(t, "node-a")
	defer nodeA.Close()
	nodeB := newLifecycleNode(t, "node-b")
	defer nodeB.Close()

	guard := &countingPanicStore{Store: store}
	cache, err := metacache.New(ctx, guard, metacache.Options{
		RefreshInterval:    metacache.RefreshDisabled,
		RefreshTimeout:     5 * time.Second,
		InitialLoadTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	nodesByTarget := map[string]pb.DataNodeRuntimeService{
		"ip://127.0.0.1:21001": nodeA,
		"ip://127.0.0.1:21002": nodeB,
	}
	checker := inProcessNodeStateChecker{nodesByTarget: nodesByTarget}
	metadataService, err := catalog.NewMetadataService(store, cache, catalog.Options{
		AuthSecret:       dataNodeManagementSecret,
		NodeStateChecker: checker,
	})
	if err != nil {
		t.Fatal(err)
	}

	deployerAuth := signedAuth(deployerAppID)
	registerNode(t, ctx, metadataService, deployerAuth, "node-a", "ip://127.0.0.1:21001")
	registerNode(t, ctx, metadataService, deployerAuth, "node-b", "ip://127.0.0.1:21002")

	primary, err := primarystore.New(primarystore.Options{
		Validator: primarystore.NewMetadataValidator(cache),
		Resolver: func(ctx context.Context, spaceID, datasetID string) (pb.DataNodeRuntimeService, error) {
			snapshot := coremetadata.RequestSnapshotFromContext(ctx)
			if snapshot == nil {
				snapshot = cache.RequestSnapshot()
			}
			dataset, ok := snapshot.GetDataset(spaceID, datasetID)
			if !ok {
				return nil, fmt.Errorf("dataset %s/%s is not in metadata snapshot", spaceID, datasetID)
			}
			node, ok := snapshot.GetDataNode(dataset.GetDataNodeId())
			if !ok || node.GetStatus() != "active" {
				return nil, fmt.Errorf("data node %q is not active in metadata snapshot", dataset.GetDataNodeId())
			}
			runtime := nodesByTarget[node.GetServiceTarget()]
			if runtime == nil {
				return nil, fmt.Errorf("no in-process runtime for target %q", node.GetServiceTarget())
			}
			return runtime, nil
		},
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			signed := proto.Clone(auth).(*pb.AuthInfo)
			signed.AppKey = datanode.ServiceAuthKey(dataNodeManagementSecret, signed.GetAppId())
			return signed, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	dataset := createDataset(t, ctx, metadataService, "dataset_a", "行情数据", "node-a")
	if dataset.GetStatus() != "disabled" || dataset.GetBindingLocked() || dataset.GetRevision() != 1 {
		t.Fatalf("created Dataset=%v, want disabled unlocked revision 1", dataset)
	}
	row := lifecycleRow()
	writeAuth := &pb.AuthInfo{AppId: primaryAppID}

	write := primaryWrite(t, ctx, primary, writeAuth, row)
	if got := write.GetRetInfo().GetCode(); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("disabled Dataset write code=%s, want INVALID_PARAM", got)
	}

	beforeCheck := getDataset(t, ctx, metadataService, "dataset_a")
	check := checkDataset(t, ctx, metadataService, "dataset_a")
	if !check.GetReady() || check.GetDatasetRevision() != 1 {
		t.Fatalf("initial activation check=%v, want ready revision 1", check)
	}
	afterCheck := getDataset(t, ctx, metadataService, "dataset_a")
	if !proto.Equal(beforeCheck, afterCheck) {
		t.Fatalf("read-only CheckDatasetActivation changed Dataset: before=%v after=%v", beforeCheck, afterCheck)
	}

	mutated := updateDataset(t, ctx, metadataService, &pb.Dataset{
		SpaceId: "quant", DatasetId: "dataset_a", DataSourceId: "source_a", DataNodeId: "node-a",
		Name: "行情数据", Description: "revision mutation", DataKind: pb.DataKind_DATA_KIND_RECORD,
		Revision: 1, Status: "disabled",
	})
	if mutated.GetRevision() != 2 || mutated.GetStatus() != "disabled" || mutated.GetBindingLocked() {
		t.Fatalf("mutated Dataset=%v, want disabled unlocked revision 2", mutated)
	}
	stale := activateDataset(t, ctx, metadataService, "dataset_a", 1)
	if got := stale.GetRetInfo().GetCode(); got != pb.ErrorCode_CONFLICT {
		t.Fatalf("stale activation code=%s, want CONFLICT", got)
	}

	upsertColumn(t, ctx, metadataService, "dataset_a")
	check = checkDataset(t, ctx, metadataService, "dataset_a")
	if !check.GetReady() || check.GetDatasetRevision() != 2 {
		t.Fatalf("post-mutation activation check=%v, want ready revision 2", check)
	}
	activated := activateDataset(t, ctx, metadataService, "dataset_a", check.GetDatasetRevision())
	if got := activated.GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS {
		t.Fatalf("activation code=%s, want SUCCESS", got)
	}
	if got := activated.GetDataset(); got.GetStatus() != "active" || !got.GetBindingLocked() || got.GetRevision() != 3 {
		t.Fatalf("activated Dataset=%v, want active locked revision 3", got)
	}

	// From this point on, PrimaryStore must use the immutable cache snapshot;
	// the wrapper panics if any cache-backed request reaches SQLite.
	guard.EnableRuntimeOnly()
	primaryWriteResponse := primaryWrite(t, ctx, primary, writeAuth, row)
	if got := primaryWriteResponse.GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS {
		t.Fatalf("active Dataset write code=%s, want SUCCESS", got)
	}
	primaryReadResponse, err := primary.ReadFields(ctx, &pb.PrimaryReadFieldsReq{
		AuthInfo: writeAuth, Keys: []*pb.RowKey{row.GetKey()}, FieldIds: []string{"value"},
	})
	if err != nil || primaryReadResponse.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("snapshot-only read response=%v err=%v", primaryReadResponse, err)
	}
	if len(primaryReadResponse.GetRows()) != 1 || primaryReadResponse.GetRows()[0].GetFields()[0].GetValue().GetStringValue() != "ok" {
		t.Fatalf("snapshot-only read rows=%v", primaryReadResponse.GetRows())
	}
	if got := guard.RuntimeSQLiteAccesses(); got != 0 {
		t.Fatalf("runtime metadata SQLite accesses=%d, want 0", got)
	}
	guard.DisableRuntimeOnly()

	disabled := updateDataset(t, ctx, metadataService, &pb.Dataset{
		SpaceId: "quant", DatasetId: "dataset_a", DataSourceId: "source_a", DataNodeId: "node-a",
		Name: "行情数据", DataKind: pb.DataKind_DATA_KIND_RECORD, Revision: 3, Status: "disabled",
	})
	if disabled.GetRevision() != 4 || disabled.GetStatus() != "disabled" || !disabled.GetBindingLocked() {
		t.Fatalf("disabled Dataset=%v, want disabled locked revision 4", disabled)
	}
	lockedRebind := rebindDataset(t, ctx, metadataService, "dataset_a", "node-b", 4)
	if got := lockedRebind.GetRetInfo().GetCode(); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("locked rebind code=%s, want INVALID_PARAM", got)
	}

	second := createDataset(t, ctx, metadataService, "dataset_b", "第二数据集", "node-a")
	if second.GetRevision() != 1 || second.GetStatus() != "disabled" || second.GetBindingLocked() {
		t.Fatalf("second Dataset=%v, want disabled unlocked revision 1", second)
	}
	rebound := rebindDataset(t, ctx, metadataService, "dataset_b", "node-b", 1)
	if got := rebound.GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS || rebound.GetDataset().GetDataNodeId() != "node-b" || rebound.GetDataset().GetRevision() != 2 {
		t.Fatalf("second Dataset rebind response=%v, want node-b revision 2", rebound)
	}

	activeDelete := deleteDataNode(t, ctx, metadataService, "node-a")
	if got := activeDelete.GetRetInfo().GetCode(); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("active node delete code=%s, want INVALID_PARAM", got)
	}
	updateDataNode(t, ctx, metadataService, "node-a", "Node A", "disabled")
	referencedDelete := deleteDataNode(t, ctx, metadataService, "node-a")
	if got := referencedDelete.GetRetInfo().GetCode(); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("referenced node delete code=%s, want INVALID_PARAM", got)
	}

	// node-c is metadata-only cleanup coverage: no Dataset ever references it,
	// so disabling it and deleting it exercises the empty-node invariant without
	// weakening the node-a/node-b binding assertions above.
	registerNode(t, ctx, metadataService, deployerAuth, "node-c", "ip://127.0.0.1:21003")
	updateDataNode(t, ctx, metadataService, "node-c", "Node C", "disabled")
	emptyDelete := deleteDataNode(t, ctx, metadataService, "node-c")
	if got := emptyDelete.GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS {
		t.Fatalf("empty disabled node delete code=%s, want SUCCESS", got)
	}
}

func openLifecycleStore(t *testing.T, ctx context.Context) *metasqlite.Store {
	t.Helper()
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(ctx); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store
}

func seedLifecycleParents(t *testing.T, ctx context.Context, store *metasqlite.Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "quant", Name: "量化空间"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "source_a", Name: "内部行情", Kind: "internal"}); err != nil {
		t.Fatal(err)
	}
}

func newLifecycleNode(t *testing.T, nodeID string) *datanode.Service {
	t.Helper()
	node, err := datanode.NewService(datanode.Options{
		NodeID: nodeID, AuthSecret: dataNodeManagementSecret,
		Pebble: pebble.Options{NodeID: nodeID, Path: filepath.Join(t.TempDir(), nodeID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

type inProcessNodeStateChecker struct {
	nodesByTarget map[string]pb.DataNodeRuntimeService
}

func (c inProcessNodeStateChecker) GetNodeState(ctx context.Context, target string, req *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	node := c.nodesByTarget[target]
	if node == nil {
		return nil, fmt.Errorf("unknown in-process target %q", target)
	}
	return node.GetNodeState(ctx, req)
}

func signedAuth(appID string) *pb.AuthInfo {
	return &pb.AuthInfo{AppId: appID, AppKey: datanode.ServiceAuthKey(dataNodeManagementSecret, appID)}
}

func registerNode(t *testing.T, ctx context.Context, service *catalog.Service, auth *pb.AuthInfo, nodeID, target string) {
	t.Helper()
	rsp, err := service.RegisterDataNode(ctx, &pb.RegisterDataNodeReq{AuthInfo: auth, NodeId: nodeID, ServiceTarget: target, InitialName: nodeID})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("register %s: rsp=%v err=%v", nodeID, rsp, err)
	}
}

func createDataset(t *testing.T, ctx context.Context, service *catalog.Service, datasetID, name, nodeID string) *pb.Dataset {
	t.Helper()
	rsp, err := service.CreateDataset(ctx, &pb.CreateDatasetReq{
		AuthInfo: signedAuth("admin"),
		Dataset:  &pb.Dataset{SpaceId: "quant", DatasetId: datasetID, DataSourceId: "source_a", DataNodeId: nodeID, Name: name, DataKind: pb.DataKind_DATA_KIND_RECORD},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("create Dataset %s: rsp=%v err=%v", datasetID, rsp, err)
	}
	return rsp.GetDataset()
}

func updateDataset(t *testing.T, ctx context.Context, service *catalog.Service, dataset *pb.Dataset) *pb.Dataset {
	t.Helper()
	rsp, err := service.UpdateDataset(ctx, &pb.UpdateDatasetReq{AuthInfo: signedAuth("admin"), Dataset: dataset})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("update Dataset %s: rsp=%v err=%v", dataset.GetDatasetId(), rsp, err)
	}
	return rsp.GetDataset()
}

func getDataset(t *testing.T, ctx context.Context, service *catalog.Service, datasetID string) *pb.Dataset {
	t.Helper()
	rsp, err := service.GetDataset(ctx, &pb.GetDatasetReq{AuthInfo: signedAuth("admin"), SpaceId: "quant", DatasetId: datasetID})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("get Dataset %s: rsp=%v err=%v", datasetID, rsp, err)
	}
	return rsp.GetDataset()
}

func checkDataset(t *testing.T, ctx context.Context, service *catalog.Service, datasetID string) *pb.CheckDatasetActivationRsp {
	t.Helper()
	rsp, err := service.CheckDatasetActivation(ctx, &pb.CheckDatasetActivationReq{AuthInfo: signedAuth("admin"), SpaceId: "quant", DatasetId: datasetID})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("check Dataset %s: rsp=%v err=%v", datasetID, rsp, err)
	}
	return rsp
}

func activateDataset(t *testing.T, ctx context.Context, service *catalog.Service, datasetID string, revision uint64) *pb.ActivateDatasetRsp {
	t.Helper()
	rsp, err := service.ActivateDataset(ctx, &pb.ActivateDatasetReq{AuthInfo: signedAuth("admin"), SpaceId: "quant", DatasetId: datasetID, ExpectedRevision: revision})
	if err != nil {
		t.Fatalf("activate Dataset %s: rsp=%v err=%v", datasetID, rsp, err)
	}
	return rsp
}

func upsertColumn(t *testing.T, ctx context.Context, service *catalog.Service, datasetID string) {
	t.Helper()
	rsp, err := service.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{
		AuthInfo: signedAuth("admin"),
		Column:   &pb.DatasetColumn{SpaceId: "quant", DatasetId: datasetID, ColumnName: "value", OriginId: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active", Attributes: map[string]string{"display_name": "数值"}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("upsert Dataset column: rsp=%v err=%v", rsp, err)
	}
}

func rebindDataset(t *testing.T, ctx context.Context, service *catalog.Service, datasetID, nodeID string, revision uint64) *pb.RebindDatasetDataNodeRsp {
	t.Helper()
	rsp, err := service.RebindDatasetDataNode(ctx, &pb.RebindDatasetDataNodeReq{AuthInfo: signedAuth("admin"), SpaceId: "quant", DatasetId: datasetID, DataNodeId: nodeID, ExpectedRevision: revision})
	if err != nil {
		t.Fatalf("rebind Dataset %s: rsp=%v err=%v", datasetID, rsp, err)
	}
	return rsp
}

func updateDataNode(t *testing.T, ctx context.Context, service *catalog.Service, nodeID, name, status string) {
	t.Helper()
	rsp, err := service.UpdateDataNode(ctx, &pb.UpdateDataNodeReq{AuthInfo: signedAuth("admin"), NodeId: nodeID, Name: name, Status: status})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("update DataNode %s: rsp=%v err=%v", nodeID, rsp, err)
	}
}

func deleteDataNode(t *testing.T, ctx context.Context, service *catalog.Service, nodeID string) *pb.DeleteDataNodeRsp {
	t.Helper()
	rsp, err := service.DeleteDataNode(ctx, &pb.DeleteDataNodeReq{AuthInfo: signedAuth("admin"), NodeId: nodeID})
	if err != nil {
		t.Fatalf("delete DataNode %s: rsp=%v err=%v", nodeID, rsp, err)
	}
	return rsp
}

func lifecycleRow() *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "quant", DatasetId: "dataset_a", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}},
	}
}

func primaryWrite(t *testing.T, ctx context.Context, service *primarystore.Service, auth *pb.AuthInfo, row *pb.RowFieldUpsert) *pb.PrimaryUpsertFieldsRsp {
	t.Helper()
	rsp, err := service.UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row}})
	if err != nil {
		t.Fatalf("PrimaryStore write: rsp=%v err=%v", rsp, err)
	}
	return rsp
}

// countingPanicStore guards the Reader methods used by snapshotcache. The
// cache is the only metadata reader given to PrimaryStore; enabling the guard
// after publication therefore proves that runtime reads do not reach SQLite.
type countingPanicStore struct {
	coremetadata.Store
	runtimeOnly atomic.Bool
	accesses    atomic.Int64
}

func (s *countingPanicStore) EnableRuntimeOnly()  { s.runtimeOnly.Store(true) }
func (s *countingPanicStore) DisableRuntimeOnly() { s.runtimeOnly.Store(false) }
func (s *countingPanicStore) RuntimeSQLiteAccesses() int64 {
	return s.accesses.Load()
}

func (s *countingPanicStore) guard(method string) {
	if s.runtimeOnly.Load() {
		s.accesses.Add(1)
		panic("runtime SQLite metadata access: " + method)
	}
}

func (s *countingPanicStore) WithReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	s.guard("WithReadSnapshot")
	snapshotter, ok := s.Store.(interface {
		WithReadSnapshot(context.Context, func(context.Context) error) error
	})
	if !ok {
		return fn(ctx)
	}
	return snapshotter.WithReadSnapshot(ctx, fn)
}

func (s *countingPanicStore) GetSpace(ctx context.Context, id string) (*pb.Space, error) {
	s.guard("GetSpace")
	return s.Store.GetSpace(ctx, id)
}
func (s *countingPanicStore) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	s.guard("ListSpaces")
	return s.Store.ListSpaces(ctx, owner, page)
}
func (s *countingPanicStore) GetView(ctx context.Context, space, id string) (*pb.View, error) {
	s.guard("GetView")
	return s.Store.GetView(ctx, space, id)
}
func (s *countingPanicStore) ListViews(ctx context.Context, space, dataset, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	s.guard("ListViews")
	return s.Store.ListViews(ctx, space, dataset, status, page)
}
func (s *countingPanicStore) ListViewsByDataset(ctx context.Context, space, dataset string) ([]*pb.View, error) {
	s.guard("ListViewsByDataset")
	return s.Store.ListViewsByDataset(ctx, space, dataset)
}
func (s *countingPanicStore) ListViewColumns(ctx context.Context, space, view string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	s.guard("ListViewColumns")
	return s.Store.ListViewColumns(ctx, space, view, page)
}
func (s *countingPanicStore) GetDataSource(ctx context.Context, space, source string) (*pb.DataSource, error) {
	s.guard("GetDataSource")
	return s.Store.GetDataSource(ctx, space, source)
}
func (s *countingPanicStore) ListDataSources(ctx context.Context, space, kind, market, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	s.guard("ListDataSources")
	return s.Store.ListDataSources(ctx, space, kind, market, keyword, page)
}
func (s *countingPanicStore) GetSubject(ctx context.Context, space, subject string) (*pb.Subject, error) {
	s.guard("GetSubject")
	return s.Store.GetSubject(ctx, space, subject)
}
func (s *countingPanicStore) ListSubjects(ctx context.Context, space, kind, market string, ids []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	s.guard("ListSubjects")
	return s.Store.ListSubjects(ctx, space, kind, market, ids, keyword, page)
}
func (s *countingPanicStore) ListSubjectSymbols(ctx context.Context, space, subject, source, external string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	s.guard("ListSubjectSymbols")
	return s.Store.ListSubjectSymbols(ctx, space, subject, source, external, page)
}
func (s *countingPanicStore) GetDataset(ctx context.Context, space, dataset string) (*pb.Dataset, error) {
	s.guard("GetDataset")
	return s.Store.GetDataset(ctx, space, dataset)
}
func (s *countingPanicStore) ListDatasets(ctx context.Context, query coremetadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	s.guard("ListDatasets")
	return s.Store.ListDatasets(ctx, query)
}
func (s *countingPanicStore) ListDatasetSubjects(ctx context.Context, space, dataset, subject string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	s.guard("ListDatasetSubjects")
	return s.Store.ListDatasetSubjects(ctx, space, dataset, subject, page)
}
func (s *countingPanicStore) GetFieldGroup(ctx context.Context, space, group string) (*pb.FieldGroup, error) {
	s.guard("GetFieldGroup")
	return s.Store.GetFieldGroup(ctx, space, group)
}
func (s *countingPanicStore) ListFieldGroups(ctx context.Context, space, parent string, page *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error) {
	s.guard("ListFieldGroups")
	return s.Store.ListFieldGroups(ctx, space, parent, page)
}
func (s *countingPanicStore) GetField(ctx context.Context, space, field string) (*pb.Field, error) {
	s.guard("GetField")
	return s.Store.GetField(ctx, space, field)
}
func (s *countingPanicStore) ListFields(ctx context.Context, query coremetadata.FieldQuery) ([]*pb.Field, *pb.PageResult, error) {
	s.guard("ListFields")
	return s.Store.ListFields(ctx, query)
}
func (s *countingPanicStore) CountFieldsByGroup(ctx context.Context, space string) (coremetadata.FieldGroupCounts, error) {
	s.guard("CountFieldsByGroup")
	return s.Store.CountFieldsByGroup(ctx, space)
}
func (s *countingPanicStore) GetFactor(ctx context.Context, space, factor string) (*pb.Factor, error) {
	s.guard("GetFactor")
	return s.Store.GetFactor(ctx, space, factor)
}
func (s *countingPanicStore) ListFactors(ctx context.Context, space, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	s.guard("ListFactors")
	return s.Store.ListFactors(ctx, space, algorithm, page)
}
func (s *countingPanicStore) ListDatasetColumns(ctx context.Context, space, dataset string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	s.guard("ListDatasetColumns")
	return s.Store.ListDatasetColumns(ctx, space, dataset, page)
}
func (s *countingPanicStore) GetDataNode(ctx context.Context, node string) (*pb.DataNode, error) {
	s.guard("GetDataNode")
	return s.Store.GetDataNode(ctx, node)
}
func (s *countingPanicStore) ListDataNodes(ctx context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	s.guard("ListDataNodes")
	return s.Store.ListDataNodes(ctx, page)
}
func (s *countingPanicStore) GetDevice(ctx context.Context, device string) (*pb.Device, error) {
	s.guard("GetDevice")
	return s.Store.GetDevice(ctx, device)
}
func (s *countingPanicStore) ListDevices(ctx context.Context, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	s.guard("ListDevices")
	return s.Store.ListDevices(ctx, engine, page)
}
func (s *countingPanicStore) ListArchiveFiles(ctx context.Context, space, dataset string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	s.guard("ListArchiveFiles")
	return s.Store.ListArchiveFiles(ctx, space, dataset, page)
}

var _ coremetadata.Reader = (*countingPanicStore)(nil)
