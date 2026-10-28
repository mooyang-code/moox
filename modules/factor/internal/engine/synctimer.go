package engine

import (
	"context"
	"sync"
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
	// 下次同步会重新取得。applied 在每次同步成功之后调用（释放同步登记，卸载不再引用的模块）。都可为 nil。
	prepare func(ctx context.Context, snapshot CatalogSnapshot) error
	applied func()
	// okAt 是最近一次成功同步的请求发出的时间：恢复追赶的前置条件要的是“恢复之后发出的请求”成功过，而不是响应在恢复之后到达
	// （恢复前发出、延迟到达的旧响应不能算数）。
	mu   sync.Mutex
	okAt time.Time
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

// SyncedSince 返回最近一次成功同步的请求发出的时间（没有成功过为零）。
func (s *catalogSyncer) SyncedSince() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.okAt
}

func (s *catalogSyncer) SyncOnce(ctx context.Context) error {
	began := time.Now()
	if s.now != nil {
		began = s.now()
	}
	snapshot, err := s.client.SyncCatalog(ctx, s.cache.Status().Hash)
	if err == nil && !snapshot.NotModified && s.prepare != nil {
		err = s.prepare(ctx, snapshot)
	}
	if err == nil {
		var changed bool
		if changed, err = s.cache.Apply(snapshot); err == nil {
			// 目录没变（NotModified）也要调用：上一次准备失败遗留的同步登记要在这里释放，保留集合以当前目录为准。
			if s.applied != nil {
				s.applied()
			}
			s.mu.Lock()
			s.okAt = began
			s.mu.Unlock()
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
