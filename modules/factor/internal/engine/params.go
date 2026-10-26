package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// 取参（设计 6.3~6.5）：开算一期之前，引擎向 FactorMgr 读取任务各可变成员最近一次被接受的提交值，把这一期用的值
// 记成"进行中记录"并落盘，之后才开始计算；这一期的完成标记写成功后才清掉记录。所以同一期重试、写出前重启，
// 用的都是第一次开算时的参数。FactorMgr 不可达时用本地缓存的最近一次读到的值，计算不阻塞在参数上；
// 读到的提交值目标期晚于本期（超前）时同样用缓存的值并告警。积压追赶时引擎不等策略。

// GetTaskParams 读取任务各可变成员最近一次被接受的提交；只有持有引擎租约的进程能读。
func (c *ManagerClient) GetTaskParams(ctx context.Context, taskID string) ([]factorwire.MemberParamState, error) {
	var rsp factorpb.GetTaskParamsRsp
	req := &factorpb.GetTaskParamsReq{Engine: factorwire.EngineIdentityToPB(c.identity), TaskId: taskID}
	if err := c.call(ctx, "GetTaskParams", req, &rsp, rsp.GetRetInfo); err != nil {
		return nil, err
	}
	out := make([]factorwire.MemberParamState, 0, len(rsp.GetMembers()))
	for _, member := range rsp.GetMembers() {
		state, err := factorwire.MemberParamStateFromPB(member)
		if err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, nil
}

// paramManager 是取参用到的管理端接口。
type paramManager interface {
	GetTaskParams(ctx context.Context, taskID string) ([]factorwire.MemberParamState, error)
}

// paramCache 是本地持久化的取参状态（data/engine/param_cache.json）。
type paramCache struct {
	// Latest 是每个可变成员最近一次读到的、目标期不晚于已开算期次的提交值（键：任务/成员键）；管理端不可达或读到超前提交时用它。
	Latest map[string]cachedParams `json:"latest"`
	// InFlight 是每个任务正在计算的一期用的参数（键：任务）；这一期的完成标记写成功后才清掉。
	InFlight map[string]inFlightParams `json:"in_flight"`
}

type cachedParams struct {
	Period   int64                `json:"period"`
	Snapshot domain.ParamSnapshot `json:"snapshot"`
}

type inFlightParams struct {
	Period int64 `json:"period"`
	// Members 是这一期各可变成员用的快照（成员键 → 快照）；没有提交过的成员不在里面，按成员自己的默认参数计算。
	Members map[string]domain.ParamSnapshot `json:"members"`
}

// paramSelector 实现 trigger.ParamSelector。
type paramSelector struct {
	manager paramManager
	file    string
	timeout time.Duration
	retry   time.Duration
	// onStale 在读到超前的提交或管理端不可达而用缓存值时调用（指标与告警）；reason 是 ahead 或 unreachable。
	onStale func(taskID, reason string)

	mu    sync.Mutex
	cache paramCache
}

func newParamSelector(manager paramManager, file string, timeout time.Duration) (*paramSelector, error) {
	s := &paramSelector{manager: manager, file: file, timeout: timeout, retry: 500 * time.Millisecond,
		cache: paramCache{Latest: map[string]cachedParams{}, InFlight: map[string]inFlightParams{}}}
	if timeout <= 0 {
		s.timeout = 5 * time.Second
	}
	raw, err := os.ReadFile(file)
	switch {
	case file == "" || errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read factor param cache: %w", err)
	}
	if err := json.Unmarshal(raw, &s.cache); err != nil {
		return nil, fmt.Errorf("decode factor param cache %s: %w", file, err)
	}
	if s.cache.Latest == nil {
		s.cache.Latest = map[string]cachedParams{}
	}
	if s.cache.InFlight == nil {
		s.cache.InFlight = map[string]inFlightParams{}
	}
	return s, nil
}

func cacheKey(taskID, memberKey string) string { return taskID + "/" + memberKey }

// Select 返回任务在 period 这一期各可变成员用的参数。这一期已有进行中记录时直接用它；否则读取提交值、记成进行中记录并落盘后返回。
// 租约冲突（另一个引擎接管）返回错误，这一期不算；落盘失败也返回错误（按基础设施错误让这一期重试，不能带着没落盘的参数开算）。
func (s *paramSelector) Select(ctx context.Context, task domain.FactorTask, members []domain.MemberDef, period time.Time) (map[string]domain.ParamSnapshot, error) {
	var vars []domain.MemberDef
	for _, member := range members {
		if member.IsVar() {
			vars = append(vars, member)
		}
	}
	if len(vars) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	if record, ok := s.cache.InFlight[task.TaskID]; ok && record.Period == period.Unix() && coversMembers(record, vars) {
		defer s.mu.Unlock()
		return cloneSnapshots(record.Members), nil
	}
	s.mu.Unlock()

	states, fetchErr := s.fetch(ctx, task.TaskID)
	if errors.Is(fetchErr, ErrLeaseConflict) || ctx.Err() != nil {
		if fetchErr == nil {
			fetchErr = ctx.Err()
		}
		return nil, fetchErr
	}
	byKey := make(map[string]factorwire.MemberParamState, len(states))
	for _, state := range states {
		byKey[state.MemberKey] = state
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chosen := make(map[string]domain.ParamSnapshot, len(vars))
	for _, member := range vars {
		key := cacheKey(task.TaskID, member.Key)
		state, got := byKey[member.Key]
		switch {
		case fetchErr != nil:
			s.warn(ctx, task.TaskID, member.Key, period, "unreachable", fetchErr)
		case got && state.LastSubmittedPeriod > period.Unix():
			s.warn(ctx, task.TaskID, member.Key, period, "ahead", fmt.Errorf("最近一次提交的目标期 %s 晚于本期", time.Unix(state.LastSubmittedPeriod, 0).UTC().Format(time.RFC3339)))
		case got && state.LastSubmittedPeriod > 0 && len(state.Params) > 0:
			s.cache.Latest[key] = cachedParams{Period: state.LastSubmittedPeriod, Snapshot: state.Params}
			chosen[member.Key] = state.Params
			continue
		default:
			continue // 没有提交过：按成员自己的默认参数计算。
		}
		if cached, ok := s.cache.Latest[key]; ok {
			chosen[member.Key] = cached.Snapshot
		}
	}
	s.cache.InFlight[task.TaskID] = inFlightParams{Period: period.Unix(), Members: chosen}
	if err := s.persistLocked(); err != nil {
		delete(s.cache.InFlight, task.TaskID)
		return nil, fmt.Errorf("persist factor param cache: %w", err)
	}
	return cloneSnapshots(chosen), nil
}

// Done 在这一期的完成标记写成功（或发现标记已经存在）后清掉进行中记录。没有记录或记录是别的期次时什么也不做。
func (s *paramSelector) Done(_ context.Context, taskID string, period time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.cache.InFlight[taskID]
	if !ok || record.Period != period.Unix() {
		return nil
	}
	delete(s.cache.InFlight, taskID)
	return s.persistLocked()
}

// Reset 丢弃全部进行中记录与最近一次读到的提交值并落盘（引擎重置时调用：管理端的提交已一并清除，旧值不能再被复用）。
func (s *paramSelector) Reset() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = paramCache{Latest: map[string]cachedParams{}, InFlight: map[string]inFlightParams{}}
	return s.persistLocked()
}

// fetch 在 timeout 内读取提交值，失败按间隔重试；读不到时返回最后一次错误。
func (s *paramSelector) fetch(ctx context.Context, taskID string) ([]factorwire.MemberParamState, error) {
	deadline := time.Now().Add(s.timeout)
	var lastErr error
	for {
		callCtx, cancel := context.WithDeadline(ctx, deadline)
		states, err := s.manager.GetTaskParams(callCtx, taskID)
		cancel()
		if err == nil {
			return states, nil
		}
		if errors.Is(err, ErrLeaseConflict) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
		if time.Now().Add(s.retry).After(deadline) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.retry):
		}
	}
}

func (s *paramSelector) warn(ctx context.Context, taskID, memberKey string, period time.Time, reason string, cause error) {
	log.WarnContextf(ctx, "factor_param_stale task_id=%s member=%s period=%s reason=%s error=%v（用本地缓存的最近一次提交值）",
		taskID, memberKey, period.UTC().Format(time.RFC3339), reason, cause)
	if s.onStale != nil {
		s.onStale(taskID, reason)
	}
}

func coversMembers(record inFlightParams, vars []domain.MemberDef) bool {
	// 记录只含有提交值的成员，所以不能要求每个成员都在里面；成员集合变化（目录同步）时按新成员重新取参更稳妥，
	// 但同一期内成员集合变化极少，这里只在记录里出现了不在当前成员里的键时才认为失效。
	current := make(map[string]struct{}, len(vars))
	for _, member := range vars {
		current[member.Key] = struct{}{}
	}
	for key := range record.Members {
		if _, ok := current[key]; !ok {
			return false
		}
	}
	return true
}

func cloneSnapshots(in map[string]domain.ParamSnapshot) map[string]domain.ParamSnapshot {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]domain.ParamSnapshot, len(in))
	for key, snapshot := range in {
		out[key] = snapshot
	}
	return out
}

// persistLocked 原子写入：先写临时文件，fsync 后再 rename，不原地覆盖。
func (s *paramSelector) persistLocked() error {
	if s.file == "" {
		return nil
	}
	raw, err := json.Marshal(s.cache)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".param-cache-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.file)
}

var _ trigger.ParamSelector = (*paramSelector)(nil)
