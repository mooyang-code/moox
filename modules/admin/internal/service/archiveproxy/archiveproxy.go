// Package archiveproxy 把控制台的分区副本下载与最新数据预览转给归档 worker 只听 127.0.0.1 的数据端口。
// 浏览器的鉴权在 console 分派里用一次性 ticket 完成；这里只按拓扑找到 worker 所在主机并原样转发：
// worker 在 control（本机）上就直连 127.0.0.1:<端口>，在只出站主机上就经 console 网关客户端打开 control 中转流，
// 在其他可直达主机上不支持（数据端口不对外，也没有中转）。
package archiveproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/log"
)

// Component 是归档 worker 在服务目录里的组件 ID。
const Component = "archive-worker"

const (
	// RecentTimeout 是最新数据预览的转发超时；下载不另设整体超时，受 Console 服务的超时上限约束。
	RecentTimeout = 15 * time.Second
	// ResponseHeaderTimeout 只限制等 worker 响应头的时间（worker 卡住时不让下载一直挂着），不限制正文传输。
	ResponseHeaderTimeout = 90 * time.Second
)

// unsupportedHost 是 worker 装在 control 与只出站主机以外时的中文错误。
const unsupportedHost = "归档执行器所在主机不支持网页下载与预览（只支持 control 与只出站主机）"

// Topology 读取主机与部署登记（sysdeploy.TopologyDAO）。
type Topology interface {
	Read(ctx context.Context) ([]sysdeploy.HostRecord, []sysdeploy.DeploymentRecord, error)
}

// RelayDialer 经 control 中转连接只出站主机的本机端口（console 网关客户端）。
type RelayDialer interface {
	DialRelay(ctx context.Context, hostID string, port int) (net.Conn, error)
}

// Options 是处理器的依赖。
type Options struct {
	Topology Topology
	Relay    RelayDialer
	// ControlHostID 是本机（control）的主机 ID。
	ControlHostID string
	// Port 是 worker 的数据端口；为 0 时取服务目录里 archive-worker 的 relay_http 端口。
	Port int
	// DialLocal 连接本机地址，默认 net.Dialer（测试替换）。
	DialLocal func(ctx context.Context, address string) (net.Conn, error)
	// RecentTimeout 是预览的转发超时，为 0 时取 RecentTimeout（测试缩短）。
	RecentTimeout time.Duration
}

// Proxy 是两个裸 HTTP 处理器的实现。
type Proxy struct {
	opts Options
}

// New 创建转发器。
func New(opts Options) (*Proxy, error) {
	if opts.Topology == nil || opts.Relay == nil || opts.ControlHostID == "" {
		return nil, errors.New("archiveproxy 需要拓扑、中转客户端与 control 主机 ID")
	}
	if opts.Port == 0 {
		catalog, err := servicecatalog.LoadEmbedded()
		if err != nil {
			return nil, err
		}
		component, ok := catalog.Component(Component)
		if !ok || component.RelayHTTP == nil {
			return nil, errors.New("服务目录里没有 archive-worker 的 relay_http 端口")
		}
		opts.Port = component.RelayHTTP.Port
	}
	if opts.RecentTimeout == 0 {
		opts.RecentTimeout = RecentTimeout
	}
	if opts.DialLocal == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		opts.DialLocal = func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		}
	}
	return &Proxy{opts: opts}, nil
}

// statusError 是带 HTTP 状态码的错误，消息直接给页面看。
type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string { return e.message }

// dialer 按拓扑里唯一启用的 archive-worker 部署决定怎样连 worker 的数据端口：
// 部署与所在主机都启用才算；所在主机是 control 就直连本机，是只出站主机就经中转，其他主机不支持。
// 拓扑校验保证 replicas: single 的组件同时只有一条启用的部署，多于一条按登记异常处理。
func (p *Proxy) dialer(ctx context.Context) (func(ctx context.Context) (net.Conn, error), error) {
	hosts, deployments, err := p.opts.Topology.Read(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[archive proxy] 读取主机拓扑失败: %v", err)
		return nil, &statusError{http.StatusInternalServerError, "读取主机拓扑失败"}
	}
	byID := map[string]sysdeploy.HostRecord{}
	for _, host := range hosts {
		byID[host.HostID] = host
	}
	var found []sysdeploy.HostRecord
	for _, deployment := range deployments {
		host, ok := byID[deployment.HostID]
		if deployment.ComponentID == Component && deployment.Status == servicecatalog.Enabled && ok && host.Status == servicecatalog.Enabled {
			found = append(found, host)
		}
	}
	switch len(found) {
	case 0:
		return nil, &statusError{http.StatusServiceUnavailable, "拓扑里没有启用的归档执行器部署"}
	case 1:
	default:
		return nil, &statusError{http.StatusInternalServerError, "拓扑里有多条启用的归档执行器部署"}
	}
	host := found[0]
	if host.HostID == p.opts.ControlHostID {
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.opts.Port))
		return func(ctx context.Context) (net.Conn, error) { return p.opts.DialLocal(ctx, address) }, nil
	}
	if reachability, _ := servicecatalog.ParseReachability(host.Reachability); reachability == servicecatalog.ReachabilityOutbound {
		return func(ctx context.Context) (net.Conn, error) {
			return p.opts.Relay.DialRelay(ctx, host.HostID, p.opts.Port)
		}, nil
	}
	return nil, &statusError{http.StatusNotImplemented, unsupportedHost}
}

// PartitionDownload 转发 GET /v1/partition（space、dataset、partition），不另设超时。
func (p *Proxy) PartitionDownload(w http.ResponseWriter, r *http.Request) {
	p.forward(w, r, "/v1/partition", []string{"space", "dataset", "partition"}, 0)
}

// RecentRows 转发 GET /v1/recent（space、dataset、subject、before、limit），默认 15 秒超时。
func (p *Proxy) RecentRows(w http.ResponseWriter, r *http.Request) {
	p.forward(w, r, "/v1/recent", []string{"space", "dataset", "subject", "before", "limit"}, p.opts.RecentTimeout)
}

// passHeaders 是透传给浏览器的响应头。
var passHeaders = []string{"Content-Type", "Content-Length", "Content-Disposition", "X-Moox-SHA256"}

// forward 只转发白名单里的 query 参数（ticket 不发给 worker），原样透传状态码、几个响应头与响应体。
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, path string, params []string, timeout time.Duration) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	dial, err := p.dialer(ctx)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			writeError(w, se.status, se.message)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	query := url.Values{}
	for _, name := range params {
		if value := r.URL.Query().Get(name); value != "" {
			query.Set(name, value)
		}
	}
	target := (&url.URL{Scheme: "http", Host: "archive-worker", Path: path, RawQuery: query.Encode()}).String()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	// 每个请求一条连接：中转流与本机连接都不复用；不读代理环境变量，不自动解压（下载要原样交给浏览器核对 sha256）。
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ResponseHeaderTimeout: ResponseHeaderTimeout,
	}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		log.ErrorContextf(ctx, "[archive proxy] 连接归档执行器失败 %s: %v", path, err)
		var netErr net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			writeError(w, http.StatusGatewayTimeout, "归档执行器响应超时")
			return
		}
		writeError(w, http.StatusBadGateway, "连接归档执行器失败，确认执行器在运行")
		return
	}
	defer response.Body.Close()
	for _, name := range passHeaders {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	// 先把响应头发给浏览器：下载能立刻开始显示，浏览器断开时也能尽早发现并取消转给 worker 的请求。
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if _, err := io.Copy(w, response.Body); err != nil {
		// 已经写出响应头：返回后写出的字节少于 Content-Length，浏览器得到不完整的下载。
		log.WarnContextf(ctx, "[archive proxy] 转发 %s 中断: %v", path, err)
	}
}

// writeError 写出与 SSH 裸处理器一致的 JSON 错误。
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": message, "success": false})
}
