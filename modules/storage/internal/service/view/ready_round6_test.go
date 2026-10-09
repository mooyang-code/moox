package view

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// 重建切换活动索引后，写入围栏要随之转到新索引：行在切换前写入、周期完成事件在切换后才到（或切换时还在队列里）的
// 就绪事件不能等到同一来源的下一次行写入才放行。配置了落盘目录时也一样（第五轮按“非活动索引”清理键会删掉 next 的围栏）。
func TestReadyFenceFollowsIndexSwitch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		noteNext bool
	}{
		{name: "新索引开始双写之前写入的行", noteNext: false},
		{name: "重建期间双写的行", noteNext: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher := newReadyPublisherFake()
			service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
			dir := t.TempDir()
			service.readyFenceDir = dir
			enqueueReadyPeriods(t, service, "prices-ready-1")
			runtime := service.views[viewRef{spaceID: "quant", viewID: "source-view"}]
			runtime.mu.Lock()
			runtime.next, runtime.status = "source-view-b", "ready"
			runtime.mu.Unlock()
			service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
			if tc.noteNext {
				service.NoteAppliedPosition("quant", "source-view", "source-view-b", "node-a", "store-a", 12)
			}
			if err := service.flushAppliedFence(); err != nil {
				t.Fatal(err)
			}
			runtime.mu.Lock()
			_, _, err := service.switchViewLocked(context.Background(), runtime)
			runtime.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
				t.Fatal(err)
			}
			if len(publisher.byID) != 1 || service.hasPendingReady() {
				t.Fatalf("切换后旧索引已写入到的位置新索引也有，事件应放行：published=%d pending=%v", len(publisher.byID), service.hasPendingReady())
			}
			if err := service.flushAppliedFence(); err != nil {
				t.Fatal(err)
			}
			reloaded := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-b"})
			if err := reloaded.OpenReadyFence(dir); err != nil {
				t.Fatal(err)
			}
			if seq := reloaded.appliedSequence("quant", "source-view", "source-view-b", "node-a", "store-a"); seq != 12 {
				t.Fatalf("重启后新索引的围栏应为 12：%d", seq)
			}
		})
	}
}

// 记录位置只改内存并标脏；落盘只在有新的更新时进行，不再每条行事件都写文件。
func TestAppliedFencePersistsOnlyWhenDirty(t *testing.T) {
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	service.readyFenceDir = t.TempDir()
	path := filepath.Join(service.readyFenceDir, "applied.json")
	exists := func() bool {
		_, err := os.Stat(path)
		return err == nil
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 5)
	if exists() {
		t.Fatal("记录位置不应同步落盘")
	}
	if err := service.flushAppliedFence(); err != nil || !exists() {
		t.Fatalf("标脏后应落盘：%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 4)
	if err := service.flushAppliedFence(); err != nil || exists() {
		t.Fatalf("没有新的更新（较小的位置不推进围栏）时不应落盘：%v", err)
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 6)
	if err := service.flushAppliedFence(); err != nil || !exists() {
		t.Fatalf("有新的更新时应落盘：%v", err)
	}
	reloaded := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	if err := reloaded.OpenReadyFence(service.readyFenceDir); err != nil {
		t.Fatal(err)
	}
	if seq := reloaded.appliedSequence("quant", "source-view", "source-view-a", "node-a", "store-a"); seq != 6 {
		t.Fatalf("重启后围栏应为 6：%d", seq)
	}
}

// 崩溃时写到一半的临时文件在启动时清掉。
func TestOpenReadyFenceRemovesLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"applied.json.tmp-123", "pending.json.tmp-456"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[{"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	if err := service.OpenReadyFence(dir); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*")); len(matches) != 0 {
		t.Fatalf("残留的临时文件应被清掉：%v", matches)
	}
}

// captureLog 在测试期间截获标准 log 的输出。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// 就绪队列的异常按类别去重：交替出现的发布失败与重连失败各记一次，不互相顶掉去重记录。
func TestReadyIssuesDeduplicatePerKind(t *testing.T) {
	buf := captureLog(t)
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake())
	for i := 0; i < 3; i++ {
		service.readyIssue(readyIssuePublish, "发布失败 X")
		service.readyIssue(readyIssueReconnect, "重连失败 Y")
	}
	if got := strings.Count(buf.String(), "发布失败 X"); got != 1 {
		t.Fatalf("发布失败应只记一次：%d\n%s", got, buf.String())
	}
	if got := strings.Count(buf.String(), "重连失败 Y"); got != 1 {
		t.Fatalf("重连失败应只记一次：%d\n%s", got, buf.String())
	}
	service.readyResolved(readyIssueReconnect, "重连已恢复")
	service.readyResolved(readyIssueReconnect, "重连已恢复")
	if got := strings.Count(buf.String(), "重连已恢复"); got != 1 {
		t.Fatalf("恢复只在发生过异常时记一次：%d", got)
	}
}

// 一个空间的事件持续发布失败时，另一空间一条都没发的刷新不能记“已恢复”；真正发布成功后才记。
func TestReadyRecoveryRequiresAPublish(t *testing.T) {
	buf := captureLog(t)
	inner := newReadyPublisherFake()
	publisher := &readyErrorPublisher{inner: inner, errs: []error{fmt.Errorf("%w: nats: timeout", jetstream.ErrPublishTimeout)}}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	enqueueReadyPeriods(t, service, "prices-ready-1")
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err == nil {
		t.Fatal("发布超时应返回错误")
	}
	service.mu.Lock()
	service.readyBackoffUntil = service.readyBackoffUntil.AddDate(-1, 0, 0)
	service.mu.Unlock()
	if err := service.FlushViewDataReady(context.Background(), "other", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "发布已恢复") {
		t.Fatalf("没有发布任何事件的刷新不能记恢复：\n%s", buf.String())
	}
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "发布已恢复"); got != 1 || len(inner.byID) != 1 {
		t.Fatalf("发布成功后应记一次恢复：%d published=%d", got, len(inner.byID))
	}
}

// 写入围栏落盘持续失败（例如磁盘满）时只记一次，恢复后记一次，不随每条行事件交替刷屏。
func TestAppliedFencePersistFailureLogsOnce(t *testing.T) {
	buf := captureLog(t)
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.readyFenceDir = blocked
	for seq := uint64(1); seq <= 3; seq++ {
		service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", seq)
		service.persistAppliedFenceLogged()
	}
	if got := strings.Count(buf.String(), "写入围栏落盘失败"); got != 1 {
		t.Fatalf("持续失败应只记一次：%d\n%s", got, buf.String())
	}
	service.readyFenceDir = t.TempDir()
	service.persistAppliedFenceLogged()
	service.persistAppliedFenceLogged()
	if got := strings.Count(buf.String(), "写入围栏落盘已恢复"); got != 1 {
		t.Fatalf("恢复应只记一次：%d", got)
	}
	if _, err := os.Stat(filepath.Join(service.readyFenceDir, "applied.json")); err != nil {
		t.Fatalf("失败期间的更新应在恢复后落盘：%v", err)
	}
}
