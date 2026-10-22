package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Collector 的生命周期（设计 2、2.2）：模块级，没有对象。
//
//	暂停      ：每个采集计划空间都置为暂停（SCF 定时器停用、collector-stream 停止订阅、不再认领补采分块），
//	           并等定时器停用确认、在途补采分块结束才算停下；
//	恢复追赶  ：空间进入「恢复模式」——补采调度运行（对暂停区间提交补采作业），实时定时器与订阅保持关闭；
//	放行      ：暂停区间补齐后清除暂停开关，对账协程重新启用定时器，恢复实时采集。
//
// 恢复模式只在进程内存里：重启后回到暂停，再 resume 即可。

const (
	collectorLifecycleStateFile = "lifecycle.json"
	collectorLifecycleRefresh   = 30 * time.Second
	recoveryTickEvery           = 20 * time.Second
	collectorQuiesceTimeout     = 5 * time.Minute
)

// collectorControl 把生命周期钩子和 registerFetchPlanManager 里建好的组件连起来：生命周期在健康检查之后才创建，
// 采集计划管理器先于它存在，所以由后者调用 bind 填入。
type collectorControl struct {
	mu         sync.Mutex
	plans      *store.FetchPlanRepository
	backfiller *klinecheck.Backfiller
	manager    *marketfetch.PlanManager
	spaces     []string
	recovering map[string]bool
	// recoveryTo 是本次恢复的追赶目标：进入恢复追赶的时刻，固定不变（见 Backfiller.Catchup）。
	recoveryTo time.Time
}

func (c *collectorControl) bind(plans *store.FetchPlanRepository, backfiller *klinecheck.Backfiller, manager *marketfetch.PlanManager, spaces []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plans, c.backfiller, c.manager, c.spaces = plans, backfiller, manager, append([]string(nil), spaces...)
}

// Recovering 报告空间是否处于恢复模式：暂停开关仍在（实时采集关闭），但补采调度要运行。
func (c *collectorControl) Recovering(space string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recovering[strings.ToLower(space)]
}

func (c *collectorControl) setRecovering(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.recoveryTo = time.Now().UTC()
		c.recovering = map[string]bool{}
		for _, space := range c.spaces {
			c.recovering[strings.ToLower(space)] = true
		}
		return
	}
	c.recovering, c.recoveryTo = nil, time.Time{}
}

func (c *collectorControl) snapshot() (*store.FetchPlanRepository, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.plans, append([]string(nil), c.spaces...)
}

// setPaused 保存全部采集计划空间的暂停开关。
func (c *collectorControl) setPaused(ctx context.Context, paused bool) error {
	plans, spaces := c.snapshot()
	if plans == nil {
		return nil
	}
	for _, space := range spaces {
		if err := plans.SetPaused(ctx, space, paused); err != nil {
			return fmt.Errorf("保存空间 %s 的暂停开关：%w", space, err)
		}
	}
	return nil
}

// onChange 把模块阶段的变化落到采集上：进入暂停时关实时、清恢复模式；进入恢复追赶时开恢复模式；放行回到就绪时清暂停开关。
func (c *collectorControl) onChange(ctx context.Context, from, to lifecycle.Stage) {
	switch to {
	case lifecycle.StagePaused:
		c.setRecovering(false)
		if err := c.setPaused(ctx, true); err != nil {
			log.ErrorContextf(ctx, "collector_lifecycle_pause_failed error=%v", err)
		}
	case lifecycle.StageResuming:
		c.setRecovering(true)
		if err := c.setPaused(ctx, true); err != nil {
			log.ErrorContextf(ctx, "collector_lifecycle_resume_failed error=%v", err)
		}
	default:
		if from == lifecycle.StageResuming || from == lifecycle.StagePaused {
			c.setRecovering(false)
			if err := c.setPaused(ctx, false); err != nil {
				log.ErrorContextf(ctx, "collector_lifecycle_release_failed error=%v", err)
			}
		}
	}
}

// quiesce 等暂停真正生效：每个空间的定时器停用已确认、在途的补采分块已结束。
func (c *collectorControl) quiesce(ctx context.Context) error {
	c.mu.Lock()
	backfiller, manager, spaces := c.backfiller, c.manager, append([]string(nil), c.spaces...)
	c.mu.Unlock()
	if backfiller == nil || manager == nil {
		return nil
	}
	if err := backfiller.Quiesce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var waiting []string
		for _, space := range spaces {
			if !manager.TimersDisabled(space) {
				waiting = append(waiting, space)
			}
		}
		if len(waiting) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("空间 %s 的 SCF 定时器还没有确认停用：%w", strings.Join(waiting, "、"), ctx.Err())
		case <-ticker.C:
		}
	}
}

// catchup 汇总各空间暂停区间的补采进度。
func (c *collectorControl) catchup(ctx context.Context, since time.Time) (lifecycle.Catchup, error) {
	c.mu.Lock()
	backfiller, spaces, to := c.backfiller, append([]string(nil), c.spaces...), c.recoveryTo
	c.mu.Unlock()
	out := lifecycle.Catchup{CaughtUp: true, Target: to}
	if backfiller == nil {
		return out, nil
	}
	var expected, missing int64
	for _, space := range spaces {
		state, err := backfiller.Catchup(ctx, space, since, to)
		if err != nil {
			return lifecycle.Catchup{Message: fmt.Sprintf("空间 %s：%v", space, err)}, err
		}
		expected += int64(state.Expected)
		missing += int64(state.Missing)
		out.Unfillable += state.Unfillable
		if !state.CaughtUp() {
			out.CaughtUp = false
		}
	}
	out.Total, out.Done = expected, expected-missing
	if !out.CaughtUp && missing == 0 {
		out.Message = "补采作业收尾中"
	}
	return out, nil
}

// runRecovery 在恢复模式下推进补采：对账协程在暂停期间已经退出，补采的认领要在这里单独驱动。
func (c *collectorControl) runRecovery(ctx context.Context) {
	ticker := time.NewTicker(recoveryTickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		backfiller, spaces := c.backfiller, append([]string(nil), c.spaces...)
		c.mu.Unlock()
		if backfiller == nil {
			continue
		}
		for _, space := range spaces {
			if !c.Recovering(space) {
				continue
			}
			if err := backfiller.Tick(ctx, space); err != nil {
				log.WarnContextf(ctx, "collector_recovery_tick_failed space=%s error=%v", space, err)
			}
		}
	}
}

func newCollectorLifecycle(dbPath string, control *collectorControl, ready func() bool) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Module:         "collector",
		StateFile:      filepath.Join(filepath.Dir(dbPath), collectorLifecycleStateFile),
		QuiesceTimeout: collectorQuiesceTimeout,
		Hooks: lifecycle.Hooks{
			CheckModule: func(context.Context) []lifecycle.Check {
				if ready != nil && !ready() {
					return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckFail, Message: "Collector 数据库不可用"}}
				}
				return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckPass}}
			},
			Quiesce: func(ctx context.Context, _ lifecycle.Ref) error { return control.quiesce(ctx) },
			Catchup: func(ctx context.Context, ref lifecycle.Ref, since time.Time) (lifecycle.Catchup, error) {
				if ref != (lifecycle.Ref{}) {
					return lifecycle.Catchup{CaughtUp: true}, nil
				}
				return control.catchup(ctx, since)
			},
			OnChange: func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("collector_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
				control.onChange(context.Background(), from, to)
			},
		},
	})
}

func collectorLifecycleRoutes(c *lifecycle.Controller) []health.Route {
	return []health.Route{
		{Path: lifecycle.StatusPath, Handler: c.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: c.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: c.CatchupHandler()},
	}
}

// withCollectorLifecycle 把生命周期快照放进健康检查响应，生命周期的检查本身不改变就绪判定。
func withCollectorLifecycle(snapshot healthz.SnapshotFunc, c *lifecycle.Controller) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		rsp := snapshot(ctx)
		if rsp.Details == nil {
			rsp.Details = map[string]any{}
		}
		rsp.Details["lifecycle"] = c.Compact()
		return rsp
	}
}
