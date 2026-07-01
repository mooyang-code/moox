package view

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

// RemoteMetadata implements Metadata by calling the Metadata tRPC service.
type RemoteMetadata struct {
	proxy pb.MetadataClientProxy
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

func remoteMetadataNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
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
	views, _, err := m.ListViews(ctx, spaceID, datasetID, "", &pb.Page{Page: 1, Size: 10000})
	return views, err
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

func (m *RemoteMetadata) BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error) {
	if spaceID == "" || viewID == "" || targetVersion == 0 || resultName == "" {
		return nil, errors.New("space_id, view_id, target_version and result_name are required")
	}
	item, err := m.GetView(ctx, spaceID, viewID)
	if err != nil {
		return nil, err
	}
	if item.GetViewVersion() < targetVersion {
		return nil, fmt.Errorf("view %s/%s version %d is older than target %d", spaceID, viewID, item.GetViewVersion(), targetVersion)
	}
	copied := proto.Clone(item).(*pb.View)
	copied.BuildStatus = "building"
	copied.BuildingViewVersion = targetVersion
	copied.BuildingResult = resultName
	copied.BuildError = ""
	copied.BuildStartedAt = remoteMetadataNow()
	copied.BuildFinishedAt = ""
	return m.UpsertView(ctx, copied)
}

func (m *RemoteMetadata) CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error {
	item, err := m.GetView(ctx, spaceID, viewID)
	if err != nil {
		return err
	}
	if item.GetViewVersion() != targetVersion {
		return fmt.Errorf("view %s/%s version changed from target %d to %d", spaceID, viewID, targetVersion, item.GetViewVersion())
	}
	if item.GetBuildingViewVersion() != targetVersion || item.GetBuildingResult() != resultName {
		return fmt.Errorf("view %s/%s building target changed", spaceID, viewID)
	}
	copied := proto.Clone(item).(*pb.View)
	copied.ActiveResult = resultName
	copied.ActiveViewVersion = targetVersion
	copied.BuildingViewVersion = 0
	copied.BuildingResult = ""
	copied.BuildStatus = "active"
	copied.BuildError = ""
	copied.BuildFinishedAt = remoteMetadataNow()
	_, err = m.UpsertView(ctx, copied)
	return err
}

func (m *RemoteMetadata) FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error {
	item, err := m.GetView(ctx, spaceID, viewID)
	if err != nil {
		return err
	}
	if item.GetBuildingViewVersion() != targetVersion || item.GetBuildingResult() != resultName {
		return fmt.Errorf("view %s/%s building target changed", spaceID, viewID)
	}
	copied := proto.Clone(item).(*pb.View)
	copied.BuildStatus = "failed"
	if buildErr != nil {
		copied.BuildError = buildErr.Error()
	} else {
		copied.BuildError = "build failed"
	}
	copied.BuildFinishedAt = remoteMetadataNow()
	_, err = m.UpsertView(ctx, copied)
	return err
}
