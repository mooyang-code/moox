package report

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePublisher struct {
	messages []*messagepb.MooxMessage
}

func validConfig(serviceName string) Config {
	return Config{ServiceName: serviceName, InstanceID: serviceName + "@node-a", NodeID: "node-a", BootID: "boot-a"}
}

func (f *fakePublisher) Publish(_ context.Context, message *messagepb.MooxMessage, _ ...jetstream.PublishOption) (*jetstream.PublishAck, error) {
	f.messages = append(f.messages, message)
	return &jetstream.PublishAck{Stream: "MOOX_METRICS", Sequence: uint64(len(f.messages))}, nil
}

func TestBuildSnapshotPreservesFamiliesAndLimits(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_reporter_test", Help: "reporter help"}, []string{"kind"})
	if err := registry.Register(gauge); err != nil {
		t.Fatal(err)
	}
	gauge.WithLabelValues("unit").Set(42)
	cfg := validConfig("monitor")
	cfg.GzipLevel = 1
	h, err := NewHandlerWithPublisher(cfg, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.BuildSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GetCompression() != metricspb.Compression_COMPRESSION_GZIP || snapshot.GetSampleCount() != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if len(snapshot.GetUncompressedSha256()) != 32 {
		t.Fatalf("checksum length = %d", len(snapshot.GetUncompressedSha256()))
	}
	reader, err := gzip.NewReader(strings.NewReader(string(snapshot.GetData())))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# HELP moox_reporter_test reporter help") || !strings.Contains(string(raw), "# TYPE moox_reporter_test gauge") {
		t.Fatalf("family metadata missing: %s", raw)
	}
}

func TestBuildSnapshotFiltersAndRejectsOversize(t *testing.T) {
	registry := prometheus.NewRegistry()
	for _, name := range []string{"included_metric", "excluded_metric"} {
		g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: name})
		if err := registry.Register(g); err != nil {
			t.Fatal(err)
		}
		g.Set(1)
	}
	cfg := validConfig("monitor")
	cfg.IncludeRegex = "^included_"
	cfg.MaxUncompressedBytes = 8
	h, err := NewHandlerWithPublisher(cfg, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.BuildSnapshot(); err == nil {
		t.Fatal("expected uncompressed limit error")
	}
}

func TestBuildSnapshotCountsFlattenedHistogramSamples(t *testing.T) {
	registry := prometheus.NewRegistry()
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "moox_reporter_histogram", Help: "histogram", Buckets: []float64{1, 2}})
	if err := registry.Register(hist); err != nil {
		t.Fatal(err)
	}
	hist.Observe(1.5)
	h, err := NewHandlerWithPublisher(validConfig("monitor"), nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.BuildSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GetSampleCount() != 4 { // sum, count, and two buckets
		t.Fatalf("sample count = %d, want 4", snapshot.GetSampleCount())
	}
}

func TestDefaultConfigUsesCentralEventBus(t *testing.T) {
	cfg := DefaultConfig("monitor")
	if cfg.EventBusURL != DefaultBusURL || cfg.Topic != DefaultTopic || cfg.SpaceID != DefaultSpace {
		t.Fatalf("defaults: %+v", cfg)
	}
}

func TestHandlePublishesMooxMessage(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_reporter_publish_test", Help: "publish"})
	if err := registry.Register(gauge); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	cfg := validConfig("moox-monitor")
	cfg.InstanceID = "instance-a"
	h, err := NewHandlerWithPublisher(cfg, publisher, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("published messages = %d", len(publisher.messages))
	}
	message := publisher.messages[0]
	if message.GetTopic() != DefaultTopic || message.GetContentType() != SnapshotContentType || message.GetKind() != messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT {
		t.Fatalf("unexpected message metadata: %+v", message)
	}
	if message.GetProducer().GetServiceName() != "moox-monitor" || message.GetProducer().GetInstanceId() != "instance-a" || message.GetSequence() != 1 {
		t.Fatalf("unexpected producer metadata: %+v", message.GetProducer())
	}
}

func TestReportErrorIncrementsErrorCount(t *testing.T) {
	h := &Handler{cfg: validConfig("monitor")}

	err := h.reportError(context.Background(), errors.New("publish failed"))
	require.Error(t, err)
	assert.Equal(t, uint64(1), h.ErrorCount())
	assert.Equal(t, "publish failed", h.LastError())

	require.NoError(t, h.reportError(context.Background(), nil))
	assert.Equal(t, uint64(1), h.ErrorCount())

	var nilHandler *Handler
	assert.Equal(t, uint64(0), nilHandler.ErrorCount())
	assert.Empty(t, nilHandler.LastError())
}

func TestHandleReportsPublisherError(t *testing.T) {
	registry := prometheus.NewRegistry()
	h, err := NewHandlerWithPublisher(validConfig("monitor"), nil, registry)
	require.NoError(t, err)
	h.connector = func(context.Context, Config) (Publisher, error) {
		return nil, errors.New("eventbus down")
	}

	err = h.Handle(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "eventbus down")
	assert.Equal(t, uint64(1), h.ErrorCount())
}
