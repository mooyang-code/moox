package httpclient

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// Trace 汇总同一个取数项（一个标的）在若干次 HTTP 请求里的阶段耗时，用来判断慢在 DNS、建连、TLS 握手，
// 还是行情源本身的响应。一个 Trace 只服务一个取数项，请求阶段的回调可能来自拨号协程，所以内部加锁。
type Trace struct {
	mu   sync.Mutex
	snap TraceSnapshot

	dnsStart, connectStart, tlsStart, wroteAt time.Time
}

// TraceSnapshot 是 Trace 的一份只读拷贝。耗时单位为毫秒，同一项的多次请求（换 IP、换端点、重试）累加。
type TraceSnapshot struct {
	// Requests 是实际发出的 HTTP 请求次数（含失败）。
	Requests int
	// NewConns、ReusedConns 是新建连接与复用连接的请求数。
	NewConns, ReusedConns   int
	DNSMs, ConnectMs, TLSMs int64
	// TTFBMs 是最后一次请求从写完请求头到收到响应首字节的耗时，即行情源的处理与网络往返时间。
	TTFBMs int64
	// RequestMs 是各次请求从发起到读完响应（或失败）的总耗时。
	RequestMs int64
	// LastStatus 是最后一次收到的 HTTP 状态码，0 表示没有收到响应。
	LastStatus int
	// LastDomain 是最后一次请求的端点域名，RemoteIP 是最后一次建立或复用的连接对端地址。
	LastDomain, RemoteIP string
}

type traceKey struct{}

// WithTrace 让后续经 getWithClient 发出的请求把阶段耗时记入 trace；trace 为 nil 时原样返回。
func WithTrace(ctx context.Context, trace *Trace) context.Context {
	if trace == nil {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, trace)
}

func traceFrom(ctx context.Context) *Trace {
	trace, _ := ctx.Value(traceKey{}).(*Trace)
	return trace
}

// Snapshot 返回当前累计值。
func (t *Trace) Snapshot() TraceSnapshot {
	if t == nil {
		return TraceSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snap
}

// begin 登记一次请求并返回挂了回调的上下文；done 在请求结束（成功、失败、读完响应体）后调用。
func (t *Trace) begin(ctx context.Context, domain string) (context.Context, func(status int)) {
	started := time.Now()
	t.mu.Lock()
	t.snap.Requests++
	t.snap.LastDomain = domain
	t.snap.LastStatus = 0
	t.mu.Unlock()
	hooks := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { t.mark(&t.dnsStart) },
		DNSDone:           func(httptrace.DNSDoneInfo) { t.addSince(&t.dnsStart, &t.snap.DNSMs) },
		ConnectStart:      func(string, string) { t.mark(&t.connectStart) },
		ConnectDone:       func(string, string, error) { t.addSince(&t.connectStart, &t.snap.ConnectMs) },
		TLSHandshakeStart: func() { t.mark(&t.tlsStart) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { t.addSince(&t.tlsStart, &t.snap.TLSMs) },
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			if info.Reused {
				t.snap.ReusedConns++
			} else {
				t.snap.NewConns++
			}
			if info.Conn != nil && info.Conn.RemoteAddr() != nil {
				t.snap.RemoteIP = info.Conn.RemoteAddr().String()
			}
			t.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { t.mark(&t.wroteAt) },
		GotFirstResponseByte: func() {
			t.mu.Lock()
			if !t.wroteAt.IsZero() {
				t.snap.TTFBMs = time.Since(t.wroteAt).Milliseconds()
			}
			t.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, hooks), func(status int) {
		t.mu.Lock()
		t.snap.RequestMs += time.Since(started).Milliseconds()
		t.snap.LastStatus = status
		t.mu.Unlock()
	}
}

func (t *Trace) mark(at *time.Time) {
	t.mu.Lock()
	*at = time.Now()
	t.mu.Unlock()
}

func (t *Trace) addSince(from *time.Time, total *int64) {
	t.mu.Lock()
	if !from.IsZero() {
		*total += time.Since(*from).Milliseconds()
		*from = time.Time{}
	}
	t.mu.Unlock()
}
