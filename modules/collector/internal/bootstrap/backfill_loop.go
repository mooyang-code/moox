package bootstrap

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultBackfillSyncInterval = time.Minute
	defaultBackfillRunInterval  = time.Minute
)

type backfillLoopConfig struct {
	Spaces         func(context.Context) ([]string, error)
	ManualPaused   func(context.Context) (map[string]bool, error)
	Stage          func() lifecycle.Stage
	RunRunning     func(ctx context.Context, spaceID string)
	RunResuming    func(ctx context.Context, spaceID string)
	SyncInterval   time.Duration
	RunInterval    time.Duration
	ResumeInterval time.Duration
}

type backfillSpaceWorker struct {
	cancel context.CancelFunc
	wake   chan struct{}
}

// backfillLoop 为管理集合里的每个空间维持独立的补采定时循环，不再挂在采集计划对账协程的 AfterTick 上：
//   - StagePaused 或手工暂停：不跑；
//   - StageResuming（未手工暂停）：按较短间隔只推进补采作业；
//   - StageRunning（未手工暂停）：按常规分钟间隔执行收市重取、历史补齐、补采推进与异步自动扫描。
type backfillLoop struct {
	cfg     backfillLoopConfig
	wake    chan struct{}
	mu      sync.Mutex
	workers map[string]*backfillSpaceWorker
	wg      sync.WaitGroup
}

func newBackfillLoop(cfg backfillLoopConfig) *backfillLoop {
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = defaultBackfillSyncInterval
	}
	if cfg.RunInterval <= 0 {
		cfg.RunInterval = defaultBackfillRunInterval
	}
	if cfg.ResumeInterval <= 0 {
		cfg.ResumeInterval = recoveryTickEvery
	}
	return &backfillLoop{
		cfg:     cfg,
		wake:    make(chan struct{}, 1),
		workers: map[string]*backfillSpaceWorker{},
	}
}

// Wake 在生命周期阶段变化时唤醒循环：立刻同步空间列表并通知各空间协程按新阶段调整间隔与动作。
func (l *backfillLoop) Wake() {
	if l == nil {
		return
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run 阻塞运行直到 ctx 取消，并等待全部空间补采协程退出。
func (l *backfillLoop) Run(ctx context.Context) {
	l.sync(ctx, false)
	ticker := time.NewTicker(l.cfg.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.stopAll()
			l.wg.Wait()
			return
		case <-ticker.C:
			l.sync(ctx, false)
		case <-l.wake:
			l.sync(ctx, true)
		}
	}
}

func (l *backfillLoop) stopAll() {
	l.mu.Lock()
	for space, worker := range l.workers {
		worker.cancel()
		delete(l.workers, space)
	}
	l.mu.Unlock()
}

func (l *backfillLoop) sync(ctx context.Context, wakeExisting bool) {
	if l.cfg.Spaces == nil {
		return
	}
	spaces, err := l.cfg.Spaces(ctx)
	if err != nil {
		log.WarnContextf(ctx, "collector backfill loop list managed spaces failed: %v", err)
		return
	}
	wanted := make(map[string]struct{}, len(spaces))
	for _, space := range spaces {
		if trimmed := strings.ToLower(strings.TrimSpace(space)); trimmed != "" {
			wanted[trimmed] = struct{}{}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for space, worker := range l.workers {
		if _, ok := wanted[space]; !ok {
			worker.cancel()
			delete(l.workers, space)
			continue
		}
		if wakeExisting {
			select {
			case worker.wake <- struct{}{}:
			default:
			}
		}
	}
	for space := range wanted {
		if _, exists := l.workers[space]; exists {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &backfillSpaceWorker{cancel: cancel, wake: make(chan struct{}, 1)}
		l.workers[space] = worker
		l.wg.Add(1)
		go l.runSpace(workerCtx, space, worker)
	}
}

func (l *backfillLoop) runSpace(ctx context.Context, spaceID string, worker *backfillSpaceWorker) {
	defer l.wg.Done()
	for {
		wait := l.nextInterval()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-worker.wake:
			timer.Stop()
		case <-timer.C:
		}
		l.step(ctx, spaceID)
	}
}

func (l *backfillLoop) nextInterval() time.Duration {
	if l.cfg.Stage != nil && l.cfg.Stage() == lifecycle.StageResuming {
		return l.cfg.ResumeInterval
	}
	return l.cfg.RunInterval
}

func (l *backfillLoop) step(ctx context.Context, spaceID string) {
	stage := lifecycle.StageRunning
	if l.cfg.Stage != nil {
		stage = l.cfg.Stage()
	}
	if stage == lifecycle.StagePaused {
		return
	}
	if l.cfg.ManualPaused != nil {
		paused, err := l.cfg.ManualPaused(ctx)
		if err != nil {
			log.WarnContextf(ctx, "collector backfill loop read manual pauses failed space=%s: %v", spaceID, err)
			return
		}
		if paused[strings.ToLower(spaceID)] {
			return
		}
	}
	switch stage {
	case lifecycle.StageResuming:
		if l.cfg.RunResuming != nil {
			l.cfg.RunResuming(ctx, spaceID)
		}
	case lifecycle.StageRunning:
		if l.cfg.RunRunning != nil {
			l.cfg.RunRunning(ctx, spaceID)
		}
	}
}
