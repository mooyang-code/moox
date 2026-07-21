package view

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type reconcileMetadata struct {
	view      *pb.View
	activated bool
}

func (m *reconcileMetadata) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: []*pb.View{m.view}, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}
func (m *reconcileMetadata) GetDataset(_ context.Context, req *pb.GetDatasetReq, _ ...client.Option) (*pb.GetDatasetRsp, error) {
	kind := pb.DataKind_DATA_KIND_RECORD
	if req.GetDatasetId() == "prices" {
		kind = pb.DataKind_DATA_KIND_TIME_SERIES
	}
	return &pb.GetDatasetRsp{RetInfo: successRetInfo(), Dataset: &pb.Dataset{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), DataKind: kind}}, nil
}
func (m *reconcileMetadata) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
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
	if needsRebuild(base, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("stable view unexpectedly needs rebuild")
	}
	missing := proto.Clone(base).(*pb.View)
	missing.ActiveIndexId = ""
	if !needsRebuild(missing, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("missing active index did not trigger rebuild")
	}
	revision := proto.Clone(base).(*pb.View)
	revision.DesiredViewRevision = 2
	if !needsRebuild(revision, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("desired revision did not trigger rebuild")
	}
	wide := viewindex.ViewIndexStats{Exists: true, IndexedFrom: "2026-07-17T00:00:00Z", IndexedTo: "2026-07-20T00:00:00Z"}
	if !needsRebuild(base, wide) {
		t.Fatal("coverage wider than twice keep_duration did not trigger rebuild")
	}
	permanent := proto.Clone(base).(*pb.View)
	permanent.KeepDuration = "0"
	if needsRebuild(permanent, wide) {
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
		PrimaryDatasetId:    "records",
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
	indexID := viewindex.InactiveViewIndexID("space", "records", "")
	engine, err := svc.engineFor(indexID)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := engine.Query(context.Background(), indexID, viewindex.QuerySpec{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("initial rows=%v err=%v", rows, err)
	}
}

func TestReconcilerUsesDatasetKindForTimeSeriesWithoutGrainKeys(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices", DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{SpaceId: "space", ViewId: "prices", OriginId: "prices.close", ColumnName: "prices.close"}},
	}}
	stop, err := svc.StartReconciler(context.Background(), ReconcilerOptions{Metadata: metadata, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	indexID := viewindex.InactiveViewIndexID("space", "prices", "")
	engine, err := svc.engineFor(indexID)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := engine.Query(context.Background(), indexID, viewindex.QuerySpec{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("time-series rows=%v err=%v", rows, err)
	}
}
