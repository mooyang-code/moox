// Package watermark 保存因子任务的连续水位：committed_through 这一期及之前已经连续算完，
// 引擎据此逐期推进，不跳过任何还没补到的周期（设计见 docs/计划/因子连续水位详细设计.md）。
package watermark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// State 是一个任务的推进状态。
type State struct {
	TaskID    string `json:"task_id"`
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	Freq      string `json:"freq"`
	// CommittedThrough 是已连续算完的最后一期（K 线开始时间）。
	CommittedThrough time.Time `json:"committed_through"`
	// Target 是已知的最新收盘周期（收到的事件里最大的周期）。
	Target time.Time `json:"target"`
	// Universe 是最近一次事件的期望标的：读回判断没有事件可用的周期时使用。
	Universe []string `json:"universe,omitempty"`
	// BlockedSince 是当前阻塞开始的时刻，没有阻塞为零。
	BlockedSince time.Time `json:"blocked_since,omitempty"`
	// BlockedPeriod 与 BlockedReason 说明卡在哪一期、为什么。
	BlockedPeriod time.Time `json:"blocked_period,omitempty"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
	// InFlightPeriod 与 InFlightTrigger 是开始计算、还没推进水位的一期及其触发事件 ID：
	// 计算与 marker 成功后、水位落盘前进程退出时，重启后沿用同一个 ID，已完成检查能认出它，不会重复计算与发布。
	InFlightPeriod  time.Time `json:"in_flight_period,omitempty"`
	InFlightTrigger string    `json:"in_flight_trigger,omitempty"`
}

// Behind 判断还有已知的周期没有推进到。
func (s State) Behind() bool { return s.Target.After(s.CommittedThrough) }

// Store 是持久化在一个 JSON 文件里的全部任务状态；每次修改原子写盘。
type Store struct {
	path   string
	mu     sync.Mutex
	states map[string]State
}

// Open 读取状态文件；文件不存在视为空。
func Open(path string) (*Store, error) {
	store := &Store{path: path, states: map[string]State{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read factor watermarks: %w", err)
	}
	var states []State
	if err := json.Unmarshal(raw, &states); err != nil {
		return nil, fmt.Errorf("decode factor watermarks %s: %w", path, err)
	}
	for _, state := range states {
		store.states[state.TaskID] = state
	}
	return store, nil
}

// Path 是状态文件路径。
func (s *Store) Path() string { return s.path }

// Get 返回任务的状态。
func (s *Store) Get(taskID string) (State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[taskID]
	if ok {
		state.Universe = append([]string(nil), state.Universe...)
	}
	return state, ok
}

// Put 保存任务的状态并写盘。
func (s *Store) Put(state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state.Universe = append([]string(nil), state.Universe...)
	s.states[state.TaskID] = state
	return s.flushLocked()
}

// Reset 清空全部状态并写盘（重置时用）：数据被清空后，旧的连续水位指向已不存在的数据。
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = map[string]State{}
	return s.flushLocked()
}

// All 返回全部状态（按任务 ID 排序）。
func (s *Store) All() []State {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]State, 0, len(s.states))
	for _, state := range s.states {
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

func (s *Store) flushLocked() error {
	states := make([]State, 0, len(s.states))
	for _, state := range s.states {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].TaskID < states[j].TaskID })
	raw, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}
