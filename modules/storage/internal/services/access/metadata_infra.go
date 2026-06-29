package access

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
)

// 本文件聚合主存储节点、设备、路由及归档文件相关的元数据 CRUD 入口。

func (s *Service) CreatePrimaryStoreNode(ctx context.Context, req *pb.CreatePrimaryStoreNodeReq) (*pb.CreatePrimaryStoreNodeRsp, error) {
	item := req.GetNode()
	if item == nil || (item.GetNodeId() == "" && item.GetName() == "") {
		return &pb.CreatePrimaryStoreNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id or name is required"))}, nil
	}
	if item.NodeId == "" {
		item.NodeId = defaultID(item.GetName(), "node")
	}
	if item.Name == "" {
		item.Name = item.GetNodeId()
	}
	created, err := s.metadata.UpsertPrimaryStoreNode(ctx, item)
	if err != nil {
		return &pb.CreatePrimaryStoreNodeRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreatePrimaryStoreNodeRsp{RetInfo: response.Success("success"), Node: created}, nil
}

func (s *Service) UpdatePrimaryStoreNode(ctx context.Context, req *pb.UpdatePrimaryStoreNodeReq) (*pb.UpdatePrimaryStoreNodeRsp, error) {
	updated, err := s.metadata.UpsertPrimaryStoreNode(ctx, req.GetNode())
	if err != nil {
		return &pb.UpdatePrimaryStoreNodeRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdatePrimaryStoreNodeRsp{RetInfo: response.Success("success"), Node: updated}, nil
}

func (s *Service) GetPrimaryStoreNode(ctx context.Context, req *pb.GetPrimaryStoreNodeReq) (*pb.GetPrimaryStoreNodeRsp, error) {
	item, err := s.metadata.GetPrimaryStoreNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetPrimaryStoreNodeRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetPrimaryStoreNodeRsp{RetInfo: response.Success("success"), Node: item}, nil
}

func (s *Service) ListPrimaryStoreNodes(ctx context.Context, req *pb.ListPrimaryStoreNodesReq) (*pb.ListPrimaryStoreNodesRsp, error) {
	items, page, err := s.metadata.ListPrimaryStoreNodes(ctx, req.GetPage())
	if err != nil {
		return &pb.ListPrimaryStoreNodesRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListPrimaryStoreNodesRsp{RetInfo: response.Success("success"), Nodes: items, PageResult: page}, nil
}

func (s *Service) CreateDevice(ctx context.Context, req *pb.CreateDeviceReq) (*pb.CreateDeviceRsp, error) {
	item := req.GetDevice()
	if item == nil || (item.GetDeviceId() == "" && item.GetName() == "") {
		return &pb.CreateDeviceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id or name is required"))}, nil
	}
	if item.DeviceId == "" {
		item.DeviceId = defaultID(item.GetName(), "device")
	}
	if item.Name == "" {
		item.Name = item.GetDeviceId()
	}
	created, err := s.metadata.UpsertDevice(ctx, item)
	if err != nil {
		return &pb.CreateDeviceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDeviceRsp{RetInfo: response.Success("success"), Device: created}, nil
}

func (s *Service) UpdateDevice(ctx context.Context, req *pb.UpdateDeviceReq) (*pb.UpdateDeviceRsp, error) {
	updated, err := s.metadata.UpsertDevice(ctx, req.GetDevice())
	if err != nil {
		return &pb.UpdateDeviceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateDeviceRsp{RetInfo: response.Success("success"), Device: updated}, nil
}

func (s *Service) GetDevice(ctx context.Context, req *pb.GetDeviceReq) (*pb.GetDeviceRsp, error) {
	item, err := s.metadata.GetDevice(ctx, req.GetDeviceId())
	if err != nil {
		return &pb.GetDeviceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetDeviceRsp{RetInfo: response.Success("success"), Device: item}, nil
}

func (s *Service) ListDevices(ctx context.Context, req *pb.ListDevicesReq) (*pb.ListDevicesRsp, error) {
	items, page, err := s.metadata.ListDevices(ctx, req.GetNodeId(), req.GetEngine(), req.GetPage())
	if err != nil {
		return &pb.ListDevicesRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDevicesRsp{RetInfo: response.Success("success"), Devices: items, PageResult: page}, nil
}

func (s *Service) CreatePrimaryStoreRoute(ctx context.Context, req *pb.CreatePrimaryStoreRouteReq) (*pb.CreatePrimaryStoreRouteRsp, error) {
	item := req.GetPrimaryStoreRoute()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return &pb.CreatePrimaryStoreRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and node_id are required"))}, nil
	}
	if item.RouteId == "" {
		item.RouteId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetNodeId()}, "-"), "route")
	}
	created, err := s.metadata.UpsertPrimaryStoreRoute(ctx, item)
	if err != nil {
		return &pb.CreatePrimaryStoreRouteRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreatePrimaryStoreRouteRsp{RetInfo: response.Success("success"), PrimaryStoreRoute: created}, nil
}

func (s *Service) UpdatePrimaryStoreRoute(ctx context.Context, req *pb.UpdatePrimaryStoreRouteReq) (*pb.UpdatePrimaryStoreRouteRsp, error) {
	updated, err := s.metadata.UpsertPrimaryStoreRoute(ctx, req.GetPrimaryStoreRoute())
	if err != nil {
		return &pb.UpdatePrimaryStoreRouteRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdatePrimaryStoreRouteRsp{RetInfo: response.Success("success"), PrimaryStoreRoute: updated}, nil
}

func (s *Service) GetPrimaryStoreRoute(ctx context.Context, req *pb.GetPrimaryStoreRouteReq) (*pb.GetPrimaryStoreRouteRsp, error) {
	item, err := s.metadata.GetPrimaryStoreRoute(ctx, req.GetSpaceId(), req.GetRouteId())
	if err != nil {
		return &pb.GetPrimaryStoreRouteRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	return &pb.GetPrimaryStoreRouteRsp{RetInfo: response.Success("success"), PrimaryStoreRoute: item}, nil
}

func (s *Service) ListPrimaryStoreRoutes(ctx context.Context, req *pb.ListPrimaryStoreRoutesReq) (*pb.ListPrimaryStoreRoutesRsp, error) {
	items, page, err := s.metadata.ListPrimaryStoreRoutes(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetNodeId(), req.GetPage())
	if err != nil {
		return &pb.ListPrimaryStoreRoutesRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListPrimaryStoreRoutesRsp{RetInfo: response.Success("success"), PrimaryStoreRoutes: items, PageResult: page}, nil
}

func (s *Service) RegisterArchiveFile(ctx context.Context, req *pb.RegisterArchiveFileReq) (*pb.RegisterArchiveFileRsp, error) {
	item := req.GetArchiveFile()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return &pb.RegisterArchiveFileRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, device_id and file_uri are required"))}, nil
	}
	if item.ArchiveFileId == "" {
		item.ArchiveFileId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetPartitionKey(), item.GetFileUri()}, "-"), "archive_file")
	}
	created, err := s.metadata.RegisterArchiveFile(ctx, item)
	if err != nil {
		return &pb.RegisterArchiveFileRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.RegisterArchiveFileRsp{RetInfo: response.Success("success"), ArchiveFile: created}, nil
}

func (s *Service) ListArchiveFiles(ctx context.Context, req *pb.ListArchiveFilesReq) (*pb.ListArchiveFilesRsp, error) {
	items, _, err := s.metadata.ListArchiveFiles(ctx, req.GetSpaceId(), req.GetDatasetId(), nil)
	if err != nil {
		return &pb.ListArchiveFilesRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	filtered := items[:0]
	for _, item := range items {
		if req.GetDeviceId() != "" && item.GetDeviceId() != req.GetDeviceId() {
			continue
		}
		if req.GetPartitionKey() != "" && item.GetPartitionKey() != req.GetPartitionKey() {
			continue
		}
		if !archiveFileOverlaps(item, req.GetTimeRange()) {
			continue
		}
		filtered = append(filtered, item)
	}
	paged, page := pageSlice(filtered, req.GetPage())
	return &pb.ListArchiveFilesRsp{RetInfo: response.Success("success"), ArchiveFiles: paged, PageResult: page}, nil
}

func defaultID(name, prefix string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return prefix + "_" + xid.New().String()
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(name)
}

func pageSlice[T any](items []T, page *pb.Page) ([]T, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(items)), HasMore: end < len(items)}
}

func archiveFileOverlaps(item *pb.ArchiveFile, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	if timeRange.GetStartTime() != "" && item.GetMaxTime() != "" && item.GetMaxTime() < timeRange.GetStartTime() {
		return false
	}
	if timeRange.GetEndTime() != "" && item.GetMinTime() != "" && item.GetMinTime() > timeRange.GetEndTime() {
		return false
	}
	return true
}
