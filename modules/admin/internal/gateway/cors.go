package gateway

import (
	"net/http"
	"strings"
)

// applyCORSHeaders 根据配置白名单设置 CORS 响应头。
func applyCORSHeaders(w http.ResponseWriter, origin string) {
	cfg := GetConfig()
	allowedOrigins := []string{"*"}
	if cfg != nil && len(cfg.CORS.AllowedOrigins) > 0 {
		allowedOrigins = cfg.CORS.AllowedOrigins
	}
	if !isOriginAllowed(origin, allowedOrigins) {
		return
	}
	if origin != "" && origin != "*" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	} else if containsWildcard(allowedOrigins) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Auth, X-App-Id, X-App-Key, X-Access-Token, X-Trace-Id, X-Space-Id")
	w.Header().Set("Access-Control-Expose-Headers", "trpc-ret, trpc-func-ret, X-Trace-Id")
}

func isOriginAllowed(origin string, allowedOrigins []string) bool {
	if len(allowedOrigins) == 0 {
		return false
	}
	if containsWildcard(allowedOrigins) {
		return true
	}
	if origin == "" {
		return false
	}
	for _, allowed := range allowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

func containsWildcard(origins []string) bool {
	for _, origin := range origins {
		if strings.TrimSpace(origin) == "*" {
			return true
		}
	}
	return false
}
