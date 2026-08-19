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

func cleanupIndexID(viewID string, slot viewindex.Slot) string {
	return viewindex.ViewIndexID("space", viewID, slot)
}

func TestCleanupRetiredIndexesRequiresContinuousUnreferencedAge(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
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
	active := cleanupIndexID("active", viewindex.SlotA)
	build := cleanupIndexID("build", viewindex.SlotB)
	runtimeActive := cleanupIndexID("runtime-active", viewindex.SlotA)
	runtimeNext := cleanupIndexID("runtime-next", viewindex.SlotB)
	orphan := cleanupIndexID("orphan", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{active, build, runtimeActive, runtimeNext, orphan}}
	svc := newCleanupService(t, engine)
	svc.views[viewRef{spaceID: "space", viewID: "runtime"}] = &viewRuntime{active: runtimeActive, next: runtimeNext}
	metadata := &cleanupMetadata{views: []*pb.View{{
		SpaceId: "space", ViewId: "metadata", ActiveIndexId: active,
		IndexBuild: &pb.ViewIndexBuild{IndexId: build},
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
	if !reflect.DeepEqual(engine.removeCalls, []string{orphan}) {
		t.Fatalf("remove calls = %v, want orphan only", engine.removeCalls)
	}
}

func TestCleanupRetiredIndexesBusyRuntimeDoesNotStarveOtherViews(t *testing.T) {
	busyID := cleanupIndexID("busy", viewindex.SlotA)
	orphanID := cleanupIndexID("orphan", viewindex.SlotA)
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
	id := cleanupIndexID("prices", viewindex.SlotA)
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
			id := cleanupIndexID("prices", viewindex.SlotA)
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
	id := cleanupIndexID("prices", viewindex.SlotA)
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
	id := cleanupIndexID("prices", viewindex.SlotA)
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

func TestRetireIndexSharesPrepareGate(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake"}
	svc := newCleanupService(t, engine)
	release, err := svc.indexWriteGate(id).lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.retireIndex(context.Background(), id, 0) }()
	select {
	case err := <-done:
		t.Fatalf("retirement bypassed per-index gate: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if generation, retiring := svc.retiringGeneration(id); !retiring || generation != 0 {
		t.Fatalf("retiring generation = %d, present=%v", generation, retiring)
	}
}

func TestCleanupRetiredIndexesCancelsReusedGeneration(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	metadata := &cleanupMetadata{}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	svc.markIndexRetiring(id, 0)

	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	svc.nextIndexGeneration(id)
	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(engine.removeCalls) != 0 {
		t.Fatalf("reused generation was removed: %v", engine.removeCalls)
	}
	if _, retiring := svc.retiringGeneration(id); retiring {
		t.Fatal("stale retiring generation was not cleared")
	}
}

func TestCleanupRetiredIndexesClearsProtectedStaleRetirement(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	metadata := &cleanupMetadata{views: []*pb.View{{
		SpaceId: "space", ViewId: "prices", Engine: "fake", ActiveIndexId: id,
	}}}
	svc.markIndexRetiring(id, 0)
	svc.nextIndexGeneration(id)

	if err := svc.CleanupRetiredIndexes(context.Background(), RetiredIndexCleanupOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	if _, retiring := svc.retiringGeneration(id); retiring {
		t.Fatal("protected reused index kept a stale retiring marker")
	}
}

func TestCleanupRetiredIndexesProtectsOnlyMatchingEngine(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
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

func TestCleanupRetiredIndexesSiblingCandidateDoesNotClearRetirement(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	duckdb := &fakeManagedEngine{name: "duckdb", ids: []string{id}}
	bleve := &fakeManagedEngine{name: "bleve", ids: []string{id}}
	svc := newCleanupService(t, duckdb)
	svc.engines[bleve.name] = bleve
	svc.indexEngine[id] = "duckdb"
	svc.markIndexRetiring(id, 0)
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: &cleanupMetadata{}, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	bleve.ids = nil
	now = now.Add(time.Second)
	if err := svc.CleanupRetiredIndexes(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if generation, retiring := svc.retiringGeneration(id); !retiring || generation != 0 {
		t.Fatalf("Bleve candidate cancellation cleared DuckDB retirement: generation=%d present=%v", generation, retiring)
	}
}

func TestCleanupRetiredIndexesRediscoveryAfterRestart(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
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

func TestCleanupRetiredIndexesClearsMissingOldActiveForSlotReuse(t *testing.T) {
	oldID := cleanupIndexID("prices", viewindex.SlotA)
	newID := cleanupIndexID("prices", viewindex.SlotB)
	engine := &fakeManagedEngine{name: "fake", ids: []string{newID}}
	svc := newCleanupService(t, engine)
	svc.indexEngine[oldID] = "fake"
	svc.indexEngine[newID] = "fake"
	svc.views[viewRef{spaceID: "space", viewID: "prices"}] = &viewRuntime{
		active: oldID, next: newID, status: "ready",
	}
	if err := svc.SwitchView(context.Background(), "space", "prices", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, retiring := svc.retiringGeneration(oldID); !retiring {
		t.Fatal("missing old active was not marked retiring during switch")
	}
	metadata := &cleanupMetadata{views: []*pb.View{{
		SpaceId: "space", ViewId: "prices", Engine: "fake", ActiveIndexId: newID,
	}}}
	if err := svc.CleanupRetiredIndexes(context.Background(), RetiredIndexCleanupOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	if _, retiring := svc.retiringGeneration(oldID); retiring {
		t.Fatal("missing old active kept its slot permanently retiring")
	}

	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		AuthInfo: auth, IndexId: oldID,
		Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "prices", Engine: "fake", ViewVersion: 3},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("reuse missing old slot: rsp=%v err=%v", rsp, err)
	}
}

func TestRemoveViewIndexClearsRetirementForImmediateSlotReuse(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	svc.indexEngine[id] = "fake"
	svc.schemas[id] = viewindex.ViewIndexSchema{Engine: "fake"}
	svc.indexView[id] = viewRef{spaceID: "space", viewID: "prices"}
	svc.markIndexRetiring(id, 0)
	svc.cleanupCandidates[managedIndexRef{engine: "fake", indexID: id}] = retiredIndexCandidate{generation: 0}
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	rsp, err := svc.RemoveViewIndex(context.Background(), &pb.RemoveViewIndexReq{AuthInfo: auth, IndexId: id})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("remove retired index: rsp=%v err=%v", rsp, err)
	}
	if _, retiring := svc.retiringGeneration(id); retiring {
		t.Fatal("explicit removal left the slot retiring")
	}
	prepare, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		AuthInfo: auth, IndexId: id,
		Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "prices", Engine: "fake", ViewVersion: 2},
	})
	if err != nil || prepare.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("reuse explicitly removed slot: rsp=%v err=%v", prepare, err)
	}
}

func TestRemoveViewIndexCannotCrossPrepareAttachWindow(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	prepareStarted := make(chan struct{})
	prepareRelease := make(chan struct{})
	engine := &fakeManagedEngine{
		name: "fake", ids: []string{id}, prepareStarted: prepareStarted, prepareRelease: prepareRelease,
	}
	svc := newCleanupService(t, engine)
	key := viewRef{spaceID: "space", viewID: "prices"}
	runtime := &viewRuntime{}
	svc.views[key] = runtime
	svc.indexEngine[id] = "fake"
	svc.indexView[id] = key
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	prepareDone := make(chan *pb.PrepareViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "prices", Engine: "fake", ViewVersion: 2},
		})
		prepareDone <- rsp
	}()
	<-prepareStarted
	removeDone := make(chan *pb.RemoveViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.RemoveViewIndex(context.Background(), &pb.RemoveViewIndexReq{AuthInfo: auth, IndexId: id})
		removeDone <- rsp
	}()
	deadline := time.Now().Add(time.Second)
	for runtime.mu.TryLock() {
		runtime.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("RemoveViewIndex did not acquire the runtime lock")
		}
		time.Sleep(time.Millisecond)
	}
	close(prepareRelease)
	removeRsp := <-removeDone
	if removeRsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatal("RemoveViewIndex deleted a generation that was still attaching")
	}
	prepareRsp := <-prepareDone
	if prepareRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("PrepareViewIndex failed after protected attach: %v", prepareRsp.GetRetInfo())
	}
	if len(engine.ids) != 1 || engine.ids[0] != id {
		t.Fatalf("prepared physical index was removed: %v", engine.ids)
	}
}

func TestCleanupCannotCrossPrepareAttachWindowOrLeakRuntimeLock(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	prepareStarted := make(chan struct{})
	prepareRelease := make(chan struct{})
	engine := &fakeManagedEngine{
		name: "fake", ids: []string{id}, prepareStarted: prepareStarted, prepareRelease: prepareRelease,
	}
	svc := newCleanupService(t, engine)
	key := viewRef{spaceID: "space", viewID: "prices"}
	runtime := &viewRuntime{}
	svc.views[key] = runtime
	svc.indexEngine[id] = "fake"
	svc.indexView[id] = key
	ref := managedIndexRef{engine: "fake", indexID: id}
	svc.cleanupCandidates[ref] = retiredIndexCandidate{generation: 0}
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	prepareDone := make(chan *pb.PrepareViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "prices", Engine: "fake", ViewVersion: 2},
		})
		prepareDone <- rsp
	}()
	<-prepareStarted
	cleanupDone := make(chan error, 1)
	go func() {
		cleanupDone <- svc.removeRetiredIndex(context.Background(), ref, engine, &cleanupMetadata{})
	}()
	deadline := time.Now().Add(time.Second)
	for runtime.mu.TryLock() {
		runtime.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not acquire the runtime lock")
		}
		time.Sleep(time.Millisecond)
	}
	close(prepareRelease)
	if err := <-cleanupDone; err != nil {
		t.Fatal(err)
	}
	prepareRsp := <-prepareDone
	if prepareRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("PrepareViewIndex failed after cleanup defer: %v", prepareRsp.GetRetInfo())
	}
	if !runtime.mu.TryLock() {
		t.Fatal("cleanup leaked the runtime lock")
	}
	runtime.mu.Unlock()
	if len(engine.ids) != 1 || engine.ids[0] != id {
		t.Fatalf("cleanup removed the preparing physical index: %v", engine.ids)
	}
}

func TestPrepareViewIndexRejectsConcurrentSameSlotPreparation(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	prepareStarted := make(chan struct{})
	prepareRelease := make(chan struct{})
	engine := &fakeManagedEngine{name: "fake", prepareStarted: prepareStarted, prepareRelease: prepareRelease}
	svc := newCleanupService(t, engine)
	runtime := &viewRuntime{}
	svc.views[viewRef{spaceID: "space", viewID: "prices"}] = runtime
	runtime.mu.Lock()
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	request := func(version uint64) *pb.PrepareViewIndexReq {
		return &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "prices", Engine: "fake", ViewVersion: version},
		}
	}
	firstDone := make(chan *pb.PrepareViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.PrepareViewIndex(context.Background(), request(1))
		firstDone <- rsp
	}()
	<-prepareStarted
	secondDone := make(chan *pb.PrepareViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.PrepareViewIndex(context.Background(), request(2))
		secondDone <- rsp
	}()
	close(prepareRelease)
	if rsp := <-secondDone; rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		runtime.mu.Unlock()
		t.Fatal("concurrent PrepareViewIndex unexpectedly succeeded")
	}
	runtime.mu.Unlock()
	if rsp := <-firstDone; rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("first PrepareViewIndex failed: %v", rsp.GetRetInfo())
	}
}
