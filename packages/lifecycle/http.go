package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// 生命周期接口（挂在模块的健康检查端口上，只监听本机，沿用健康检查的签名鉴权）：
//
//	GET  /v1/lifecycle/status    模块与各对象的状态快照
//	POST /v1/lifecycle/command   {"command":"init|check|start|confirm|pause|resume|release","kind":"...","id":"..."}
//	                             kind 与 id 都为空表示模块自身；pause/resume 可带 pause_kind、reason、by、force、manual_release
//	GET  /v1/lifecycle/catchup   恢复追赶中的模块与对象的进度（系统级恢复的栅栏据此判断）

// StatusPath 与 CommandPath 是接口路径。
const (
	StatusPath  = "/v1/lifecycle/status"
	CommandPath = "/v1/lifecycle/command"
	CatchupPath = "/v1/lifecycle/catchup"
)

// CommandRequest 是命令请求。
type CommandRequest struct {
	Command Command `json:"command"`
	Kind    string  `json:"kind,omitempty"`
	ID      string  `json:"id,omitempty"`
	// PauseKind、Reason、By 用于 pause；resume 的 PauseKind 表示只恢复该类型的暂停。
	PauseKind PauseKind `json:"pause_kind,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	By        string    `json:"by,omitempty"`
	// Force 用于模块暂停期间对单个对象 resume；ManualRelease 让追平后停在待放行，等 release。
	Force         bool `json:"force,omitempty"`
	ManualRelease bool `json:"manual_release,omitempty"`
}

// Request 转成控制器的命令请求。
func (r CommandRequest) Request() Request {
	return Request{
		Command: r.Command, Ref: Ref{Kind: r.Kind, ID: r.ID},
		PauseKind: r.PauseKind, Reason: r.Reason, By: r.By, Force: r.Force, ManualRelease: r.ManualRelease,
	}
}

// CatchupReport 是恢复追赶的进度汇总。
type CatchupReport struct {
	Module string `json:"module"`
	// Resuming 为真表示模块自身或任一对象在恢复追赶中。
	Resuming bool `json:"resuming"`
	// CaughtUp 为真表示所有恢复追赶中的记录都已追平（没有恢复追赶中的记录时也为真）。
	CaughtUp bool     `json:"caught_up"`
	Self     *Object  `json:"self,omitempty"`
	Objects  []Object `json:"objects,omitempty"`
}

// CatchupReport 返回恢复追赶的进度；先推进一次追赶，让进度是最新的。
func (c *Controller) CatchupReport(ctx context.Context) CatchupReport {
	c.settle(ctx)
	snap := c.Snapshot()
	rep := CatchupReport{Module: snap.Module, CaughtUp: true}
	add := func(o Object, self bool) {
		if o.Stage != StageResuming && o.Catchup == nil {
			return
		}
		rep.Resuming = rep.Resuming || o.Stage == StageResuming
		if o.Catchup == nil || !o.Catchup.CaughtUp {
			rep.CaughtUp = false
		}
		o.Checks = failedChecks(o.Checks)
		if self {
			rep.Self = &o
			return
		}
		rep.Objects = append(rep.Objects, o)
	}
	add(snap.Self, true)
	for _, o := range snap.Objects {
		add(o, false)
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

// StatusHandler 返回状态快照。
func (c *Controller) StatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
			return
		}
		write(w, http.StatusOK, c.Snapshot())
	})
}

// CommandHandler 执行命令；命令在当前阶段不允许时返回 409。
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
		if (req.Kind == "") != (req.ID == "") {
			writeError(w, http.StatusBadRequest, errors.New("kind 与 id 要么都写，要么都不写（模块自身）"))
			return
		}
		obj, err := c.Execute(r.Context(), req.Request())
		var denied *ErrNotAllowed
		switch {
		case errors.As(err, &denied):
			write(w, http.StatusConflict, map[string]any{"error": err.Error(), "object": obj})
		case err != nil:
			writeError(w, http.StatusBadRequest, err)
		default:
			write(w, http.StatusOK, obj)
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
