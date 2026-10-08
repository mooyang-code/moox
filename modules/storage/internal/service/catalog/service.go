package catalog

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/mooyang-code/snapshotcache"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	pb.UnimplementedMetadata
	metadata       metadata.Store
	metadataCache  *metacache.Store
	nodeAuthSecret string
	operatorSecret string
	viewAuthSecret string
	nodeState      NodeStateChecker
	retention      storagepolicy.Retention
}

type Options struct {
	AuthSecret string
	// OperatorAuthSecret authenticates expensive admin/CLI metadata actions.
	// It is separate from the DataNode registration secret used by the
	// storage-primary role, while tests and legacy callers may use AuthSecret
	// as a fallback.
	OperatorAuthSecret string
	// ViewAuthSecret authenticates the View role's append-only active-schema CAS.
	ViewAuthSecret   string
	NodeStateChecker NodeStateChecker
	// Retention resolves the read-only Dataset retention. Tools and tests
	// without a policy file get the recommended defaults.
	Retention storagepolicy.Retention
}

func NewMetadataService(store metadata.Store, cache *metacache.Store, options Options) (*Service, error) {
	if store == nil {
		return nil, errors.New("metadata store is required")
	}
	secret := options.AuthSecret
	if secret == "" {
		secret = os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	}
	operatorSecret := options.OperatorAuthSecret
	if operatorSecret == "" {
		operatorSecret = os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	}
	if operatorSecret == "" {
		operatorSecret = secret
	}
	viewSecret := options.ViewAuthSecret
	if viewSecret == "" {
		viewSecret = os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	}
	if options.NodeStateChecker == nil {
		options.NodeStateChecker = rpcNodeStateChecker{}
	}
	if options.Retention.Defaults == nil {
		options.Retention = storagepolicy.Default().Retention
	}
	return &Service{metadata: store, metadataCache: cache, nodeAuthSecret: secret, operatorSecret: operatorSecret, viewAuthSecret: viewSecret, nodeState: options.NodeStateChecker, retention: options.Retention}, nil
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
	if s == nil || s.metadataCache == nil {
		return nil
	}
	if err := s.metadataCache.Refresh(ctx); err != nil {
		// A committed mutation can race with the cache's periodic full refresh.
		// The in-flight refresh will publish the latest committed metadata (or
		// the next tick will retry it), so it must not turn a successful write
		// into a false failure.
		if errors.Is(err, snapshotcache.ErrRefreshInProgress) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) refreshMetadataCacheAfterCommit(ctx context.Context, operation string) {
	if err := s.refreshMetadataCache(ctx); err != nil {
		log.ErrorContextf(ctx, "%s committed but metadata cache refresh failed: %v", operation, err)
	}
}

// refreshMetadataCacheSynchronously gives lifecycle mutations a publication
// point before they report success. A committed Dataset is returned alongside
// the safe error when all bounded attempts fail; callers may retry because the
// active+locked terminal state is idempotent.
func (s *Service) refreshMetadataCacheSynchronously(ctx context.Context, operation string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.refreshMetadataCache(ctx); err == nil {
			return nil
		}
		if attempt < 2 {
			timer := time.NewTimer(25 * time.Millisecond)
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
