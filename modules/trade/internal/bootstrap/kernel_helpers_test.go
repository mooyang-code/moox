package bootstrap

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
)

func TestDeliveryTraceContext_WithTrace_ShouldInjectTelemetry(t *testing.T) {
	delivery := &jetstream.Delivery{
		Message: &messagepb.MooxMessage{
			Trace: &messagepb.TraceContext{TraceId: "trace-1", RequestId: "req-1"},
		},
	}
	ctx := deliveryTraceContext(context.Background(), delivery)
	// Context should differ when trace is present.
	assert.NotEqual(t, context.Background(), ctx)
}

func TestDeliveryTraceContext_NilDelivery_ShouldReturnOriginal(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, ctx, deliveryTraceContext(ctx, nil))
}

func TestKernelEventBusReady_WithNilClient_ShouldReturnFalse(t *testing.T) {
	setKernelEventBusClient(nil)
	assert.False(t, kernelEventBusReady())
}

func TestRegisterMetricsReporter_NilServer_ShouldNoop(t *testing.T) {
	registerMetricsReporter(nil)
}
