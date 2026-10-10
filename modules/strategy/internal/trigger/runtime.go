package trigger

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// runtime 是一个会话在内存中的可执行状态：固化的解析结果与编译好的程序。重启后由会话快照重建。
type runtime struct {
	sessionID string
	dslHash   string
	resolved  input.Resolved
	program   *dsl.Program
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
		return &runtime{sessionID: sessionID, dslHash: session.DSLHash}, &input.SkipError{Reason: input.SkipConfigError, Detail: err.Error()}
	}
	// 编译失败时仍用快照里的日历与周期记录跳过：零值会让周期按 crypto 日历换算，写错 bar_end 与有效期。
	compiled, program, err := input.Compile(resolved, version.DSLYaml)
	if err != nil {
		return &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: resolved}, &input.SkipError{Reason: input.SkipConfigError, Detail: fmt.Sprintf("按会话快照重新编译 DSL 失败：%v", err)}
	}
	loaded := &runtime{sessionID: sessionID, dslHash: session.DSLHash, resolved: compiled, program: program}
	// 每次重新启用都会产生新会话：缓存超过上限时整体清空，按需从会话快照重建（编译很便宜）。
	if len(h.programs) >= maxCachedPrograms {
		h.programs = make(map[string]*runtime)
	}
	h.programs[sessionID] = loaded
	return loaded, nil
}

// maxCachedPrograms 是进程内缓存的会话运行时上限。
const maxCachedPrograms = 512
