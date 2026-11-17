package lifecycle

import (
	"context"
	"fmt"
	"time"
)

// quiesceTimeout 是暂停后等待在途操作结束的时限。
const quiesceTimeout = 30 * time.Second

// Request 是一个命令请求。
type Request struct {
	Command Command
	// PauseKind：pause 时是要设置的暂停类型（默认手动）；resume 时是只恢复该类型的暂停（空表示不过滤）。
	PauseKind PauseKind
	Reason    string
	// By 是操作者，写进暂停详情与审计。
	By string
	// ManualRelease 为真时追平后不自动放行，等 release（系统级恢复由编排器统一放行）。
	ManualRelease bool
	// ExpiresAt 用于 pause：到点自动恢复（进入恢复追赶，追平后放行）。只用于手动暂停；
	// 系统暂停要经编排器统一放行，不能由某个模块自己到点恢复。
	ExpiresAt time.Time
}

// Execute 执行命令请求并返回模块的新状态。
func (c *Controller) Execute(ctx context.Context, req Request) (State, error) {
	var (
		state State
		err   error
	)
	switch req.Command {
	case CmdPause:
		state, err = c.pause(req)
		if err == nil {
			state, err = c.quiesce(ctx, state)
		}
	case CmdResume:
		if state, err = c.resume(req); err == nil {
			c.settle(ctx)
			state = c.State()
		}
	case CmdRelease:
		return c.release(ctx)
	default:
		return c.State(), fmt.Errorf("未知命令 %q", req.Command)
	}
	if err == nil {
		c.audit("lifecycle_%s module=%s kind=%s by=%s reason=%q", req.Command, c.opts.Module, req.PauseKind, req.By, req.Reason)
	}
	return state, err
}

func (c *Controller) pause(req Request) (State, error) {
	kind := req.PauseKind
	if kind == "" {
		kind = PauseManual
	}
	c.mu.Lock()
	deny := func(why string) (State, error) {
		state := c.state()
		c.mu.Unlock()
		return state, &ErrNotAllowed{Command: CmdPause, Stage: state.Stage, Why: why}
	}
	if kind.rank() == 0 {
		return deny(fmt.Sprintf("未知的暂停类型 %q", kind))
	}
	info := &PauseInfo{Kind: kind, Reason: req.Reason, By: req.By, Since: c.opts.Now().UTC()}
	if !req.ExpiresAt.IsZero() {
		switch {
		case kind == PauseSystem:
			return deny("系统暂停不支持定时恢复：要经 system resume 先追赶再统一放行")
		case !req.ExpiresAt.After(info.Since):
			return deny("定时恢复的时间必须晚于现在")
		}
		info.ExpiresAt = req.ExpiresAt.UTC()
	}
	var changes []change
	reason := pauseReason(kind, req.Reason)
	switch c.rec.Stage {
	case StagePaused:
		// 已暂停：高优先级的暂停覆盖低优先级的（手动 > 系统），反过来不覆盖。
		if c.rec.Pause == nil || kind.rank() > c.rec.Pause.Kind.rank() {
			c.rec.Pause = info
			c.rec.Reason = reason
		}
	default:
		// 运行或恢复追赶中：回到暂停，暂停起点保持最早的那次。
		c.rec.Pause = info
		if c.rec.PausedSince.IsZero() {
			c.rec.PausedSince = info.Since
		}
		c.endCatchup()
		c.set(StagePaused, reason, &changes)
	}
	_ = c.save()
	state := c.state()
	c.mu.Unlock()
	c.notify(changes)
	return state, nil
}

// quiesce 在门禁关闭之后等在途操作结束，成功才写入收敛时间。
func (c *Controller) quiesce(ctx context.Context, state State) (State, error) {
	if c.opts.Hooks.Quiesce != nil {
		timeout := c.opts.QuiesceTimeout
		if timeout <= 0 {
			timeout = quiesceTimeout
		}
		qctx, cancel := context.WithTimeout(ctx, timeout)
		err := c.opts.Hooks.Quiesce(qctx)
		cancel()
		if err != nil {
			return state, fmt.Errorf("已暂停，但在途操作没有结束：%w", err)
		}
	}
	return c.markQuiesced(state), nil
}

// markQuiesced 写入收敛时间并返回新状态。等待是针对 state 所属的那一轮暂停做的：期间恢复过、又暂停过（新的一轮有新的
// 暂停详情）时不写，否则上一轮的等待结果会把还没收敛的下一轮误报为已收敛。
func (c *Controller) markQuiesced(state State) State {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.rec.Pause
	// 轮次按暂停开始的时间与类型认定：同一轮暂停里原因被改写不算新一轮。
	if c.rec.Stage != StagePaused || p == nil || state.Pause == nil || !p.Since.Equal(state.Pause.Since) || p.Kind != state.Pause.Kind {
		return c.state()
	}
	c.rec.QuiescedAt = c.opts.Now().UTC()
	_ = c.save()
	return c.state()
}

func (c *Controller) resume(req Request) (State, error) {
	c.mu.Lock()
	if c.rec.Stage != StagePaused {
		state := c.state()
		c.mu.Unlock()
		return state, &ErrNotAllowed{Command: CmdResume, Stage: state.Stage, Why: "只有暂停的模块才能恢复"}
	}
	var changes []change
	if req.PauseKind == "" || c.rec.Pause == nil || c.rec.Pause.Kind == req.PauseKind {
		// 只恢复指定类型的暂停：系统恢复不动手动暂停的模块。
		c.autoRelease = !req.ManualRelease
		c.catchup = nil
		c.set(StageResuming, "恢复追赶", &changes)
		_ = c.save()
	}
	state := c.state()
	c.mu.Unlock()
	c.notify(changes)
	return state, nil
}

func pauseReason(kind PauseKind, reason string) string {
	label := map[PauseKind]string{PauseManual: "手动暂停", PauseSystem: "系统暂停"}[kind]
	if reason == "" {
		return label
	}
	return label + "：" + reason
}

// endCatchup 清除恢复追赶的进程内状态；调用方持有锁。
func (c *Controller) endCatchup() {
	c.catchup = nil
	c.autoRelease = false
}

// settle 推进恢复追赶：调用 Catchup 钩子读取进度，追平且设为自动放行的回到运行；系统级恢复停在「已追平待放行」，等 release。
// 钩子在锁外调用，回写时按轮次（进入恢复追赶的时间与暂停详情）丢弃上一轮迟到的结果：不能用旧结果覆盖新一轮，更不能据此放行。
func (c *Controller) settle(ctx context.Context) {
	c.mu.Lock()
	if c.rec.Stage != StageResuming {
		c.mu.Unlock()
		return
	}
	since, round, pause := c.rec.PausedSince, c.rec.Since, c.rec.Pause
	c.mu.Unlock()

	cu := Catchup{CaughtUp: true}
	if c.opts.Hooks.Catchup != nil {
		var err error
		if cu, err = c.opts.Hooks.Catchup(ctx, since); err != nil {
			cu.CaughtUp = false
			cu.Message = "追赶进度读取失败：" + err.Error()
		}
	}
	cu.Since = since

	c.mu.Lock()
	var changes []change
	if c.rec.Stage == StageResuming && c.rec.Since.Equal(round) && c.rec.Pause == pause {
		c.catchup = &cu
		if c.autoRelease && cu.CaughtUp {
			c.finishResume(&changes)
		}
		_ = c.save()
	}
	c.mu.Unlock()
	c.notify(changes)
}

// finishResume 放行：回到运行并清掉恢复追赶状态；调用方持有锁。
func (c *Controller) finishResume(changes *[]change) {
	c.endCatchup()
	c.rec.Pause, c.rec.PausedSince = nil, time.Time{}
	c.rec.ReleasedAt = c.opts.Now().UTC()
	c.set(StageRunning, "恢复追赶完成，已放行", changes)
}

func (c *Controller) release(ctx context.Context) (State, error) {
	// 放行前再核一次：重新读取追赶进度。
	c.settle(ctx)
	c.mu.Lock()
	deny := func(why string) (State, error) {
		state := c.state()
		c.mu.Unlock()
		return state, &ErrNotAllowed{Command: CmdRelease, Stage: state.Stage, Why: why}
	}
	switch {
	case c.rec.Stage != StageResuming:
		return deny("只有恢复追赶中的模块才能放行")
	case c.catchup == nil || !c.catchup.CaughtUp:
		return deny("尚未追平：" + catchupText(c.catchup))
	}
	var changes []change
	c.finishResume(&changes)
	_ = c.save()
	state := c.state()
	c.mu.Unlock()
	c.notify(changes)
	return state, nil
}

func catchupText(cu *Catchup) string {
	switch {
	case cu == nil:
		return "还没有追赶进度"
	case cu.BlockedBy != "":
		return "被 " + cu.BlockedBy + " 阻塞" + messageSuffix(cu.Message)
	case cu.Total > 0:
		return fmt.Sprintf("%d/%d", cu.Done, cu.Total) + messageSuffix(cu.Message)
	case cu.Message != "":
		return cu.Message
	}
	return "追赶中"
}

func messageSuffix(message string) string {
	if message == "" {
		return ""
	}
	return "（" + message + "）"
}
