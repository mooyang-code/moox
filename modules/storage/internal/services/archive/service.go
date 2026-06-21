package archive

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	deviceparquet "github.com/mooyang-code/moox/modules/storage/internal/infra/device/parquet"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// FactReader 定义归档服务读取 TimeSeries 行所需的接口。
type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
}

// Options 保存归档服务创建时的依赖与路径配置。
type Options struct {
	Metadata    metadata.Store
	Facts       FactReader
	ArchiveRoot string
	DeviceID    string
	Now         func() time.Time
}

// Service 实现主存数据到 Parquet 文件的归档流程。
type Service struct {
	metadata    metadata.Store
	facts       FactReader
	archiveRoot string
	deviceID    string
	now         func() time.Time
}

func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		metadata:    opts.Metadata,
		facts:       opts.Facts,
		archiveRoot: opts.ArchiveRoot,
		deviceID:    opts.DeviceID,
		now:         now,
	}
}

func (s *Service) ArchiveDataset(ctx context.Context, spaceID string, datasetID string, partitionKey string, timeRange *pb.TimeRange) (*pb.ArchiveFile, error) {
	if s == nil || s.metadata == nil || s.facts == nil {
		return nil, errors.New("metadata and facts are required")
	}
	if spaceID == "" || datasetID == "" {
		return nil, errors.New("space_id and dataset_id are required")
	}
	if s.archiveRoot == "" {
		return nil, errors.New("archive root is required")
	}
	deviceID := s.deviceID
	if deviceID == "" {
		var err error
		deviceID, err = s.defaultDeviceID(ctx)
		if err != nil {
			return nil, err
		}
	}
	if partitionKey == "" {
		partitionKey = "default"
	}
	rows, err := s.readAllRows(ctx, spaceID, datasetID, timeRange)
	if err != nil {
		return nil, err
	}
	archiveID := archiveFileID(spaceID, datasetID, s.now())
	path := filepath.Join(s.archiveRoot, safePathPart(spaceID), safePathPart(datasetID), safePathPart(partitionKey), archiveID+".parquet")
	manifest, err := deviceparquet.WriteFacts(ctx, path, rows)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file := &pb.ArchiveFile{
		SpaceId:       spaceID,
		ArchiveFileId: archiveID,
		DatasetId:     datasetID,
		DeviceId:      deviceID,
		PartitionKey:  partitionKey,
		FileUri:       "file://" + absPath,
		FileFormat:    "parquet",
		MinTime:       manifest.MinTime,
		MaxTime:       manifest.MaxTime,
		RowCount:      manifest.RowCount,
		ContentHash:   manifest.ContentHash,
		Columns:       manifest.Columns,
		Status:        "active",
	}
	return s.metadata.RegisterArchiveFile(ctx, file)
}

func (s *Service) ArchiveDatasets(ctx context.Context, spaceID string, partitionKey string, timeRange *pb.TimeRange) ([]*pb.ArchiveFile, error) {
	if s == nil || s.metadata == nil {
		return nil, errors.New("metadata is required")
	}
	if spaceID == "" {
		return nil, errors.New("space_id is required")
	}
	const pageSize = uint32(1000)
	var out []*pb.ArchiveFile
	for pageNo := uint32(1); ; pageNo++ {
		datasets, page, err := s.metadata.ListDatasets(ctx, spaceID, "", pb.DataKind_DATA_KIND_UNSPECIFIED, "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		for _, dataset := range datasets {
			if dataset.GetStatus() != "" && dataset.GetStatus() != "active" {
				continue
			}
			if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
				continue
			}
			file, err := s.ArchiveDataset(ctx, spaceID, dataset.GetDatasetId(), partitionKey, timeRange)
			if err != nil {
				return out, err
			}
			out = append(out, file)
		}
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
}

func (s *Service) defaultDeviceID(ctx context.Context) (string, error) {
	devices, _, err := s.metadata.ListDevices(ctx, "", "parquet_archive", &pb.Page{Page: 1, Size: 1000})
	if err != nil {
		return "", err
	}
	for _, device := range devices {
		if device.GetStatus() == "" || device.GetStatus() == "active" {
			return device.GetDeviceId(), nil
		}
	}
	return "", errors.New("active parquet_archive device not found")
}

func (s *Service) readAllRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange) ([]*pb.TimeSeriesRow, error) {
	const pageSize = uint32(1000)
	subjects, err := s.datasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	freqs, err := s.datasetFreqs(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	var out []*pb.TimeSeriesRow
	for _, subjectID := range subjects {
		for _, freq := range freqs {
			for pageNo := uint32(1); ; pageNo++ {
				rsp, err := s.facts.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
					Keys: []*pb.TimeSeriesKey{{
						SpaceId:   spaceID,
						DatasetId: datasetID,
						SubjectId: subjectID,
						Freq:      freq,
					}},
					TimeRange: timeRange,
					Page:      &pb.Page{Page: pageNo, Size: pageSize},
				})
				if err != nil {
					return nil, err
				}
				if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
					return nil, errors.New(rsp.GetRetInfo().GetMsg())
				}
				out = append(out, rsp.GetRows()...)
				if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
					break
				}
			}
		}
	}
	return out, nil
}

func (s *Service) datasetSubjects(ctx context.Context, spaceID string, datasetID string) ([]string, error) {
	const pageSize = uint32(1000)
	seen := make(map[string]bool)
	var out []string
	for pageNo := uint32(1); ; pageNo++ {
		bindings, page, err := s.metadata.ListDatasetSubjects(ctx, spaceID, datasetID, "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if binding.GetStatus() != "" && binding.GetStatus() != "active" {
				continue
			}
			subjectID := strings.TrimSpace(binding.GetSubjectId())
			if subjectID == "" || seen[subjectID] {
				continue
			}
			seen[subjectID] = true
			out = append(out, subjectID)
		}
		if page == nil || !page.GetHasMore() {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("dataset subjects are required for archive")
	}
	return out, nil
}

func (s *Service) datasetFreqs(ctx context.Context, spaceID string, datasetID string) ([]string, error) {
	dataset, err := s.metadata.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]string, 0, len(dataset.GetFreqs()))
	for _, freq := range dataset.GetFreqs() {
		freq = strings.TrimSpace(freq)
		if freq == "" || seen[freq] {
			continue
		}
		seen[freq] = true
		out = append(out, freq)
	}
	if len(out) == 0 {
		return nil, errors.New("dataset freqs are required for archive")
	}
	return out, nil
}

var unsafePathPart = regexp.MustCompile(`[^A-Za-z0-9_.=-]+`)

func safePathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	value = unsafePathPart.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._")
	if value == "" {
		return "default"
	}
	return value
}

func archiveFileID(spaceID string, datasetID string, now time.Time) string {
	return safePathPart(fmt.Sprintf("archive_%s_%s_%d", spaceID, datasetID, now.UnixNano()))
}
