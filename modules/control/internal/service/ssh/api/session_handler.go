package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/modules/control/internal/common"
	apperrors "github.com/mooyang-code/moox/modules/control/internal/errors"
	ssh "github.com/mooyang-code/moox/modules/control/internal/service/ssh"
	"golang.org/x/net/websocket"

	"trpc.group/trpc-go/trpc-go/log"
)

// SessionHandler SSH 会话 handler
type SessionHandler struct {
	svc ssh.Service
}

// NewSessionHandler 创建 handler
func NewSessionHandler(svc ssh.Service) *SessionHandler {
	return &SessionHandler{svc: svc}
}

// CreateSession 创建 SSH 会话
func (sh *SessionHandler) CreateSession(c *gin.Context) {
	var req CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	sessionID, err := sh.svc.CreateSession(c.Request.Context(), req.HostID, c.RemoteIP())
	if err != nil {
		common.HandleAppError(c, apperrors.Internal("创建会话失败", err))
		return
	}

	common.SuccessResponse(c, "ok", map[string]string{"session_id": sessionID})
}

// DisconnectSession 断开 SSH 会话
func (sh *SessionHandler) DisconnectSession(c *gin.Context) {
	var req DisconnectSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	if err := sh.svc.DisconnectSession(c.Request.Context(), req.SessionID); err != nil {
		common.HandleAppError(c, apperrors.Internal("断开会话失败", err))
		return
	}

	common.SuccessResponse(c, "断开成功", nil)
}

// ResizeWindow 调整终端窗口大小
func (sh *SessionHandler) ResizeWindow(c *gin.Context) {
	var req ResizeWindowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	if err := sh.svc.ResizeWindow(c.Request.Context(), req.SessionID, req.W, req.H); err != nil {
		common.HandleAppError(c, apperrors.Internal("调整窗口失败", err))
		return
	}

	common.SuccessResponse(c, "ok", nil)
}

// ExecCommand 执行命令
func (sh *SessionHandler) ExecCommand(c *gin.Context) {
	var req ExecCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	output, err := sh.svc.ExecCommand(c.Request.Context(), req.SessionID, req.Cmd)
	if err != nil {
		common.HandleAppError(c, apperrors.Internal("执行命令失败", err))
		return
	}

	common.SuccessResponse(c, "ok", map[string]string{"output": output})
}

// WebSocketConn WebSocket 终端连接（直连端点，不走 Gateway）
func (sh *SessionHandler) WebSocketConn(c *gin.Context) {
	svc := sh.svc

	wsServer := websocket.Server{
		// 跳过 Origin 检查，允许跨端口连接
		Handshake: func(config *websocket.Config, req *http.Request) error {
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			sessionID := ws.Request().URL.Query().Get("session_id")

			w, err := strconv.Atoi(ws.Request().URL.Query().Get("w"))
			if err != nil || w < 40 || w > 8192 {
				_ = websocket.Message.Send(ws, "invalid window width")
				return
			}
			h, err := strconv.Atoi(ws.Request().URL.Query().Get("h"))
			if err != nil || h < 2 || h > 4096 {
				_ = websocket.Message.Send(ws, "invalid window height")
				return
			}

			sshConn, ok := svc.GetSessionConn(sessionID)
			if !ok || sshConn == nil {
				_ = websocket.Message.Send(ws, "session not found")
				return
			}

			// 不在 WebSocket 关闭时自动断开 SSH 会话
			// 会话由前端显式调用 DisconnectSession 或服务端空闲清理器回收
			sshConn.RefreshActiveTime()
			err = sshConn.RunTerminal(ws, ws, ws, w, h, ws)
			if err != nil {
				log.Errorf("[SSH WebSocket] RunTerminal 失败: %v", err)
				_ = websocket.Message.Send(ws, "terminal error: "+err.Error())
			}
		},
	}
	wsServer.ServeHTTP(c.Writer, c.Request)
}
