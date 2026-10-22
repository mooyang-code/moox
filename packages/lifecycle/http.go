package lifecycle

import (
	"encoding/json"
	"errors"
	"net/http"
)

// 生命周期接口（挂在模块的健康检查端口上，只监听本机，沿用健康检查的签名鉴权）：
//
//	GET  /v1/lifecycle/status    模块与各对象的状态快照
//	POST /v1/lifecycle/command   {"command":"init|check|start|confirm|pause|resume","kind":"...","id":"..."}
//	                             kind 与 id 都为空表示模块自身

// StatusPath 与 CommandPath 是接口路径。
const (
	StatusPath  = "/v1/lifecycle/status"
	CommandPath = "/v1/lifecycle/command"
)

// CommandRequest 是命令请求。
type CommandRequest struct {
	Command Command `json:"command"`
	Kind    string  `json:"kind,omitempty"`
	ID      string  `json:"id,omitempty"`
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
		obj, err := c.Command(r.Context(), req.Command, Ref{Kind: req.Kind, ID: req.ID})
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
