package eventconsumer

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type snapshotWriter struct{ calls int }

func (w *snapshotWriter) WriteSnapshot(context.Context, *hostmetricpb.HostSnapshot, string, time.Time, string) error {
	w.calls++
	return nil
}

func TestHandlePersistsValidMetricBeforeAck(t *testing.T) {
	writer := &snapshotWriter{}
	store := hostmetrics.NewStoreWithWriter(writer)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	agentID := "0190f4d0-7b1c-4f45-9a3e-7c28f6479a73"
	encoded, err := registry.Encode(events.MetricsHostReported, &hostmetricpb.HostMetric{
		AgentId: agentID, Hostname: "host",
		Snapshot: &hostmetricpb.HostSnapshot{
			Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 2},
			Memory: &hostmetricpb.MemoryMetric{TotalBytes: 10, AvailableBytes: 10},
		},
	}, events.PublishOptions{EventID: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", OccurredAt: time.Now().UTC(), SpaceID: hostmetrics.SpaceID, SubjectID: agentID})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	result := (&Consumer{store: store}).Handle(context.Background(), &jetstream.Delivery{
		Subject: encoded.Subject, RawData: raw, RawMessageID: encoded.Message.GetEventId(), ContentType: events.ContentType,
	})
	assert.Equal(t, jetstream.ACK, result.Decision)
	assert.NoError(t, result.Err)
	assert.Equal(t, 1, writer.calls)
}

func TestRetryDelay(t *testing.T) {
	assert.Equal(t, time.Second, retryDelay(1))
	assert.Equal(t, 5*time.Second, retryDelay(2))
	assert.Equal(t, 15*time.Second, retryDelay(3))
}
