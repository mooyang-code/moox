package catalog

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	metadata      metadata.Store
	metadataCache *metacache.Store
	topologyMu    sync.Mutex
}

func NewMetadataService(store metadata.Store, cache *metacache.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("metadata store is required")
	}
	return &Service{metadata: store, metadataCache: cache}, nil
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
