package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	probeTimeout  = 5 * time.Second
	maxProbeBytes = 1 << 20
	probePrefix   = ".moox-doctor-probe-"
)

type ProbeResult struct {
	StatusCode int
	Body       []byte
	Digest     string
	ObservedAt time.Time
}

// HealthProbe 在节点上请求组件只听本机的健康端口：本节点直接请求 127.0.0.1，
// 其他主机经 SSH 转发到目标机的 127.0.0.1（健康端口不监听网卡地址、不验签）。
type HealthProbe func(ctx context.Context, nodeID string, port int, path string) (ProbeResult, error)

// ErrNodeUnreachable 表示 SSH 连不上节点，诊断无法在目标机上请求本机端口（结论记为无法判断，不算组件故障）。
var ErrNodeUnreachable = errors.New("无法经 SSH 连到节点")

// LoopbackURL 是本机回环地址上健康端口的地址。
func LoopbackURL(port int, path string) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + path
}

// LocalHealthProbe 只探测本节点：没有 moox.toml（无法 SSH）时用它，其他节点一律报不可达。
func LocalHealthProbe(localNodeID string) HealthProbe {
	return func(ctx context.Context, nodeID string, port int, path string) (ProbeResult, error) {
		if nodeID != localNodeID {
			return ProbeResult{}, fmt.Errorf("%w %s：读不到 moox.toml，只能探测本机", ErrNodeUnreachable, nodeID)
		}
		return HTTPProber{}.Get(ctx, LoopbackURL(port, path))
	}
}

// HTTPProber 请求本机回环地址上的 /healthz、/readyz、/metrics：不跟随重定向、不走代理、响应有大小上限。
type HTTPProber struct {
	Client    *http.Client
	Now       func() time.Time
	AllowHost func(string) bool
}

func (p HTTPProber) Get(ctx context.Context, rawURL string) (ProbeResult, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProbeResult{}, fmt.Errorf("probe URL must be an absolute HTTP URL without query or fragment")
	}
	allowedHost := p.AllowHost
	if allowedHost == nil {
		allowedHost = localProbeHost
	}
	if !allowedHost(parsed.Hostname()) {
		return ProbeResult{}, fmt.Errorf("probe host %q is not allowed", parsed.Hostname())
	}
	if parsed.EscapedPath() != "/healthz" && parsed.EscapedPath() != "/readyz" && parsed.EscapedPath() != "/metrics" {
		return ProbeResult{}, fmt.Errorf("probe path %q is not allowed", parsed.EscapedPath())
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return ProbeResult{}, err
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	client := p.Client
	if client == nil {
		// 不复用连接：经 SSH 转发时每次探测的本地端口都不同，留着空闲连接只会占住转发通道。
		client = &http.Client{Timeout: probeTimeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("redirects are not allowed for health probes")
	}
	rsp, err := clientCopy.Do(req)
	if err != nil {
		return ProbeResult{}, err
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, maxProbeBytes+1))
	if err != nil {
		return ProbeResult{}, err
	}
	if len(body) > maxProbeBytes {
		return ProbeResult{}, fmt.Errorf("probe response exceeds %d bytes", maxProbeBytes)
	}
	sum := sha256.Sum256(body)
	result := ProbeResult{StatusCode: rsp.StatusCode, Body: body, Digest: "sha256:" + hex.EncodeToString(sum[:]), ObservedAt: now}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return result, fmt.Errorf("probe returned HTTP %s", rsp.Status)
	}
	return result, nil
}

func ProbeWritablePath(ctx context.Context, releaseRoot, relativePath string) (err error) {
	if filepath.IsAbs(relativePath) || relativePath == "" || filepath.Clean(relativePath) != relativePath || strings.HasPrefix(relativePath, "..") {
		return fmt.Errorf("probe path must be a clean release-relative path")
	}
	root, err := filepath.Abs(releaseRoot)
	if err != nil {
		return err
	}
	if err := rejectSymlinkComponents(root, relativePath); err != nil {
		return err
	}
	target, err := filepath.Abs(filepath.Join(root, relativePath))
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve release root: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve probe path: %w", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("probe path escapes release root")
	}
	file, err := os.CreateTemp(resolvedTarget, probePrefix)
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		closeErr := file.Close()
		removeErr := os.Remove(name)
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if err == nil && removeErr != nil && !os.IsNotExist(removeErr) {
			err = removeErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err = file.WriteString("moox doctor permission probe\n"); err != nil {
		return err
	}
	return file.Sync()
}

func localProbeHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || host == "localhost.localdomain" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func rejectSymlinkComponents(root, relative string) error {
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("probe path escapes release root: symlink component")
		}
	}
	return nil
}
