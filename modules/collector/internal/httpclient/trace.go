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
	// StandbyFired 是发出备援请求的次数，StandbyWon 是备援请求先成功的次数。
	StandbyFired, StandbyWon int
}

// maxTraceAttempts 限制每个取数项保留的逐次记录条数。
const maxTraceAttempts = 8

// TraceAttempt 是一次 HTTP 请求的结果。
type TraceAttempt struct {
	Domain string
	Remote string
	// StartedAt 是这次请求的发起时刻，调用方据此换算相对收盘的偏移。
	StartedAt time.Time
	// Phase 是请求结束时所处的阶段：dns、connect、tls、get_conn（等连接）、write、wait_response（已写出请求、没收到首字节）、
	// read_body（已收到响应头），成功的请求为 done。
	Phase string
	// Kind 是失败原因：deadline、canceled、timeout、reset、eof、tls、dial、status_<码>、decode、other；成功为空。
	Kind   string
	Status int
	// Ms 是这次请求的总耗时，IdleMs 是复用连接此前空闲的时间（新建连接为 0）。
	Ms, IdleMs int64
	Reused     bool
	// DNSMs、ConnectMs、TLSMs 是这次请求自己的建连阶段耗时；WroteMs 是从发起到请求写完的耗时（含等连接、建连、握手）；
	// TTFBMs 是请求写完到收到首字节，-1 表示没有收到首字节。
	DNSMs, ConnectMs, TLSMs, WroteMs, TTFBMs int64
	// 响应头里的线索（收到响应才有）：CDN 节点（x-amz-cf-pop）、缓存结果（x-cache）、服务端 Date 与本机收到时刻的差（毫秒，
	// 正数表示服务端时间更晚）、Server。
	Pop, Cache, Server string
	DateSkewMs         int64
	// BodyHint 是非 200 响应体的前 100 个字节（可打印字符），用来弄清 202 等状态码的含义。
	BodyHint string
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

// requestTrace 是一次 HTTP 请求自己的阶段状态：Go 的传输层可能在一次请求内换连接重试，回调因此可能触发多次。
type requestTrace struct {
	t      *Trace
	domain string

	mu                                                       sync.Mutex
	started                                                  time.Time
	dnsStart, connectStart, tlsStart, wroteAt                time.Time
	dnsOpen, connectOpen, tlsOpen, gotConn, wrote, firstByte bool
	dnsMs, connectMs, tlsMs, wroteMs, ttfbMs                 int64
	reused                                                   bool
	idle                                                     time.Duration
	remote                                                   string
	pop, cache, server                                       string
	skewMs                                                   int64
	bodyHint                                                 string
}

// begin 登记一次请求并返回挂了回调的上下文；请求结束（成功、失败、读完响应体）后调用返回值的 done。
func (t *Trace) begin(ctx context.Context, domain string) (context.Context, *requestTrace) {
	r := &requestTrace{t: t, domain: domain, started: time.Now(), ttfbMs: -1}
	t.mu.Lock()
	t.snap.Requests++
	t.snap.LastDomain = domain
	t.snap.LastStatus = 0
	t.mu.Unlock()
	lap := func(from *time.Time, total *int64, shared *int64) {
		r.mu.Lock()
		started := *from
		*from = time.Time{}
		var took int64
		if !started.IsZero() {
			took = time.Since(started).Milliseconds()
			*total += took
		}
		r.mu.Unlock()
		if took > 0 {
			t.mu.Lock()
			*shared += took
			t.mu.Unlock()
		}
	}
	with := func(f func()) {
		r.mu.Lock()
		f()
		r.mu.Unlock()
	}
	hooks := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			with(func() { r.dnsStart, r.dnsOpen = time.Now(), true })
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			lap(&r.dnsStart, &r.dnsMs, &t.snap.DNSMs)
			with(func() { r.dnsOpen = false })
		},
		ConnectStart: func(string, string) {
			with(func() { r.connectStart, r.connectOpen = time.Now(), true })
		},
		ConnectDone: func(string, string, error) {
			lap(&r.connectStart, &r.connectMs, &t.snap.ConnectMs)
			with(func() { r.connectOpen = false })
		},
		TLSHandshakeStart: func() {
			with(func() { r.tlsStart, r.tlsOpen = time.Now(), true })
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			lap(&r.tlsStart, &r.tlsMs, &t.snap.TLSMs)
			with(func() { r.tlsOpen = false })
		},
		GotConn: func(info httptrace.GotConnInfo) {
			remote := ""
			if info.Conn != nil && info.Conn.RemoteAddr() != nil {
				remote = info.Conn.RemoteAddr().String()
			}
			t.mu.Lock()
			if info.Reused {
				t.snap.ReusedConns++
			} else {
				t.snap.NewConns++
			}
			if remote != "" {
				t.snap.RemoteIP = remote
			}
			t.mu.Unlock()
			with(func() {
				r.gotConn, r.reused, r.idle, r.remote = true, info.Reused, info.IdleTime, remote
			})
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			with(func() {
				r.wroteAt, r.wrote = time.Now(), true
				r.wroteMs = time.Since(r.started).Milliseconds()
			})
		},
		GotFirstResponseByte: func() {
			var ttfb int64
			with(func() {
				r.firstByte = true
				if !r.wroteAt.IsZero() {
					ttfb = time.Since(r.wroteAt).Milliseconds()
					r.ttfbMs = ttfb
				}
			})
			t.mu.Lock()
			t.snap.TTFBMs = ttfb
			t.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, hooks), r
}

// response 记录响应头里的线索；r 为 nil 时什么也不做。
func (r *requestTrace) response(header http.Header) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pop, r.cache, r.server = header.Get("X-Amz-Cf-Pop"), header.Get("X-Cache"), header.Get("Server")
	if date, err := http.ParseTime(header.Get("Date")); err == nil {
		r.skewMs = date.Sub(time.Now()).Milliseconds()
	}
}

// body 记录非 200 响应体的前几个字节。
func (r *requestTrace) body(hint []byte) {
	if r == nil {
		return
	}
	cleaned := strings.Map(func(c rune) rune {
		if c < 0x20 || c > 0x7e {
			return ' '
		}
		return c
	}, string(hint))
	r.mu.Lock()
	r.bodyHint = strings.TrimSpace(cleaned)
	r.mu.Unlock()
}

// done 在请求结束时把这次请求的结果记入 Trace。
func (r *requestTrace) done(status int, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	attempt := TraceAttempt{
		Domain: r.domain, StartedAt: r.started, Status: status, Ms: time.Since(r.started).Milliseconds(), Phase: "done",
		Reused: r.reused, IdleMs: r.idle.Milliseconds(), Remote: r.remote,
		DNSMs: r.dnsMs, ConnectMs: r.connectMs, TLSMs: r.tlsMs, WroteMs: r.wroteMs, TTFBMs: r.ttfbMs,
		Pop: r.pop, Cache: r.cache, Server: r.server, DateSkewMs: r.skewMs, BodyHint: r.bodyHint,
	}
	if err != nil {
		attempt.Phase = failurePhase(r.dnsOpen, r.connectOpen, r.tlsOpen, r.gotConn, r.wrote, r.firstByte)
		attempt.Kind = errorKind(err, status)
	}
	r.mu.Unlock()
	t := r.t
	t.mu.Lock()
	t.snap.RequestMs += attempt.Ms
	t.snap.LastStatus = status
	if len(t.snap.Attempts) < maxTraceAttempts {
		t.snap.Attempts = append(t.snap.Attempts, attempt)
	}
	t.mu.Unlock()
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

// AttemptsText 把逐次记录压成一行，供 CLS 检索，分号分隔各次：
// 域名:阶段:原因或状态码:耗时ms:new 或 reused<空闲ms>，后接 | 分隔的明细：
// dns、c（TCP 建连）、t（TLS）、w（发起到请求写完）、f（请求写完到首字节，-1 为没有首字节）、ip（对端地址）、pop、cache、skew（服务端 Date 与本机之差）、body（非 200 的响应体前缀）。
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
		text := a.Domain + ":" + a.Phase + ":" + reason + ":" + strconv.FormatInt(a.Ms, 10) + "ms:" + conn
		extras := []string{
			"dns=" + strconv.FormatInt(a.DNSMs, 10), "c=" + strconv.FormatInt(a.ConnectMs, 10), "t=" + strconv.FormatInt(a.TLSMs, 10),
			"w=" + strconv.FormatInt(a.WroteMs, 10), "f=" + strconv.FormatInt(a.TTFBMs, 10),
		}
		if a.Remote != "" {
			extras = append(extras, "ip="+a.Remote)
		}
		if a.Pop != "" {
			extras = append(extras, "pop="+a.Pop)
		}
		if a.Cache != "" {
			extras = append(extras, "cache="+a.Cache)
		}
		if a.Pop != "" || a.Server != "" || a.Cache != "" {
			extras = append(extras, "skew="+strconv.FormatInt(a.DateSkewMs, 10))
		}
		if a.BodyHint != "" {
			extras = append(extras, "body="+a.BodyHint)
		}
		parts = append(parts, text+"|"+strings.Join(extras, ","))
	}
	return strings.Join(parts, ";")
}

// TraceFromContext 返回上下文里的 Trace，没有则为 nil；备援请求用它登记触发与获胜。
func TraceFromContext(ctx context.Context) *Trace { return traceFrom(ctx) }

// NoteStandby 登记一次备援：fired 表示发出了备援请求，won 表示备援请求先成功。nil 接收者安全。
func (t *Trace) NoteStandby(fired, won bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if fired {
		t.snap.StandbyFired++
	}
	if won {
		t.snap.StandbyWon++
	}
	t.mu.Unlock()
}
