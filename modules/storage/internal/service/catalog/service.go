package catalog

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	pb.UnimplementedMetadata
	metadata       metadata.Store
	metadataCache  *metacache.Store
	nodeAuthSecret string
	nodeState      NodeStateChecker
}

func NewMetadataService(store metadata.Store, cache *metacache.Store, authSecret ...string) (*Service, error) {
	return newMetadataService(store, cache, firstAuthSecret(authSecret), nil)
}

// NewMetadataServiceWithNodeStateChecker is used by tests and by embedders
// that provide a local DataNode runtime client. The production constructor
// uses the generated DataNodeRuntime client automatically.
func NewMetadataServiceWithNodeStateChecker(store metadata.Store, cache *metacache.Store, authSecret string, checker NodeStateChecker) (*Service, error) {
	return newMetadataService(store, cache, authSecret, checker)
}

func firstAuthSecret(authSecret []string) string {
	if len(authSecret) == 0 {
		return ""
	}
	return authSecret[0]
}

func newMetadataService(store metadata.Store, cache *metacache.Store, authSecret string, checker NodeStateChecker) (*Service, error) {
	if store == nil {
		return nil, errors.New("metadata store is required")
	}
	secret := authSecret
	if secret == "" {
		secret = os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	}
	if checker == nil {
		checker = rpcNodeStateChecker{}
	}
	return &Service{metadata: store, metadataCache: cache, nodeAuthSecret: secret, nodeState: checker}, nil
}

func (s *Service) refreshMetadataCache(ctx context.Context) error {
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
