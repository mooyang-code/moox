// Package relay 定义主机网关反向连接（中转）的线上协议，由 control 网关、出站主机网关与调用方共用。
//
// 连接都打到 control 网关的跨主机 TLS 端口，按 ALPN 区分：
//   - ALPNAttach：出站主机的网关提交含 ClientAuth 的证书，发送签名的 Attach 帧，通过后这条连接成为多路复用会话；
//   - ALPNOpen：调用方发送签名的 Open 帧，通过后这条 TLS 连接直接透传到出站主机本机的目标端口。
//
// 帧是 4 字节大端长度加 JSON，长度有上限，未知字段拒绝。签名沿用网关调用方签名，TargetNode 是 control。
package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

const (
	ALPNAttach = "moox-relay-attach"
	ALPNOpen   = "moox-relay-open"

	// Service 只用于签名里的路径与被调方，不是可路由的 tRPC 服务。
	Service      = "trpc.moox.hostgateway.Relay"
	MethodAttach = "Attach"
	MethodOpen   = "Open"

	// PreambleTimeout 是握手后完成签名前导与应答的上限。
	PreambleTimeout = 10 * time.Second
	maxFrame        = 16 << 10
)

// Attach 由出站主机的网关发出，声明自己的主机与网关实例。
type Attach struct {
	HostID     string `json:"host_id"`
	InstanceID string `json:"instance_id"`
}

// Open 由调用方发出，指定出站主机与它本机的目标端口。
type Open struct {
	HostID string `json:"host_id"`
	Port   int    `json:"port"`
}

// Stream 由 control 在会话的每条流开头发给出站主机，指定本机目标端口。
type Stream struct {
	Port int `json:"port"`
}

// Reply 是每一步的应答，Error 为空表示成功。错误文本不含密钥或上游内容。
type Reply struct {
	Error string `json:"error,omitempty"`
}

// Signed 是带调用方签名的前导，Body 是 Attach 或 Open 的 JSON。
type Signed struct {
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

// Request 返回校验签名用的请求描述。
func (s Signed) Request(method, controlHost string) gatewayauth.Request {
	return request(method, controlHost, s.Body)
}

func request(method, controlHost string, body []byte) gatewayauth.Request {
	return gatewayauth.Request{Method: "POST", Path: "/" + Service + "/" + method, TargetNode: controlHost, Callee: Service, Func: method, Body: body}
}

// Sign 生成签名前导。
func Sign(credentials gatewayauth.Credentials, method, controlHost string, body any, now time.Time) (Signed, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Signed{}, err
	}
	headers, err := gatewayauth.Sign(credentials, request(method, controlHost, raw), now)
	if err != nil {
		return Signed{}, err
	}
	return Signed{Headers: headers, Body: raw}, nil
}

// Decode 严格解析前导里的 JSON。
func Decode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid relay message")
	}
	if decoder.More() {
		return errors.New("invalid relay message")
	}
	return nil
}

// WriteFrame 写一帧。
func WriteFrame(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > maxFrame {
		return errors.New("relay frame exceeds its limit")
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	_, err = w.Write(frame)
	return err
}

// ReadFrame 读一帧并严格解析。
func ReadFrame(r io.Reader, value any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > maxFrame {
		return errors.New("relay frame exceeds its limit")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	return Decode(raw, value)
}

// ValidPort 检查 TCP 端口范围。
func ValidPort(port int) bool { return port > 0 && port <= 65535 }

// OpenOptions 描述一次经 control 中转的连接。
type OpenOptions struct {
	// Address 是 control 网关的跨主机端点（host:port），TLS 校验 ServerName 为 control 主机 ID。
	Address     string
	ControlHost string
	// TLS 用于与 control 网关的外层 TLS；函数会复制并设置 ALPN 与 ServerName。
	TLS         *tls.Config
	Credentials gatewayauth.Credentials
	Target      Open
	Dialer      *net.Dialer
	Now         func() time.Time
}

// Dial 与 control 网关建立外层 TLS，发送签名的打开流前导，成功后返回透传到目标端口的连接。
// 调用方在返回的连接上再做端到端 TLS（RPC）或直接说 HTTP（健康探测）。
func Dial(ctx context.Context, options OpenOptions) (net.Conn, error) {
	if options.TLS == nil || options.ControlHost == "" || !ValidPort(options.Target.Port) || options.Target.HostID == "" {
		return nil, errors.New("relay dial requires TLS, control host, target host and port")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 5 * time.Second}
	}
	config := options.TLS.Clone()
	config.ServerName = options.ControlHost
	config.NextProtos = []string{ALPNOpen}
	raw, err := dialer.DialContext(ctx, "tcp", options.Address)
	if err != nil {
		return nil, fmt.Errorf("relay dial control: %w", err)
	}
	conn := tls.Client(raw, config)
	deadline := time.Now().Add(PreambleTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetDeadline(deadline)
	fail := func(err error) (net.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return fail(fmt.Errorf("relay TLS handshake: %w", err))
	}
	if conn.ConnectionState().NegotiatedProtocol != ALPNOpen {
		return fail(errors.New("control gateway does not support relay"))
	}
	signed, err := Sign(options.Credentials, MethodOpen, options.ControlHost, options.Target, options.Now())
	if err != nil {
		return fail(err)
	}
	if err := WriteFrame(conn, signed); err != nil {
		return fail(fmt.Errorf("relay open: %w", err))
	}
	var reply Reply
	if err := ReadFrame(conn, &reply); err != nil {
		return fail(fmt.Errorf("relay open reply: %w", err))
	}
	if reply.Error != "" {
		return fail(fmt.Errorf("relay open rejected: %s", reply.Error))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
