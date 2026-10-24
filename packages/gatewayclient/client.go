// Package gatewayclient provides signed object calls and raw tRPC forwarding
// through the host gateway, fixed external access, or SSH tunnels.
package gatewayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient/relay"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/wirecompress"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

const (
	Internal = "local"
	External = "access"
	Tunnel   = "tunnel"
)

// TunnelResolver connects to a host selected by ID using trusted SSH settings.
// Returned addresses must be local forwards to the host's 127.0.0.1:11002.
type TunnelResolver interface {
	Resolve(ctx context.Context, hostID string) (string, error)
}

// RelayTunnelResolver 由隧道模式的解析器可选实现：返回到 control 主机 127.0.0.1:11003 的本地转发，
// 用来经 control 网关中转连接只能出站的主机。
type RelayTunnelResolver interface {
	ResolveRelay(ctx context.Context, controlHostID string) (string, error)
}

// Invoker is the object-call interface used by service clients.
type Invoker interface {
	Invoke(context.Context, string, string, any, any) error
}

type Config struct {
	Mode             string
	Credentials      gatewayauth.Credentials
	LocalHostID      string
	LocalAddress     string
	CAFile           string
	Source           DirectorySource
	CachePath        string
	Tunnels          TunnelResolver
	AccessAddress    string
	AccessInstanceID string
	Serialization    int
	// Pool 调整连接本机/跨主机网关的连接池（Internal 与 Tunnel 模式）；零值使用默认。
	Pool             PoolConfig
	Timeout          time.Duration
	DirectoryTimeout time.Duration
	RefreshInterval  time.Duration
	OnRefreshError   func(error)
}

// CallFailure 描述一次失败的尝试。
type CallFailure struct {
	Service, Method string
	Target          string // 目标主机
	Attempt         int    // 第几次尝试，从 0 开始
	// ConnectFailed 为 true 表示获取连接失败、请求尚未发出。
	ConnectFailed bool
	// Retrying 为 true 表示本次失败之后会再尝试一次。
	Retrying bool
	// Remaining 是失败时调用预算的剩余时间。
	Remaining time.Duration
	Err       error
}

type endpoint struct {
	address, target, caFile, serverName string
	compress                            bool
	// relay 非空时经 control 网关中转：address 是只在本进程内使用的合成地址，拨号时先连 control 再连目标。
	relay *relayRoute
}

// relayRoute 描述一次经 control 中转的连接。
type relayRoute struct {
	controlAddress, controlHost, target string
	caFile                              string
	credentials                         gatewayauth.Credentials
}

// RelayHostSuffix 标记经 control 中转的合成主机名（<主机 ID>.moox-relay），只在本进程内使用、从不解析。
const RelayHostSuffix = ".moox-relay"

// relayAddress 是中转目标在连接池里的合成地址，与真实地址不会冲突。
func relayAddress(target string) string {
	return net.JoinHostPort(target+RelayHostSuffix, strconv.Itoa(servicecatalog.HostGatewayRemotePort))
}

// DialRelay 经 control 中转连接只能出站主机的本机端口（例如 Monitor 的健康探测），
// 返回的连接里是目标端口的原始字节；外层与 control 之间是 TLS。
func (c *Client) DialRelay(ctx context.Context, hostID string, port int) (net.Conn, error) {
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	c.mu.RLock()
	directory := c.directory
	c.mu.RUnlock()
	host, ok := directory.Hosts[hostID]
	if !ok || host.Relay == "" {
		return nil, fmt.Errorf("host %s is not an outbound host in the directory", hostID)
	}
	ep, err := c.relayEndpoint(ctx, directory, hostID, host.Relay)
	if err != nil {
		return nil, err
	}
	roots, err := loadRoots(ep.relay.caFile)
	if err != nil {
		return nil, err
	}
	return relay.Dial(ctx, relay.OpenOptions{
		Address: ep.relay.controlAddress, ControlHost: ep.relay.controlHost,
		TLS:         &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		Credentials: c.config.Credentials, Target: relay.Open{HostID: hostID, Port: port},
	})
}

type invokeFunc func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error)

type Client struct {
	config      Config
	catalog     servicecatalog.Catalog
	mu          sync.RWMutex
	directory   servicecatalog.Directory
	refresh     chan struct{}
	invoke      invokeFunc
	pool        *rpcPool
	ownedSource io.Closer
	cancel      context.CancelFunc
	done        chan struct{}
	// unpersisted 记录目录已应用但缓存落盘失败；仅在 refresh 令牌持有期间读写。
	unpersisted bool
	closed      atomic.Bool
	onFailure   atomic.Pointer[func(CallFailure)]
}

var hostID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func New(config Config) (*Client, error) {
	if config.DirectoryTimeout == 0 {
		config.DirectoryTimeout = 5 * time.Second
	}
	if config.RefreshInterval == 0 {
		// Directory publication follows the host snapshot pull. Leave room for
		// both polling stages within the 15-second deployment visibility budget.
		config.RefreshInterval = 5 * time.Second
	}
	if config.Timeout < 0 || config.DirectoryTimeout <= 0 || config.RefreshInterval <= 0 || config.Serialization != codec.SerializationTypePB && config.Serialization != codec.SerializationTypeJSON {
		return nil, fmt.Errorf("gateway client requires positive timeouts and PB or JSON serialization")
	}
	if err := config.Pool.Validate(); err != nil {
		return nil, err
	}
	if config.Credentials.Caller == "" {
		return nil, fmt.Errorf("gateway caller is required")
	}
	if _, err := gatewayauth.Sign(config.Credentials, gatewayauth.Request{Method: "POST", Path: "/validate", TargetNode: "bootstrap"}, time.Now()); err != nil {
		return nil, err
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	var ownedSource io.Closer
	switch config.Mode {
	case Internal:
		if !hostID.MatchString(config.LocalHostID) {
			return nil, fmt.Errorf("internal client requires local host ID")
		}
		if config.LocalAddress == "" {
			config.LocalAddress = "127.0.0.1:11002"
		}
		if !loopbackAddress(config.LocalAddress) {
			return nil, fmt.Errorf("local gateway must use a loopback address")
		}
		if config.CAFile == "" || config.CAFile == "none" || config.CAFile == "root" {
			return nil, fmt.Errorf("internal client requires the MooX private CA file")
		}
		pem, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read MooX CA: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("MooX CA file contains no certificates")
		}
		if config.Source == nil {
			source, err := NewLocalDirectorySource(config.LocalAddress, config.DirectoryTimeout)
			if err != nil {
				return nil, err
			}
			config.Source, ownedSource = source, source
		}
	case Tunnel:
		if config.Credentials.Caller != "moox-cli" || config.Source == nil || config.Tunnels == nil {
			return nil, fmt.Errorf("tunnel client requires moox-cli identity, directory source and tunnel resolver")
		}
		// 隧道模式平时不需要 CA；连接出站主机时要用它校验 control 与目标网关的证书。
	case External:
		if err := validateExternalIdentity(catalog, config.Credentials.Caller, config.AccessAddress, config.AccessInstanceID); err != nil {
			return nil, err
		}
		if config.Source != nil || config.CachePath != "" || config.Tunnels != nil {
			return nil, fmt.Errorf("external client does not use directory discovery")
		}
	default:
		return nil, fmt.Errorf("unknown gateway client mode")
	}
	settings := legacyPoolSettings()
	if config.Mode != External {
		settings = tunedPoolSettings(config.Pool)
	}
	pool := newRPCPool(settings)
	c := &Client{config: config, catalog: catalog, pool: pool, ownedSource: ownedSource, invoke: pool.invoke, done: make(chan struct{}), refresh: make(chan struct{}, 1)}
	if config.Mode != External {
		if config.CachePath != "" {
			if cached, err := readCache(config.CachePath); err == nil {
				c.directory = cached
			}
		}
		err := c.Refresh(context.Background())
		if c.directory.Version == "" {
			_ = pool.Close()
			if ownedSource != nil {
				_ = ownedSource.Close()
			}
			return nil, fmt.Errorf("initial directory unavailable: %w", err)
		}
		if err != nil {
			c.report(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.poll(ctx)
	return c, nil
}

// PoolStats 返回连接池拨号与建连重试的累计计数。
func (c *Client) PoolStats() PoolStats { return c.pool.stats.snapshot() }

func (c *Client) Directory() servicecatalog.Directory {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.directory.Clone()
}

// LocalHostID is the identity loaded from this internal client's host gateway
// configuration. External clients have no local host identity.
func (c *Client) LocalHostID() string { return c.config.LocalHostID }

func (c *Client) Refresh(ctx context.Context) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	if c.config.Mode == External {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.DirectoryTimeout)
	defer cancel()
	select {
	case c.refresh <- struct{}{}:
		defer func() { <-c.refresh }()
	case <-ctx.Done():
		return ctx.Err()
	}
	current := c.Directory().Version
	update, err := c.config.Source.Fetch(ctx, current)
	if err != nil {
		return err
	}
	if !update.Changed {
		if current == "" || update.Directory.Version != current {
			return fmt.Errorf("unchanged directory has an unexpected version")
		}
		// 上次落盘失败时，目录版本不变也要补写，否则缓存会一直停留在旧版本。
		if c.unpersisted {
			if err := writeCache(c.config.CachePath, c.Directory()); err != nil {
				return fmt.Errorf("persist directory: %w", err)
			}
			c.unpersisted = false
		}
		return nil
	}
	if err := update.Directory.Validate(); err != nil {
		return err
	}
	directory := update.Directory.Clone()
	c.mu.Lock()
	c.directory = directory
	c.mu.Unlock()
	// A disk failure must not keep a disabled host in the active in-memory map.
	if err := writeCache(c.config.CachePath, directory); err != nil {
		c.unpersisted = true
		return fmt.Errorf("persist directory: %w", err)
	}
	c.unpersisted = false
	return nil
}

func (c *Client) poll(ctx context.Context) {
	defer close(c.done)
	if c.config.Mode == External {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(c.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
				c.report(err)
			}
		}
	}
}

func (c *Client) report(err error) {
	if c.config.OnRefreshError != nil {
		c.config.OnRefreshError(err)
	}
}

func (c *Client) Close() error {
	c.closed.Store(true)
	c.cancel()
	err := c.pool.Close()
	if c.ownedSource != nil {
		err = errors.Join(err, c.ownedSource.Close())
	}
	timer := time.NewTimer(c.config.DirectoryTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return err
	case <-timer.C:
		return errors.Join(err, fmt.Errorf("directory source did not stop within client timeout"))
	}
}

func (c *Client) Invoke(ctx context.Context, service, method string, req, rsp interface{}) error {
	// 字典按空间选择：请求顶层带 space_id 的直接用；顶层没有的调用点自行用 wirecompress.WithSpace 指定。
	if space := wirecompress.SpaceOf(ctx, req); space != "" {
		ctx = wirecompress.WithSpace(ctx, space)
	}
	body, err := codec.Marshal(c.config.Serialization, req)
	if err != nil {
		return err
	}
	response, err := c.Forward(ctx, service, method, c.config.Serialization, body)
	if err != nil {
		return err
	}
	if err := codec.Unmarshal(c.config.Serialization, response, rsp); err != nil {
		return errs.NewFrameError(errs.RetClientDecodeFail, "decode gateway response")
	}
	return nil
}

func (c *Client) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	if serialization != codec.SerializationTypePB && serialization != codec.SerializationTypeJSON {
		return nil, fmt.Errorf("forward requires PB or JSON serialization")
	}
	allowed := c.catalog.Allowed(c.config.Credentials.Caller, service, method)
	if c.config.Mode == External {
		allowed = c.catalog.PrincipalAllowed(c.config.Credentials.Caller, service, method)
	}
	if !allowed {
		return nil, fmt.Errorf("gateway caller is not allowed for this method")
	}
	spec, _ := c.catalog.Service(service)
	limit := spec.MaxBodyBytes
	if limit == 0 {
		limit = servicecatalog.DefaultMaxBodyBytes
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("gateway request exceeds service body limit")
	}
	timeout := c.config.Timeout
	if timeout == 0 {
		timeout = time.Duration(spec.TimeoutFor(method)) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failedHost := ""
	connectRetried := false
	for attempt := 0; attempt < 2; attempt++ {
		ep, err := c.endpoint(ctx, service, failedHost)
		if err != nil {
			return nil, err
		}
		headers, err := gatewayauth.Sign(c.config.Credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: ep.target, Caller: c.config.Credentials.Caller, Callee: service, Func: method, Body: body}, time.Now())
		if err != nil {
			return nil, err
		}
		metadata := CallMetadataFromContext(ctx)
		for name, value := range map[string]string{
			"X-Space-Id": metadata.SpaceID, "X-User-Id": metadata.UserID, "X-Trace-Id": metadata.TraceID,
		} {
			if value != "" {
				headers.Set(name, value)
			}
		}
		callCtx, state := withCallState(ctx)
		state.attempt = attempt
		response, err := c.invoke(callCtx, ep, service, method, serialization, body, headers)
		if err == nil {
			if connectRetried {
				c.pool.stats.retriesRecovered.Add(1)
			}
			if int64(len(response)) > limit {
				return nil, fmt.Errorf("gateway response exceeds service body limit")
			}
			return response, nil
		}
		// 获取连接失败：请求尚未发出，对所有方法（包括写）都可以安全重试；预算不足时放弃，交给补采。
		if state.connectFailed.Load() {
			giveUp := attempt == 1 || ctx.Err() != nil || noRetry(ctx) || remainingBudget(ctx) < minRetryRemaining
			c.reportFailure(CallFailure{Service: service, Method: method, Target: ep.target, Attempt: attempt, ConnectFailed: true, Retrying: !giveUp, Remaining: remainingBudget(ctx), Err: err})
			if giveUp {
				if attempt == 1 {
					c.pool.stats.retriesFailed.Add(1)
				}
				return nil, err
			}
			connectRetried = true
			failedHost = ep.target
			continue
		}
		code := errs.Code(err)
		// 服务撤离后旧目录的调用方可能先在验签处失败（撤离同时撤销了调用方验证密钥），也需要刷新目录。
		refresh := code == errs.RetClientNetErr || code == errs.RetServerNoService || code == errs.RetServerAuthFail
		// 响应读取阶段失败（连接已被对端关闭或重置）与超时、网络错误一样，请求可能已经发出。
		retry := refresh || code == errs.RetClientTimeout || code == errs.RetClientReadFrameErr
		if refresh && c.config.Mode != External {
			if refreshErr := c.Refresh(ctx); refreshErr != nil {
				c.report(refreshErr)
			}
		}
		// 请求可能已发出：只有只读方法与声明为幂等的写方法可以重发，其余写方法绝不重试。
		giveUp := attempt == 1 || !retry || !c.catalog.Retryable(service, method) || ctx.Err() != nil || noRetry(ctx) || !c.catalog.ReadOnly(service, method) && remainingBudget(ctx) < minRetryRemaining
		c.reportFailure(CallFailure{Service: service, Method: method, Target: ep.target, Attempt: attempt, Retrying: !giveUp, Remaining: remainingBudget(ctx), Err: err})
		if giveUp {
			if connectRetried {
				c.pool.stats.retriesFailed.Add(1)
			}
			return nil, err
		}
		failedHost = ep.target
	}
	panic("unreachable gateway retry attempt")
}

func (c *Client) endpoint(ctx context.Context, service, previous string) (endpoint, error) {
	if c.config.Mode == External {
		return endpoint{address: c.config.AccessAddress, target: c.config.AccessInstanceID, compress: true}, nil
	}
	c.mu.RLock()
	// Published directory maps are immutable. Keep one coherent snapshot without
	// copying the entire fleet on every RPC.
	directory := c.directory
	c.mu.RUnlock()
	hosts := slices.Clone(directory.Services[service])
	if len(hosts) == 0 {
		return endpoint{}, fmt.Errorf("service has no enabled deployment")
	}
	slices.Sort(hosts)
	if i := slices.Index(hosts, c.config.LocalHostID); i >= 0 {
		// 本机优先。不能写成 append([]string{hosts[i]}, append(hosts[:i], hosts[i+1:]...)...)：内层 append 原地改写底层数组，
		// 而字面量里的 hosts[i] 与内层 append 的求值先后不确定，本机排在第一位时会读到被覆盖的值，把调用发到远端主机。
		local := hosts[i]
		hosts = append([]string{local}, slices.Delete(hosts, i, i+1)...)
	}
	chosen := hosts[0]
	if chosen == previous && len(hosts) > 1 {
		chosen = hosts[1]
	}
	if relayHost := directory.Hosts[chosen].Relay; relayHost != "" && chosen != c.config.LocalHostID {
		return c.relayEndpoint(ctx, directory, chosen, relayHost)
	}
	if c.config.Mode == Tunnel {
		address, err := c.config.Tunnels.Resolve(ctx, chosen)
		if err != nil {
			return endpoint{}, err
		}
		if !loopbackAddress(address) {
			return endpoint{}, fmt.Errorf("SSH tunnel returned a non-loopback endpoint")
		}
		// 隧道的本地端口只是 SSH 转发入口，实际经公网到达目标主机，按跨主机调用压缩。
		return endpoint{address: address, target: chosen, compress: true}, nil
	}
	if chosen == c.config.LocalHostID {
		return endpoint{address: c.config.LocalAddress, target: chosen}, nil
	}
	host := directory.Hosts[chosen]
	return endpoint{address: net.JoinHostPort(host.Address, "11003"), target: chosen, caFile: c.config.CAFile, serverName: chosen, compress: true}, nil
}

// relayEndpoint 为只能出站的主机构造经 control 中转的端点；端到端 TLS 仍校验目标主机身份。
func (c *Client) relayEndpoint(ctx context.Context, directory servicecatalog.Directory, target, controlHost string) (endpoint, error) {
	if c.config.CAFile == "" {
		return endpoint{}, fmt.Errorf("host %s is reachable only through the control relay, which requires the MooX CA file", target)
	}
	route := &relayRoute{controlHost: controlHost, target: target, caFile: c.config.CAFile, credentials: c.config.Credentials}
	port := strconv.Itoa(servicecatalog.HostGatewayRemotePort)
	switch {
	case c.config.Mode == Tunnel:
		resolver, ok := c.config.Tunnels.(RelayTunnelResolver)
		if !ok {
			return endpoint{}, fmt.Errorf("tunnel resolver cannot reach the control relay for host %s", target)
		}
		address, err := resolver.ResolveRelay(ctx, controlHost)
		if err != nil {
			return endpoint{}, err
		}
		if !loopbackAddress(address) {
			return endpoint{}, fmt.Errorf("SSH tunnel returned a non-loopback endpoint")
		}
		route.controlAddress = address
	case controlHost == c.config.LocalHostID:
		// control 上的调用方直接连本机的跨主机端口，不绕公网地址。
		route.controlAddress = net.JoinHostPort("127.0.0.1", port)
	default:
		control, ok := directory.Hosts[controlHost]
		if !ok {
			return endpoint{}, fmt.Errorf("relay host %s is not in the directory", controlHost)
		}
		route.controlAddress = net.JoinHostPort(control.Address, port)
	}
	return endpoint{address: relayAddress(target), target: target, caFile: c.config.CAFile, serverName: target, compress: true, relay: route}, nil
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, "/@?# \t\r\n") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func loopbackAddress(address string) bool {
	if !validAddress(address) {
		return false
	}
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type noRetryKey struct{}

// WithoutRetry 让通过该 ctx 发起的调用不在网关客户端内部重试（只读调用默认会换节点重发一次）。
// 调用方自己控制请求节奏（如归档 worker 的避让窗口）时使用：由调用方重试，每次重试前重新检查时间。
func WithoutRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRetryKey{}, true)
}

func noRetry(ctx context.Context) bool {
	v, _ := ctx.Value(noRetryKey{}).(bool)
	return v
}

// SetCallFailureObserver 设置每次尝试失败（含将要重试的尝试）时调用的回调，用来记录失败原因；回调必须快速返回。
func (c *Client) SetCallFailureObserver(observer func(CallFailure)) {
	if observer == nil {
		c.onFailure.Store(nil)
		return
	}
	c.onFailure.Store(&observer)
}

func (c *Client) reportFailure(failure CallFailure) {
	if observer := c.onFailure.Load(); observer != nil {
		(*observer)(failure)
	}
}

// remainingBudget 是 ctx 截止时间前的剩余时间；没有截止时间时视为充足。
func remainingBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return time.Hour
}
