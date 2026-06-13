package fileserver

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// respondWithError 统一错误响应处理
func (s *Server) respondWithError(c *gin.Context, statusCode int, message, errorCode string) {
	c.JSON(statusCode, gin.H{
		"error":     message,
		"code":      errorCode,
		"timestamp": time.Now().Unix(),
	})
	c.Abort()
}

// fileDownloadHandler 文件下载处理器
func (s *Server) fileDownloadHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从context中获取验证后的信息
		userID := c.GetString("user_id")
		validatedFilepath := c.GetString("validated_filepath")
		clientIP := c.GetString("client_ip")
		accessTime := c.GetTime("access_time")

		// 构建完整的文件路径
		fullPath := filepath.Join(s.config.PackageDir, validatedFilepath)

		// 安全检查：确保文件路径在允许的目录内
		cleanPath := filepath.Clean(fullPath)
		if !strings.HasPrefix(cleanPath, filepath.Clean(s.config.PackageDir)) {
			log.ErrorContextf(c.Request.Context(), "[FileServer] 检测到路径遍历尝试: %s (IP: %s, User: %s)",
				fullPath, clientIP, userID)
			s.respondWithError(c, http.StatusForbidden, "非法文件访问", "ILLEGAL_ACCESS")
			return
		}

		// 检查文件是否存在
		fileInfo, err := os.Stat(cleanPath)
		if os.IsNotExist(err) {
			log.ErrorContextf(c.Request.Context(), "[FileServer] 文件不存在: %s (User: %s, IP: %s)",
				cleanPath, userID, clientIP)
			s.respondWithError(c, http.StatusNotFound, "文件不存在", "FILE_NOT_FOUND")
			return
		}
		if err != nil {
			log.ErrorContextf(c.Request.Context(), "[FileServer] 文件访问错误: %v (Path: %s, User: %s, IP: %s)",
				err, cleanPath, userID, clientIP)
			s.respondWithError(c, http.StatusInternalServerError, "文件访问失败", "FILE_ACCESS_ERROR")
			return
		}

		// 检查是否为目录（不允许下载目录）
		if fileInfo.IsDir() {
			log.WarnContextf(c.Request.Context(), "[FileServer] 尝试下载目录: %s (User: %s, IP: %s)",
				cleanPath, userID, clientIP)
			s.respondWithError(c, http.StatusForbidden, "不允许下载目录", "DIRECTORY_NOT_ALLOWED")
			return
		}

		// 记录详细的下载日志
		log.InfoContextf(c.Request.Context(), "[FileServer] 文件下载开始 - 用户: %s, 文件: %s, 大小: %d bytes, IP: %s, 访问时间: %v",
			userID, validatedFilepath, fileInfo.Size(), clientIP, accessTime)

		// 设置安全响应头
		filename := filepath.Base(validatedFilepath)
		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")

		// 提供文件下载
		c.File(cleanPath)

		// 记录下载完成日志
		log.InfoContextf(c.Request.Context(), "[FileServer] 文件下载完成 - 用户: %s, 文件: %s, IP: %s",
			userID, validatedFilepath, clientIP)
	}
}
