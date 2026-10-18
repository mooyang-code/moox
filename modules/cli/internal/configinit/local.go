package configinit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxBodyBytes = 1 << 20

// localPage 是只监听 127.0.0.1、用一次性路径令牌保护的本机页面：初始化表单和配置页共用。
// 只接受回环主机名（挡 DNS 重绑定）、同源 JSON 请求，响应不缓存、不带来源、禁止外部资源。
type localPage struct {
	addr   string
	token  string
	tmpl   *template.Template
	boot   template.JS
	routes map[string]http.HandlerFunc

	mu   sync.Mutex
	done bool
}

func newLocalPage(addr string, tmpl *template.Template, boot any) (*localPage, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	// json.Marshal 默认转义 <、>、&，放进 <script> 数据块是安全的。
	encoded, err := json.Marshal(boot)
	if err != nil {
		return nil, err
	}
	return &localPage{addr: addr, token: base64.RawURLEncoding.EncodeToString(raw), tmpl: tmpl, boot: template.JS(encoded)}, nil
}

func (p *localPage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if !p.hostAllowed(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	segment, rest, ok := splitPath(r.URL.Path)
	if !ok || subtle.ConstantTimeCompare([]byte(segment), []byte(p.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	finished := p.done
	p.mu.Unlock()
	if finished {
		http.Error(w, "页面已使用完毕，请重新运行命令", http.StatusGone)
		return
	}
	if rest == "" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p.page(w)
		return
	}
	handler, ok := p.routes[rest]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handler(w, r)
}

// splitPath 把 /<令牌>/<其余> 拆成两段。
func splitPath(p string) (token, rest string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	token, rest, found := strings.Cut(p, "/")
	return token, rest, found && token != ""
}

// hostAllowed 只接受回环主机名加本次端口，挡掉 DNS 重绑定。
func (p *localPage) hostAllowed(host string) bool {
	_, port, err := net.SplitHostPort(p.addr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1:"+port || host == "localhost:"+port
}

func (p *localPage) originAllowed(origin string) bool {
	_, port, err := net.SplitHostPort(p.addr)
	if err != nil {
		return false
	}
	return origin == "http://127.0.0.1:"+port || origin == "http://localhost:"+port
}

func (p *localPage) page(w http.ResponseWriter) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	n := base64.RawURLEncoding.EncodeToString(nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+n+"'; script-src 'nonce-"+n+"'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_ = p.tmpl.Execute(w, map[string]any{"Nonce": n, "Boot": p.boot})
}

// response 是各接口共用的 JSON 响应；只填用到的字段。
type response struct {
	OK       bool         `json:"ok"`
	Message  string       `json:"message,omitempty"`
	Account  *Account     `json:"account,omitempty"`
	Instance *Instance    `json:"instance,omitempty"`
	Report   *ProbeReport `json:"report,omitempty"`
	Overview *Overview    `json:"overview,omitempty"`
	Form     *ConfigForm  `json:"form,omitempty"`
	Value    string       `json:"value,omitempty"`
	// Fingerprint 是待用户核对的管理台证书指纹（证书不被这台电脑信任时）。
	Fingerprint string `json:"fingerprint,omitempty"`
	// Sync 说明这次保存有没有同步到管理后台的配置备份。
	Sync string `json:"sync,omitempty"`
}

func reply(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decode 检查请求来源与格式，并严格解码请求体；失败时已经写好了响应。
func (p *localPage) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !p.originAllowed(r.Header.Get("Origin")) || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin") {
		reply(w, http.StatusForbidden, response{Message: "请求来源不被允许"})
		return false
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		reply(w, http.StatusUnsupportedMediaType, response{Message: "请求格式不对"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		reply(w, http.StatusBadRequest, response{Message: "请求内容不对或过大"})
		return false
	}
	return true
}

// serveOptions 是本机页面的运行参数。
type serveOptions struct {
	Timeout time.Duration
	Open    func(url string) error
	Notice  io.Writer
	// Name 是给用户看的页面名称，例如「录入页」「配置页」。
	Name string
}

func listenLocal() (net.Listener, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("无法在本机起页面: %w", err)
	}
	return listener, nil
}

// serveLocal 启动页面、打开浏览器，等到 results 有结果或超时；返回前关闭服务。
func serveLocal[T any](ctx context.Context, listener net.Listener, page *localPage, options serveOptions, results <-chan T) (T, error) {
	var zero T
	server := &http.Server{
		Handler: page, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()

	url := "http://" + listener.Addr().String() + "/" + page.token + "/"
	if err := options.Open(url); err != nil {
		// 浏览器打不开时才把地址给用户（令牌在里面，一次性且只在有效期内可用）。
		fmt.Fprintf(options.Notice, "没能自动打开浏览器，请手动打开这个地址（只在本机有效）：\n%s\n", url)
	} else {
		fmt.Fprintf(options.Notice, "已在浏览器里打开本机%s（%s，%s 内有效）。请在页面里操作；页面上的内容不会发给 Agent。\n", options.Name, listener.Addr().String(), options.Timeout)
	}
	select {
	case result := <-results:
		return result, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return zero, fmt.Errorf("%s已超过 %s 没有操作，已关闭；需要时重新运行", options.Name, options.Timeout)
		}
		return zero, ctx.Err()
	}
}
