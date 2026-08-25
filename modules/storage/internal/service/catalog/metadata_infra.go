package catalog

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/rs/xid"
)

// 本文件聚合设备及归档文件相关的元数据 CRUD 入口。

func (s *Service) CreateDevice(ctx context.Context, req *pb.CreateDeviceReq) (*pb.CreateDeviceRsp, error) {
	item := req.GetDevice()
	if item == nil || (item.GetDeviceId() == "" && item.GetName() == "") {
		return &pb.CreateDeviceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id or name is required"))}, nil
	}
	if item.DeviceId == "" {
		item.DeviceId = defaultID(item.GetName(), "device")
	}
	if item.Name == "" {
		item.Name = item.GetDeviceId()
	}
	created, err := s.metadata.UpsertDevice(ctx, item)
	if err != nil {
		return &pb.CreateDeviceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CreateDeviceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDeviceRsp{RetInfo: retinfo.Success("success"), Device: created}, nil
}

func (s *Service) UpdateDevice(ctx context.Context, req *pb.UpdateDeviceReq) (*pb.UpdateDeviceRsp, error) {
	updated, err := s.metadata.UpsertDevice(ctx, req.GetDevice())
	if err != nil {
		return &pb.UpdateDeviceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateDeviceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateDeviceRsp{RetInfo: retinfo.Success("success"), Device: updated}, nil
}

func (s *Service) GetDevice(ctx context.Context, req *pb.GetDeviceReq) (*pb.GetDeviceRsp, error) {
	item, err := s.metadata.GetDevice(ctx, req.GetDeviceId())
	if err != nil {
		return &pb.GetDeviceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetDeviceRsp{RetInfo: retinfo.Success("success"), Device: item}, nil
}

func (s *Service) ListDevices(ctx context.Context, req *pb.ListDevicesReq) (*pb.ListDevicesRsp, error) {
	items, page, err := s.metadata.ListDevices(ctx, req.GetEngine(), req.GetPage())
	if err != nil {
		return &pb.ListDevicesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDevicesRsp{RetInfo: retinfo.Success("success"), Devices: items, PageResult: page}, nil
}

func (s *Service) RegisterArchiveFile(ctx context.Context, req *pb.RegisterArchiveFileReq) (*pb.RegisterArchiveFileRsp, error) {
	item := req.GetArchiveFile()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return &pb.RegisterArchiveFileRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, device_id and file_uri are required"))}, nil
	}
	if item.ArchiveFileId == "" {
		item.ArchiveFileId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetPartitionKey(), item.GetFileUri()}, "-"), "archive_file")
	}
	created, err := s.metadata.RegisterArchiveFile(ctx, item)
	if err != nil {
		return &pb.RegisterArchiveFileRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	// The SQLite upsert is already committed at this point.  Cache publication
	// is best-effort, just like RegisterDataSubject: a concurrent snapshot
	// refresh must not turn a successful archive materialization into a retry
	// loop (or make the archive worker report a false failure).
	s.refreshMetadataCacheAfterCommit(ctx, "RegisterArchiveFile")
	return &pb.RegisterArchiveFileRsp{RetInfo: retinfo.Success("success"), ArchiveFile: created}, nil
}

func (s *Service) ListArchiveFiles(ctx context.Context, req *pb.ListArchiveFilesReq) (*pb.ListArchiveFilesRsp, error) {
	if req == nil {
		req = &pb.ListArchiveFilesReq{}
	}
	if !validArchiveFileSort(req.GetSortBy(), req.GetSortOrder()) {
		return &pb.ListArchiveFilesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("invalid archive file sort"))}, nil
	}
	items, err := s.listAllArchiveFiles(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.ListArchiveFilesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
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
	sortArchiveFiles(filtered, req.GetSortBy(), req.GetSortOrder())
	paged, page := pageSlice(filtered, req.GetPage())
	return &pb.ListArchiveFilesRsp{RetInfo: retinfo.Success("success"), ArchiveFiles: paged, PageResult: page}, nil
}

func (s *Service) listAllArchiveFiles(ctx context.Context, spaceID string, datasetID string) ([]*pb.ArchiveFile, error) {
	const pageSize = uint32(1000)
	var items []*pb.ArchiveFile
	for pageNo := uint32(1); ; pageNo++ {
		pageItems, page, err := s.metadata.ListArchiveFiles(ctx, spaceID, datasetID, &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		items = append(items, pageItems...)
		if page == nil || !page.GetHasMore() || len(pageItems) == 0 {
			return items, nil
		}
	}
}

func validArchiveFileSort(sortBy string, sortOrder string) bool {
	if sortBy == "" {
		return sortOrder == ""
	}
	if sortBy != "min_time" && sortBy != "max_time" && sortBy != "created_at" && sortBy != "updated_at" {
		return false
	}
	return sortOrder == "" || strings.EqualFold(sortOrder, "asc") || strings.EqualFold(sortOrder, "desc")
}

func sortArchiveFiles(items []*pb.ArchiveFile, sortBy string, sortOrder string) {
	if sortBy == "" {
		return
	}
	desc := strings.EqualFold(sortOrder, "desc")
	sort.SliceStable(items, func(i, j int) bool {
		left := archiveFileSortValue(items[i], sortBy)
		right := archiveFileSortValue(items[j], sortBy)
		if left == right {
			return archiveFileID(items[i]) < archiveFileID(items[j])
		}
		if desc {
			return left > right
		}
		return left < right
	})
}

func archiveFileID(item *pb.ArchiveFile) string {
	if item == nil {
		return ""
	}
	return item.GetArchiveFileId()
}

func archiveFileSortValue(item *pb.ArchiveFile, sortBy string) string {
	if item == nil {
		return ""
	}
	switch sortBy {
	case "min_time":
		return item.GetMinTime()
	case "max_time":
		return item.GetMaxTime()
	case "created_at":
		return item.GetCreatedAt()
	case "updated_at":
		return item.GetUpdatedAt()
	default:
		return ""
	}
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
