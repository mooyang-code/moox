// Package trpcotel registers body-free OpenTelemetry filters for tRPC-Go while
// keeping the repository on its current OpenTelemetry dependency.
package trpcotel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

const filterName = "opentelemetry"

var tracer = otel.Tracer("github.com/mooyang-code/moox/trpc")
var tracerProvider *sdktrace.TracerProvider

func init() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if endpoint := strings.TrimSpace(os.Getenv("MOOX_OTEL_ENDPOINT")); endpoint != "" {
		if err := configure(endpoint); err != nil {
			panic(fmt.Sprintf("initialize OpenTelemetry: %v", err))
		}
	}
	filter.Register(filterName, ServerFilter, ClientFilter)
}

func configure(endpoint string) error {
	ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
	defer cancel()
	opts := []otlptracehttp.Option{}
	if strings.HasPrefix(endpoint, "http://") {
		endpoint = strings.TrimPrefix(endpoint, "http://")
		opts = append(opts, otlptracehttp.WithInsecure())
	} else {
		endpoint = strings.TrimPrefix(endpoint, "https://")
	}
	opts = append(opts, otlptracehttp.WithEndpoint(endpoint))
	if value, _ := strconv.ParseBool(os.Getenv("MOOX_OTEL_INSECURE")); value {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return err
	}
	fraction := 0.01
	if raw := strings.TrimSpace(os.Getenv("MOOX_OTEL_SAMPLE_FRACTION")); raw != "" {
		fraction, err = strconv.ParseFloat(raw, 64)
		if err != nil || fraction < 0 || fraction > 1 {
			return fmt.Errorf("MOOX_OTEL_SAMPLE_FRACTION must be between 0 and 1")
		}
	}
	serviceName := strings.TrimSpace(os.Getenv("MOOX_OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = "moox"
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(fraction))),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	)
	otel.SetTracerProvider(provider)
	tracerProvider = provider
	tracer = provider.Tracer("github.com/mooyang-code/moox/trpc")
	return nil
}

// Shutdown flushes queued spans. It is safe when exporting was not enabled.
func Shutdown(ctx context.Context) error {
	if tracerProvider == nil {
		return nil
	}
	return tracerProvider.Shutdown(ctx)
}

// ServerFilter extracts W3C trace metadata and records only RPC metadata. It
// intentionally never records request or response bodies.
func ServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	msg := trpc.Message(ctx)
	ctx = otel.GetTextMapPropagator().Extract(ctx, metadataCarrier(msg.ServerMetaData()))
	name := msg.ServerRPCName()
	if name == "" {
		name = "trpc.server"
	}
	ctx, span := tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	rsp, err := next(ctx, req)
	recordError(span, err)
	return rsp, err
}

// ClientFilter creates a client span and injects only W3C propagation fields
// into tRPC metadata. transinfo-blocker remains the final allowlist boundary.
func ClientFilter(ctx context.Context, req, rsp interface{}, next filter.ClientHandleFunc) error {
	msg := trpc.Message(ctx)
	name := msg.ClientRPCName()
	if name == "" {
		name = "trpc.client"
	}
	ctx, span := tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	md := msg.ClientMetaData()
	if md == nil {
		md = codec.MetaData{}
	}
	otel.GetTextMapPropagator().Inject(ctx, metadataCarrier(md))
	msg.WithClientMetaData(md)
	err := next(ctx, req, rsp)
	recordError(span, err)
	return err
}

func recordError(span trace.Span, err error) {
	if err == nil {
		span.SetStatus(codes.Ok, "")
		return
	}
	span.SetAttributes(attribute.Int64("rpc.trpc.status_code", int64(errs.Code(err))))
	span.SetStatus(codes.Error, "rpc error")
}

type metadataCarrier codec.MetaData

func (c metadataCarrier) Get(key string) string { return string(c[key]) }
func (c metadataCarrier) Set(key, value string) { c[key] = []byte(value) }
func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}
