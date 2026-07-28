package report

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type fakePublisher struct {
	events   []events.Event
	payloads []proto.Message
	options  []events.PublishOptions
}

func validConfig(serviceName string) Config {
	return Config{Module: "monitor", ServiceName: serviceName, InstanceID: serviceName + "@node-a", NodeID: "node-a", BootID: "boot-a"}
}

func NewHandlerWithPublisher(cfg Config, p Publisher, gatherer prometheus.Gatherer) (*Handler, error) {
	h, err := NewHandler(cfg)
	if err != nil {
		return nil, err
	}
	if p != nil {
		h.client = p
	}
	if gatherer != nil {
		h.gatherer = gatherer
	}
	return h, nil
}

func (f *fakePublisher) Publish(_ context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	f.events = append(f.events, event)
	f.payloads = append(f.payloads, payload)
	f.options = append(f.options, opts)
	return &jetstream.PublishAck{Stream: "MOOX_METRICS", Sequence: uint64(len(f.events))}, nil
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
	cfg := DefaultConfig("monitor", "moox_monitor")
	if cfg.EventBusURL != DefaultBusURL || cfg.SpaceID != DefaultSpace {
		t.Fatalf("defaults: %+v", cfg)
	}
}

func TestNewHandlerWithRegistrySupportsDedicatedSCFMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	h, err := NewHandlerWithRegistry(validConfig("moox-collector-scf"), registry)
	require.NoError(t, err)
	require.Same(t, registry, h.gatherer)
	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricFamily(t, families, "moox_monitor_report_errors_total")
	requireMetricFamily(t, families, "moox_monitor_report_last_error_timestamp_seconds")
	requireNoLabels(t, families, "service", "module", "subject_id", "job_id", "error")
}

func TestHandlePublishesEventMessage(t *testing.T) {
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
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d", len(publisher.events))
	}
	if publisher.events[0].Name() != events.ObservabilityMetricsSnapshotReported.Name() ||
		publisher.events[0].Version() != events.ObservabilityMetricsSnapshotReported.Version() {
		t.Fatalf("event = %+v", publisher.events[0])
	}
	report, ok := publisher.payloads[0].(*metricspb.MetricReport)
	if !ok || report.GetServiceName() != "moox-monitor" || report.GetInstanceId() != "instance-a" || report.GetSequence() != 1 {
		t.Fatalf("unexpected report: %T %+v", publisher.payloads[0], publisher.payloads[0])
	}
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
}
