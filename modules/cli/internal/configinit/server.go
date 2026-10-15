package configinit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTimeout 是录入页的有效期：超时后令牌失效，进程退出。
	DefaultTimeout = time.Hour
	maxBodyBytes   = 1 << 20
)

//go:embed page.html
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

// Options 控制一次录入。
type Options struct {
	// Output 是要写入的 moox.toml 路径；已存在时拒绝覆盖。
	Output string
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// Open 为空时用系统默认浏览器；测试里替换。
	Open func(url string) error
	// Notice 接收给用户看的提示（不含任何填写的值）；为空则丢弃。
	Notice io.Writer
}

// Result 只含「写到哪里、有哪些主机」，不含任何值。
type Result struct {
	Path        string   `json:"path"`
	Hosts       []string `json:"hosts"`
	CompileHost string   `json:"compile_host,omitempty"`
}

// Run 起一个只绑回环地址的一次性页面，等用户提交，渲染并写出 moox.toml（0600，不覆盖已有文件）。
// 令牌放在 URL 路径里，写出成功后立即失效；超时或取消时返回错误且不写任何文件。
func Run(ctx context.Context, options Options) (Result, error) {
	output, err := filepath.Abs(options.Output)
	if err != nil || options.Output == "" {
		return Result{}, errors.New("需要指定输出文件路径")
	}
	if err := checkOutput(output); err != nil {
		return Result{}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	notice := options.Notice
	if notice == nil {
		notice = io.Discard
	}
	open := options.Open
	if open == nil {
		open = openBrowser
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Result{}, fmt.Errorf("无法在本机起录入页: %w", err)
	}
	session, err := newSession(listener.Addr().String(), output, time.Now().Add(timeout))
	if err != nil {
		_ = listener.Close()
		return Result{}, err
	}
	server := &http.Server{
		Handler: session, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()

	url := "http://" + listener.Addr().String() + "/" + session.token + "/"
	if err := open(url); err != nil {
		// 浏览器打不开时才把地址给用户（令牌在里面，一次性且只在有效期内可用）。
		fmt.Fprintf(notice, "没能自动打开浏览器，请手动打开这个地址填写（只在本机有效）：\n%s\n", url)
	} else {
		fmt.Fprintf(notice, "已在浏览器里打开本机录入页（%s，%s 内有效）。请在页面里填写；填写的内容不会发给 Agent。\n", listener.Addr().String(), timeout)
	}
	select {
	case result := <-session.result:
		return result, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("录入页已超过 %s 没有提交，已关闭；需要时重新运行", timeout)
		}
		return Result{}, ctx.Err()
	}
}

// checkOutput 在起页面之前就确认输出位置可用，免得用户填完才发现写不进去。
func checkOutput(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s 已存在，不会覆盖：请先改名备份，或用 --output 指定新的路径", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return fmt.Errorf("输出目录 %s 不存在", filepath.Dir(path))
	}
	return nil
}

type session struct {
	addr   string
	output string
	token  string
	boot   template.JS

	mu     sync.Mutex
	done   bool
	result chan Result
}

// newSession 的 deadline 只用于让页面显示倒计时；真正的超时由 Run 的 context 负责。
func newSession(addr, output string, deadline time.Time) (*session, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	roles, err := loadRoles()
	if err != nil {
		return nil, err
	}
	// json.Marshal 默认转义 <、>、&，放进 <script> 数据块是安全的。
	boot, err := json.Marshal(map[string]any{"roles": roles, "deadline": deadline.UnixMilli()})
	if err != nil {
		return nil, err
	}
	return &session{
		addr: addr, output: output, token: base64.RawURLEncoding.EncodeToString(raw),
		boot: template.JS(boot), result: make(chan Result, 1),
	}, nil
}

func (s *session) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if !s.hostAllowed(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	segment, rest, ok := splitPath(r.URL.Path)
	if !ok || subtle.ConstantTimeCompare([]byte(segment), []byte(s.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	finished := s.done
	s.mu.Unlock()
	if finished {
		http.Error(w, "页面已使用完毕，请重新运行命令", http.StatusGone)
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.page(w)
	case rest == "submit" && r.Method == http.MethodPost:
		s.submit(w, r)
	case rest == "" || rest == "submit":
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// splitPath 把 /<令牌>/<其余> 拆成两段。
func splitPath(p string) (token, rest string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	token, rest, found := strings.Cut(p, "/")
	return token, rest, found && token != ""
}

// hostAllowed 只接受回环主机名加本次端口，挡掉 DNS 重绑定。
func (s *session) hostAllowed(host string) bool {
	_, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1:"+port || host == "localhost:"+port
}

func (s *session) originAllowed(origin string) bool {
	_, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return false
	}
	return origin == "http://127.0.0.1:"+port || origin == "http://localhost:"+port
}

func (s *session) page(w http.ResponseWriter) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	n := base64.RawURLEncoding.EncodeToString(nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+n+"'; script-src 'nonce-"+n+"'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_ = pageTemplate.Execute(w, map[string]any{"Nonce": n, "Output": s.output, "Boot": s.boot})
}

type response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Path    string `json:"path,omitempty"`
}

func reply(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *session) submit(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r.Header.Get("Origin")) || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin") {
		reply(w, http.StatusForbidden, response{Message: "请求来源不被允许"})
		return
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		reply(w, http.StatusUnsupportedMediaType, response{Message: "请求格式不对"})
		return
	}
	var request Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		reply(w, http.StatusBadRequest, response{Message: "请求内容不对或过大"})
		return
	}
	raw, err := Render(&request)
	if err != nil {
		// 校验没过不消耗令牌：用户改完可以直接再提交。
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		reply(w, http.StatusGone, response{Message: "页面已使用完毕"})
		return
	}
	if err := writeNew(s.output, raw); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		reply(w, status, response{Message: err.Error()})
		return
	}
	s.done = true
	result := Result{Path: s.output}
	for _, host := range request.hosts {
		result.Hosts = append(result.Hosts, host.ID)
	}
	if request.compile != nil {
		result.CompileHost = request.compile.ID
	}
	reply(w, http.StatusOK, response{OK: true, Path: s.output})
	s.result <- result
}

// writeNew 创建新文件（已存在则失败）、权限 0600，写完落盘；写失败时删掉半截文件。
func writeNew(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s 已存在，不会覆盖：请先改名备份，或用 --output 指定新的路径: %w", path, os.ErrExist)
		}
		return fmt.Errorf("无法创建 %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}
