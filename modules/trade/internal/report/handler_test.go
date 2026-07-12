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
)

type fakePublisher struct {
	messages []*messagepb.MooxMessage
	err      error
}

func (f *fakePublisher) Publish(_ context.Context, message *messagepb.MooxMessage, _ ...jetstream.PublishOption) (*jetstream.PublishAck, error) {
	if f.err != nil {
		return nil, f.err
	}
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
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor", InstanceID: "i", GzipLevel: 1}, nil, registry)
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
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor", IncludeRegex: "^included_", MaxUncompressedBytes: 8}, nil, registry)
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
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor", InstanceID: "i"}, nil, registry)
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
	h, err := NewHandlerWithPublisher(Config{ServiceName: "moox-monitor", InstanceID: "instance-a"}, publisher, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background(), ""); err != nil {
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

func TestHandler_ReportErrorAndErrorCount(t *testing.T) {
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor"}, nil, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if h.ErrorCount() != 0 {
		t.Fatalf("initial ErrorCount=%d, want 0", h.ErrorCount())
	}
	if err := h.reportError(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if h.ErrorCount() != 0 {
		t.Fatalf("nil report ErrorCount=%d, want 0", h.ErrorCount())
	}
	wantErr := errors.New("publish failed")
	if got := h.reportError(context.Background(), wantErr); !errors.Is(got, wantErr) {
		t.Fatalf("reportError=%v, want %v", got, wantErr)
	}
	if h.ErrorCount() != 1 {
		t.Fatalf("ErrorCount=%d, want 1", h.ErrorCount())
	}
	var nilHandler *Handler
	if nilHandler.ErrorCount() != 0 {
		t.Fatalf("nil handler ErrorCount=%d, want 0", nilHandler.ErrorCount())
	}
}

func TestHandler_PublisherConnectsOnceAndCaches(t *testing.T) {
	publisher := &fakePublisher{}
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor"}, nil, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	h.connector = func(context.Context, Config) (Publisher, error) {
		calls++
		return publisher, nil
	}

	first, err := h.publisher(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.publisher(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != publisher || second != publisher || calls != 1 {
		t.Fatalf("publisher cache failed: first=%p second=%p calls=%d", first, second, calls)
	}
}

func TestHandler_PublisherConnectorErrorIncrementsErrorCount(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_reporter_connector_error", Help: "connector error"})
	if err := registry.Register(gauge); err != nil {
		t.Fatal(err)
	}
	gauge.Set(1)
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor"}, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	h.connector = func(context.Context, Config) (Publisher, error) {
		return nil, errors.New("eventbus unavailable")
	}

	err = h.Handle(context.Background(), "")

	if err == nil || !strings.Contains(err.Error(), "eventbus unavailable") {
		t.Fatalf("err=%v, want connector error", err)
	}
	if h.ErrorCount() != 1 {
		t.Fatalf("ErrorCount=%d, want 1", h.ErrorCount())
	}
}

func TestHandler_HandlePublishErrorIncrementsErrorCount(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_reporter_publish_error", Help: "publish error"})
	if err := registry.Register(gauge); err != nil {
		t.Fatal(err)
	}
	gauge.Set(1)
	publisher := &fakePublisher{err: errors.New("nats publish failed")}
	h, err := NewHandlerWithPublisher(Config{ServiceName: "monitor"}, publisher, registry)
	if err != nil {
		t.Fatal(err)
	}

	err = h.Handle(nil, "")

	if err == nil || !strings.Contains(err.Error(), "publish metrics snapshot") {
		t.Fatalf("err=%v, want publish metrics snapshot", err)
	}
	if h.ErrorCount() != 1 {
		t.Fatalf("ErrorCount=%d, want 1", h.ErrorCount())
	}
}
