package view

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
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
	protectOnCall     int
}

func (m *cleanupMetadata) ListViews(_ context.Context, req *pb.ListViewsReq, _ ...client.Option) (*pb.ListViewsRsp, error) {
	m.listCalls++
	views := m.views
	if m.protectOnCall > 0 && m.listCalls != m.protectOnCall {
		views = nil
	}
	if m.listErr != nil {
		return nil, m.listErr
	}
	if m.nilPageResult {
		return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: views}, nil
	}
	if m.emptyPageWithMore {
		return &pb.ListViewsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 100, HasMore: true}}, nil
	}
	if m.protectOnCall > 0 && m.listCalls == m.protectOnCall {
		return &pb.ListViewsRsp{
			RetInfo: successRetInfo(), Views: views,
			PageResult: &pb.PageResult{Page: 1, Size: 100},
		}, nil
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
	name           string
	ids            []string
	removeCalls    []string
	removeErr      error
	listErr        error
	prepareStarted chan struct{}
	prepareRelease <-chan struct{}
}

func (e *fakeManagedEngine) Engine() string { return e.name }

func (e *fakeManagedEngine) Prepare(_ context.Context, id string, _ viewindex.ViewIndexSchema) error {
	if e.prepareStarted != nil {
		close(e.prepareStarted)
		e.prepareStarted = nil
	}
	if e.prepareRelease != nil {
		<-e.prepareRelease
	}
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
	svc, err := New(t.TempDir(), "view-secret")
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

func TestCleanupRetiredIndexesRequiresContinuousUnreferencedAge(t *testing.T) {
	id := cleanupIndexID("prices")
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	metadata := &cleanupMetadata{}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}

	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(engine.removeCalls) != 0 {
		t.Fatalf("new candidate was removed before grace: %v", engine.removeCalls)
	}
	now = now.Add(time.Second)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(engine.removeCalls, []string{id}) {
		t.Fatalf("remove calls = %v, want [%s]", engine.removeCalls, id)
	}
}

func TestCleanupRetiredIndexesProtectsMetadataAndRuntimeIndexes(t *testing.T) {
	active := cleanupIndexID("active")
	runtimeActive := cleanupIndexID("runtime-active")
	building := cleanupIndexID("building")
	orphan := cleanupIndexID("orphan")
	engine := &fakeManagedEngine{name: "fake", ids: []string{active, runtimeActive, building, orphan}}
	svc := newCleanupService(t, engine)
	svc.views[viewRef{spaceID: "space", viewID: "runtime-active"}] = &viewRuntime{active: runtimeActive}
	svc.views[viewRef{spaceID: "space", viewID: "building"}] = &viewRuntime{building: building}
	metadata := &cleanupMetadata{views: []*pb.View{{SpaceId: "space", ViewId: "metadata", ActiveIndexId: active}}}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}

	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(engine.removeCalls, []string{orphan}) {
		t.Fatalf("remove calls = %v, want orphan only", engine.removeCalls)
	}
}

func TestCleanupRetiredIndexesBusyRuntimeDoesNotStarveOtherViews(t *testing.T) {
	busyID := cleanupIndexID("busy")
	orphanID := cleanupIndexID("orphan")
	engine := &fakeManagedEngine{name: "fake", ids: []string{busyID, orphanID}}
	svc := newCleanupService(t, engine)
	busyRuntime := &viewRuntime{}
	svc.views[viewRef{spaceID: "space", viewID: "busy"}] = busyRuntime
	busyRuntime.mu.Lock()
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: &cleanupMetadata{}, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		busyRuntime.mu.Unlock()
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		busyRuntime.mu.Unlock()
		t.Fatal(err)
	}
	busyRuntime.mu.Unlock()
	if !reflect.DeepEqual(engine.removeCalls, []string{orphanID}) {
		t.Fatalf("busy runtime blocked unrelated cleanup: removals=%v", engine.removeCalls)
	}
	if len(engine.ids) != 1 || engine.ids[0] != busyID {
		t.Fatalf("busy View was removed or unrelated orphan retained: %v", engine.ids)
	}
}

func TestCleanupRetiredIndexesMetadataFailureBreaksCandidateContinuity(t *testing.T) {
	id := cleanupIndexID("prices")
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	metadata := &cleanupMetadata{}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}

	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	metadata.listErr = errors.New("metadata unavailable")
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err == nil {
		t.Fatal("metadata failure returned success")
	}
	metadata.listErr = nil
	now = now.Add(time.Second)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(engine.removeCalls) != 0 {
		t.Fatalf("candidate survived an unobserved interval: %v", engine.removeCalls)
	}
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(engine.removeCalls, []string{id}) {
		t.Fatalf("remove calls = %v, want [%s]", engine.removeCalls, id)
	}
}

func TestCleanupRetiredIndexesRejectsIncompleteMetadataPagination(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cleanupMetadata)
	}{
		{name: "missing page result", mutate: func(metadata *cleanupMetadata) { metadata.nilPageResult = true }},
		{name: "empty page with more", mutate: func(metadata *cleanupMetadata) { metadata.emptyPageWithMore = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := cleanupIndexID("prices")
			engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
			svc := newCleanupService(t, engine)
			metadata := &cleanupMetadata{}
			now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
			opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}

			if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			tc.mutate(metadata)
			if err := svc.CleanupRetiredIndexes(context.Background(), opts); err == nil {
				t.Fatal("incomplete Metadata pagination returned success")
			}
			if len(engine.removeCalls) != 0 {
				t.Fatalf("incomplete Metadata pagination removed indexes: %v", engine.removeCalls)
			}
		})
	}
}

func TestCleanupRetiredIndexesRetriesRemoveFailure(t *testing.T) {
	id := cleanupIndexID("prices")
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}, removeErr: errors.New("busy")}
	svc := newCleanupService(t, engine)
	metadata := &cleanupMetadata{}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}

	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err == nil {
		t.Fatal("remove failure returned success")
	}
	now = now.Add(30 * time.Second)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(engine.removeCalls, []string{id, id}) {
		t.Fatalf("remove calls = %v, want two attempts", engine.removeCalls)
	}
}

func TestCleanupRetiredIndexesRevalidatesMetadataInsideIndexGate(t *testing.T) {
	id := cleanupIndexID("prices")
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	metadata := &cleanupMetadata{
		views: []*pb.View{{SpaceId: "space", ViewId: "prices", Engine: "fake", ActiveIndexId: id}},
		// First discovery is call 1. The due run performs the run-wide reads on
		// calls 2 and 3; call 4 is the revalidation under the per-index gate.
		protectOnCall: 4,
	}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(engine.removeCalls) != 0 {
		t.Fatalf("index referenced during gated revalidation was removed: %v", engine.removeCalls)
	}
}

func TestCleanupRetiredIndexesProtectsOnlyMatchingEngine(t *testing.T) {
	id := cleanupIndexID("prices")
	duckdb := &fakeManagedEngine{name: "duckdb", ids: []string{id}}
	bleve := &fakeManagedEngine{name: "bleve", ids: []string{id}}
	svc := newCleanupService(t, duckdb)
	svc.engines[bleve.name] = bleve
	svc.views[viewRef{spaceID: "space", viewID: "prices"}] = &viewRuntime{active: id}
	svc.indexEngine[id] = "duckdb"
	svc.schemas[id] = viewindex.ViewIndexSchema{Engine: "duckdb"}
	svc.indexView[id] = viewRef{spaceID: "space", viewID: "prices"}
	metadata := &cleanupMetadata{views: []*pb.View{{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", ActiveIndexId: id,
	}}}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(duckdb.removeCalls) != 0 {
		t.Fatalf("active DuckDB index was removed: %v", duckdb.removeCalls)
	}
	if !reflect.DeepEqual(bleve.removeCalls, []string{id}) {
		t.Fatalf("stale Bleve index removals = %v, want [%s]", bleve.removeCalls, id)
	}
	if got := svc.indexEngine[id]; got != "duckdb" {
		t.Fatalf("active DuckDB mapping was cleared: %q", got)
	}
	if _, ok := svc.schemas[id]; !ok {
		t.Fatal("active DuckDB schema mapping was cleared")
	}
}

func TestCleanupRetiredIndexesRediscoveryAfterRestart(t *testing.T) {
	id := cleanupIndexID("prices")
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	metadata := &cleanupMetadata{}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	first := newCleanupService(t, engine)
	if err := first.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	restarted := newCleanupService(t, engine)
	if err := restarted.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(engine.removeCalls) != 0 {
		t.Fatalf("restart discovery removed immediately: %v", engine.removeCalls)
	}
	now = now.Add(time.Minute)
	if err := restarted.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	sort.Strings(engine.removeCalls)
	if !reflect.DeepEqual(engine.removeCalls, []string{id}) {
		t.Fatalf("remove calls = %v, want [%s]", engine.removeCalls, id)
	}
}

// 删除挂在 View 上的当前索引：整个过程持有运行时锁，结束时清掉 active 并释放锁（不能在已持有时再次加锁）。
func TestRemoveViewIndexClearsActiveOfAttachedView(t *testing.T) {
	id := cleanupIndexID("prices")
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	key := viewRef{spaceID: "space", viewID: "prices"}
	runtime := &viewRuntime{active: id}
	svc.views[key] = runtime
	svc.indexEngine[id] = "fake"
	svc.indexView[id] = key
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	done := make(chan *pb.RemoveViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.RemoveViewIndex(context.Background(), &pb.RemoveViewIndexReq{AuthInfo: auth, IndexId: id})
		done <- rsp
	}()
	select {
	case rsp := <-done:
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("remove attached index: %v", rsp.GetRetInfo())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveViewIndex 在成功路径上卡死")
	}
	if !runtime.mu.TryLock() {
		t.Fatal("RemoveViewIndex 返回后仍持有运行时锁")
	}
	defer runtime.mu.Unlock()
	if runtime.active != "" {
		t.Fatalf("active 未清空：%q", runtime.active)
	}
}
