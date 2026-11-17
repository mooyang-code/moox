// Package webserver 是控制机上浏览器的唯一入口：Admin 自己在 9527 提供 HTTPS（HTTP/2 与 HTTP/1.1），
// 内嵌的管理台静态页面、MooX CA 根证书下载，并把 /api/admin/* 交给控制台处理器。
//
// 压缩：静态文件在这里按 gzip 预先压好；/api/admin/* 的 RPC JSON 响应由控制台处理器自己压缩（console/forward.go），
// 这里不再包一层。终端 WebSocket、文件与归档下载这些原始通道原样交给处理器，不包装 ResponseWriter，
// 因为终端握手要直接断言 http.Hijacker。
//
// WebSocket 与 HTTP/2：Go 默认不开 RFC 8441（WebSocket over HTTP/2，GODEBUG http2xconnect），服务端的 SETTINGS
// 里没有 ENABLE_CONNECT_PROTOCOL，浏览器因此为 WebSocket 另开一条 HTTP/1.1 连接，终端的升级照常可用。不要打开它。
package webserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	stdlog "log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// DefaultListen 是浏览器入口的监听地址。
	DefaultListen = "0.0.0.0:9527"

	readHeaderTimeout      = 10 * time.Second
	idleTimeout            = 2 * time.Minute
	defaultShutdownTimeout = 30 * time.Second
)

// Config 是浏览器入口的部署参数。
type Config struct {
	// Listen 是监听地址，空着用 DefaultListen。
	Listen string
	// Address 是控制机地址：请求的 Host（去掉端口）必须是它，网页证书的 SAN 也是它。
	Address string
	// PKIDir 是 MooX CA 所在目录（ca.crt、ca.key），网页证书由它签发。
	PKIDir string
	// CertDir 是网页证书的存放目录（Admin 数据目录下的 web-cert）。
	CertDir string
	// ShutdownTimeout 是停机时等在途请求的上限，空着是 30 秒；测试里改小。
	ShutdownTimeout time.Duration
	// Now 是证书检查用的时钟，空着用 time.Now；测试里改。
	Now func() time.Time
}

// securityHeaders 照搬原控制台代理（Caddy）对每个响应下发的安全响应头。
var securityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Referrer-Policy":         "no-referrer",
	"Content-Security-Policy": "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; connect-src 'self' https: wss:; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'",
}

// Server 是 9527 上的 HTTPS 服务。
type Server struct {
	cfg      Config
	certs    *certificates
	http     *http.Server
	listener net.Listener
	served   chan struct{}
}

// New 准备入口：检查或签发网页证书、读出 CA 根证书、载入静态文件，还不监听。
// api 处理 /api/admin/*；static 是前端文件（根目录即 web/dist）。
func New(cfg Config, api http.Handler, static fs.FS) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return nil, fmt.Errorf("浏览器入口监听地址 %q 不对：%w", cfg.Listen, err)
	}
	if !servicecatalog.ValidHostAddress(cfg.Address) {
		return nil, fmt.Errorf("浏览器入口要配置控制机地址（IP 或域名，不带端口），当前是 %q", cfg.Address)
	}
	if cfg.PKIDir == "" || cfg.CertDir == "" {
		return nil, errors.New("浏览器入口要配置 MooX CA 目录和网页证书目录")
	}
	if api == nil || static == nil {
		return nil, errors.New("浏览器入口需要控制台处理器和静态文件")
	}
	certs := &certificates{address: cfg.Address, pkiDir: cfg.PKIDir, dir: cfg.CertDir, now: cfg.Now}
	if err := certs.Refresh(); err != nil {
		return nil, err
	}
	ca, err := newCADownload(cfg.PKIDir)
	if err != nil {
		return nil, err
	}
	site, err := newStaticSite(static)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, certs: certs, served: make(chan struct{})}
	s.http = &http.Server{
		Handler: s.routes(api, ca, site),
		// 不设 WriteTimeout 和 ReadTimeout：下载、上传和终端是长连接；普通接口的时限由控制台处理器按服务目录控制。
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// 自建的 tls.Config 要自己写上 h2，否则只有 HTTP/1.1。
			NextProtos:     []string{"h2", "http/1.1"},
			GetCertificate: certs.get,
		},
		// 扫描器的 TLS 握手失败之类只记调试日志。
		ErrorLog: stdlog.New(debugWriter{}, "", 0),
	}
	return s, nil
}

// Start 开始监听；端口被占用等错误直接返回。
func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("浏览器入口监听 %s 失败：%w", s.cfg.Listen, err)
	}
	s.listener = listener
	go func() {
		defer close(s.served)
		if err := s.http.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("[web] 浏览器入口停止服务：%v", err)
		}
	}()
	log.Infof("[web] 浏览器入口已监听 https://%s（地址 %s）", listener.Addr(), s.cfg.Address)
	return nil
}

// Addr 是实际监听的地址（测试里用 :0 监听时取端口）。
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// RefreshCertificate 由每天一次的证书巡检调用：剩余不足 30 天就重签并热替换。
func (s *Server) RefreshCertificate(context.Context) error { return s.certs.Refresh() }

// Shutdown 先停止接新连接，再等在途请求最多 ShutdownTimeout（默认 30 秒），到时还没完的直接断开。
// 已经升级的终端 WebSocket 不在等待之列，随进程退出断开。
func (s *Server) Shutdown() error {
	// 停机发生在进程收尾，这里是新的根上下文，只受停机时限约束。
	ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), s.cfg.ShutdownTimeout)
	defer cancel()
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
	}
	if s.listener != nil {
		<-s.served
	}
	return err
}

func (s *Server) routes(api http.Handler, ca *caDownload, site *staticSite) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostMatches(r.Host, s.cfg.Address) {
			http.NotFound(w, r)
			return
		}
		header := w.Header()
		for name, value := range securityHeaders {
			header.Set(name, value)
		}
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/api/admin/"):
			api.ServeHTTP(w, r)
		case blocked(path):
			http.NotFound(w, r)
		case ca.handles(path):
			ca.ServeHTTP(w, r)
		default:
			site.ServeHTTP(w, r)
		}
	})
}

// blocked 是不能落到前端回退的路径：/api/admin/* 以外的 /api，以及健康与指标路径（它们只在本机端口上）。
func blocked(path string) bool {
	for _, prefix := range []string{"/api", "/healthz", "/readyz", "/metrics"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// hostMatches 去掉端口后比较 Host 与控制机地址：域名不区分大小写，IP 按地址比较（IPv6 带方括号）。
func hostMatches(host, address string) bool {
	name := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		name = parsed
	} else {
		name = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if name == "" {
		return false
	}
	if ip := net.ParseIP(name); ip != nil {
		want := net.ParseIP(address)
		return want != nil && want.Equal(ip)
	}
	return strings.EqualFold(name, address)
}

type debugWriter struct{}

func (debugWriter) Write(p []byte) (int, error) {
	log.Debugf("[web] %s", strings.TrimSpace(string(p)))
	return len(p), nil
}
