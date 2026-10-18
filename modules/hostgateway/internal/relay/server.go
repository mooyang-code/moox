// Package relay 实现主机网关的反向连接：control 侧接受出站主机的会话并为调用方打开中转流，
// 出站主机侧保持会话、把流接到本机端口。control 只转发字节，端到端 TLS 与业务签名仍由调用方与目标网关完成。
package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	protocol "github.com/mooyang-code/moox/packages/gatewayclient/relay"
)

type nonceConsumer interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

// Limits 限制中转资源，防止单个来源或会话耗尽 control。
type Limits struct {
	TotalStreams      int
	StreamsPerSession int
}

func (l Limits) withDefaults() Limits {
	if l.TotalStreams <= 0 {
		l.TotalStreams = 4096
	}
	if l.StreamsPerSession <= 0 {
		l.StreamsPerSession = 1024
	}
	return l
}

type ServerOptions struct {
	State  *snapshot.State
	Nonces nonceConsumer
	Limits Limits
	Now    func() time.Time
	Warn   func(string)
}

// Status 是一台出站主机的会话状态，写进网关心跳。
type Status struct {
	HostID      string
	Online      bool
	InstanceID  string
	ConnectedAt time.Time
	Reconnects  int64
	Streams     int
}

type session struct {
	mux       *yamux.Session
	instance  string
	connected time.Time
	streams   int
}

type hostRecord struct {
	current    *session
	reconnects int64
	attached   bool
}

// Server 是 control 网关的中转端。
type Server struct {
	options ServerOptions
	mu      sync.Mutex
	hosts   map[string]*hostRecord
	streams int
	closed  bool
}

func NewServer(options ServerOptions) (*Server, error) {
	if options.State == nil || options.Nonces == nil {
		return nil, errors.New("relay server requires snapshot and durable nonce store")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(s string) { log.Print(s) }
	}
	options.Limits = options.Limits.withDefaults()
	return &Server{options: options, hosts: map[string]*hostRecord{}}, nil
}

func muxConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	// 每 15 秒心跳，应答等待 30 秒：最长约 45 秒判定断开。
	config.KeepAliveInterval = 15 * time.Second
	config.ConnectionWriteTimeout = 30 * time.Second
	config.StreamOpenTimeout = 10 * time.Second
	config.LogOutput = io.Discard
	return config
}

func reply(conn net.Conn, message string) {
	_ = protocol.WriteFrame(conn, protocol.Reply{Error: message})
}

// verify 读取并校验签名前导，返回已消费 nonce 的调用方。
func (s *Server) verify(conn net.Conn, method string, body any) (*snapshot.View, string, error) {
	view := s.options.State.Load()
	if view == nil {
		return nil, "", errors.New("gateway snapshot not initialized")
	}
	var signed protocol.Signed
	if err := protocol.ReadFrame(conn, &signed); err != nil {
		return nil, "", errors.New("invalid relay preamble")
	}
	if err := protocol.Decode(signed.Body, body); err != nil {
		return nil, "", err
	}
	claims, err := view.Verify(signed.Request(method, view.HostID()), signed.Headers, s.options.Now())
	if err != nil {
		return nil, "", errors.New("relay authentication failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	consumed, err := s.options.Nonces.Consume(ctx, "host-gateway-relay:"+claims.KeyID, claims.Nonce, claims.TTL)
	if err != nil {
		return nil, "", errors.New("relay replay store unavailable")
	}
	if !consumed {
		return nil, "", errors.New("relay request replayed")
	}
	return view, claims.Caller, nil
}

// HandleAttach 接管一条已完成 TLS 握手（ALPN 为 attach、已校验客户端证书链）的连接。
func (s *Server) HandleAttach(conn *tls.Conn) {
	_ = conn.SetDeadline(time.Now().Add(protocol.PreambleTimeout))
	var attach protocol.Attach
	view, caller, err := s.verify(conn, protocol.MethodAttach, &attach)
	if err == nil {
		err = s.checkAttach(conn, view, caller, attach)
	}
	if err != nil {
		reply(conn, err.Error())
		_ = conn.Close()
		return
	}
	reply(conn, "")
	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Client(conn, muxConfig())
	if err != nil {
		_ = conn.Close()
		return
	}
	current := &session{mux: mux, instance: attach.InstanceID, connected: s.options.Now()}
	var previous *session
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = mux.Close()
		return
	}
	record := s.hosts[attach.HostID]
	if record == nil {
		record = &hostRecord{}
		s.hosts[attach.HostID] = record
	}
	if record.attached {
		record.reconnects++
	}
	record.attached = true
	previous, record.current = record.current, current
	s.mu.Unlock()
	if previous != nil {
		// 新会话接替旧会话：出口 IP 变化或网关重启后旧连接可能还没被判定断开。
		_ = previous.mux.Close()
	}
	go func() {
		<-mux.CloseChan()
		s.mu.Lock()
		if record.current == current {
			record.current = nil
		}
		s.mu.Unlock()
	}()
}

func (s *Server) checkAttach(conn *tls.Conn, view *snapshot.View, caller string, attach protocol.Attach) error {
	if attach.InstanceID == "" || len(attach.InstanceID) > 128 || caller != "host-gateway@"+attach.HostID {
		return errors.New("relay attach identity mismatch")
	}
	// 只接受目录里由本机中转、且仍启用的出站主机（停用的主机不在目录里）。
	if !view.OutboundHost(attach.HostID) {
		return errors.New("host is not an enabled outbound host")
	}
	peers := conn.ConnectionState().PeerCertificates
	if len(peers) == 0 || peers[0].VerifyHostname(attach.HostID) != nil || peers[0].Subject.CommonName != attach.HostID {
		return errors.New("relay attach certificate does not identify the host")
	}
	return nil
}

// HandleOpen 接管一条 ALPN 为 open 的连接，校验后透传到出站主机的目标端口。
func (s *Server) HandleOpen(conn *tls.Conn) {
	_ = conn.SetDeadline(time.Now().Add(protocol.PreambleTimeout))
	var open protocol.Open
	view, caller, err := s.verify(conn, protocol.MethodOpen, &open)
	if err == nil {
		target, ok := view.Relay(open.HostID)
		if !ok || !target.Allowed(caller, open.Port) {
			err = errors.New("relay target or port is not allowed for this caller")
		}
	}
	var stream net.Conn
	var release func()
	if err == nil {
		stream, release, err = s.openStream(open)
	}
	if err != nil {
		reply(conn, err.Error())
		_ = conn.Close()
		return
	}
	defer release()
	reply(conn, "")
	_ = conn.SetDeadline(time.Time{})
	pipe(conn, stream)
}

func (s *Server) openStream(open protocol.Open) (net.Conn, func(), error) {
	s.mu.Lock()
	record := s.hosts[open.HostID]
	if record == nil || record.current == nil {
		s.mu.Unlock()
		return nil, nil, errors.New("relay session offline")
	}
	current := record.current
	if s.streams >= s.options.Limits.TotalStreams || current.streams >= s.options.Limits.StreamsPerSession {
		s.mu.Unlock()
		return nil, nil, errors.New("relay stream limit reached")
	}
	s.streams++
	current.streams++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		s.streams--
		current.streams--
		s.mu.Unlock()
	}
	stream, err := current.mux.OpenStream()
	if err != nil {
		release()
		return nil, nil, errors.New("relay session offline")
	}
	_ = stream.SetDeadline(time.Now().Add(protocol.PreambleTimeout))
	var answer protocol.Reply
	if err := protocol.WriteFrame(stream, protocol.Stream{Port: open.Port}); err == nil {
		err = protocol.ReadFrame(stream, &answer)
		if err == nil && answer.Error != "" {
			err = errors.New(answer.Error)
		}
		if err != nil {
			_ = stream.Close()
			release()
			return nil, nil, errors.New("relay target unavailable on outbound host")
		}
	} else {
		_ = stream.Close()
		release()
		return nil, nil, errors.New("relay session offline")
	}
	_ = stream.SetDeadline(time.Time{})
	return stream, release, nil
}

// Status 返回所有接入过的出站主机的会话状态，按主机排序。
func (s *Server) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Status, 0, len(s.hosts))
	for id, record := range s.hosts {
		status := Status{HostID: id, Reconnects: record.reconnects}
		if record.current != nil {
			status.Online, status.InstanceID, status.ConnectedAt, status.Streams = true, record.current.instance, record.current.connected, record.current.streams
		}
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].HostID < result[j].HostID })
	return result
}

// Prune 断开已不再由本机中转的主机（停用或改为直达）的会话，并忘记它们的状态。
func (s *Server) Prune() {
	view := s.options.State.Load()
	if view == nil {
		return
	}
	var stale []*session
	s.mu.Lock()
	for id, record := range s.hosts {
		if view.OutboundHost(id) {
			continue
		}
		if record.current != nil {
			stale = append(stale, record.current)
		}
		delete(s.hosts, id)
	}
	s.mu.Unlock()
	for _, item := range stale {
		_ = item.mux.Close()
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	var sessions []*session
	for _, record := range s.hosts {
		if record.current != nil {
			sessions = append(sessions, record.current)
		}
	}
	s.mu.Unlock()
	for _, item := range sessions {
		_ = item.mux.Close()
	}
	return nil
}

// pipe 双向透传，任一方向结束即关闭两端。
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}
