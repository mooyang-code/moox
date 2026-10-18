package engine

import (
	"context"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// NextSyncAt returns the first instant after now that is a UTC multiple of
// interval plus offset; with interval=1m and offset=45s that is second 45 of
// the next minute that has not passed yet.
func NextSyncAt(now time.Time, interval, offset time.Duration) time.Time {
	now = now.UTC()
	next := now.Truncate(interval).Add(offset)
	if !next.After(now) {
		next = next.Add(interval)
	}
	return next
}

// CatalogClient is the manager call the syncer needs.
type CatalogClient interface {
	SyncCatalog(ctx context.Context, knownHash string) (CatalogSnapshot, error)
}

// catalogSyncer pulls the manager's catalog on the aligned timer and installs
// it into the cache; onChange runs when the set of factor sets changed.
type catalogSyncer struct {
	client   CatalogClient
	cache    *CatalogCache
	interval time.Duration
	offset   time.Duration
	onChange func()
	now      func() time.Time
}

func (s *catalogSyncer) SyncOnce(ctx context.Context) error {
	snapshot, err := s.client.SyncCatalog(ctx, s.cache.Status().Hash)
	if err == nil {
		var changed bool
		if changed, err = s.cache.Apply(snapshot); err == nil {
			if changed && s.onChange != nil {
				s.onChange()
			}
			return nil
		}
	}
	s.cache.MarkSyncFailed(err)
	log.WarnContextf(ctx, "factor_catalog_sync_failed error=%v", err)
	return err
}

// Run syncs at every aligned instant until ctx ends.
func (s *catalogSyncer) Run(ctx context.Context) {
	for {
		wait := NextSyncAt(s.now(), s.interval, s.offset).Sub(s.now())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		syncCtx, cancel := context.WithTimeout(ctx, s.interval)
		_ = s.SyncOnce(syncCtx)
		cancel()
	}
}
