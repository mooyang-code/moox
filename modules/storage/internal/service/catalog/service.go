package catalog

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// DatasetRows 删除一个数据集的全部行，即同进程的 Pebble 库。
type DatasetRows interface {
	DeleteDatasetRows(ctx context.Context, spaceID, datasetID string) (uint64, error)
}

type Service struct {
	pb.UnimplementedMetadata
	metadata     metadata.Store
	datasetCache *metacache.DatasetCache
	// refreshOverride 仅供测试替换缓存刷新实现
	refreshOverride func(context.Context) error
	rows            DatasetRows
	retention       storagepolicy.Retention
}

type Options struct {
	// Rows 删除数据集时一并删行；只读元数据的工具可以不给，此时 DeleteDataset 报错。
	Rows DatasetRows
	// Retention resolves the read-only Dataset retention. Tools and tests
	// without a policy file get the recommended defaults.
	Retention storagepolicy.Retention
}

func NewMetadataService(store metadata.Store, cache *metacache.DatasetCache, options Options) (*Service, error) {
	if store == nil {
		return nil, errors.New("metadata store is required")
	}
	if options.Retention.Defaults == nil {
		options.Retention = storagepolicy.Default().Retention
	}
	return &Service{metadata: store, datasetCache: cache, rows: options.Rows, retention: options.Retention}, nil
}

// withRetention returns a copy of dataset carrying its effective retention.
func (s *Service) withRetention(dataset *pb.Dataset) *pb.Dataset {
	if dataset == nil {
		return nil
	}
	out := proto.Clone(dataset).(*pb.Dataset)
	out.Retention, out.RetentionSource = s.datasetRetention(dataset)
	return out
}

func (s *Service) withRetentions(items []*pb.Dataset) []*pb.Dataset {
	out := make([]*pb.Dataset, len(items))
	for i, item := range items {
		out[i] = s.withRetention(item)
	}
	return out
}

// datasetRetention resolves a Dataset's retention from the Storage policy. A
// record Dataset is never trimmed; a time-series Dataset whose frequency the
// policy cannot resolve reports no retention.
func (s *Service) datasetRetention(dataset *pb.Dataset) (string, string) {
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return storagepolicy.Forever, storagepolicy.SourceRecord
	}
	period, source, err := s.retention.Resolve(dataset.GetSpaceId(), dataset.GetFrequency())
	if err != nil {
		return "", ""
	}
	return period.String(), source
}

// refreshDatasetCache 在数据集或其列定义变更提交后同步发布最新的 DatasetCache。
func (s *Service) refreshDatasetCache(ctx context.Context, operation string) error {
	var err error
	if s != nil && s.refreshOverride != nil {
		err = s.refreshOverride(ctx)
	} else if s != nil && s.datasetCache != nil {
		err = s.datasetCache.Refresh(ctx)
	}
	if err != nil {
		log.ErrorContextf(ctx, "%s committed but dataset cache refresh failed: %v", operation, err)
	}
	return err
}

var _ pb.MetadataService = (*Service)(nil)
