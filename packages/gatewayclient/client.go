// Package gatewayclient 是 MooX 组件、外部调用方和 moox-cli 调用 tRPC 服务的唯一客户端。
//
// 它按服务目录选出目标主机网关，对实际发出的字节签名，再经 tRPC 发送：
//   - Invoke 序列化请求对象后发送，供各组件之间的普通调用使用；
//   - Forward 保留调用方给定的序列化类型，直接对原始字节签名并原样发送，供外部接入和控制台透传使用；
//   - ClientOptions 让生成的 tRPC 客户端桩走同一条路径。
package gatewayclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

const directoryFetchTimeout = 3 * time.Second

// Tunnel 是隧道方式下的 SSH 隧道：返回连到目标主机 127.0.0.1:11002 的本地地址。
type Tunnel interface {
	Address(ctx context.Context, hostID string) (string, error)
}

// Options 是 New 的参数。
type Options struct {
	Config Config
	// Credentials 直接给出签名凭据时不读 key_file，供 SCF 环境变量和测试使用。
	Credentials *gatewayauth.Credentials
	// Tunnel 是隧道方式必需的 SSH 隧道。
	Tunnel Tunnel
	// Source 覆盖默认的目录来源，供测试使用。
	Source Source
	// RemoteAddress 覆盖跨主机目标地址的计算，供在一台机器上模拟多台主机的测试使用。
	RemoteAddress   func(local, target servicecatalog.DirectoryHost) string
	RefreshInterval time.Duration
	Catalog         *servicecatalog.Catalog
	Now             func() time.Time
}

// Client 是并发安全的网关客户端，应在进程内长期复用。
type Client struct {
	mode          Mode
	credentials   gatewayauth.Credentials
	caFile        string
	localAddress  string
	accessAddress string
	accessID      string
	tunnel        Tunnel
	directory     *directoryState
	remoteAddress func(local, target servicecatalog.DirectoryHost) string
	catalog       *servicecatalog.Catalog
	now           func() time.Time
	// pool 是客户端自己的连接池：tRPC 默认连接池只按网络、地址和协议区分连接，
	// 不同的 TLS 设置（或明文与 TLS）会共用同一条连接；它也检测不出失效的 TLS 连接。
	pool connpool.Pool
}

// New 按配置创建客户端。内部方式和隧道方式会在后台每 15 秒比对一次服务目录版本。
func New(options Options) (*Client, error) {
	// 相对路径按进程工作目录解析，只展开 ~/。
	config, err := options.Config.ResolvePaths("")
	if err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var credentials gatewayauth.Credentials
	if options.Credentials != nil {
		credentials = *options.Credentials
	} else {
		loaded, err := gatewayauth.LoadCallerKey(config.KeyFile)
		if err != nil {
			return nil, err
		}
		credentials = loaded
	}
	if credentials.Caller == "" {
		credentials.Caller = config.Caller
	}
	if credentials.Caller != strings.TrimSpace(config.Caller) {
		return nil, fmt.Errorf("密钥属于调用方 %s，但配置的调用方是 %s", credentials.Caller, config.Caller)
	}
	c := &Client{
		mode: config.Mode, credentials: credentials, caFile: strings.TrimSpace(config.CAFile),
		localAddress: strings.TrimSpace(config.LocalAddress), accessAddress: strings.TrimSpace(config.AccessAddress),
		accessID: strings.TrimSpace(config.AccessID), tunnel: options.Tunnel, remoteAddress: options.RemoteAddress,
		catalog: options.Catalog, now: options.Now, pool: NewConnectionPool(),
	}
	if c.localAddress == "" {
		c.localAddress = DefaultLocalAddress
	}
	if c.catalog == nil {
		c.catalog = servicecatalog.Default()
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.remoteAddress == nil {
		c.remoteAddress = defaultRemoteAddress
	}
	source := options.Source
	switch config.Mode {
	case ModeLocal:
		if source == nil {
			address := c.localAddress
			source = rpcSource{address: func(context.Context) (string, error) { return address, nil }, timeout: directoryFetchTimeout, pool: c.pool}
		}
		c.directory = newDirectoryState(source, config.CacheDir, options.RefreshInterval, false)
	case ModeTunnel:
		if options.Tunnel == nil {
			return nil, errors.New("隧道方式必须提供 SSH 隧道")
		}
		if source == nil {
			tunnel := options.Tunnel
			source = rpcSource{address: func(ctx context.Context) (string, error) {
				return tunnel.Address(ctx, servicecatalog.ControlHostID)
			}, timeout: directoryFetchTimeout, pool: c.pool}
		}
		c.directory = newDirectoryState(source, config.CacheDir, options.RefreshInterval, true)
	}
	if c.directory != nil {
		c.directory.start()
	}
	return c, nil
}

// Close 停止后台刷新。
func (c *Client) Close() {
	if c.directory != nil {
		c.directory.close()
	}
}

// Caller 返回签名使用的调用方身份。
func (c *Client) Caller() string { return c.credentials.Caller }

// Directory 返回当前服务目录；外部方式没有服务目录。
func (c *Client) Directory(ctx context.Context) (View, error) {
	if c.directory == nil {
		return View{}, errors.New("外部方式没有服务目录")
	}
	return c.directory.current(ctx)
}

// CallOption 调整单次调用。
type CallOption func(*callOptions)

type callOptions struct {
	host          string
	timeout       time.Duration
	serialization int
	metadata      map[string][]byte
}

// WithHost 指定目标主机，用于每台主机都有一份的服务（例如主机采集器）。
func WithHost(hostID string) CallOption {
	return func(o *callOptions) { o.host = strings.TrimSpace(hostID) }
}

// WithTimeout 设置单次调用的超时。
func WithTimeout(timeout time.Duration) CallOption {
	return func(o *callOptions) { o.timeout = timeout }
}

// WithSerialization 设置 Invoke 的序列化类型，默认 PB。
func WithSerialization(serialization int) CallOption {
	return func(o *callOptions) { o.serialization = serialization }
}

// WithMetadata 附加透传给目标服务的 tRPC 元数据。以 x-moox- 开头的键会被主机网关丢弃。
func WithMetadata(key string, value []byte) CallOption {
	return func(o *callOptions) {
		if o.metadata == nil {
			o.metadata = map[string][]byte{}
		}
		o.metadata[key] = append([]byte(nil), value...)
	}
}

func buildCallOptions(opts []CallOption) callOptions {
	out := callOptions{serialization: codec.SerializationTypePB}
	for _, opt := range opts {
		opt(&out)
	}
	return out
}

// Invoke 序列化请求对象，对序列化后的字节签名并发送，再把响应反序列化到 rsp。
func (c *Client) Invoke(ctx context.Context, servicePath, method string, req, rsp any, opts ...CallOption) error {
	options := buildCallOptions(opts)
	body, err := codec.Marshal(options.serialization, req)
	if err != nil {
		return fmt.Errorf("序列化 %s/%s 请求: %w", servicePath, method, err)
	}
	out, err := c.do(ctx, servicePath, method, body, options)
	if err != nil {
		return err
	}
	if err := codec.Unmarshal(options.serialization, out, rsp); err != nil {
		return fmt.Errorf("解析 %s/%s 响应: %w", servicePath, method, err)
	}
	return nil
}

// Forward 保留给定的序列化类型，直接对 body 签名并原样发送，返回原始响应字节。
func (c *Client) Forward(ctx context.Context, servicePath, method string, serialization int, body []byte, opts ...CallOption) ([]byte, error) {
	options := buildCallOptions(opts)
	options.serialization = serialization
	return c.do(ctx, servicePath, method, body, options)
}

// ClientOptions 返回给生成的 tRPC 客户端桩使用的选项：桩照常序列化请求，由一个终结过滤器
// 完成选路、签名和发送，不再经过 tRPC 自带的寻址。
func (c *Client) ClientOptions(opts ...CallOption) []client.Option {
	return []client.Option{client.WithFilter(c.stubFilter(opts))}
}

func (c *Client) stubFilter(base []CallOption) filter.ClientFilter {
	return func(ctx context.Context, req, rsp interface{}, _ filter.ClientHandleFunc) error {
		msg := codec.Message(ctx)
		servicePath, method := msg.CalleeServiceName(), msg.CalleeMethod()
		if servicePath == "" || method == "" {
			servicePath, method, _ = splitRPCName(msg.ClientRPCName())
		}
		if servicePath == "" || method == "" {
			return errors.New("生成桩没有给出 tRPC 服务名和方法名")
		}
		options := buildCallOptions(base)
		options.serialization = msg.SerializationType()
		for key, value := range msg.ClientMetaData() {
			if options.metadata == nil {
				options.metadata = map[string][]byte{}
			}
			options.metadata[key] = value
		}
		if deadline, ok := ctx.Deadline(); ok && options.timeout == 0 {
			options.timeout = time.Until(deadline)
		}
		body, err := codec.Marshal(options.serialization, req)
		if err != nil {
			return fmt.Errorf("序列化 %s/%s 请求: %w", servicePath, method, err)
		}
		out, err := c.do(ctx, servicePath, method, body, options)
		if err != nil {
			return err
		}
		return codec.Unmarshal(options.serialization, out, rsp)
	}
}

type target struct {
	hostID  string
	address string
	tls     bool
}

func (c *Client) do(ctx context.Context, servicePath, method string, body []byte, options callOptions) ([]byte, error) {
	attempts := 1
	if c.catalog.IsReadOnly(servicePath, method) {
		attempts = 2
	}
	failed := map[string]bool{}
	refreshed := false
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		chosen, err := c.pick(ctx, servicePath, options.host, failed)
		if err != nil && !refreshed && c.directory != nil && errs.Code(err) == gatewayroute.RetServiceNotHere {
			// 本地目录里没有可用的部署，目录可能已过期：刷新后再选一次。请求还没有发出，任何方法都可以重选。
			refreshed = true
			if c.directory.refresh(ctx) == nil {
				chosen, err = c.pick(ctx, servicePath, options.host, failed)
			}
		}
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		out, err := c.send(ctx, chosen, servicePath, method, body, options)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !needsDirectoryRefresh(err) {
			return nil, err
		}
		failed[chosen.hostID] = true
		if c.directory != nil {
			refreshed = true
			_ = c.directory.refresh(ctx)
		}
	}
	return nil, lastErr
}

// needsDirectoryRefresh 判断错误是否说明目录可能过期：服务不在目标主机、目标主机已停用、连不上，
// 或连接在收到响应前断开（例如目标主机网关正在重启）。只读方法遇到这些错误时刷新目录后重试一次。
func needsDirectoryRefresh(err error) bool {
	switch errs.Code(err) {
	case gatewayroute.RetServiceNotHere, gatewayroute.RetHostDisabled, errs.RetClientConnectFail, errs.RetClientNetErr, errs.RetClientReadFrameErr:
		return true
	default:
		return false
	}
}

func (c *Client) pick(ctx context.Context, servicePath, host string, failed map[string]bool) (target, error) {
	if c.mode == ModeAccess {
		return target{hostID: c.accessID, address: c.accessAddress}, nil
	}
	view, err := c.directory.current(ctx)
	if err != nil {
		return target{}, err
	}
	hosts := view.Directory.ServiceHostIDs(servicePath)
	if host != "" {
		if !contains(hosts, host) {
			return target{}, errs.New(gatewayroute.RetServiceNotHere, fmt.Sprintf("服务 %s 不在主机 %s 上", servicePath, host))
		}
		hosts = []string{host}
	}
	if len(hosts) == 0 {
		return target{}, errs.New(gatewayroute.RetServiceNotHere, fmt.Sprintf("服务 %s 没有已启用的部署", servicePath))
	}
	ordered := orderHosts(hosts, view.LocalHostID, failed)
	hostID := ordered[0]
	if c.mode == ModeTunnel {
		address, err := c.tunnel.Address(ctx, hostID)
		if err != nil {
			return target{}, fmt.Errorf("建立到主机 %s 的隧道失败: %w", hostID, err)
		}
		return target{hostID: hostID, address: address}, nil
	}
	if hostID == view.LocalHostID {
		return target{hostID: hostID, address: c.localAddress}, nil
	}
	targetHost, ok := view.Directory.Host(hostID)
	if !ok {
		return target{}, fmt.Errorf("服务目录中没有主机 %s 的地址", hostID)
	}
	localHost, _ := view.Directory.Host(view.LocalHostID)
	return target{hostID: hostID, address: c.remoteAddress(localHost, targetHost), tls: true}, nil
}

// orderHosts 优先本机，其次按主机 ID 排序；刚失败的主机排到最后。
func orderHosts(hosts []string, local string, failed map[string]bool) []string {
	ordered := append([]string(nil), hosts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if failed[left] != failed[right] {
			return !failed[left]
		}
		if (left == local) != (right == local) {
			return left == local
		}
		return left < right
	})
	return ordered
}

// defaultRemoteAddress 选择跨主机地址：同一地域且双方都有私网地址时走私网，否则走公网。
func defaultRemoteAddress(local, targetHost servicecatalog.DirectoryHost) string {
	address := targetHost.Address
	if targetHost.PrivateAddress != "" && local.PrivateAddress != "" && local.Region != "" && local.Region == targetHost.Region {
		address = targetHost.PrivateAddress
	}
	return net.JoinHostPort(address, RemotePort)
}

func (c *Client) send(ctx context.Context, chosen target, servicePath, method string, body []byte, options callOptions) ([]byte, error) {
	rpcName := "/" + servicePath + "/" + method
	headers, err := gatewayauth.Sign(c.credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: rpcName, TargetNode: chosen.hostID, Caller: c.credentials.Caller,
		Callee: servicePath, Func: method, Body: body,
	}, c.now())
	if err != nil {
		return nil, fmt.Errorf("签名 %s 失败: %w", rpcName, err)
	}
	callCtx, msg := codec.WithNewMessage(ctx)
	msg.WithClientRPCName(rpcName)
	msg.WithCalleeServiceName(servicePath)
	msg.WithCalleeMethod(method)
	invokeOptions := []client.Option{
		client.WithTarget("ip://" + chosen.address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(servicePath), client.WithCalleeMethod(method),
		client.WithSerializationType(options.serialization),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithPool(c.pool),
	}
	if chosen.tls {
		if c.caFile == "" {
			return nil, errors.New("跨主机调用需要 MooX 私有 CA 证书")
		}
		invokeOptions = append(invokeOptions, client.WithTLS("", "", c.caFile, chosen.hostID))
	}
	if options.timeout > 0 {
		invokeOptions = append(invokeOptions, client.WithTimeout(options.timeout))
	}
	for key, value := range options.metadata {
		if strings.HasPrefix(strings.ToLower(key), "x-moox-") {
			continue
		}
		invokeOptions = append(invokeOptions, client.WithMetaData(key, value))
	}
	for key, values := range headers {
		if len(values) == 1 {
			invokeOptions = append(invokeOptions, client.WithMetaData(key, []byte(values[0])))
		}
	}
	rsp := &codec.Body{}
	if err := client.New().Invoke(callCtx, &codec.Body{Data: body}, rsp, invokeOptions...); err != nil {
		return nil, err
	}
	return rsp.Data, nil
}

func splitRPCName(rpcName string) (string, string, bool) {
	servicePath, method, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(rpcName), "/"), "/")
	return servicePath, method, ok && servicePath != "" && method != ""
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
