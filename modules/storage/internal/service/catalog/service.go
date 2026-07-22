package catalog

import (
	"context"
	"errors"
	"os"

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
}

func NewMetadataService(store metadata.Store, cache *metacache.Store, authSecret ...string) (*Service, error) {
	if store == nil {
		return nil, errors.New("metadata store is required")
	}
	secret := ""
	if len(authSecret) > 0 {
		secret = authSecret[0]
	}
	if secret == "" {
		secret = os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	}
	return &Service{metadata: store, metadataCache: cache, nodeAuthSecret: secret}, nil
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

var _ pb.MetadataService = (*Service)(nil)
