package unitwatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Policy 是持续崩溃与台账的参数。阶段 5 起由主机策略缓存下发，这里给出默认值。
type Policy struct {
	CrashLoopFailures int           // 窗口内自动拉起（含失败）达到这么多次即判为持续崩溃
	CrashLoopWindow   time.Duration // 统计窗口
	CrashLoopRetry    time.Duration // 持续崩溃时两次尝试的间隔
	CrashLoopSlow     time.Duration // 持续崩溃超过 1 小时后的尝试间隔
	StableAfter       time.Duration // 连续正常这么久才判定恢复并清零
	LedgerRetention   time.Duration
}

func (p Policy) withDefaults() Policy {
	if p.CrashLoopFailures <= 0 {
		p.CrashLoopFailures = 3
	}
	if p.CrashLoopWindow <= 0 {
		p.CrashLoopWindow = 10 * time.Minute
	}
	if p.CrashLoopRetry <= 0 {
		p.CrashLoopRetry = 5 * time.Minute
	}
	if p.CrashLoopSlow <= 0 {
		p.CrashLoopSlow = 30 * time.Minute
	}
	if p.StableAfter <= 0 {
		p.StableAfter = 10 * time.Minute
	}
	if p.LedgerRetention <= 0 {
		p.LedgerRetention = 30 * 24 * time.Hour
	}
	return p
}

// componentState 是一个组件跨轮次的守护状态。
type componentState struct {
	// Restarts 是近 1 小时内自动拉起（started / restarted / start_failed）的时刻。
	Restarts       []time.Time `json:"restarts,omitempty"`
	CrashLoopSince time.Time   `json:"crash_loop_since,omitempty"`
	LastAttempt    time.Time   `json:"last_attempt,omitempty"`
	HealthySince   time.Time   `json:"healthy_since,omitempty"`
	NotReadySince  time.Time   `json:"not_ready_since,omitempty"`
	LastOutcome    string      `json:"last_outcome,omitempty"`
	// LastLogged 是上一次写进台账的结果，用来避免同一种非正常结果每分钟重复记一行。
	LastLogged string `json:"last_logged,omitempty"`
}

type watchState struct {
	Components map[string]*componentState `json:"components"`
}

func (s *watchState) component(id string) *componentState {
	if s.Components == nil {
		s.Components = map[string]*componentState{}
	}
	cs := s.Components[id]
	if cs == nil {
		cs = &componentState{}
		s.Components[id] = cs
	}
	return cs
}

// backoffPending 报告持续崩溃的组件是否还没到下一次尝试时间。
func (c *componentState) backoffPending(now time.Time, p Policy) bool {
	if c.CrashLoopSince.IsZero() || c.LastAttempt.IsZero() {
		return false
	}
	interval := p.CrashLoopRetry
	if now.Sub(c.CrashLoopSince) >= time.Hour {
		interval = p.CrashLoopSlow
	}
	return now.Sub(c.LastAttempt) < interval
}

// observe 根据本轮结果更新状态，并把持续崩溃、恢复、未就绪时长写回报告。
// 持续崩溃按“自动拉起次数”统计：启动成功后又崩溃的组件每轮都能拉起成功，只数失败永远累计不到。
func (c *componentState) observe(now time.Time, entry *ComponentReport, p Policy) {
	switch entry.Outcome {
	case OutcomeStarted, OutcomeRestarted, OutcomeStartFailed:
		c.LastAttempt = now
		c.HealthySince = time.Time{}
		c.NotReadySince = time.Time{}
		c.Restarts = append(c.Restarts, now)
	case OutcomeHealthy:
		c.NotReadySince = time.Time{}
		if c.HealthySince.IsZero() {
			c.HealthySince = now
		}
	case OutcomeNotReady:
		c.HealthySince = time.Time{}
		if c.NotReadySince.IsZero() {
			c.NotReadySince = now
		}
		entry.NotReadySeconds = int64(now.Sub(c.NotReadySince) / time.Second)
	case OutcomeSkippedBackoff, OutcomeSkippedLocked, OutcomeSkippedDeadline, OutcomeSkippedDependency:
		// 本轮没有检查，状态保持不变。
	default:
		c.HealthySince = time.Time{}
	}
	kept := c.Restarts[:0]
	recent := 0
	for _, at := range c.Restarts {
		if now.Sub(at) < time.Hour {
			kept = append(kept, at)
			if now.Sub(at) < p.CrashLoopWindow {
				recent++
			}
		}
	}
	c.Restarts = kept
	if entry.Outcome == OutcomeStarted || entry.Outcome == OutcomeRestarted || entry.Outcome == OutcomeStartFailed {
		entry.RecentRestarts = len(c.Restarts)
	}
	if c.CrashLoopSince.IsZero() && recent >= p.CrashLoopFailures {
		c.CrashLoopSince = now
		entry.EnteredCrashLoop = true
	}
	if !c.CrashLoopSince.IsZero() && !c.HealthySince.IsZero() && now.Sub(c.HealthySince) >= p.StableAfter {
		c.CrashLoopSince, c.Restarts = time.Time{}, nil
		entry.Recovered = true
	}
	entry.CrashLoop = !c.CrashLoopSince.IsZero()
	if entry.Outcome != OutcomeSkippedBackoff && entry.Outcome != OutcomeSkippedLocked && entry.Outcome != OutcomeSkippedDeadline {
		c.LastOutcome = entry.Outcome
	}
}

// store 是 run/watchdog 下的状态文件与台账。
type store struct {
	dir string
}

func openStore(runDir string) (*store, error) {
	dir := filepath.Join(runDir, "watchdog")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("run/watchdog must be a private 0700 directory")
	}
	return &store{dir: dir}, nil
}

func (s *store) load() (*watchState, error) {
	state := &watchState{}
	raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, state); err != nil {
		// 状态损坏只影响退避与计时，从空状态重新开始，不让守护停摆。
		return &watchState{}, nil
	}
	return state, nil
}

func (s *store) save(state *watchState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, "state.json"), append(raw, '\n'))
}

type ledgerEntry struct {
	Time      time.Time `json:"time"`
	Outcome   string    `json:"outcome"`
	PID       int       `json:"pid,omitempty"`
	Error     string    `json:"error,omitempty"`
	Evidence  []string  `json:"evidence,omitempty"`
	CrashLoop bool      `json:"crash_loop,omitempty"`
}

const ledgerCompactBytes = 512 << 10

// appendLedger 记录值得追溯的结果：每次拉起与失败都记；其余非正常结果只在与上一轮不同时记，避免每分钟重复一行。
func (s *store) appendLedger(component string, now time.Time, entry ComponentReport, cs *componentState) error {
	switch entry.Outcome {
	case OutcomeHealthy, OutcomeSkippedPaused, OutcomeSkippedLocked, OutcomeSkippedDeadline:
		return nil
	}
	always := entry.Outcome == OutcomeStarted || entry.Outcome == OutcomeRestarted || entry.Outcome == OutcomeStartFailed ||
		entry.Outcome == OutcomeIdentityError || entry.EnteredCrashLoop || entry.Recovered
	if !always && cs.LastLogged == entry.Outcome {
		return nil
	}
	cs.LastLogged = entry.Outcome
	line, err := json.Marshal(ledgerEntry{Time: now.UTC(), Outcome: entry.Outcome, PID: entry.PID, Error: entry.Error, Evidence: entry.Evidence, CrashLoop: entry.CrashLoop})
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, component+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(line, '\n'))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return compactLedger(path, now.Add(-(Policy{}).withDefaults().LedgerRetention))
}

// compactLedger 在台账超过 512 KiB 时只保留保留期内的行。
func compactLedger(path string, cutoff time.Time) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() < ledgerCompactBytes {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var kept bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var entry ledgerEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Time.After(cutoff) {
			kept.Write(scanner.Bytes())
			kept.WriteByte('\n')
		}
	}
	return writeFileAtomic(path, kept.Bytes())
}

func writeFileAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
