package archive

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
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

func archiveMetadataRetInfoError(ret *pb.RetInfo) error {
	if ret == nil || ret.GetCode() == 0 {
		return nil
	}
	return fmt.Errorf("metadata rpc failed: code=%d msg=%s", ret.GetCode(), ret.GetMsg())
}

func (m *RemoteMetadata) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	rsp, err := m.proxy.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return nil, err
	}
	if err := archiveMetadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetDataset(), nil
}

func (m *RemoteMetadata) ListDatasets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	rsp, err := m.proxy.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: spaceID, DataSourceId: dataSourceID, DataKind: dataKind, Freq: freq, Page: page})
	if err != nil {
		return nil, nil, err
	}
	if err := archiveMetadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetDatasets(), rsp.GetPageResult(), nil
}

func (m *RemoteMetadata) ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	rsp, err := m.proxy.ListDatasetSubjects(ctx, &pb.ListDatasetSubjectsReq{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Page: page})
	if err != nil {
		return nil, nil, err
	}
	if err := archiveMetadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetDatasetSubjects(), rsp.GetPageResult(), nil
}

func (m *RemoteMetadata) ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	rsp, err := m.proxy.ListDevices(ctx, &pb.ListDevicesReq{NodeId: nodeID, Engine: engine, Page: page})
	if err != nil {
		return nil, nil, err
	}
	if err := archiveMetadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetDevices(), rsp.GetPageResult(), nil
}

func (m *RemoteMetadata) RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	rsp, err := m.proxy.RegisterArchiveFile(ctx, &pb.RegisterArchiveFileReq{ArchiveFile: item})
	if err != nil {
		return nil, err
	}
	if err := archiveMetadataRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetArchiveFile(), nil
}
