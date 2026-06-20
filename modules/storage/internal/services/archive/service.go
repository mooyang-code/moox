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

type FactReader interface {
	ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error)
}

type Options struct {
	Metadata    metadata.Store
	Facts       FactReader
	ArchiveRoot string
	DeviceID    string
	Now         func() time.Time
}

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

func (s *Service) ArchiveDataSet(ctx context.Context, spaceID string, datasetID string, partitionKey string, timeRange *pb.TimeRange) (*pb.ArchiveFile, error) {
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
	rows, err := s.readAllRows(ctx, &pb.DataScope{SpaceId: spaceID, DatasetId: datasetID}, timeRange)
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

func (s *Service) ArchiveDataSets(ctx context.Context, spaceID string, partitionKey string, timeRange *pb.TimeRange) ([]*pb.ArchiveFile, error) {
	if s == nil || s.metadata == nil {
		return nil, errors.New("metadata is required")
	}
	if spaceID == "" {
		return nil, errors.New("space_id is required")
	}
	const pageSize = uint32(1000)
	var out []*pb.ArchiveFile
	for pageNo := uint32(1); ; pageNo++ {
		datasets, page, err := s.metadata.ListDataSets(ctx, spaceID, "", pb.DataKind_DATA_KIND_UNSPECIFIED, "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		for _, dataset := range datasets {
			if dataset.GetStatus() != "" && dataset.GetStatus() != "active" {
				continue
			}
			file, err := s.ArchiveDataSet(ctx, spaceID, dataset.GetDatasetId(), partitionKey, timeRange)
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

func (s *Service) readAllRows(ctx context.Context, scope *pb.DataScope, timeRange *pb.TimeRange) ([]*pb.DataRow, error) {
	const pageSize = uint32(1000)
	var out []*pb.DataRow
	for pageNo := uint32(1); ; pageNo++ {
		rows, page, err := s.facts.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, timeRange, "", "", nil, &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
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
