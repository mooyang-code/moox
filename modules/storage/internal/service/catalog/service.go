package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/mooyang-code/snapshotcache"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// DatasetRows 删除一个数据集的全部行，即同进程的 Pebble 库。
type DatasetRows interface {
	DeleteDatasetRows(ctx context.Context, spaceID, datasetID string) (uint64, error)
}

type Service struct {
	pb.UnimplementedMetadata
	metadata      metadata.Store
	metadataCache *metacache.Store
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

func NewMetadataService(store metadata.Store, cache *metacache.Store, options Options) (*Service, error) {
	if store == nil {
		return nil, errors.New("metadata store is required")
	}
	if options.Retention.Defaults == nil {
		options.Retention = storagepolicy.Default().Retention
	}
	return &Service{metadata: store, metadataCache: cache, rows: options.Rows, retention: options.Retention}, nil
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
	period, source, err := s.retention.Resolve(dataset.GetSpaceId(), dataset.GetFreq())
	if err != nil {
		return "", ""
	}
	return period.String(), source
}

func (s *Service) refreshMetadataCache(ctx context.Context) error {
	err := s.refreshMetadataCacheStrict(ctx)
	if errors.Is(err, snapshotcache.ErrRefreshInProgress) {
		// A committed mutation can race with the cache's periodic full refresh.
		// The in-flight refresh will publish the latest committed metadata (or
		// the next tick will retry it), so it must not turn a successful write
		// into a false failure.
		return nil
	}
	return err
}

// refreshMetadataCacheStrict 不吞掉 ErrRefreshInProgress：进行中的刷新可能读取的是提交前的快照，
// 需要同步发布点的调用方必须等它结束后重新刷新。
func (s *Service) refreshMetadataCacheStrict(ctx context.Context) error {
	if s != nil && s.refreshOverride != nil {
		return s.refreshOverride(ctx)
	}
	if s == nil || s.metadataCache == nil {
		return nil
	}
	return s.metadataCache.Refresh(ctx)
}

func (s *Service) refreshMetadataCacheAfterCommit(ctx context.Context, operation string) {
	if err := s.refreshMetadataCache(ctx); err != nil {
		log.ErrorContextf(ctx, "%s committed but metadata cache refresh failed: %v", operation, err)
	}
}

// refreshMetadataCacheSynchronously 让启停、删除数据集这类变更在返回前把元数据缓存发布出去；
// 有限次重试都失败时返回错误，调用方重试即可（这些变更都是幂等的）。
func (s *Service) refreshMetadataCacheSynchronously(ctx context.Context, operation string) error {
	var err error
	const attempts = 5
	for attempt := 0; attempt < attempts; attempt++ {
		if err = s.refreshMetadataCacheStrict(ctx); err == nil {
			return nil
		}
		if attempt < attempts-1 {
			timer := time.NewTimer(time.Duration(25<<attempt) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	log.ErrorContextf(ctx, "%s committed but metadata cache publication is pending: %v", operation, err)
	return err
}

var _ pb.MetadataService = (*Service)(nil)
