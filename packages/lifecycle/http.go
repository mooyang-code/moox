package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// 生命周期接口（挂在模块的健康检查端口上，只监听本机，沿用健康检查的签名鉴权）：
//
//	GET  /v1/lifecycle/status    模块的状态
//	POST /v1/lifecycle/command   {"command":"pause|resume|release", ...}；pause/resume 可带 pause_kind、reason、by、manual_release、expires_at
//	GET  /v1/lifecycle/catchup   恢复追赶的进度（系统级恢复的栅栏据此判断）

// StatusPath、CommandPath、CatchupPath 是接口路径。
const (
	StatusPath  = "/v1/lifecycle/status"
	CommandPath = "/v1/lifecycle/command"
	CatchupPath = "/v1/lifecycle/catchup"
)

// CommandRequest 是命令请求。
type CommandRequest struct {
	Command Command `json:"command"`
	// PauseKind、Reason、By 用于 pause；resume 的 PauseKind 表示只恢复该类型的暂停。
	PauseKind PauseKind `json:"pause_kind,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	By        string    `json:"by,omitempty"`
	// ManualRelease 让追平后停在待放行，等 release。
	ManualRelease bool `json:"manual_release,omitempty"`
	// ExpiresAt 用于 pause：到点自动恢复。
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Request 转成控制器的命令请求。
func (r CommandRequest) Request() Request {
	return Request{Command: r.Command, PauseKind: r.PauseKind, Reason: r.Reason, By: r.By, ManualRelease: r.ManualRelease, ExpiresAt: r.ExpiresAt}
}

// CatchupReport 是恢复追赶的进度。
type CatchupReport struct {
	Module string `json:"module"`
	// Resuming 为真表示模块在恢复追赶中。
	Resuming bool `json:"resuming"`
	// CaughtUp 为真表示已追平（没有在恢复追赶时也为真）。
	CaughtUp bool  `json:"caught_up"`
	State    State `json:"state"`
}

// CatchupReport 返回恢复追赶的进度；先推进一次追赶，让进度是最新的。
func (c *Controller) CatchupReport(ctx context.Context) CatchupReport {
	c.settle(ctx)
	state := c.State()
	rep := CatchupReport{Module: state.Module, Resuming: state.Stage == StageResuming, CaughtUp: true, State: state}
	if rep.Resuming && (state.Catchup == nil || !state.Catchup.CaughtUp) {
		rep.CaughtUp = false
	}
	return rep
}

// CatchupHandler 返回恢复追赶的进度。
func (c *Controller) CatchupHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
			return
		}
		write(w, http.StatusOK, c.CatchupReport(r.Context()))
	})
}

// StatusHandler 返回模块的状态。
func (c *Controller) StatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
			return
		}
		write(w, http.StatusOK, c.State())
	})
}

// CommandHandler 执行命令；命令在当前状态不允许时返回 409。
func (c *Controller) CommandHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
			return
		}
		var req CommandRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state, err := c.Execute(r.Context(), req.Request())
		var denied *ErrNotAllowed
		switch {
		case errors.As(err, &denied):
			write(w, http.StatusConflict, map[string]any{"error": err.Error(), "state": state})
		case err != nil:
			writeError(w, http.StatusBadRequest, err)
		default:
			write(w, http.StatusOK, state)
		}
	})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	write(w, status, map[string]string{"error": err.Error()})
}
