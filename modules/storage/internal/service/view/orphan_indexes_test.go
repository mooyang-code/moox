package view

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type cleanupMetadata struct {
	maintenanceMetadata
	views             []*pb.View
	listErr           error
	nilPageResult     bool
	emptyPageWithMore bool
	listCalls         int
}

func (m *cleanupMetadata) ListViews(_ context.Context, req *pb.ListViewsReq, _ ...client.Option) (*pb.ListViewsRsp, error) {
	m.listCalls++
	views := m.views
	if m.listErr != nil {
		return nil, m.listErr
	}
	if m.nilPageResult {
		return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: views}, nil
	}
	if m.emptyPageWithMore {
		return &pb.ListViewsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 100, HasMore: true}}, nil
	}
	page := req.GetPage().GetPage()
	if page == 0 {
		page = 1
	}
	size := req.GetPage().GetSize()
	if size == 0 {
		size = 100
	}
	start := int((page - 1) * size)
	if start >= len(views) {
		return &pb.ListViewsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: page, Size: size}}, nil
	}
	end := start + int(size)
	if end > len(views) {
		end = len(views)
	}
	return &pb.ListViewsRsp{
		RetInfo: successRetInfo(), Views: views[start:end],
		PageResult: &pb.PageResult{Page: page, Size: size, HasMore: end < len(views)},
	}, nil
}

type fakeManagedEngine struct {
	name        string
	ids         []string
	removeCalls []string
	removeErr   error
	listErr     error
}

func (e *fakeManagedEngine) Engine() string { return e.name }

func (e *fakeManagedEngine) Prepare(_ context.Context, id string, _ viewindex.ViewIndexSchema) error {
	for _, existing := range e.ids {
		if existing == id {
			return nil
		}
	}
	e.ids = append(e.ids, id)
	return nil
}
func (*fakeManagedEngine) Write(context.Context, string, viewindex.ViewIndexWriteBatch) error {
	return nil
}
func (*fakeManagedEngine) Query(context.Context, string, viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	return nil, 0, nil
}
func (e *fakeManagedEngine) Stat(_ context.Context, id string) (viewindex.ViewIndexStats, error) {
	for _, candidate := range e.ids {
		if candidate == id {
			return viewindex.ViewIndexStats{Exists: true}, nil
		}
	}
	return viewindex.ViewIndexStats{}, nil
}
func (e *fakeManagedEngine) Remove(_ context.Context, id string) error {
	e.removeCalls = append(e.removeCalls, id)
	if e.removeErr != nil {
		err := e.removeErr
		e.removeErr = nil
		return err
	}
	for pos, candidate := range e.ids {
		if candidate == id {
			e.ids = append(e.ids[:pos], e.ids[pos+1:]...)
			break
		}
	}
	return nil
}
func (e *fakeManagedEngine) ListManagedIndexes(context.Context) ([]string, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return append([]string(nil), e.ids...), nil
}

func newCleanupService(t *testing.T, engine *fakeManagedEngine) *Service {
	t.Helper()
	svc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	originalEngines := svc.engines
	t.Cleanup(func() {
		for _, current := range originalEngines {
			if closer, ok := current.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	})
	svc.engines = map[string]viewindex.Engine{engine.name: engine}
	return svc
}

func cleanupIndexID(viewID string) string {
	return viewindex.ViewIndexID("space", viewID)
}

// 启动时删掉没有对应 View 的索引文件；元数据里的 View（含停用的）保留它所用引擎下的索引。
func TestRestoreRemovesOrphanIndexes(t *testing.T) {
	kept, disabled, orphan := cleanupIndexID("prices"), cleanupIndexID("disabled"), cleanupIndexID("deleted")
	duckdb := &fakeManagedEngine{name: "duckdb", ids: []string{kept, disabled, orphan}}
	// 同一个标识在另一个引擎下的文件不属于这个 View。
	bleve := &fakeManagedEngine{name: "bleve", ids: []string{kept}}
	svc := newCleanupService(t, duckdb)
	svc.engines[bleve.name] = bleve
	metadata := &cleanupMetadata{views: []*pb.View{
		{SpaceId: "space", ViewId: "prices", Engine: "duckdb", Status: "active"},
		{SpaceId: "space", ViewId: "disabled", Engine: "duckdb", Status: "disabled"},
	}}
	svc.removeOrphanIndexes(context.Background(), metadata)
	if !reflect.DeepEqual(duckdb.removeCalls, []string{orphan}) {
		t.Fatalf("DuckDB 删除 = %v，期望只删 [%s]", duckdb.removeCalls, orphan)
	}
	if !reflect.DeepEqual(bleve.removeCalls, []string{kept}) {
		t.Fatalf("Bleve 删除 = %v，期望删掉另一个引擎下的同名文件 [%s]", bleve.removeCalls, kept)
	}
	if !reflect.DeepEqual(duckdb.ids, []string{kept, disabled}) {
		t.Fatalf("DuckDB 剩余 = %v", duckdb.ids)
	}
}

// 元数据读不出来或分页信息不完整时不能断定哪些文件没有 View：一个都不删。
func TestRemoveOrphanIndexesKeepsEverythingWhenMetadataIsIncomplete(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cleanupMetadata)
	}{
		{name: "列出失败", mutate: func(metadata *cleanupMetadata) { metadata.listErr = errors.New("metadata unavailable") }},
		{name: "没有分页信息", mutate: func(metadata *cleanupMetadata) { metadata.nilPageResult = true }},
		{name: "空页却说还有下一页", mutate: func(metadata *cleanupMetadata) { metadata.emptyPageWithMore = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeManagedEngine{name: "fake", ids: []string{cleanupIndexID("prices")}}
			svc := newCleanupService(t, engine)
			metadata := &cleanupMetadata{}
			tc.mutate(metadata)
			svc.removeOrphanIndexes(context.Background(), metadata)
			if len(engine.removeCalls) != 0 {
				t.Fatalf("元数据不完整时删了索引：%v", engine.removeCalls)
			}
		})
	}
}

// 某个引擎列不出文件或删除失败只影响它自己，不挡其他文件。
func TestRemoveOrphanIndexesContinuesAfterEngineErrors(t *testing.T) {
	first, second := cleanupIndexID("a"), cleanupIndexID("b")
	failing := &fakeManagedEngine{name: "bleve", ids: []string{first}, listErr: errors.New("list failed")}
	engine := &fakeManagedEngine{name: "duckdb", ids: []string{first, second}, removeErr: errors.New("remove failed")}
	svc := newCleanupService(t, engine)
	svc.engines[failing.name] = failing
	svc.removeOrphanIndexes(context.Background(), &cleanupMetadata{})
	if len(failing.removeCalls) != 0 {
		t.Fatalf("列不出文件的引擎不应删除：%v", failing.removeCalls)
	}
	if !reflect.DeepEqual(engine.removeCalls, []string{first, second}) || !reflect.DeepEqual(engine.ids, []string{first}) {
		t.Fatalf("删除 = %v 剩余 = %v，期望第一个失败后继续删第二个", engine.removeCalls, engine.ids)
	}
}
