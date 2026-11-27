package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsEndpoint(t *testing.T) {
	s := &Server{}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, r.Code)
	body := r.Body.String()
	assert.Contains(t, body, "# TYPE moox_eventbus_connections gauge\nmoox_eventbus_connections 0\n")
	assert.Contains(t, body, "# TYPE moox_eventbus_publish_advisories_total counter\nmoox_eventbus_publish_advisories_total 0\n")
}

func TestPrometheusCollectorReportsBrokerStreamsAndConsumers(t *testing.T) {
	cfg, err := config.Load("../../config/app.yaml")
	require.NoError(t, err)
	for i := range cfg.Streams {
		cfg.Streams[i].MaxBytes = 1 << 20
	}
	cfg.Broker.StoreDir = t.TempDir()
	cfg.Broker.Port = freePort(t)
	cfg.Broker.ServerName = "eventbus-health-test"

	b, err := broker.New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, b.Start(ctx))
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	nc, err := nats.Connect(b.URL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	reg, err := registry.New(js, cfg)
	require.NoError(t, err)
	_, err = reg.Reconcile(ctx)
	require.NoError(t, err)

	_, err = js.Publish("moox.event.storage.dataset.period.completed.v1.crypto.test", []byte("payload"))
	require.NoError(t, err)
	_, err = js.AddConsumer("MOOX_STORAGE", &nats.ConsumerConfig{
		Durable:       "health_test_consumer",
		AckPolicy:     nats.AckExplicitPolicy,
		FilterSubject: "moox.event.storage.dataset.period.completed.v1.>",
	})
	require.NoError(t, err)

	s := New(b, reg, nc)
	s.advisory.Store(3)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	// 直接注册到独立 prometheus.Registry 验证 Collector 契约。
	promReg := prometheus.NewRegistry()
	require.NoError(t, promReg.Register(s))
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE moox_eventbus_connections gauge\nmoox_eventbus_connections 1\n",
		"# TYPE moox_eventbus_publish_advisories_total counter\nmoox_eventbus_publish_advisories_total 3\n",
		`moox_eventbus_stream_messages{stream="MOOX_STORAGE"} 1`,
		`moox_eventbus_stream_bytes{stream="MOOX_STORAGE"} `,
		`moox_eventbus_consumer_pending{consumer="health_test_consumer",stream="MOOX_STORAGE"} 1`,
		`moox_eventbus_consumer_redelivered{consumer="health_test_consumer",stream="MOOX_STORAGE"} 0`,
	} {
		assert.Contains(t, body, want)
	}

	// Handler() 同时暴露本实例 EventBus 指标与默认注册器指标，但不把 EventBus 指标注册进全局 DefaultGatherer。
	handlerRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(handlerRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, handlerRec.Code)
	handlerBody := handlerRec.Body.String()
	assert.Contains(t, handlerBody, `moox_eventbus_stream_messages{stream="MOOX_STORAGE"} 1`)
	assert.Contains(t, handlerBody, "go_goroutines")

	defaultFamilies, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range defaultFamilies {
		assert.False(t, strings.HasPrefix(family.GetName(), "moox_eventbus_"),
			"unexpected eventbus metric registered on DefaultGatherer: %s", family.GetName())
	}
}

func TestMultipleServerInstancesStayIsolatedFromEachOtherAndDefaultGatherer(t *testing.T) {
	s1 := New(nil, nil)
	s1.advisory.Store(11)
	s2 := New(nil, nil)
	s2.advisory.Store(22)

	h1 := s1.Handler()
	h2 := s2.Handler()

	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec1.Code)
	assert.Contains(t, rec1.Body.String(), "moox_eventbus_publish_advisories_total 11\n")

	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "moox_eventbus_publish_advisories_total 22\n")

	// s2 停机不影响 s1 的独立指标暴露。
	require.NoError(t, s2.Shutdown(context.Background()))
	afterRec1 := httptest.NewRecorder()
	h1.ServeHTTP(afterRec1, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, afterRec1.Code)
	assert.Contains(t, afterRec1.Body.String(), "moox_eventbus_connections 0\n")
	assert.Contains(t, afterRec1.Body.String(), "moox_eventbus_publish_advisories_total 11\n")

	defaultFamilies, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range defaultFamilies {
		assert.False(t, strings.HasPrefix(family.GetName(), "moox_eventbus_"),
			"unexpected eventbus metric registered on DefaultGatherer: %s", family.GetName())
	}
}

func TestRegister_NilService_ShouldReturnError(t *testing.T) {
	s := &Server{}
	err := s.Register(nil)
	require.Error(t, err)
}

func TestRegister_NilServer_ShouldReturnError(t *testing.T) {
	var s *Server
	err := s.Register(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestHandlerRoutesLivenessAndReadiness(t *testing.T) {
	s := &Server{}
	handler := s.Handler()

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/healthz", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		assert.Equal(t, tc.want, rec.Code)
	}
}

func TestShutdownNilServer_ShouldNoop(t *testing.T) {
	var s *Server
	assert.NoError(t, s.Shutdown(nil))
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
