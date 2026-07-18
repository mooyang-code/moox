package primarystore

import (
	"context"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type viewIndexMetadataStore struct {
	stubMetadataStore
	build   *pb.ViewIndexBuild
	view    *pb.View
	claimOK bool
}

func (s *viewIndexMetadataStore) ClaimViewIndexBuild(_ context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	if !s.claimOK {
		return nil, false, os.ErrNotExist
	}
	build := proto.Clone(s.build).(*pb.ViewIndexBuild)
	build.SpaceId = req.GetSpaceId()
	build.ViewId = req.GetViewId()
	return build, true, nil
}

func (s *viewIndexMetadataStore) GetView(_ context.Context, spaceID, viewID string) (*pb.View, error) {
	if s.view == nil {
		return nil, os.ErrNotExist
	}
	view := proto.Clone(s.view).(*pb.View)
	view.SpaceId = spaceID
	view.ViewId = viewID
	return view, nil
}

func (s *viewIndexMetadataStore) UpdateViewIndexBuild(_ context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if s.build == nil {
		return nil, os.ErrNotExist
	}
	build := proto.Clone(s.build).(*pb.ViewIndexBuild)
	build.State = req.GetNextState()
	return build, nil
}

func (s *viewIndexMetadataStore) ActivateViewIndex(_ context.Context, req *pb.ActivateViewIndexReq) (*pb.View, error) {
	if s.view == nil {
		return nil, os.ErrNotExist
	}
	view := proto.Clone(s.view).(*pb.View)
	view.SpaceId = req.GetSpaceId()
	view.ViewId = req.GetViewId()
	view.Status = "active"
	return view, nil
}

func (s *viewIndexMetadataStore) FailViewIndexBuild(_ context.Context, req *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if s.build == nil {
		return nil, os.ErrNotExist
	}
	build := proto.Clone(s.build).(*pb.ViewIndexBuild)
	build.State = pb.ViewIndexBuild_FAILED
	build.Error = req.GetError()
	return build, nil
}

func TestClaimViewIndexBuildReturnsViewAndBuild(t *testing.T) {
	store := &viewIndexMetadataStore{
		claimOK: true,
		build:   &pb.ViewIndexBuild{BuildId: "build-1", State: pb.ViewIndexBuild_BUILDING},
		view:    &pb.View{SpaceId: "crypto", ViewId: "kline_view", Name: "K线视图"},
	}
	svc := &Service{metadata: store}

	rsp, err := svc.ClaimViewIndexBuild(context.Background(), &pb.ClaimViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "kline_view",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.True(t, rsp.GetResumed())
	assert.Equal(t, "build-1", rsp.GetBuild().GetBuildId())
	assert.Equal(t, "kline_view", rsp.GetView().GetViewId())
}

func TestClaimViewIndexBuildReturnsStoreError(t *testing.T) {
	svc := &Service{metadata: &viewIndexMetadataStore{}}
	rsp, err := svc.ClaimViewIndexBuild(context.Background(), &pb.ClaimViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "missing",
	})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestUpdateViewIndexBuildReturnsBuild(t *testing.T) {
	store := &viewIndexMetadataStore{build: &pb.ViewIndexBuild{BuildId: "build-1"}}
	svc := &Service{metadata: store}

	rsp, err := svc.UpdateViewIndexBuild(context.Background(), &pb.UpdateViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "kline_view", BuildId: "build-1", NextState: pb.ViewIndexBuild_CATCHING_UP,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, pb.ViewIndexBuild_CATCHING_UP, rsp.GetBuild().GetState())
}

func TestActivateViewIndexRefreshesCache(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto", Name: "Crypto", Status: "active",
	}})
	mustRetOK(t, spaceRsp, err)

	dataSourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active",
	}})
	mustRetOK(t, dataSourceRsp, err)

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance",
		Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active",
		Freqs: []string{"1m"},
	}})
	mustRetOK(t, datasetRsp, err)

	viewRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId: "crypto", ViewId: "kline_view", Name: "K线视图",
		DatasetIds: []string{"kline"}, Status: "active", FilterJson: `{"freq":"1m"}`,
	}})
	mustRetOK(t, viewRsp, err)

	indexID := viewindex.ViewIndexID("crypto", "kline_view", viewindex.SlotA)
	claimRsp, err := svc.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		SpaceId:           "crypto",
		ViewId:            "kline_view",
		BuildId:           "build-1",
		IndexId:           indexID,
		Engine:            "duckdb",
		TargetViewVersion: 1,
		OwnerId:           "worker-1",
		SchemaHash:        "schema-hash",
		LeaseTtlSeconds:   60,
	})
	mustRetOK(t, claimRsp, err)

	build := claimRsp.GetBuild()
	for build.GetState() == pb.ViewIndexBuild_PREPARING {
		updateRsp, err := svc.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
			SpaceId: "crypto", ViewId: "kline_view", BuildId: build.GetBuildId(), OwnerId: "worker-1",
			ExpectedState: pb.ViewIndexBuild_PREPARING, NextState: pb.ViewIndexBuild_BUILDING,
		})
		mustRetOK(t, updateRsp, err)
		build = updateRsp.GetBuild()
	}
	for build.GetState() == pb.ViewIndexBuild_BUILDING {
		updateRsp, err := svc.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
			SpaceId: "crypto", ViewId: "kline_view", BuildId: build.GetBuildId(), OwnerId: "worker-1",
			ExpectedState: pb.ViewIndexBuild_BUILDING, NextState: pb.ViewIndexBuild_CATCHING_UP,
		})
		mustRetOK(t, updateRsp, err)
		build = updateRsp.GetBuild()
	}
	updateRsp, err := svc.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "kline_view", BuildId: build.GetBuildId(), OwnerId: "worker-1",
		ExpectedState: pb.ViewIndexBuild_CATCHING_UP, NextState: pb.ViewIndexBuild_READY,
	})
	mustRetOK(t, updateRsp, err)

	activateRsp, err := svc.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "kline_view", BuildId: build.GetBuildId(), OwnerId: "worker-1",
	})
	mustRetOK(t, activateRsp, err)
	assert.Equal(t, indexID, activateRsp.GetView().GetActiveIndexId())
}

func TestFailViewIndexBuildReturnsFailedBuild(t *testing.T) {
	store := &viewIndexMetadataStore{build: &pb.ViewIndexBuild{BuildId: "build-1"}}
	svc := &Service{metadata: store}

	rsp, err := svc.FailViewIndexBuild(context.Background(), &pb.FailViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "kline_view", BuildId: "build-1", Error: "boom",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, pb.ViewIndexBuild_FAILED, rsp.GetBuild().GetState())
	assert.Equal(t, "boom", rsp.GetBuild().GetError())
}
