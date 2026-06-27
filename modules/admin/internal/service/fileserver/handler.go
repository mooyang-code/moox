// Package fileserver 提供云函数包文件下载的裸 HTTP 处理器，
// 经统一 HTTP 转发层 rawhandler 分派（/api/admin/fileserver/download）。
// 鉴权复用 file_download JWT token（绑定 filePath，由 cloudnode GetPackageDownloadURL 生成），
// token 与 filepath 均通过 query 参数传递，浏览器 <a> 直跳下载可携带。
// 网关 authorize filter 对本路径放行（no_auth_methods 配置），下载鉴权在本处理器内完成。
package fileserver

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// DownloadHandler 返回处理云函数包下载的 http.HandlerFunc。
//   - query 参数 token：file_download JWT（由 GenerateFileDownloadToken 生成）
//   - query 参数 file：相对包目录的文件路径
func DownloadHandler(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		token := r.URL.Query().Get("token")
		fp := strings.TrimPrefix(r.URL.Query().Get("file"), "/")

		if token == "" {
			log.WarnContextf(ctx, "[FileServer] 缺少token: %s", r.URL.Path)
			writeError(w, http.StatusUnauthorized, "访问令牌缺失")
			return
		}
		if fp == "" || !isValidFilePath(fp) {
			log.WarnContextf(ctx, "[FileServer] 非法文件路径: %s", fp)
			writeError(w, http.StatusBadRequest, "非法文件路径")
			return
		}

		// 校验 file_download token（绑定 filePath）
		claims, err := ValidateFileDownloadToken(token, fp)
		if err != nil {
			log.WarnContextf(ctx, "[FileServer] token校验失败: %v (path: %s)", err, fp)
			writeError(w, http.StatusForbidden, "访问令牌无效或已过期")
			return
		}

		// 路径遍历防护：清洗后必须仍在包目录内
		fullPath := filepath.Join(cfg.PackageDir, fp)
		cleanPath := filepath.Clean(fullPath)
		if !strings.HasPrefix(cleanPath, filepath.Clean(cfg.PackageDir)) {
			log.ErrorContextf(ctx, "[FileServer] 路径遍历尝试: %s (user: %s)", fullPath, claims.UserID)
			writeError(w, http.StatusForbidden, "非法文件访问")
			return
		}

		fileInfo, err := os.Stat(cleanPath)
		if os.IsNotExist(err) {
			log.ErrorContextf(ctx, "[FileServer] 文件不存在: %s (user: %s)", cleanPath, claims.UserID)
			writeError(w, http.StatusNotFound, "文件不存在")
			return
		}
		if err != nil {
			log.ErrorContextf(ctx, "[FileServer] 文件访问错误: %v", err)
			writeError(w, http.StatusInternalServerError, "文件访问失败")
			return
		}
		if fileInfo.IsDir() {
			writeError(w, http.StatusForbidden, "不允许下载目录")
			return
		}

		log.InfoContextf(ctx, "[FileServer] 文件下载开始: user=%s, file=%s, size=%d", claims.UserID, fp, fileInfo.Size())

		w.Header().Set("Content-Description", "File Transfer")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(fp)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		http.ServeFile(w, r, cleanPath)
		log.InfoContextf(ctx, "[FileServer] 文件下载完成: user=%s, file=%s", claims.UserID, fp)
	}
}

// writeError 写入 JSON 错误响应。
func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	fmt.Fprintf(w, `{"error":%q,"timestamp":%d}`, message, time.Now().Unix())
}
