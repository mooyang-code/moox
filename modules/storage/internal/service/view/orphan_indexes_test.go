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

func cleanupIndexID(datasetID string) string {
	return viewindex.IndexID("space", datasetID)
}

// 启动时删掉没有对应数据集的索引文件；元数据里的数据集（含停用的）保留它所用引擎下的索引。
func TestRestoreRemovesOrphanIndexes(t *testing.T) {
	kept, disabled, orphan := cleanupIndexID("prices"), cleanupIndexID("disabled"), cleanupIndexID("deleted")
	duckdb := &fakeManagedEngine{name: "duckdb", ids: []string{kept, disabled, orphan}}
	// 同一个标识在另一个引擎下的文件不属于这个数据集。
	bleve := &fakeManagedEngine{name: "bleve", ids: []string{kept}}
	svc := newCleanupService(t, duckdb)
	svc.engines[bleve.name] = bleve
	off := timeSeriesSpec("space", "disabled", "1m")
	off.Status = "disabled"
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadataFor(timeSeriesSpec("space", "prices", "1m"), off)}); err != nil {
		t.Fatal(err)
	}
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

// 元数据读不出来或分页信息不完整时不能断定哪些文件没有数据集：启动报错，一个都不删。
func TestRestoreKeepsEverythingWhenMetadataIsIncomplete(t *testing.T) {
	cases := map[string][]*pb.ListDatasetsRsp{
		"没有分页信息":    {{RetInfo: successRetInfo()}},
		"空页却说还有下一页": {{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 100, HasMore: true}}},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			engine := &fakeManagedEngine{name: "duckdb", ids: []string{cleanupIndexID("prices")}}
			svc := newCleanupService(t, engine)
			if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: &pageMetadata{pages: pages}}); err == nil {
				t.Fatal("元数据不完整时启动恢复应当报错")
			}
			if len(engine.removeCalls) != 0 {
				t.Fatalf("元数据不完整时删了索引：%v", engine.removeCalls)
			}
		})
	}
	t.Run("列出失败", func(t *testing.T) {
		engine := &fakeManagedEngine{name: "duckdb", ids: []string{cleanupIndexID("prices")}}
		svc := newCleanupService(t, engine)
		if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: &failingMetadata{}}); err == nil {
			t.Fatal("列出失败时启动恢复应当报错")
		}
		if len(engine.removeCalls) != 0 {
			t.Fatalf("列出失败时删了索引：%v", engine.removeCalls)
		}
	})
}

// failingMetadata 列出数据集总是失败。
type failingMetadata struct{ testMetadata }

func (failingMetadata) ListDatasets(context.Context, *pb.ListDatasetsReq, ...client.Option) (*pb.ListDatasetsRsp, error) {
	return nil, errors.New("metadata unavailable")
}

// 某个引擎列不出文件或删除失败只影响它自己，不挡其他文件。
func TestRemoveOrphanIndexesContinuesAfterEngineErrors(t *testing.T) {
	first, second := cleanupIndexID("a"), cleanupIndexID("b")
	failing := &fakeManagedEngine{name: "bleve", ids: []string{first}, listErr: errors.New("list failed")}
	engine := &fakeManagedEngine{name: "duckdb", ids: []string{first, second}, removeErr: errors.New("remove failed")}
	svc := newCleanupService(t, engine)
	svc.engines[failing.name] = failing
	svc.removeOrphanIndexes(context.Background(), nil)
	if len(failing.removeCalls) != 0 {
		t.Fatalf("列不出文件的引擎不应删除：%v", failing.removeCalls)
	}
	if !reflect.DeepEqual(engine.removeCalls, []string{first, second}) || !reflect.DeepEqual(engine.ids, []string{first}) {
		t.Fatalf("删除 = %v 剩余 = %v，期望第一个失败后继续删第二个", engine.removeCalls, engine.ids)
	}
}
