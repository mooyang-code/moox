//go:build cgo

package e2e

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// primaryProcess 按 cmd/server 的 primary 角色装配：元数据、元数据缓存、Pebble 库、PrimaryStore 与 Metadata 服务在同一进程。
type primaryProcess struct {
	store    *pebble.Store
	cache    *metacache.Store
	primary  *primarystore.Service
	metadata *catalog.Service
}

func newPrimaryProcess(t *testing.T, ctx context.Context, meta coremetadata.Store, reader coremetadata.Reader) *primaryProcess {
	t.Helper()
	cache, err := metacache.New(ctx, reader, metacache.Options{
		RefreshInterval:    metacache.RefreshDisabled,
		RefreshTimeout:     5 * time.Second,
		InitialLoadTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	store, err := pebble.Open(pebble.Options{
		Path: filepath.Join(t.TempDir(), "pebble"),
		DatasetExists: func(spaceID, datasetID string) bool {
			_, err := cache.GetDataset(context.Background(), spaceID, datasetID)
			return err == nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	primary, err := primarystore.New(primarystore.Options{Store: store, Validator: primarystore.NewMetadataValidator(cache), Snapshot: cache.RequestSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	metadataService, err := catalog.NewMetadataService(meta, cache, catalog.Options{Rows: store})
	if err != nil {
		t.Fatal(err)
	}
	return &primaryProcess{store: store, cache: cache, primary: primary, metadata: metadataService}
}

// 数据集的完整生命周期只经过主服务一个进程：建数据集时带列、建好即可写，停用与启用、删除后行一并消失，
// 删除后迟到的写入被拒绝，同名重建后从空开始。
func TestDatasetLifecycleInPrimaryProcess(t *testing.T) {
	ctx := context.Background()
	store := openLifecycleStore(t, ctx)
	defer store.Close()
	seedLifecycleParents(t, ctx, store)
	guard := &countingPanicStore{Store: store}
	process := newPrimaryProcess(t, ctx, store, guard)
	writeAuth := &pb.AuthInfo{AppId: "storage-primary"}
	row := lifecycleRow()

	dataset := createDataset(t, ctx, process.metadata, "dataset_a", "行情数据")
	if dataset.GetStatus() != "active" || dataset.GetRevision() != 1 {
		t.Fatalf("created Dataset=%v, want active revision 1", dataset)
	}

	// 建好即可写；之后的读写只用元数据缓存，不再访问 SQLite。
	guard.EnableRuntimeOnly()
	if got := primaryWrite(t, ctx, process.primary, writeAuth, row).GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS {
		t.Fatalf("new Dataset write code=%s, want SUCCESS", got)
	}
	if got := readLifecycleValues(t, ctx, process.primary, writeAuth, row.GetKey()); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("read after write=%v, want [ok]", got)
	}
	if got := guard.RuntimeSQLiteAccesses(); got != 0 {
		t.Fatalf("runtime metadata SQLite accesses=%d, want 0", got)
	}
	guard.DisableRuntimeOnly()

	disabled := updateDataset(t, ctx, process.metadata, &pb.Dataset{SpaceId: "quant", DatasetId: "dataset_a", Name: "行情数据", Status: "disabled"})
	if disabled.GetStatus() != "disabled" {
		t.Fatalf("disabled Dataset=%v", disabled)
	}
	if got := primaryWrite(t, ctx, process.primary, writeAuth, row).GetRetInfo().GetCode(); got == pb.ErrorCode_SUCCESS {
		t.Fatal("disabled Dataset accepted a write")
	}
	updateDataset(t, ctx, process.metadata, &pb.Dataset{SpaceId: "quant", DatasetId: "dataset_a", Name: "行情数据", Status: "active"})
	if got := primaryWrite(t, ctx, process.primary, writeAuth, row).GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS {
		t.Fatalf("re-enabled Dataset write code=%s, want SUCCESS", got)
	}

	deleted, err := process.metadata.DeleteDataset(ctx, &pb.DeleteDatasetReq{SpaceId: "quant", DatasetId: "dataset_a"})
	if err != nil || deleted.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("delete Dataset rsp=%v err=%v", deleted, err)
	}
	rows, err := process.store.ReadRows(ctx, []*pb.RowKey{row.GetKey()}, []string{"value"}, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows after delete=%v err=%v, want none", rows, err)
	}
	if got := primaryWrite(t, ctx, process.primary, writeAuth, row).GetRetInfo().GetCode(); got == pb.ErrorCode_SUCCESS {
		t.Fatal("deleted Dataset accepted a write")
	}
	// 绕过请求快照直接写存储层（相当于删除前就已通过校验的迟到写入）：拿到读锁后按最新元数据被拒绝。
	if err := process.store.UpsertFields(ctx, "quant", "dataset_a", []*pb.RowFieldUpsert{row}); !errors.Is(err, pebble.ErrDatasetDeleted) {
		t.Fatalf("late write after delete err=%v, want ErrDatasetDeleted", err)
	}

	recreated := createDataset(t, ctx, process.metadata, "dataset_a", "行情数据")
	if recreated.GetStatus() != "active" || recreated.GetRevision() != 1 {
		t.Fatalf("recreated Dataset=%v, want active revision 1", recreated)
	}
	if got := readLifecycleValues(t, ctx, process.primary, writeAuth, row.GetKey()); len(got) != 0 {
		t.Fatalf("recreated Dataset still has old rows: %v", got)
	}
	if got := primaryWrite(t, ctx, process.primary, writeAuth, row).GetRetInfo().GetCode(); got != pb.ErrorCode_SUCCESS {
		t.Fatalf("recreated Dataset write code=%s, want SUCCESS", got)
	}
}

func readLifecycleValues(t *testing.T, ctx context.Context, primary *primarystore.Service, auth *pb.AuthInfo, key *pb.RowKey) []string {
	t.Helper()
	rsp, err := primary.ReadRows(ctx, &pb.PrimaryReadRowsReq{AuthInfo: auth, Keys: []*pb.RowKey{key}, ColumnNames: []string{"value"}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	values := make([]string, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		for _, field := range row.GetFields() {
			values = append(values, field.GetValue().GetStringValue())
		}
	}
	return values
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
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "source_a", Name: "内部行情"}); err != nil {
		t.Fatal(err)
	}
}

func createDataset(t *testing.T, ctx context.Context, service *catalog.Service, datasetID, name string) *pb.Dataset {
	t.Helper()
	rsp, err := service.CreateDataset(ctx, &pb.CreateDatasetReq{
		Dataset: &pb.Dataset{SpaceId: "quant", DatasetId: datasetID, DataSourceId: "source_a", Name: name, DataKind: pb.DataKind_DATA_KIND_RECORD},
		Columns: []*pb.DatasetColumn{{ColumnName: "value", OriginId: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active", Attributes: map[string]string{"display_name": "数值"}}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("create Dataset %s: rsp=%v err=%v", datasetID, rsp, err)
	}
	return rsp.GetDataset()
}

func updateDataset(t *testing.T, ctx context.Context, service *catalog.Service, dataset *pb.Dataset) *pb.Dataset {
	t.Helper()
	rsp, err := service.UpdateDataset(ctx, &pb.UpdateDatasetReq{Dataset: dataset})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("update Dataset %s: rsp=%v err=%v", dataset.GetDatasetId(), rsp, err)
	}
	return rsp.GetDataset()
}

func lifecycleRow() *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "quant", DatasetId: "dataset_a", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}},
	}
}

func primaryWrite(t *testing.T, ctx context.Context, service *primarystore.Service, auth *pb.AuthInfo, row *pb.RowFieldUpsert) *pb.PrimaryUpsertFieldsRsp {
	t.Helper()
	rsp, err := service.UpsertFields(ctx, fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row}}))
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
func (s *countingPanicStore) ListDataSources(ctx context.Context, space, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	s.guard("ListDataSources")
	return s.Store.ListDataSources(ctx, space, keyword, page)
}
func (s *countingPanicStore) GetSubject(ctx context.Context, space, subject string) (*pb.Subject, error) {
	s.guard("GetSubject")
	return s.Store.GetSubject(ctx, space, subject)
}
func (s *countingPanicStore) ListSubjects(ctx context.Context, space string, ids []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	s.guard("ListSubjects")
	return s.Store.ListSubjects(ctx, space, ids, keyword, page)
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
func (s *countingPanicStore) ListDatasetColumns(ctx context.Context, space, dataset string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	s.guard("ListDatasetColumns")
	return s.Store.ListDatasetColumns(ctx, space, dataset, page)
}

var _ coremetadata.Reader = (*countingPanicStore)(nil)
