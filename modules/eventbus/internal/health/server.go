// Package health provides EventBus liveness, readiness, and Prometheus text endpoints.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/nats-io/nats.go"
)

type Server struct {
	broker   *broker.Server
	registry *registry.Registry
	addr     string
	ready    atomic.Bool
	http     *http.Server
	conn     *nats.Conn
	advisory atomic.Uint64
	sub      *nats.Subscription
}

func New(addr string, b *broker.Server, r *registry.Registry, conn ...*nats.Conn) *Server {
	s := &Server{addr: addr, broker: b, registry: r}
	if len(conn) > 0 {
		s.conn = conn[0]
	}
	return s
}
func (s *Server) SetReady(value bool) {
	if s != nil {
		s.ready.Store(value)
	}
}
func (s *Server) Ready() bool { return s != nil && s.ready.Load() }
func (s *Server) Start(ctx context.Context) error {
	if s == nil || s.addr == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/metrics", s.metrics)
	s.http = &http.Server{Addr: s.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	if s.conn != nil {
		s.sub, err = s.conn.Subscribe("$JS.EVENT.ADVISORY.API", func(_ *nats.Msg) { s.advisory.Add(1) })
		if err != nil {
			_ = listener.Close()
			return err
		}
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdown)
	}()
	go func() { _ = s.http.Serve(listener) }()
	return nil
}
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.http == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.sub != nil {
		_ = s.sub.Unsubscribe()
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ready := s.broker != nil && s.broker.Ready()
	rsp := healthz.Base("eventbus", "", "dev", "", time.Time{}, ready)
	rsp.Details = map[string]any{"broker_ready": ready}
	writeHealth(w, rsp, ready)
}
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ready := s.Ready() && s.broker != nil && s.broker.Ready()
	rsp := healthz.Base("eventbus", "", "dev", "", time.Time{}, ready)
	rsp.Details = map[string]any{"broker_ready": s.broker != nil && s.broker.Ready(), "reconciled": s.Ready()}
	writeHealth(w, rsp, ready)
}
func writeHealth(w http.ResponseWriter, rsp healthz.Response, ready bool) {
	w.Header().Set("Content-Type", "application/json")
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(rsp)
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	connections := uint32(0)
	if s.broker != nil {
		connections = s.broker.Connections()
	}
	fmt.Fprintf(w, "# HELP moox_eventbus_connections Active NATS client connections.\n# TYPE moox_eventbus_connections gauge\nmoox_eventbus_connections %d\n", connections)
	fmt.Fprintf(w, "# HELP moox_eventbus_publish_advisories_total JetStream publish/API advisories observed by EventBus.\n# TYPE moox_eventbus_publish_advisories_total counter\nmoox_eventbus_publish_advisories_total %d\n", s.advisory.Load())
	if s.registry == nil || s.registry.JS() == nil {
		return
	}
	for info := range s.registry.JS().StreamsInfo() {
		if info == nil {
			continue
		}
		fmt.Fprintf(w, "moox_eventbus_stream_messages{stream=%q} %d\nmoox_eventbus_stream_bytes{stream=%q} %d\n", info.Config.Name, info.State.Msgs, info.Config.Name, info.State.Bytes)
		for consumer := range s.registry.JS().Consumers(info.Config.Name) {
			if consumer == nil {
				continue
			}
			fmt.Fprintf(w, "moox_eventbus_consumer_pending{stream=%q,consumer=%q} %d\nmoox_eventbus_consumer_redelivered{stream=%q,consumer=%q} %d\n", info.Config.Name, consumer.Name, consumer.NumPending, info.Config.Name, consumer.Name, consumer.NumRedelivered)
		}
	}
}
