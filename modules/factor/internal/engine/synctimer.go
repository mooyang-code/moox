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
// it into the cache; onChange runs when the set of factor tasks changed.
type catalogSyncer struct {
	client   CatalogClient
	cache    *CatalogCache
	interval time.Duration
	offset   time.Duration
	onChange func()
	now      func() time.Time
	// prepare 在新快照应用之前调用：把快照引用的自定义算子模块备齐（拉取、校验、编译）。返回错误时不应用快照、不记下快照哈希，
	// 下次同步会重新取得。applied 在快照应用之后调用（卸载不再引用的模块）。都可为 nil。
	prepare func(ctx context.Context, snapshot CatalogSnapshot) error
	applied func()
	// kick 让同步循环不等定时器立刻同步一次（容量 1，多次请求合并）；恢复追赶用它尽快确认目录。
	kick chan struct{}
}

// SyncSoon 请求尽快同步一次目录；已有待执行的请求时合并。
func (s *catalogSyncer) SyncSoon() {
	if s == nil || s.kick == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *catalogSyncer) SyncOnce(ctx context.Context) error {
	snapshot, err := s.client.SyncCatalog(ctx, s.cache.Status().Hash)
	if err == nil && !snapshot.NotModified && s.prepare != nil {
		err = s.prepare(ctx, snapshot)
	}
	if err == nil {
		var changed bool
		if changed, err = s.cache.Apply(snapshot); err == nil {
			if !snapshot.NotModified && s.applied != nil {
				s.applied()
			}
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
		case <-s.kick:
			timer.Stop()
		}
		syncCtx, cancel := context.WithTimeout(ctx, s.interval)
		_ = s.SyncOnce(syncCtx)
		cancel()
	}
}
