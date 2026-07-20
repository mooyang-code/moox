//go:build legacy_storage

package view

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

// RemoteMetadata implements Metadata by calling the Metadata tRPC service.
type RemoteMetadata struct {
	proxy pb.MetadataClientProxy
}

// Ready performs a real metadata RPC so an independent View process does not
// advertise readiness merely because its local index directories exist.
func (m *RemoteMetadata) Ready(ctx context.Context) error {
	rsp, err := m.proxy.ListSpaces(ctx, &pb.ListSpacesReq{Page: &pb.Page{Size: 1}})
	if err != nil {
		return err
	}
	return metadataRetInfoError(rsp.GetRetInfo())
}

func NewRemoteMetadata(serviceName string, opts ...client.Option) *RemoteMetadata {
	if serviceName != "" {
		opts = append([]client.Option{client.WithServiceName(serviceName)}, opts...)
	}
	return &RemoteMetadata{proxy: pb.NewMetadataClientProxy(opts...)}
}

func metadataRetInfoError(ret *pb.RetInfo) error {
	if ret == nil || ret.GetCode() == 0 {
		return nil
	}
	return fmt.Errorf("metadata rpc failed: code=%d msg=%s", ret.GetCode(), ret.GetMsg())
}

func (m *RemoteMetadata) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	rsp, err := m.proxy.GetView(ctx, &pb.GetViewReq{SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetView(), nil
}

func (m *RemoteMetadata) GetViewIndexBuild(ctx context.Context, spaceID string, viewID string) (*pb.ViewIndexBuild, error) {
	item, err := m.GetView(ctx, spaceID, viewID)
	if err != nil {
		return nil, err
	}
	if item.GetIndexBuild() == nil {
		return nil, fmt.Errorf("view %s/%s has no index build", spaceID, viewID)
	}
	return item.GetIndexBuild(), nil
}

func (m *RemoteMetadata) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	rsp, err := m.proxy.ListViews(ctx, &pb.ListViewsReq{SpaceId: spaceID, DatasetId: datasetID, Status: status, Page: page})
	if err != nil {
		return nil, nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetViews(), rsp.GetPageResult(), nil
}

func (m *RemoteMetadata) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	const pageSize = uint32(1000)
	var out []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		views, page, err := m.ListViews(ctx, spaceID, datasetID, "active", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, views...)
		if page == nil || !page.GetHasMore() || len(views) == 0 {
			return out, nil
		}
	}
}

func (m *RemoteMetadata) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	rsp, err := m.proxy.ListViewColumns(ctx, &pb.ListViewColumnsReq{SpaceId: spaceID, ViewId: viewID, Page: page})
	if err != nil {
		return nil, nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetColumns(), rsp.GetPageResult(), nil
}

func (m *RemoteMetadata) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	rsp, err := m.proxy.ListSpaces(ctx, &pb.ListSpacesReq{Owner: owner, Page: page})
	if err != nil {
		return nil, nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetSpaces(), rsp.GetPageResult(), nil
}

func (m *RemoteMetadata) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	rsp, err := m.proxy.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetDataset(), nil
}

func (m *RemoteMetadata) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	rsp, err := m.proxy.UpdateView(ctx, &pb.UpdateViewReq{View: item})
	if err != nil {
		return nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetView(), nil
}

func (m *RemoteMetadata) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	rsp, err := m.proxy.ClaimViewIndexBuild(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, false, err
	}
	return rsp.GetBuild(), rsp.GetResumed(), nil
}

func (m *RemoteMetadata) UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	rsp, err := m.proxy.UpdateViewIndexBuild(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetBuild(), nil
}

func (m *RemoteMetadata) ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.View, error) {
	rsp, err := m.proxy.ActivateViewIndex(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetView(), nil
}

func (m *RemoteMetadata) FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	rsp, err := m.proxy.FailViewIndexBuild(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := metadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetBuild(), nil
}
