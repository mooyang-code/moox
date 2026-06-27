// Package rpc 提供 ssh 直连端点（WebSocket 终端、SFTP 流式上传/下载）的裸 HTTP 处理器，
// 经统一 HTTP 转发层 rawhandler 分派，废弃原 SSH 独立 HTTP 服务（端口 20180）。
//
// 鉴权由 session_id 完成（session 由 CreateSession RPC 创建时已校验登录态），
// 网关 authorize 对这些路径放行（no_auth_methods），rawhandler 内部校验 session_id 有效性。
package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"

	"trpc.group/trpc-go/trpc-go/log"
	"golang.org/x/net/websocket"
)

// WebSocketConnectHandler 返回 SSH 终端 WebSocket 处理器。
// query: session_id, w, h
func WebSocketConnectHandler(svc ssh.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsServer := websocket.Server{
			// 跳过 Origin 检查，允许跨端口/跨源连接
			Handshake: func(config *websocket.Config, req *http.Request) error {
				return nil
			},
			Handler: func(ws *websocket.Conn) {
				ctx := ws.Request().Context()
				sessionID := ws.Request().URL.Query().Get("session_id")

				wv, err := strconv.Atoi(ws.Request().URL.Query().Get("w"))
				if err != nil || wv < 40 || wv > 8192 {
					_ = websocket.Message.Send(ws, "invalid window width")
					return
				}
				hv, err := strconv.Atoi(ws.Request().URL.Query().Get("h"))
				if err != nil || hv < 2 || hv > 4096 {
					_ = websocket.Message.Send(ws, "invalid window height")
					return
				}

				sshConn, ok := svc.GetSessionConn(sessionID)
				if !ok || sshConn == nil {
					_ = websocket.Message.Send(ws, "session not found")
					return
				}

				sshConn.RefreshActiveTime()
				if err := sshConn.RunTerminal(ws, ws, ws, wv, hv, ws); err != nil {
					log.ErrorContextf(ctx, "[SSH WebSocket] RunTerminal 失败: %v", err)
					_ = websocket.Message.Send(ws, "terminal error: "+err.Error())
				}
			},
		}
		wsServer.ServeHTTP(w, r)
	}
}

// SftpDownloadHandler 返回 SFTP 文件下载处理器（流式）。
// query: session_id, path
func SftpDownloadHandler(svc ssh.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sessionID := r.URL.Query().Get("session_id")
		filePath, err := url.QueryUnescape(r.URL.Query().Get("path"))
		if err != nil {
			writeRawError(w, http.StatusBadRequest, "路径格式不正确")
			return
		}
		if sessionID == "" || filePath == "" {
			writeRawError(w, http.StatusBadRequest, "session_id/path 不能为空")
			return
		}

		file, size, name, err := svc.SftpDownload(ctx, sessionID, filePath)
		if err != nil {
			log.ErrorContextf(ctx, "[SSH SFTP] 下载文件失败: %v", err)
			writeRawError(w, http.StatusInternalServerError, "下载文件失败")
			return
		}
		defer file.Close()

		w.Header().Set("Content-Disposition", "attachment; filename="+name)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		w.WriteHeader(http.StatusOK)
		if _, err := file.WriteTo(w); err != nil {
			log.ErrorContextf(ctx, "[SSH SFTP] 下载文件写入失败: %v", err)
		}
	}
}

// SftpUploadHandler 返回 SFTP 文件上传处理器（multipart）。
// form: session_id, path, file[] (multipart files)
func SftpUploadHandler(svc ssh.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeRawError(w, http.StatusBadRequest, "获取上传文件失败")
			return
		}
		sessionID := r.FormValue("session_id")
		dstPath := r.FormValue("path")
		if sessionID == "" || dstPath == "" {
			writeRawError(w, http.StatusBadRequest, "session_id/path 不能为空")
			return
		}

		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			writeRawError(w, http.StatusBadRequest, "没有上传文件")
			return
		}

		uploaded, err := svc.SftpUpload(ctx, sessionID, dstPath, files)
		if err != nil {
			log.ErrorContextf(ctx, "[SSH SFTP] 上传失败: %v", err)
			writeRawError(w, http.StatusInternalServerError, "上传失败")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body, _ := json.Marshal(map[string]interface{}{
			"code":    0,
			"msg":     fmt.Sprintf("%d 个文件上传成功", len(uploaded)),
			"files":   uploaded,
			"success": true,
		})
		w.Write(body)
	}
}

// writeRawError 写入 JSON 错误响应。
func writeRawError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	body, _ := json.Marshal(map[string]interface{}{
		"code":    statusCode,
		"msg":     message,
		"success": false,
	})
	w.Write(body)
}
