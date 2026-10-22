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
	// Ref 为空表示模块自身。
	Ref Ref
	// PauseKind：pause 时是要设置的暂停类型（默认手动）；resume 时是只恢复该类型的暂停（空表示不过滤）。
	PauseKind PauseKind
	Reason    string
	// By 是操作者，写进暂停详情。
	By string
	// Force 用于模块暂停期间对单个对象执行 resume（紧急情况）：只放行该对象的追赶，不放开整个模块。
	Force bool
	// ManualRelease 为真时追平后不自动放行，等 release（系统级恢复由编排器统一放行）。
	ManualRelease bool
}

// Command 执行一个用户命令并返回对象的新状态。ref 为空表示模块自身。
func (c *Controller) Command(ctx context.Context, cmd Command, ref Ref) (Object, error) {
	return c.Execute(ctx, Request{Command: cmd, Ref: ref})
}

// Execute 执行命令请求。
func (c *Controller) Execute(ctx context.Context, req Request) (Object, error) {
	switch req.Command {
	case CmdInit:
		return c.initCmd(ctx, req.Ref)
	case CmdCheck:
		return c.checkCmd(ctx, req.Ref)
	case CmdStart, CmdConfirm, CmdPause, CmdResume:
		obj, err := c.transition(ctx, req)
		switch {
		case err == nil && req.Command == CmdResume:
			c.settle(ctx)
			c.mu.Lock()
			obj = c.object(c.recordFor(req.Ref))
			c.mu.Unlock()
		case err == nil && req.Command == CmdPause && obj.Stage == StagePaused && c.opts.Hooks.Quiesce != nil:
			// 门禁已经关闭；等在途操作结束才算真正停下。
			qctx, cancel := context.WithTimeout(ctx, quiesceTimeout)
			qerr := c.opts.Hooks.Quiesce(qctx, req.Ref)
			cancel()
			if qerr != nil {
				return obj, fmt.Errorf("已暂停，但在途操作没有结束：%w", qerr)
			}
		}
		return obj, err
	case CmdRelease:
		return c.release(ctx, req.Ref)
	}
	return Object{}, fmt.Errorf("未知命令 %q", req.Command)
}

func (c *Controller) initCmd(ctx context.Context, ref Ref) (Object, error) {
	if c.opts.Hooks.Init != nil {
		if err := c.opts.Hooks.Init(ctx, ref); err != nil {
			return Object{}, fmt.Errorf("初始化失败：%w", err)
		}
	}
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	if ref == (Ref{}) && r.Hold == HoldPostReset {
		// 重置后的初始化由用户发起，到这里才解除「等待初始化」。
		r.Hold = HoldNone
	}
	if r.Stage == StageUninitialized || r.Stage == StageNotReady || r.Stage == StageReady {
		if Passed(checks) {
			c.set(r, StageReady, "", &changes)
		} else {
			c.set(r, StageNotReady, firstFailure(checks), &changes)
		}
	}
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

func (c *Controller) checkCmd(ctx context.Context, ref Ref) (Object, error) {
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	c.apply(r, checks, &changes)
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

func (c *Controller) markPausedSince(r *record) {
	if r.PausedSince.IsZero() {
		r.PausedSince = c.opts.Now().UTC()
	}
}

// beginResume 让暂停（或故障）的记录进入恢复追赶；调用方持有锁。
// 模块自身进入恢复追赶时，暂停前处于预热、运行的对象成为本次恢复的参与者（手动暂停的对象继续排除）。
func (c *Controller) beginResume(r *record, autoRelease bool, changes *[]change) {
	if r.Resume == "" {
		r.Resume = StageReady
	}
	c.markPausedSince(r)
	r.autoRelease = autoRelease
	r.catchup = nil
	if r.Ref == (Ref{}) {
		for _, o := range c.objects {
			o.catchupMember = o.Stage.Active()
			o.catchup = nil
		}
	} else {
		r.catchupMember = true
	}
	c.set(r, StageResuming, "恢复追赶", changes)
}

func (c *Controller) transition(ctx context.Context, req Request) (Object, error) {
	ref, cmd := req.Ref, req.Command
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	deny := func(why string) (Object, error) {
		obj := c.object(r)
		c.mu.Unlock()
		return obj, &ErrNotAllowed{Ref: ref, Command: cmd, Stage: r.Stage, Why: why}
	}
	switch cmd {
	case CmdStart:
		if r.Stage != StageReady {
			return deny("只有就绪的对象才能启动；先 init 并让检查通过")
		}
		if !Passed(checks) {
			return deny("检查有失败项：" + firstFailure(checks))
		}
		c.set(r, StageWarming, "", &changes)
	case CmdConfirm:
		if r.Stage != StageWarming {
			return deny("只有预热中的对象才能确认进入运行")
		}
		if !Passed(checks) {
			return deny("检查有失败项：" + firstFailure(checks))
		}
		c.set(r, StageRunning, "", &changes)
	case CmdPause:
		kind := req.PauseKind
		if kind == "" {
			kind = PauseManual
		}
		if kind.rank() == 0 {
			return deny(fmt.Sprintf("未知的暂停类型 %q", kind))
		}
		info := &PauseInfo{Kind: kind, Reason: req.Reason, By: req.By, Since: c.opts.Now().UTC()}
		reason := pauseReason(kind, req.Reason)
		switch {
		case r.Stage == StagePaused:
			// 已暂停：高优先级的暂停覆盖低优先级的（手动 > 系统 > 自动），反过来不覆盖。
			if r.Pause == nil || kind.rank() > r.Pause.Kind.rank() {
				r.Pause = info
				r.Reason = reason
			}
		case r.Stage == StageResuming:
			// 恢复追赶中再暂停：回到暂停，暂停起点保持最早的那次。
			r.Pause = info
			c.endCatchup(r)
			c.set(r, StagePaused, reason, &changes)
		case r.Stage == StageReady || r.Stage.Active():
			r.Resume = r.Stage
			r.Pause = info
			c.markPausedSince(r)
			c.set(r, StagePaused, reason, &changes)
		case ref == (Ref{}) && kind == PauseSystem && r.Stage != StageResetting && r.Stage != StageResetFailed:
			// 模块自身未就绪时也要能建立系统暂停的门禁（只靠 hold）。
		default:
			return deny("只有就绪、预热、运行或恢复追赶中的对象才能暂停")
		}
		if ref == (Ref{}) && kind == PauseSystem && (r.Pause == nil || r.Pause.Kind == PauseSystem) {
			r.Hold = HoldSystemPause
		}
	case CmdResume:
		if ref == (Ref{}) && r.Hold == HoldSystemPause && r.Stage != StagePaused && r.Stage != StageFault {
			// 只靠 hold 建立的系统暂停（模块自身当时未就绪）：解除 hold 即可，阶段由检查重新推导。
			if req.PauseKind == "" || req.PauseKind == PauseSystem {
				r.Hold = HoldNone
			}
			break
		}
		if r.Stage != StagePaused && r.Stage != StageFault {
			return deny("只有暂停或故障的对象才能恢复")
		}
		if r.Stage == StagePaused && req.PauseKind != "" && r.Pause != nil && r.Pause.Kind != req.PauseKind {
			// 只恢复指定类型的暂停（系统恢复不动手动暂停的对象）。
			break
		}
		if ref != (Ref{}) && !req.Force && (c.self.Stage == StagePaused || c.self.Hold == HoldSystemPause) {
			return deny("模块暂停中，先恢复模块；紧急情况下加 --force 只放行这个对象的追赶")
		}
		if !Passed(checks) {
			return deny("检查有失败项：" + firstFailure(checks))
		}
		c.beginResume(r, !req.ManualRelease, &changes)
	}
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

func pauseReason(kind PauseKind, reason string) string {
	label := map[PauseKind]string{PauseManual: "手动暂停", PauseSystem: "系统暂停", PauseAuto: "自动暂停"}[kind]
	if reason == "" {
		return label
	}
	return label + "：" + reason
}

// endCatchup 清除恢复追赶的进程内状态；调用方持有锁。
func (c *Controller) endCatchup(r *record) {
	r.catchup = nil
	r.autoRelease = false
	r.catchupMember = false
	if r.Ref == (Ref{}) {
		for _, o := range c.objects {
			o.catchupMember = false
			if o.Stage != StageResuming {
				o.catchup = nil
			}
		}
	}
}

// settle 推进恢复追赶：对恢复追赶中的记录（和模块恢复追赶时参与追赶的对象）调用 Catchup 钩子，
// 追平且检查通过的、设为自动放行的记录回到暂停前的阶段；系统级恢复的记录停在「已追平待放行」，等 release。
func (c *Controller) settle(ctx context.Context) {
	c.mu.Lock()
	var refs []Ref
	if c.self.Stage == StageResuming {
		refs = append(refs, Ref{})
	}
	for _, o := range c.objects {
		if o.Stage == StageResuming || (c.self.Stage == StageResuming && o.catchupMember) {
			refs = append(refs, o.Ref)
		}
	}
	since := map[string]time.Time{}
	for _, ref := range refs {
		since[ref.key()] = c.recordFor(ref).PausedSince
	}
	c.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	type result struct {
		checks  []Check
		catchup Catchup
		err     error
	}
	results := make(map[string]result, len(refs))
	for _, ref := range refs {
		res := result{checks: c.runChecks(ctx, ref), catchup: Catchup{CaughtUp: true}}
		if c.opts.Hooks.Catchup != nil {
			res.catchup, res.err = c.opts.Hooks.Catchup(ctx, ref, since[ref.key()])
		}
		results[ref.key()] = res
	}

	c.mu.Lock()
	var changes []change
	for _, ref := range refs {
		r := c.recordFor(ref)
		res := results[ref.key()]
		cu := res.catchup
		cu.Since = r.PausedSince
		if res.err != nil {
			cu.CaughtUp = false
			cu.Message = "追赶进度读取失败：" + res.err.Error()
		}
		r.catchup = &cu
		c.checks[ref.key()] = res.checks
	}
	// 模块自身：自己和所有参与追赶的对象都追平，并且自身检查没有失败项，才算追平。
	if c.self.Stage == StageResuming && c.self.catchup != nil {
		self := c.self
		caught := self.catchup.CaughtUp && Passed(c.checks[self.key()])
		for _, o := range c.objects {
			if o.catchupMember && (o.catchup == nil || !o.catchup.CaughtUp) {
				caught = false
				if self.catchup.BlockedBy == "" {
					self.catchup.BlockedBy = o.Ref.key()
				}
			}
		}
		self.catchup.CaughtUp = caught
	}
	for _, ref := range refs {
		r := c.recordFor(ref)
		if r.Stage != StageResuming || !r.autoRelease || r.catchup == nil || !r.catchup.CaughtUp {
			continue
		}
		if !Passed(c.checks[ref.key()]) {
			continue
		}
		c.finishResume(r, &changes)
	}
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
}

// finishResume 放行：回到暂停前的阶段并清掉恢复追赶状态；调用方持有锁。
func (c *Controller) finishResume(r *record, changes *[]change) {
	back := r.Resume
	if back == "" {
		back = StageReady
	}
	c.endCatchup(r)
	r.Pause, r.PausedSince, r.Resume = nil, time.Time{}, ""
	if r.Ref == (Ref{}) {
		r.Hold = HoldNone
	}
	c.set(r, back, "恢复追赶完成，已放行", changes)
}

func (c *Controller) release(ctx context.Context, ref Ref) (Object, error) {
	// 放行前再核一次：重新检查并重新读取追赶进度。
	c.settle(ctx)
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	deny := func(why string) (Object, error) {
		obj := c.object(r)
		c.mu.Unlock()
		return obj, &ErrNotAllowed{Ref: ref, Command: CmdRelease, Stage: r.Stage, Why: why}
	}
	switch {
	case r.Stage != StageResuming:
		return deny("只有恢复追赶中的对象才能放行")
	case r.catchup == nil || !r.catchup.CaughtUp:
		return deny("尚未追平：" + catchupText(r.catchup))
	case !Passed(checks):
		return deny("检查有失败项：" + firstFailure(checks))
	}
	c.finishResume(r, &changes)
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

func catchupText(cu *Catchup) string {
	switch {
	case cu == nil:
		return "还没有追赶进度"
	case cu.BlockedBy != "":
		return "被 " + cu.BlockedBy + " 阻塞"
	case cu.Message != "":
		return cu.Message
	case cu.Total > 0:
		return fmt.Sprintf("%d/%d", cu.Done, cu.Total)
	}
	return "追赶中"
}

// Fault 报告对象运行中出现自身无法恢复的错误：进入故障，要用户 resume。
func (c *Controller) Fault(ref Ref, err error) {
	c.mu.Lock()
	r := c.recordFor(ref)
	var changes []change
	if r.Stage.Active() {
		r.Resume = r.Stage
	}
	c.markPausedSince(r)
	c.set(r, StageFault, err.Error(), &changes)
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
}
