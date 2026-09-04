package catalog

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/snapshotcache"
	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	pb.UnimplementedMetadata
	metadata       metadata.Store
	metadataCache  *metacache.Store
	nodeAuthSecret string
	operatorSecret string
	nodeState      NodeStateChecker
}

type Options struct {
	AuthSecret string
	// OperatorAuthSecret authenticates expensive admin/CLI metadata actions.
	// It is separate from the DataNode registration secret used by the
	// storage-primary role, while tests and legacy callers may use AuthSecret
	// as a fallback.
	OperatorAuthSecret string
	NodeStateChecker   NodeStateChecker
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
	if options.NodeStateChecker == nil {
		options.NodeStateChecker = rpcNodeStateChecker{}
	}
	return &Service{metadata: store, metadataCache: cache, nodeAuthSecret: secret, operatorSecret: operatorSecret, nodeState: options.NodeStateChecker}, nil
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
