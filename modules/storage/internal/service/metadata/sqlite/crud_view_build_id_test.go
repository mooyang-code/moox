package sqlite

import (
	"context"
	"testing"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// activateTestBuild 在当前活动索引之外的槽位上认领、完成并激活一次构建。
func activateTestBuild(t *testing.T, ctx context.Context, store *Store, buildID string) *pb.View {
	t.Helper()
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	indexID := coreviewindex.InactiveViewIndexID("space", "source-view", view.GetActiveIndexId())
	if _, _, err := store.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		SpaceId: "space", ViewId: "source-view", BuildId: buildID, IndexId: indexID, Engine: "duckdb",
		TargetViewVersion: view.GetDesiredViewRevision(), OwnerId: "owner", ExpectedActiveIndexId: view.GetActiveIndexId(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]pb.ViewIndexBuild_State{{pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING}, {pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_READY}} {
		if _, err := store.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{SpaceId: "space", ViewId: "source-view", BuildId: buildID, OwnerId: "owner", ExpectedState: step[0], NextState: step[1]}); err != nil {
			t.Fatal(err)
		}
	}
	activated, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{SpaceId: "space", ViewId: "source-view", BuildId: buildID, OwnerId: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	return activated
}

// 激活索引时记录构建 ID：A/B 槽位名在重建时交替复用，构建 ID 才能区分活动索引的代次；修改 View 定义不改变它。
func TestActivateViewIndexRecordsBuildID(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	first := activateTestBuild(t, ctx, store, "build-1")
	if first.GetAttributes()[metadatastore.ActiveBuildIDAttribute] != "build-1" {
		t.Fatalf("激活应记录构建 ID：%v", first.GetAttributes())
	}
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	slotA := view.GetActiveIndexId()
	view.Description = "改了说明"
	view.Attributes[metadatastore.ActiveBuildIDAttribute] = "forged"
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	if edited, err := store.GetView(ctx, "space", "source-view"); err != nil || edited.GetAttributes()[metadatastore.ActiveBuildIDAttribute] != "build-1" {
		t.Fatalf("修改 View 不应改变活动索引的构建 ID：%v err=%v", edited.GetAttributes(), err)
	}
	second := activateTestBuild(t, ctx, store, "build-2")
	third := activateTestBuild(t, ctx, store, "build-3")
	if second.GetAttributes()[metadatastore.ActiveBuildIDAttribute] != "build-2" || third.GetActiveIndexId() != slotA || third.GetAttributes()[metadatastore.ActiveBuildIDAttribute] != "build-3" {
		t.Fatalf("A→B→A 后槽位名相同，构建 ID 应不同：%s %v", third.GetActiveIndexId(), third.GetAttributes())
	}
}
