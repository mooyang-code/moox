package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Trace 汇总同一个取数项（一个标的）在若干次 HTTP 请求里的阶段耗时，用来判断慢在 DNS、建连、TLS 握手，
// 还是行情源本身的响应。一个 Trace 只服务一个取数项，请求阶段的回调可能来自拨号协程，所以内部加锁。
type Trace struct {
	mu   sync.Mutex
	snap TraceSnapshot
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
	// Attempts 是逐次请求的记录（最多 maxTraceAttempts 条，含成功的），用来看失败的那一次停在哪个阶段、为什么失败。
	Attempts []TraceAttempt
	// HedgeFired 是发出对冲请求的次数，HedgeWon 是对冲请求先成功的次数。
	HedgeFired, HedgeWon int
}

// maxTraceAttempts 限制每个取数项保留的逐次记录条数。
const maxTraceAttempts = 8

// TraceAttempt 是一次 HTTP 请求的结果。
type TraceAttempt struct {
	Domain string
	Remote string
	// Phase 是请求结束时所处的阶段：dns、connect、tls、get_conn（等连接）、write、wait_response（已写出请求、没收到首字节）、
	// read_body（已收到响应头），成功的请求为 done。
	Phase string
	// Kind 是失败原因：deadline、canceled、timeout、reset、eof、tls、dial、status_<码>、decode、other；成功为空。
	Kind   string
	Status int
	// Ms 是这次请求的总耗时，IdleMs 是复用连接此前空闲的时间（新建连接为 0）。
	Ms, IdleMs int64
	Reused     bool
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

// begin 登记一次请求并返回挂了回调的上下文；done 在请求结束（成功、失败、读完响应体）后调用，err 为这次请求的结果。
func (t *Trace) begin(ctx context.Context, domain string) (context.Context, func(status int, err error)) {
	started := time.Now()
	t.mu.Lock()
	t.snap.Requests++
	t.snap.LastDomain = domain
	t.snap.LastStatus = 0
	t.mu.Unlock()
	// 本次请求自己的阶段状态：Go 的传输层可能在一次请求内换连接重试，回调因此可能触发多次。
	var req struct {
		sync.Mutex
		// 各阶段的起点放在请求自己的状态里：对冲请求会让同一个 Trace 并发发出多个请求，不能共用起点。
		dnsStart, connectStart, tlsStart, wroteAt                time.Time
		dnsOpen, connectOpen, tlsOpen, gotConn, wrote, firstByte bool
		reused                                                   bool
		idle                                                     time.Duration
		remote                                                   string
	}
	with := func(f func()) {
		req.Lock()
		f()
		req.Unlock()
	}
	// lap 把从 from 到现在的毫秒数累加到快照字段 total，并清除起点。
	lap := func(from *time.Time, total *int64) {
		req.Lock()
		started := *from
		*from = time.Time{}
		req.Unlock()
		if started.IsZero() {
			return
		}
		t.mu.Lock()
		*total += time.Since(started).Milliseconds()
		t.mu.Unlock()
	}
	hooks := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			with(func() { req.dnsStart, req.dnsOpen = time.Now(), true })
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			lap(&req.dnsStart, &t.snap.DNSMs)
			with(func() { req.dnsOpen = false })
		},
		ConnectStart: func(string, string) {
			with(func() { req.connectStart, req.connectOpen = time.Now(), true })
		},
		ConnectDone: func(string, string, error) {
			lap(&req.connectStart, &t.snap.ConnectMs)
			with(func() { req.connectOpen = false })
		},
		TLSHandshakeStart: func() {
			with(func() { req.tlsStart, req.tlsOpen = time.Now(), true })
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			lap(&req.tlsStart, &t.snap.TLSMs)
			with(func() { req.tlsOpen = false })
		},
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
			with(func() {
				req.gotConn, req.reused, req.idle = true, info.Reused, info.IdleTime
				if info.Conn != nil && info.Conn.RemoteAddr() != nil {
					req.remote = info.Conn.RemoteAddr().String()
				}
			})
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			with(func() { req.wroteAt, req.wrote = time.Now(), true })
		},
		GotFirstResponseByte: func() {
			var ttfb int64
			with(func() {
				req.firstByte = true
				if !req.wroteAt.IsZero() {
					ttfb = time.Since(req.wroteAt).Milliseconds()
				}
			})
			t.mu.Lock()
			t.snap.TTFBMs = ttfb
			t.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, hooks), func(status int, err error) {
		attempt := TraceAttempt{Domain: domain, Status: status, Ms: time.Since(started).Milliseconds(), Phase: "done"}
		req.Lock()
		attempt.Reused, attempt.IdleMs, attempt.Remote = req.reused, req.idle.Milliseconds(), req.remote
		if err != nil {
			attempt.Phase = failurePhase(req.dnsOpen, req.connectOpen, req.tlsOpen, req.gotConn, req.wrote, req.firstByte)
			attempt.Kind = errorKind(err, status)
		}
		req.Unlock()
		t.mu.Lock()
		t.snap.RequestMs += attempt.Ms
		t.snap.LastStatus = status
		if len(t.snap.Attempts) < maxTraceAttempts {
			t.snap.Attempts = append(t.snap.Attempts, attempt)
		}
		t.mu.Unlock()
	}
}

// failurePhase 按已发生的回调推断请求失败时停在哪个阶段。
func failurePhase(dnsOpen, connectOpen, tlsOpen, gotConn, wrote, firstByte bool) string {
	switch {
	case firstByte:
		return "read_body"
	case wrote:
		return "wait_response"
	case gotConn:
		return "write"
	case tlsOpen:
		return "tls"
	case connectOpen:
		return "connect"
	case dnsOpen:
		return "dns"
	default:
		return "get_conn"
	}
}

// errorKind 把请求错误归成少数几类，便于在 CLS 里统计；状态码错误带上码值。
func errorKind(err error, status int) string {
	var statusErr *StatusError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &statusErr):
		return "status_" + strconv.Itoa(statusErr.StatusCode)
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "reset"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	var tlsErr tls.RecordHeaderError
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &tlsErr) || errors.As(err, &certErr) {
		return "tls"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "dial"
	}
	if status == http.StatusOK {
		return "decode"
	}
	return "other"
}

// AttemptsText 把逐次记录压成一行，供 CLS 检索：域名:阶段:原因:耗时ms:连接(空闲)，用分号分隔。
func AttemptsText(attempts []TraceAttempt) string {
	parts := make([]string, 0, len(attempts))
	for _, a := range attempts {
		reason := a.Kind
		if reason == "" {
			reason = strconv.Itoa(a.Status)
		}
		conn := "new"
		if a.Reused {
			conn = "reused" + strconv.FormatInt(a.IdleMs, 10) + "ms"
		}
		parts = append(parts, a.Domain+":"+a.Phase+":"+reason+":"+strconv.FormatInt(a.Ms, 10)+"ms:"+conn)
	}
	return strings.Join(parts, ";")
}

// TraceFromContext 返回上下文里的 Trace，没有则为 nil；对冲请求用它登记触发与获胜。
func TraceFromContext(ctx context.Context) *Trace { return traceFrom(ctx) }

// NoteHedge 登记一次对冲：fired 表示发出了对冲请求，won 表示对冲请求先成功。nil 接收者安全。
func (t *Trace) NoteHedge(fired, won bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if fired {
		t.snap.HedgeFired++
	}
	if won {
		t.snap.HedgeWon++
	}
	t.mu.Unlock()
}
