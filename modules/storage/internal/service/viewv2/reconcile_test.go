package viewv2

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type reconcileMetadata struct {
	view      *pb.View
	activated bool
}

func (m *reconcileMetadata) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: []*pb.View{m.view}, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}
func (m *reconcileMetadata) ClaimViewIndexBuild(_ context.Context, req *pb.ClaimViewIndexBuildReq, _ ...client.Option) (*pb.ClaimViewIndexBuildRsp, error) {
	return &pb.ClaimViewIndexBuildRsp{RetInfo: successRetInfo(), Build: &pb.ViewIndexBuild{BuildId: req.GetBuildId(), State: pb.ViewIndexBuild_PREPARING}}, nil
}
func (m *reconcileMetadata) UpdateViewIndexBuild(_ context.Context, req *pb.UpdateViewIndexBuildReq, _ ...client.Option) (*pb.UpdateViewIndexBuildRsp, error) {
	return &pb.UpdateViewIndexBuildRsp{RetInfo: successRetInfo(), Build: &pb.ViewIndexBuild{BuildId: req.GetBuildId(), State: req.GetNextState()}}, nil
}
func (m *reconcileMetadata) ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq, ...client.Option) (*pb.ActivateViewIndexRsp, error) {
	m.activated = true
	return &pb.ActivateViewIndexRsp{RetInfo: successRetInfo(), View: m.view}, nil
}
func (m *reconcileMetadata) FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq, ...client.Option) (*pb.FailViewIndexBuildRsp, error) {
	return &pb.FailViewIndexBuildRsp{RetInfo: successRetInfo()}, nil
}

func TestNeedsRebuildTriggers(t *testing.T) {
	base := &pb.View{
		SpaceId: "s", ViewId: "v", ActiveIndexId: "idx",
		DesiredViewRevision: 1, ActiveViewRevision: 1, KeepDuration: "24h",
	}
	if needsRebuild(base, viewindex.ViewIndexStats{}) {
		t.Fatal("stable view unexpectedly needs rebuild")
	}
	missing := *base
	missing.ActiveIndexId = ""
	if !needsRebuild(&missing, viewindex.ViewIndexStats{}) {
		t.Fatal("missing active index did not trigger rebuild")
	}
	revision := *base
	revision.DesiredViewRevision = 2
	if !needsRebuild(&revision, viewindex.ViewIndexStats{}) {
		t.Fatal("desired revision did not trigger rebuild")
	}
	wide := viewindex.ViewIndexStats{IndexedFrom: "2026-07-17T00:00:00Z", IndexedTo: "2026-07-20T00:00:00Z"}
	if !needsRebuild(base, wide) {
		t.Fatal("coverage wider than twice keep_duration did not trigger rebuild")
	}
	permanent := *base
	permanent.KeepDuration = "0"
	if needsRebuild(&permanent, wide) {
		t.Fatal("permanent view triggered time-based rebuild")
	}
}

func TestReconcilerCreatesAndActivatesInitialView(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "records", Engine: "bleve",
		DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{
			SpaceId: "space", ViewId: "records", OriginId: "records.title", ColumnName: "records.title",
		}},
	}}
	stop, err := svc.StartReconciler(context.Background(), ReconcilerOptions{Metadata: metadata, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if !metadata.activated {
		t.Fatal("initial view was not activated")
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	list, err := svc.ListViewIndexes(context.Background(), &pb.ListViewIndexesReq{AuthInfo: auth})
	if err != nil || len(list.GetIndexes()) != 1 {
		t.Fatalf("indexes=%v err=%v", list, err)
	}
}
