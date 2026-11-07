package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// runtime 是一个会话在内存中的可执行状态：固化的解析结果与编译好的程序。重启后由会话快照重建。
type runtime struct {
	sessionID string
	dslHash   string
	resolved  input.Resolved
	program   *dsl.Program
	// pool 非空表示策略池会话：program 是池自己的程序（没有规则），成员各自的绑定与程序在 pool 里。
	pool *poolRuntime
}

// poolRuntime 是策略池会话的运行时：成员的绑定与程序、池的配置，以及会话里的预热结果。
type poolRuntime struct {
	config   pool.Config
	members  []input.PoolMember
	programs []*dsl.Program
	// warmedThrough 是预热推进到的最后一期（bar_end），零值表示预热还没完成；warmState 是预热完成时的引擎状态（JSON）：
	// 实例还没有任何带状态的结果时，第一期从它起算。
	warmedThrough time.Time
	warmState     json.RawMessage
}

// setWarm 记下会话里的预热结果。
func (p *poolRuntime) setWarm(session store.Session) {
	if session.WarmedThrough == nil {
		return
	}
	p.warmedThrough = *session.WarmedThrough
	p.warmState = json.RawMessage(session.WarmState)
}

// initialState 解析预热完成时的引擎状态；没有时是零值。
func (p *poolRuntime) initialState() (engine.State, error) {
	var state engine.State
	if len(p.warmState) == 0 {
		return state, nil
	}
	err := json.Unmarshal(p.warmState, &state)
	return state, err
}

// refreshWarm 在策略池的预热还没完成时重读会话里的预热结果：预热在运行时缓存之后才完成，缓存里的零值要更新。
func (h *Handler) refreshWarm(ctx context.Context, rt *runtime) error {
	if rt == nil || rt.pool == nil || !rt.pool.warmedThrough.IsZero() {
		return nil
	}
	session, err := h.Store.GetSession(ctx, rt.sessionID)
	if err != nil {
		return fmt.Errorf("读取会话 %s 的预热结果：%w", rt.sessionID, err)
	}
	rt.pool.setWarm(session)
	return nil
}

// runtimeFor 按会话加载或复用运行时。存储错误原样返回（可重试）；快照损坏或编译失败返回 *input.SkipError。
func (h *Handler) runtimeFor(ctx context.Context, instance store.Instance) (*runtime, error) {
	if instance.SessionID == nil || *instance.SessionID == "" {
		return nil, &input.SkipError{Reason: input.SkipConfigError, Detail: "启用实例没有会话"}
	}
	sessionID := *instance.SessionID
	if cached, ok := h.programs[sessionID]; ok {
		return cached, nil
	}
	session, err := h.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("读取会话 %s：%w", sessionID, err)
	}
	version, err := h.Store.GetDefinitionVersion(ctx, session.DSLHash)
	if err != nil {
		return nil, fmt.Errorf("读取 DSL 版本 %s：%w", session.DSLHash, err)
	}
	resolved, err := input.ParseResolved([]byte(session.ResolvedJSON))
	if err != nil {
		// 会话快照损坏：退回实例上的同一份副本取日历与周期，跳过记录才能按正确的周期写；两者都坏时调用方只记日志与计数。
		fallback, _ := input.ParseResolved(instance.ResolvedJSON)
		return &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: fallback}, &input.SkipError{Reason: input.SkipConfigError, Detail: err.Error()}
	}
	// 编译失败时仍用快照里的日历与周期记录跳过：零值会让周期按 crypto 日历换算，写错 bar_end 与有效期。
	compiled, program, err := input.Compile(resolved, version.DSLYaml)
	if err != nil {
		return &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: resolved}, &input.SkipError{Reason: input.SkipConfigError, Detail: fmt.Sprintf("按会话快照重新编译 DSL 失败：%v", err)}
	}
	loaded := &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: compiled, program: program}
	if session.MembersJSON != "" {
		members, programs, err := input.ParseMembers(session.MembersJSON)
		if err != nil {
			return &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: compiled}, &input.SkipError{Reason: input.SkipConfigError, Detail: err.Error()}
		}
		config, err := pool.NewConfig(program.Strategy, compiled.Market)
		if err != nil {
			return &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: compiled}, &input.SkipError{Reason: input.SkipConfigError, Detail: err.Error()}
		}
		loaded.pool = &poolRuntime{config: config, members: members, programs: programs}
		loaded.pool.setWarm(session)
		// 池的相邻周期核对按成员各自做；池自己的 PreviousFactors 是成员的并集，只用来说明。
		previous := map[string]struct{}{}
		for _, member := range members {
			for _, factor := range member.Resolved.PreviousFactors {
				previous[factor] = struct{}{}
			}
		}
		loaded.resolved.PreviousFactors = loaded.resolved.PreviousFactors[:0]
		for factor := range previous {
			loaded.resolved.PreviousFactors = append(loaded.resolved.PreviousFactors, factor)
		}
		sort.Strings(loaded.resolved.PreviousFactors)
	}
	// 每次重新启用都会产生新会话：缓存超过上限时整体清空，按需从会话快照重建（编译很便宜）。
	if len(h.programs) >= maxCachedPrograms {
		h.programs = make(map[string]*runtime)
	}
	h.programs[sessionID] = loaded
	return loaded, nil
}

// maxCachedPrograms 是进程内缓存的会话运行时上限。
const maxCachedPrograms = 512
