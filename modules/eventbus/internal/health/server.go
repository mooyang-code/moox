// Package health provides EventBus liveness, readiness, and Prometheus text endpoints.
package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

const metricsScrapeTimeout = 2 * time.Second

var (
	connectionsDesc = prometheus.NewDesc(
		"moox_eventbus_connections",
		"Active NATS client connections.",
		nil, nil,
	)
	publishAdvisoriesDesc = prometheus.NewDesc(
		"moox_eventbus_publish_advisories_total",
		"JetStream publish/API advisories observed by EventBus.",
		nil, nil,
	)
	streamMessagesDesc = prometheus.NewDesc(
		"moox_eventbus_stream_messages",
		"Messages currently stored in an EventBus JetStream stream.",
		[]string{"stream"}, nil,
	)
	streamBytesDesc = prometheus.NewDesc(
		"moox_eventbus_stream_bytes",
		"Bytes currently stored in an EventBus JetStream stream.",
		[]string{"stream"}, nil,
	)
	consumerPendingDesc = prometheus.NewDesc(
		"moox_eventbus_consumer_pending",
		"Pending undelivered messages for an EventBus JetStream consumer.",
		[]string{"stream", "consumer"}, nil,
	)
	consumerRedeliveredDesc = prometheus.NewDesc(
		"moox_eventbus_consumer_redelivered",
		"Redelivered messages currently tracked for an EventBus JetStream consumer.",
		[]string{"stream", "consumer"}, nil,
	)
)

type Server struct {
	broker      *broker.Server
	registry    *registry.Registry
	ready       atomic.Bool
	conn        *nats.Conn
	advisory    atomic.Uint64
	sub         *nats.Subscription
	metricsOnce sync.Once
	metricsReg  *prometheus.Registry
}

func New(b *broker.Server, r *registry.Registry, conn ...*nats.Conn) *Server {
	s := &Server{broker: b, registry: r}
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

// metricsRegistry 返回本实例独享的 Prometheus 注册器（惰性初始化以兼容零值 &Server{} 构造），
// 不注册到全局 DefaultRegisterer，避免 30s 心跳上报反复遍历 JetStream 或多实例相互干扰。
func (s *Server) metricsRegistry() *prometheus.Registry {
	if s == nil {
		return nil
	}
	s.metricsOnce.Do(func() {
		reg := prometheus.NewRegistry()
		reg.MustRegister(s)
		s.metricsReg = reg
	})
	return s.metricsReg
}

func (s *Server) Handler() http.Handler {
	gatherers := prometheus.Gatherers{prometheus.DefaultGatherer}
	if reg := s.metricsRegistry(); reg != nil {
		gatherers = prometheus.Gatherers{reg, prometheus.DefaultGatherer}
	}
	return healthz.StandardMux(s.snapshot, promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{}))
}

// Readiness 是 /readyz 用的同一个就绪判断，随指标上报带给 Monitor。
func (s *Server) Readiness() func(context.Context) (bool, string) {
	return healthz.Readiness(s.snapshot)
}

func (s *Server) Register(service server.Service) error {
	if s == nil {
		return fmt.Errorf("eventbus health server is nil")
	}
	if s.conn != nil && s.sub == nil {
		sub, err := s.conn.Subscribe("$JS.EVENT.ADVISORY.API", func(_ *nats.Msg) { s.advisory.Add(1) })
		if err != nil {
			return err
		}
		s.sub = sub
	}
	return healthz.RegisterNoProtocolServiceMux(service, s.Handler())
}

func (s *Server) snapshot(context.Context) healthz.Response {
	brokerReady := s != nil && s.broker != nil && s.broker.Ready()
	ready := s != nil && s.Ready() && brokerReady
	rsp := healthz.Base("eventbus", "eventbus", "", "", time.Time{}, ready)
	rsp.Details = map[string]any{"broker_ready": brokerReady, "reconciled": s != nil && s.Ready()}
	return rsp
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.sub != nil {
		_ = s.sub.Unsubscribe()
		s.sub = nil
	}
	return nil
}

// Describe 实现 prometheus.Collector。
func (*Server) Describe(ch chan<- *prometheus.Desc) {
	ch <- connectionsDesc
	ch <- publishAdvisoriesDesc
	ch <- streamMessagesDesc
	ch <- streamBytesDesc
	ch <- consumerPendingDesc
	ch <- consumerRedeliveredDesc
}

// Collect 实现 prometheus.Collector。
func (s *Server) Collect(ch chan<- prometheus.Metric) {
	if s == nil {
		return
	}
	connections := uint32(0)
	if s.broker != nil {
		connections = s.broker.Connections()
	}
	ch <- prometheus.MustNewConstMetric(connectionsDesc, prometheus.GaugeValue, float64(connections))
	ch <- prometheus.MustNewConstMetric(publishAdvisoriesDesc, prometheus.CounterValue, float64(s.advisory.Load()))
	if s.registry == nil || s.registry.JS() == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsScrapeTimeout)
	defer cancel()
	js := s.registry.JS()
	for info := range js.StreamsInfo(nats.Context(ctx)) {
		if info == nil {
			continue
		}
		ch <- prometheus.MustNewConstMetric(streamMessagesDesc, prometheus.GaugeValue, float64(info.State.Msgs), info.Config.Name)
		ch <- prometheus.MustNewConstMetric(streamBytesDesc, prometheus.GaugeValue, float64(info.State.Bytes), info.Config.Name)
		for consumer := range js.Consumers(info.Config.Name, nats.Context(ctx)) {
			if consumer == nil {
				continue
			}
			ch <- prometheus.MustNewConstMetric(consumerPendingDesc, prometheus.GaugeValue, float64(consumer.NumPending), info.Config.Name, consumer.Name)
			ch <- prometheus.MustNewConstMetric(consumerRedeliveredDesc, prometheus.GaugeValue, float64(consumer.NumRedelivered), info.Config.Name, consumer.Name)
		}
	}
}
