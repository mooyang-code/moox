// Package warmup 是策略池启用后的预热（设计 3.5）：后台从启用时刻往前按真实历史依次求值所有成员、推进成员模拟账本，一直推进到
// 当时最新已收盘的一期，把最终状态与“预热到哪一期”写进会话。预热完成之前，触发器对这个池实例返回等待（事件稍后重投，不丢期），
// 也不允许确认进入运行阶段。
package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/replay"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// Status 是一个池实例的预热状态。
type Status int

const (
	// StatusNone 表示没有在预热（不是池实例，或已经完成）。
	StatusNone Status = iota
	// StatusRunning 表示预热进行中。
	StatusRunning
	// StatusFailed 表示预热失败（永久性错误，或暂时性错误重试用尽）：实例标 degraded，需要修复后重新启用。
	StatusFailed
)

const (
	defaultGrace       = time.Minute
	defaultRetryDelay  = 5 * time.Second
	maxRetryDelay      = time.Minute
	defaultMaxAttempts = 6
	// abortCheckEvery 是每推进多少期核对一次会话是否仍然有效。
	abortCheckEvery = 20
)

// Manager 管理策略池实例的预热任务：每个实例同时最多一个。
type Manager struct {
	Store *store.Store
	// Client 是回放用的客户端（按较大的页分页读取较长的区间）。
	Client input.Client
	Now    func() time.Time
	Logf   func(format string, args ...any)
	// Grace 是预热终点的宽限：最新一根刚收盘时因子结果还在路上，不把它算进预热终点；零值取一分钟。
	Grace     time.Duration
	ChunkBars int
	// RetryDelay 是暂时性错误重试的初始间隔（逐次翻倍，上限一分钟）；MaxAttempts 是尝试次数上限。零值取默认值。
	RetryDelay  time.Duration
	MaxAttempts int

	mu     sync.Mutex
	jobs   map[string]*job
	wg     sync.WaitGroup
	closed bool
}

type job struct {
	sessionID string
	cancel    context.CancelFunc
	status    Status
	reason    string
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

// Start 开始（或重新开始）实例当前会话的预热；同一会话已在预热时什么也不做，换了会话时取消旧的任务。
func (m *Manager) Start(ctx context.Context, instanceID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.jobs == nil {
		m.jobs = make(map[string]*job)
	}
	if current := m.jobs[instanceID]; current != nil {
		if current.sessionID == sessionID && current.status == StatusRunning {
			return
		}
		current.cancel()
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	entry := &job{sessionID: sessionID, cancel: cancel, status: StatusRunning}
	m.jobs[instanceID] = entry
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(runCtx, instanceID, entry)
	}()
}

// Ensure 只在实例没有任何预热任务（不在预热，也没有失败）时才开始预热：处理器发现池实例没有预热结果又没人预热时用它补启动，
// 不会让已经失败的预热在每条事件到达时重来一遍。
func (m *Manager) Ensure(instanceID, sessionID string) {
	if status, _ := m.State(instanceID); status != StatusNone {
		return
	}
	m.Start(context.Background(), instanceID, sessionID)
	m.logf("实例 %s 的策略池会话 %s 没有预热任务，已补启动", instanceID, sessionID)
}

// Stop 取消实例的预热（停用实例时调用）；没有任务时什么也不做。
func (m *Manager) Stop(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.jobs[instanceID]; current != nil {
		current.cancel()
		delete(m.jobs, instanceID)
	}
}

// State 返回实例的预热状态与说明（失败时是失败原因）。
func (m *Manager) State(instanceID string) (Status, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.jobs[instanceID]; current != nil {
		return current.status, current.reason
	}
	return StatusNone, ""
}

// Blocked 报告实例是否不能确认进入运行阶段，并给出拒绝的说明；预热完成或不是池实例时返回空串。
func (m *Manager) Blocked(instanceID string) string {
	switch status, reason := m.State(instanceID); status {
	case StatusRunning:
		return "策略池预热未完成"
	case StatusFailed:
		return "策略池预热失败，请修复后重新启用实例：" + reason
	}
	return ""
}

// Resume 在进程启动时为会话里还没有预热结果的池实例重新开始预热：预热在进程中途重启后从头来过。
func (m *Manager) Resume(ctx context.Context) error {
	sessions, err := m.Store.PendingWarmSessions(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		m.Start(ctx, session.InstanceID, session.SessionID)
	}
	if len(sessions) > 0 {
		m.logf("进程启动：%d 个策略池实例重新开始预热", len(sessions))
	}
	return nil
}

// Close 取消全部预热任务并等它们退出。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, current := range m.jobs {
		current.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) run(ctx context.Context, instanceID string, entry *job) {
	attempts, delay := m.MaxAttempts, m.RetryDelay
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	if delay <= 0 {
		delay = defaultRetryDelay
	}
	for attempt := 1; ; attempt++ {
		done, err := m.warm(ctx, instanceID, entry.sessionID)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if done {
				m.finish(instanceID, entry)
			}
			return
		}
		if permanent(err) || attempt >= attempts {
			m.fail(ctx, instanceID, entry, err)
			return
		}
		m.logf("实例 %s 的策略池预热失败（第 %d 次），%s 后重试：%v", instanceID, attempt, delay, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// finish 把预热任务标为完成：状态从内存里清掉，门禁放行。
func (m *Manager) finish(instanceID string, entry *job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[instanceID] == entry {
		delete(m.jobs, instanceID)
	}
}

// fail 把预热任务标为失败并把实例标记为 degraded：失败原因留在实例上，需要人工修复（补数据、改配置）后重新启用。
func (m *Manager) fail(ctx context.Context, instanceID string, entry *job, err error) {
	m.logf("实例 %s 的策略池预热失败，不再重试：%v；原始错误：%v", instanceID, err, input.RawCause(err))
	m.mu.Lock()
	entry.status, entry.reason = StatusFailed, err.Error()
	m.mu.Unlock()
	if markErr := m.Store.MarkInstanceDegraded(ctx, instanceID, entry.sessionID, m.now()); markErr != nil {
		m.logf("实例 %s 标记 degraded 失败：%v", instanceID, markErr)
	}
}

// permanent 报告错误是永久性的：重试不会成功，要人工修复。
func permanent(err error) bool {
	var early *input.EarlyStartError
	var skip *input.SkipError
	return errors.As(err, &early) || errors.As(err, &skip) || errors.Is(err, input.ErrViewNotFound) || errors.Is(err, errPermanent)
}

var errPermanent = errors.New("预热的配置无效")

// warm 执行一次预热。done 为真表示这个会话的预热已经结束（写入了结果，或会话已被停用、已经预热过，没有什么可做的）。
func (m *Manager) warm(ctx context.Context, instanceID, sessionID string) (done bool, err error) {
	session, err := m.Store.GetSession(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("读取会话：%w", err)
	}
	if session.ClosedAt != nil || session.WarmedThrough != nil {
		return true, nil
	}
	current, err := m.Store.GetInstance(ctx, instanceID)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取实例：%w", err)
	}
	if !current.Enabled || current.SessionID == nil || *current.SessionID != sessionID {
		return true, nil
	}
	resolved, err := input.ParseResolved([]byte(session.ResolvedJSON))
	if err != nil {
		return false, fmt.Errorf("%w：%v", errPermanent, err)
	}
	members, programs, err := input.ParseMembers(session.MembersJSON)
	if err != nil {
		return false, fmt.Errorf("%w：%v", errPermanent, err)
	}
	version, err := m.Store.GetDefinitionVersion(ctx, session.DSLHash)
	if err != nil {
		return false, fmt.Errorf("读取 DSL 版本：%w", err)
	}
	strategy, err := dsl.Parse([]byte(version.DSLYaml))
	if err != nil {
		return false, fmt.Errorf("%w：%v", errPermanent, err)
	}
	cfg, err := pool.NewConfig(strategy, resolved.Market)
	if err != nil {
		return false, fmt.Errorf("%w：%v", errPermanent, err)
	}
	started := m.now()
	start, end, err := Window(resolved, strategy, programs, started, m.grace())
	if err != nil {
		return false, fmt.Errorf("%w：%v", errPermanent, err)
	}
	checked := 0
	run := replay.PoolRun{
		Client: m.Client, SpaceID: current.SpaceID, Resolved: resolved, Members: members, Programs: programs, Config: cfg,
		Start: start, End: end, IncludeLatest: true, Now: m.Now, ChunkBars: m.ChunkBars, Logf: m.Logf,
		Abort: func(ctx context.Context) error {
			checked++
			if checked%abortCheckEvery != 0 {
				return ctx.Err()
			}
			return m.stillCurrent(ctx, instanceID, sessionID)
		},
	}
	result, err := replay.RunPool(ctx, run)
	if err != nil {
		if errors.Is(err, errStale) {
			return true, nil
		}
		return false, err
	}
	raw, err := json.Marshal(result.State)
	if err != nil {
		return false, err
	}
	written, err := m.Store.SetSessionWarmed(ctx, sessionID, string(raw), result.Through)
	if err != nil {
		return false, fmt.Errorf("写入预热结果：%w", err)
	}
	if written {
		m.logf("实例 %s 的策略池预热完成：推进 %d 期到 %s，向 Storage 读取 %d 次，耗时 %s", instanceID, result.Bars, result.Through.Format(time.RFC3339), result.Reads, m.now().Sub(started).Round(time.Millisecond))
	}
	return true, nil
}

var errStale = errors.New("会话已不再有效")

// stillCurrent 确认实例仍以这个会话启用：停用或重新启用后，旧会话的预热没有意义，尽早放弃。
func (m *Manager) stillCurrent(ctx context.Context, instanceID, sessionID string) error {
	current, err := m.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.SessionID == nil || *current.SessionID != sessionID {
		return errStale
	}
	return nil
}

func (m *Manager) grace() time.Duration {
	if m.Grace > 0 {
		return m.Grace
	}
	return defaultGrace
}

// WarmBars 是预热需要推进的期数（见 dsl.PoolWarmBars）。
func WarmBars(pool dsl.Strategy, members []*dsl.Program) int {
	strategies := make([]dsl.Strategy, 0, len(members))
	for _, program := range members {
		strategies = append(strategies, program.Strategy)
	}
	return dsl.PoolWarmBars(pool, strategies)
}

// Window 返回预热的区间 [start, end)（bar_start）：以 now 之前（再留 grace 宽限）最新已收盘的一期为最后一期，往前共 WarmBars 期。
// 最后一期实际有没有数据由 replay.RunPool 按 View 的覆盖范围截断（View 里还没有的期不推进）。该期的就绪事件在启用之前就已发出，
// 预热不推进它就没有别的路径会处理它，所以终点包含已闭合的最新一根（宽限与追赶判断的口径一致：收盘超过宽限即视为有数据）。
func Window(resolved input.Resolved, pool dsl.Strategy, members []*dsl.Program, now time.Time, grace time.Duration) (start, end time.Time, err error) {
	closed, err := input.ClosedPeriod(resolved.Calendar, resolved.Bar, now.Add(-grace))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if start, err = input.HistoryStart(resolved.Calendar, resolved.Bar, closed.StorageStart, WarmBars(pool, members)); err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, closed.StorageStart.Add(time.Nanosecond), nil
}
