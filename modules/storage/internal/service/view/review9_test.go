package view

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
)

// 就绪事件的刷新持有全局的串行锁：它不能为了读取某个 View 的活动索引去等该 View 的运行时锁（整段索引写入、激活的元数据
// 调用期间都被持有），否则一个 View 的长时间写入会让所有 View 的就绪发布排在后面。
func TestReadyFlushDoesNotWaitForBusyRuntime(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher,
		&pb.View{SpaceId: "quant", ViewId: "busy-view", DatasetId: "prices_a", ActiveIndexId: "busy-a", Freq: "1m"},
		&pb.View{SpaceId: "quant", ViewId: "idle-view", DatasetId: "prices_b", ActiveIndexId: "idle-a", Freq: "1m"},
	)
	// 运行时与生产一致：活动索引已发布到查询路径的无锁副本。
	for _, runtime := range service.views {
		runtime.mu.Lock()
		runtime.publishReadStateLocked()
		runtime.mu.Unlock()
	}
	at := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	for _, dataset := range []string{"prices_a", "prices_b"} {
		payload := collectorCompleted(dataset, "complete", []string{"BTC-USDT"}, nil, at, 1786032300)
		payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 5}}
		if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("ready-"+dataset, at), payload); err == nil {
			t.Fatalf("%s 的行还没有写入时应保持待发布", dataset)
		}
	}
	service.NoteAppliedPosition("quant", "busy-view", "busy-a", "node-a", "store-a", 5)
	service.NoteAppliedPosition("quant", "idle-view", "idle-a", "node-a", "store-a", 5)

	busy := service.views[viewRef{spaceID: "quant", viewID: "busy-view"}]
	busy.mu.Lock()
	defer busy.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- service.FlushViewDataReady(context.Background(), "", "") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("刷新在等待某个 View 的运行时锁：一个 View 的长时间写入会拖住所有 View 的就绪发布")
	}
	if len(publisher.byID) != 2 {
		t.Fatalf("两个 View 的就绪事件都应发布（运行时被占用不影响发布）：%d", len(publisher.byID))
	}
}

// 删除索引时运行时才被创建（首次准备索引）：先取到的闸门不能替代运行时锁，要放开重来，保证总是先取运行时锁再取闸门，
// 最终清掉指向该索引的 next，而不是物理索引没了、运行时仍指向它。
func TestRemoveViewIndexRetriesWhenRuntimeAppearsWhileWaiting(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	key := viewRef{spaceID: "space", viewID: "prices"}
	svc.indexEngine[id] = "fake"
	svc.indexView[id] = key
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}

	// 占着闸门，让删除先找运行时（此时还没有）再等闸门。
	hold, err := svc.indexWriteGate(id).lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *pb.RemoveViewIndexRsp, 1)
	go func() {
		rsp, _ := svc.RemoveViewIndex(context.Background(), &pb.RemoveViewIndexReq{AuthInfo: auth, IndexId: id})
		done <- rsp
	}()
	time.Sleep(100 * time.Millisecond)
	// 准备索引的效果：运行时被创建，并把这个索引挂成 next。
	runtime := &viewRuntime{next: id, status: "building"}
	svc.mu.Lock()
	svc.views[key] = runtime
	svc.mu.Unlock()
	hold()
	select {
	case rsp := <-done:
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("删除失败：%v", rsp.GetRetInfo())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("删除没有返回")
	}
	if !runtime.mu.TryLock() {
		t.Fatal("删除返回后仍持有运行时锁")
	}
	defer runtime.mu.Unlock()
	if runtime.next != "" {
		t.Fatalf("物理索引已删除，运行时不应继续指向它：next=%q", runtime.next)
	}
}

// 清空活动索引时一并重置活动数据集契约：之后重新挂载的新索引按新的元数据建立契约，而不是沿用被删除索引的旧数据集
// （周期事件会按旧契约路由，新数据集的完成事件收不到）。
func TestRemoveViewIndexResetsActiveDatasetContract(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	key := viewRef{spaceID: "space", viewID: "prices"}
	runtime := &viewRuntime{active: id, activeDatasetIDs: []string{"ds_old"}, activePrimaryDatasetID: "ds_old", activeDatasetSet: true}
	svc.views[key] = runtime
	svc.indexEngine[id] = "fake"
	svc.indexView[id] = key
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("view-secret", "test")}
	if rsp, err := svc.RemoveViewIndex(context.Background(), &pb.RemoveViewIndexReq{AuthInfo: auth, IndexId: id}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("删除失败：rsp=%v err=%v", rsp, err)
	}
	if runtime.active != "" || runtime.activeDatasetSet || runtime.activeDatasetIDs != nil || runtime.activePrimaryDatasetID != "" {
		t.Fatalf("清空 active 时应重置数据集契约：%+v", runtime)
	}
	if state := runtime.queryState(); state.active != "" {
		t.Fatalf("查询路径的副本也应清空：%q", state.active)
	}
}

// 放弃失败的首次构建（活动槽位）同样重置数据集契约。
func TestDiscardFailedBuildResetsActiveDatasetContract(t *testing.T) {
	id := cleanupIndexID("prices", viewindex.SlotA)
	engine := &fakeManagedEngine{name: "fake", ids: []string{id}}
	svc := newCleanupService(t, engine)
	key := viewRef{spaceID: "space", viewID: "prices"}
	runtime := &viewRuntime{active: id, activeDatasetIDs: []string{"ds_old"}, activePrimaryDatasetID: "ds_old", activeDatasetSet: true, status: "building"}
	svc.views[key] = runtime
	svc.indexEngine[id] = "fake"
	svc.indexView[id] = key
	svc.discardFailedBuild(context.Background(), "space", "prices", id, "fake")
	if runtime.active != "" || runtime.activeDatasetSet || runtime.activeDatasetIDs != nil || runtime.activePrimaryDatasetID != "" || runtime.status != "failed" {
		t.Fatalf("放弃失败构建时应清空 active 并重置数据集契约：%+v", runtime)
	}
}
