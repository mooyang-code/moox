package gateway

import (
	"net/http"
	"strings"
)

var (
	corsAllowedMethods = []string{"GET", "POST", "PUT", "DELETE"}
	corsAllowedHeaders = []string{
		"Content-Type",
		"Authorization",
		"Auth",
		"X-App-Id",
		"X-App-Key",
		"X-Access-Token",
		"X-Trace-Id",
		"X-Space-Id",
		"X-Moox-Timestamp",
		"X-Moox-Nonce",
		"X-Moox-Signature",
	}
	corsExposedHeaders = []string{"trpc-ret", "trpc-func-ret", "X-Trace-Id"}
)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if r.Method == http.MethodOptions {
			handleCORSPreflight(w, r, origin)
			return
		}
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		addVary(w.Header(), "Origin")

		cfg := GetConfig()
		if cfg == nil || !isOriginAllowed(origin, cfg.CORS.AllowedOrigins) {
			http.Error(w, "origin is not allowed", http.StatusForbidden)
			return
		}
		applyCORSHeaders(w, origin)

		next.ServeHTTP(w, r)
	})
}

func handleCORSPreflight(w http.ResponseWriter, r *http.Request, origin string) {
	addVary(w.Header(), "Origin")
	addVary(w.Header(), "Access-Control-Request-Method")
	addVary(w.Header(), "Access-Control-Request-Headers")
	requestMethod := strings.TrimSpace(r.Header.Get("Access-Control-Request-Method"))
	if origin == "" || requestMethod == "" {
		w.Header().Set("Allow", strings.Join(corsAllowedMethods, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	cfg := GetConfig()
	if cfg == nil || !isOriginAllowed(origin, cfg.CORS.AllowedOrigins) {
		http.Error(w, "origin is not allowed", http.StatusForbidden)
		return
	}
	applyCORSHeaders(w, origin)
	if !containsFold(corsAllowedMethods, requestMethod) ||
		!requestedHeadersAllowed(r.Header.Get("Access-Control-Request-Headers")) {
		http.Error(w, "CORS preflight request is not allowed", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyCORSHeaders 根据配置白名单设置 CORS 响应头。
func applyCORSHeaders(w http.ResponseWriter, origin string) {
	cfg := GetConfig()
	var allowedOrigins []string
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
	addVary(w.Header(), "Origin")
	w.Header().Set("Access-Control-Allow-Methods", strings.Join(corsAllowedMethods, ", "))
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(corsAllowedHeaders, ", "))
	w.Header().Set("Access-Control-Expose-Headers", strings.Join(corsExposedHeaders, ", "))
	w.Header().Set("Access-Control-Max-Age", "600")
}

func requestedHeadersAllowed(value string) bool {
	for _, header := range strings.Split(value, ",") {
		header = strings.TrimSpace(header)
		if header != "" && !containsFold(corsAllowedHeaders, header) {
			return false
		}
	}
	return true
}

func containsFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
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
