package relay

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	protocol "github.com/mooyang-code/moox/packages/gatewayclient/relay"
)

type ClientOptions struct {
	HostID      string
	ControlHost string
	// Target 是 control 网关的跨主机端点，与拉取快照用的 control.target 相同。
	Target string
	// TLS 带本机网关证书（含 ClientAuth）与 MooX CA；函数会设置 ALPN 与 ServerName。
	TLS         *tls.Config
	Credentials gatewayauth.Credentials
	// Active 报告本机当前是否应保持反向连接（目录里本机由 control 中转）。
	Active func() bool
	// Allowed 报告 control 要求的本机端口能否接入。
	Allowed func(port int) bool
	Now     func() time.Time
	Warn    func(string)
	// MinBackoff、MaxBackoff 是断开后的重连退避，默认 1 秒起、最长 30 秒。
	MinBackoff, MaxBackoff time.Duration
	// DialLocal 连接本机端口，默认连 127.0.0.1:<端口>。
	DialLocal func(port int) (net.Conn, error)
}

// Client 是出站主机网关的反向连接端。
type Client struct {
	options  ClientOptions
	instance string
}

// InstanceID 是本进程在 attach 时声明的网关实例。
func (c *Client) InstanceID() string { return c.instance }

func NewClient(options ClientOptions) (*Client, error) {
	if options.HostID == "" || options.ControlHost == "" || options.Target == "" || options.TLS == nil || options.Active == nil || options.Allowed == nil {
		return nil, errors.New("relay client requires host, control, target, TLS and policy callbacks")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string) {}
	}
	if options.MinBackoff <= 0 {
		options.MinBackoff = time.Second
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = 30 * time.Second
	}
	if options.DialLocal == nil {
		options.DialLocal = func(port int) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		}
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &Client{options: options, instance: hex.EncodeToString(id[:])}, nil
}

// Run 一直保持反向连接直到 ctx 结束：不需要中转时空转等待，断开后指数退避重连。
func (c *Client) Run(ctx context.Context) {
	backoff := c.options.MinBackoff
	for ctx.Err() == nil {
		if !c.options.Active() {
			backoff = c.options.MinBackoff
			sleep(ctx, 5*time.Second)
			continue
		}
		started := time.Now()
		err := c.serveOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		// 会话维持过一段时间才算稳定，下次从最短退避开始。
		if time.Since(started) > c.options.MaxBackoff {
			backoff = c.options.MinBackoff
		}
		if err != nil {
			c.options.Warn("host gateway relay disconnected: host_id=" + c.options.HostID + ": " + err.Error())
		}
		sleep(ctx, backoff)
		backoff = min(backoff*2, c.options.MaxBackoff)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (c *Client) attach(ctx context.Context) (*tls.Conn, error) {
	config := c.options.TLS.Clone()
	config.ServerName = c.options.ControlHost
	config.NextProtos = []string{protocol.ALPNAttach}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", c.options.Target)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, config)
	_ = conn.SetDeadline(time.Now().Add(protocol.PreambleTimeout))
	fail := func(err error) (*tls.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return fail(fmt.Errorf("TLS handshake: %w", err))
	}
	if conn.ConnectionState().NegotiatedProtocol != protocol.ALPNAttach {
		return fail(errors.New("control gateway does not accept relay sessions"))
	}
	signed, err := protocol.Sign(c.options.Credentials, protocol.MethodAttach, c.options.ControlHost, protocol.Attach{HostID: c.options.HostID, InstanceID: c.instance}, c.options.Now())
	if err != nil {
		return fail(err)
	}
	if err := protocol.WriteFrame(conn, signed); err != nil {
		return fail(err)
	}
	var answer protocol.Reply
	if err := protocol.ReadFrame(conn, &answer); err != nil {
		return fail(err)
	}
	if answer.Error != "" {
		return fail(errors.New("attach rejected: " + answer.Error))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (c *Client) serveOnce(ctx context.Context) error {
	conn, err := c.attach(ctx)
	if err != nil {
		return err
	}
	mux, err := yamux.Server(conn, muxConfig())
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer mux.Close()
	// ctx 结束或本机不再需要中转时关闭会话。
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = mux.Close()
				return
			case <-mux.CloseChan():
				return
			case <-ticker.C:
				if !c.options.Active() {
					_ = mux.Close()
					return
				}
			}
		}
	}()
	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			if ctx.Err() != nil || mux.IsClosed() {
				return errors.New("session closed")
			}
			return err
		}
		go c.handleStream(stream)
	}
}

func (c *Client) handleStream(stream *yamux.Stream) {
	_ = stream.SetDeadline(time.Now().Add(protocol.PreambleTimeout))
	var request protocol.Stream
	if err := protocol.ReadFrame(stream, &request); err != nil {
		_ = stream.Close()
		return
	}
	if !protocol.ValidPort(request.Port) || !c.options.Allowed(request.Port) {
		reply(stream, "port is not allowed on the outbound host")
		_ = stream.Close()
		return
	}
	local, err := c.options.DialLocal(request.Port)
	if err != nil {
		reply(stream, "local port unavailable")
		_ = stream.Close()
		return
	}
	reply(stream, "")
	_ = stream.SetDeadline(time.Time{})
	pipe(stream, local)
}
