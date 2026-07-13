package trpcotel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestMetadataCarrierRoundTrip(t *testing.T) {
	carrier := metadataCarrier(codec.MetaData{})
	prop := propagation.TraceContext{}
	prop.Inject(context.Background(), carrier)
	carrier.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := prop.Extract(context.Background(), carrier)
	require.True(t, traceFromContext(ctx))
}

func TestFiltersNeverAttachBodies(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	oldTracer := tracer
	tracer = provider.Tracer("test")
	t.Cleanup(func() { tracer = oldTracer; _ = provider.Shutdown(context.Background()) })

	_, err := ServerFilter(context.Background(), struct{ Secret string }{"do-not-export"}, func(context.Context, interface{}) (interface{}, error) {
		return struct{ Secret string }{"also-do-not-export"}, nil
	})
	require.NoError(t, err)
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	for _, attr := range spans[0].Attributes {
		require.NotContains(t, attr.Value.AsString(), "do-not-export")
	}
}

func TestFiltersNeverAttachRawErrorText(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	oldTracer := tracer
	tracer = provider.Tracer("test")
	t.Cleanup(func() { tracer = oldTracer; _ = provider.Shutdown(context.Background()) })

	_, err := ServerFilter(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		return nil, errors.New("api_secret=do-not-export")
	})
	require.Error(t, err)
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	require.NotContains(t, spans[0].Status.Description, "do-not-export")
	for _, event := range spans[0].Events {
		for _, attr := range event.Attributes {
			require.NotContains(t, attr.Value.AsString(), "do-not-export")
		}
	}
}

func traceFromContext(ctx context.Context) bool {
	return otel.GetTextMapPropagator().Fields() != nil && trace.SpanContextFromContext(ctx).IsValid()
}
