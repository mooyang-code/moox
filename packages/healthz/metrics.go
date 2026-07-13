package healthz

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

const RequestMetricsFilterName = "requestmetrics"

var (
	serverRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_trpc_server_requests_total",
		Help: "Completed tRPC server requests.",
	}, []string{"service", "method", "code"})
	serverDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moox_trpc_server_request_duration_seconds",
		Help:    "tRPC server request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method", "code"})
	clientRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_trpc_client_requests_total",
		Help: "Completed tRPC client requests.",
	}, []string{"service", "method", "code"})
	clientDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moox_trpc_client_request_duration_seconds",
		Help:    "tRPC client request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method", "code"})
)

func init() {
	prometheus.MustRegister(serverRequests, serverDuration, clientRequests, clientDuration)
	filter.Register(RequestMetricsFilterName, requestMetricsServerFilter, requestMetricsClientFilter)
}

func requestMetricsServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	started := time.Now()
	rsp, err := next(ctx, req)
	service, method := requestTarget(ctx)
	code := strconv.Itoa(int(errs.Code(err)))
	serverRequests.WithLabelValues(service, method, code).Inc()
	serverDuration.WithLabelValues(service, method, code).Observe(time.Since(started).Seconds())
	return rsp, err
}

func requestMetricsClientFilter(ctx context.Context, req, rsp interface{}, next filter.ClientHandleFunc) error {
	started := time.Now()
	err := next(ctx, req, rsp)
	service, method := requestTarget(ctx)
	code := strconv.Itoa(int(errs.Code(err)))
	clientRequests.WithLabelValues(service, method, code).Inc()
	clientDuration.WithLabelValues(service, method, code).Observe(time.Since(started).Seconds())
	return err
}

func requestTarget(ctx context.Context) (string, string) {
	msg := trpc.Message(ctx)
	if msg == nil {
		return "unknown", "unknown"
	}
	service := msg.CalleeServiceName()
	if service == "" {
		service = msg.CalleeService()
	}
	method := msg.CalleeMethod()
	if service == "" {
		service = "unknown"
	}
	if method == "" {
		method = "unknown"
	}
	return service, method
}
