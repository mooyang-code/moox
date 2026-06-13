package fileserver

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// corsMiddleware CORS中间件，允许跨域访问
func (s *Server) corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 允许所有来源的跨域请求（生产环境建议配置具体的域名）
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Accept, Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "3600")

		// 处理预检请求
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// securityMiddleware 安全中间件
func (s *Server) securityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 添加安全响应头
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Header("Content-Security-Policy", "default-src 'self'")

		// 限制请求大小（防止DOS攻击）
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20) // 1MB

		c.Next()
	}
}

// jwtAuthMiddleware JWT验证中间件
func (s *Server) jwtAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录访问日志
		clientIP := c.ClientIP()
		userAgent := c.GetHeader("User-Agent")
		log.InfoContextf(c.Request.Context(), "[FileServer] 文件访问请求: IP=%s, UserAgent=%s, Path=%s",
			clientIP, userAgent, c.Request.URL.Path)

		// 获取token参数
		token := c.Query("token")
		if token == "" {
			log.WarnContextf(c.Request.Context(), "[FileServer] 访问文件时缺少token: %s (IP: %s)",
				c.Request.URL.Path, clientIP)
			s.respondWithError(c, http.StatusUnauthorized, "访问令牌缺失", "MISSING_TOKEN")
			return
		}

		// 获取请求的文件路径
		filepath := c.Param("filepath")
		if filepath == "" {
			s.respondWithError(c, http.StatusBadRequest, "文件路径无效", "INVALID_PATH")
			return
		}

		// 统一路径格式：去掉前导斜杠（与生成token时的路径格式保持一致）
		filepath = strings.TrimPrefix(filepath, "/")

		// 安全检查：防止路径遍历攻击
		if !s.isValidFilePath(filepath) {
			log.WarnContextf(c.Request.Context(), "[FileServer] 检测到可疑的文件路径: %s (IP: %s)",
				filepath, clientIP)
			s.respondWithError(c, http.StatusBadRequest, "非法文件路径", "ILLEGAL_PATH")
			return
		}

		// 验证JWT令牌（此时filepath已经是相对路径，与token中的路径格式一致）
		claims, err := ValidateFileDownloadToken(token, filepath)
		if err != nil {
			log.WarnContextf(c.Request.Context(), "[FileServer] JWT验证失败: %v (IP: %s, Path: %s)",
				err, clientIP, filepath)
			s.respondWithError(c, http.StatusForbidden, "访问令牌无效或已过期", "INVALID_TOKEN")
			return
		}

		// 将用户信息添加到context中，供后续处理使用
		c.Set("user_id", claims.UserID)
		c.Set("validated_filepath", claims.FilePath)
		c.Set("client_ip", clientIP)
		c.Set("access_time", time.Now())

		log.InfoContextf(c.Request.Context(), "[FileServer] JWT验证成功，用户: %s, 文件: %s (IP: %s)",
			claims.UserID, claims.FilePath, clientIP)
		c.Next()
	}
}
