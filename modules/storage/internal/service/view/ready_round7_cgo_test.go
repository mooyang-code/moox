//go:build cgo

package view

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 按元数据切换活动索引（例如激活响应丢失后由维护补上）也要继承写入围栏，与 switchViewLocked 一致：否则切换前写入、
// 切换后才到的就绪事件要等同一来源的下一次行写入才放行。
func TestAttachActiveViewInheritsAppliedFence(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	old := &pb.View{SpaceId: "space", ViewId: "prices", DatasetId: "prices", ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1, ActiveViewSchemaHash: "a", Engine: "bleve", Status: "active"}
	if err := svc.AttachActiveView(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	svc.NoteAppliedPosition("space", "prices", "prices-a", "node-a", "store-a", 12)
	activeIDs, _ := json.Marshal([]string{"prices"})
	updated := &pb.View{SpaceId: "space", ViewId: "prices", DatasetId: "prices", ActiveIndexId: "prices-b", ActiveViewRevision: 2, DesiredViewRevision: 2, ActiveViewSchemaHash: "b", Engine: "bleve", Status: "active", Attributes: map[string]string{activeDatasetIDsAttr: string(activeIDs), activePrimaryDatasetAttr: "prices"}}
	if err := svc.AttachActiveView(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if seq := svc.appliedSequence("space", "prices", "prices-b", "node-a", "store-a"); seq != 12 {
		t.Fatalf("新活动索引应继承旧索引的写入围栏：%d", seq)
	}
}
