package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"trpc.group/trpc-go/trpc-go/log"
)

// Collector 的生命周期（设计 2、2.2、3.8）：只有模块级的暂停、恢复追赶与放行。
// 模块阶段唯一真相源在 lifecycle.json，不向数据库同步系统暂停列；空间是否暂停等于“手工暂停，或模块阶段不是运行”。
//
//	暂停      ：模块阶段进入 StagePaused（对账器停用全部 SCF 定时器、collector-stream 下发空计划、补采循环停跑），
//	           并等定时器停用确认、在途补采分块结束才算停下；
//	恢复追赶  ：模块阶段进入 StageResuming——独立补采循环以更短间隔只推进补采作业（手工暂停的空间不补采），
//	           实时定时器与推送订阅保持关闭；
//	放行      ：暂停区间补齐后回到 StageRunning，对账器重新启用未手工暂停空间的定时器，恢复实时采集与常规补采。
//
// 恢复模式只在进程内存里：重启后回到暂停，再 resume 即可。

const (
	collectorLifecycleRefresh = 30 * time.Second
	recoveryTickEvery         = 20 * time.Second
	collectorQuiesceTimeout   = 5 * time.Minute
)

// collectorControl 把生命周期钩子和 registerFetchPlanManager 里建好的组件连起来：生命周期在启动早期先创建，
// 对账管理器与补采循环就绪后由 registerFetchPlanManager 调用 bind 填入。
type collectorControl struct {
	mu           sync.Mutex
	plans        *store.FetchPlanRepository
	backfiller   *klinecheck.Backfiller
	manager      *fetchplan.PlanManager
	wakeBackfill func()
	// spaces 返回管理集合，每次现取：运行中新增的空间也要能暂停、恢复和收敛。known 是最近一次读到的结果，读库失败时沿用。
	spaces func(context.Context) ([]string, error)
	known  []string
	// recoveryTo 是本次恢复的追赶目标：进入恢复追赶的时刻，固定不变（见 Backfiller.Catchup）。
	recoveryTo time.Time
}

func (c *collectorControl) bind(plans *store.FetchPlanRepository, backfiller *klinecheck.Backfiller, manager *fetchplan.PlanManager, spaces func(context.Context) ([]string, error), wakeBackfill func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plans, c.backfiller, c.manager, c.spaces, c.wakeBackfill = plans, backfiller, manager, spaces, wakeBackfill
}

// managed 返回管理集合；读库失败时沿用最近一次的结果。
func (c *collectorControl) managed(ctx context.Context) []string {
	c.mu.Lock()
	read, known := c.spaces, append([]string(nil), c.known...)
	c.mu.Unlock()
	if read == nil {
		return known
	}
	spaces, err := read(ctx)
	if err != nil {
		log.WarnContextf(ctx, "collector_managed_spaces_failed error=%v", err)
		return known
	}
	c.mu.Lock()
	c.known = append([]string(nil), spaces...)
	c.mu.Unlock()
	return spaces
}

// recoverySpaces 返回恢复追赶要补采的空间：管理集合去掉手工暂停的。手工暂停的空间不补采、不计入追赶进度，
// 否则它们的缺口永远补不上，会卡住整个系统恢复。
func (c *collectorControl) recoverySpaces(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	plans := c.plans
	c.mu.Unlock()
	spaces := c.managed(ctx)
	if plans == nil {
		return spaces, nil
	}
	manual, err := plans.PausedSpaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取手工暂停的空间：%w", err)
	}
	out := make([]string, 0, len(spaces))
	for _, space := range spaces {
		if !manual[strings.ToLower(space)] {
			out = append(out, space)
		}
	}
	return out, nil
}

func (c *collectorControl) setRecoveryTo(at time.Time) {
	c.mu.Lock()
	c.recoveryTo = at
	c.mu.Unlock()
}

func (c *collectorControl) triggerManager(ctx context.Context) {
	c.mu.Lock()
	manager := c.manager
	c.mu.Unlock()
	if manager == nil {
		return
	}
	spaces := c.managed(ctx)
	if len(spaces) == 0 {
		manager.Trigger("")
		return
	}
	for _, space := range spaces {
		manager.Trigger(space)
	}
}

func (c *collectorControl) notifyBackfill() {
	c.mu.Lock()
	wake := c.wakeBackfill
	c.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// onChange 在模块状态变化后通知对账器与补采循环立即同步，不再往数据库写系统暂停列。
func (c *collectorControl) onChange(ctx context.Context, from, to lifecycle.Stage) {
	switch to {
	case lifecycle.StagePaused:
		c.setRecoveryTo(time.Time{})
		c.triggerManager(ctx)
		c.notifyBackfill()
	case lifecycle.StageResuming:
		c.setRecoveryTo(time.Now().UTC())
		c.triggerManager(ctx)
		c.notifyBackfill()
	case lifecycle.StageRunning:
		if from == lifecycle.StageResuming || from == lifecycle.StagePaused {
			c.setRecoveryTo(time.Time{})
			c.triggerManager(ctx)
			c.notifyBackfill()
		}
	}
}

// quiesce 等暂停真正生效：每个空间的定时器停用已确认、在途的补采分块已结束。
func (c *collectorControl) quiesce(ctx context.Context) error {
	c.mu.Lock()
	backfiller, manager := c.backfiller, c.manager
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
		for _, space := range c.managed(ctx) {
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
	backfiller, to := c.backfiller, c.recoveryTo
	c.mu.Unlock()
	out := lifecycle.Catchup{CaughtUp: true, Target: to}
	if backfiller == nil {
		return out, nil
	}
	spaces, err := c.recoverySpaces(ctx)
	if err != nil {
		return lifecycle.Catchup{Message: err.Error()}, err
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

func newCollectorLifecycle(dbPath string, control *collectorControl) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:          logsink.Audit,
		Module:         "collector",
		StateFile:      lifecycle.StateFileForDB(dbPath),
		QuiesceTimeout: collectorQuiesceTimeout,
		Hooks: lifecycle.Hooks{
			Quiesce: control.quiesce,
			Catchup: control.catchup,
			OnChange: func(from, to lifecycle.Stage, reason string) {
				log.Infof("collector_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
				control.onChange(context.Background(), from, to)
			},
		},
	})
}
