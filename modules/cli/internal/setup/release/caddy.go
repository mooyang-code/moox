package release

import (
	"fmt"
	"net"
	"strings"
)

// 控制台代理（Caddy）的证书方式。
const (
	TLSModePublic   = "public"
	TLSModeInternal = "internal"
)

// ResolveTLSMode 解析控制台代理的证书方式：auto（或不填）时，私网、回环地址和 localhost 用 Caddy 内置 CA，
// 其余申请公网证书。
func ResolveTLSMode(mode, publicHost string) string {
	switch mode {
	case TLSModePublic, TLSModeInternal:
		return mode
	}
	host := strings.ToLower(strings.TrimSpace(publicHost))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return TLSModeInternal
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return TLSModeInternal
	}
	return TLSModePublic
}

// renderCaddyfile 生成控制台代理的配置：只有浏览器入口一个站点，/api/admin/ 转发到控制台 API，其余转发到控制台前端。
func renderCaddyfile(publicHost, tlsMode string, port int) ([]byte, error) {
	if strings.TrimSpace(publicHost) == "" || strings.ContainsAny(publicHost, " \t\r\n{}") {
		return nil, fmt.Errorf("控制台代理的公网地址 %q 无效", publicHost)
	}
	tls := "\ttls internal\n"
	if tlsMode == TLSModePublic {
		tls = "\ttls {\n\t\tissuer acme {\n\t\t\tprofile shortlived\n\t\t\tdisable_tlsalpn_challenge\n\t\t}\n\t}\n"
	}
	return []byte(fmt.Sprintf(`# 由 moox-cli 生成，请勿手工修改。
{
	auto_https disable_redirects
	admin 127.0.0.1:2019
	default_sni %[1]s
}

https://%[1]s:%[2]d {
%[3]s	encode zstd gzip

	header {
		X-Content-Type-Options nosniff
		X-Frame-Options DENY
		Referrer-Policy no-referrer
		Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; connect-src 'self' https: wss:; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
	}

	handle /api/admin/* {
		reverse_proxy 127.0.0.1:11000 {
			stream_close_delay 5m
		}
	}
	handle /api/* {
		respond 404
	}
	handle /healthz {
		respond 404
	}
	handle /readyz {
		respond 404
	}
	handle /metrics {
		respond 404
	}
	handle {
		reverse_proxy 127.0.0.1:9528
	}
}
`, publicHost, port, tls)), nil
}
