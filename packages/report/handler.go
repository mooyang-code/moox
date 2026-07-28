package report

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

type Publisher interface {
	Publish(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)
}

type Connector func(context.Context, Config) (Publisher, error)

type Handler struct {
	cfg             Config
	gatherer        prometheus.Gatherer
	connector       Connector
	mu              sync.Mutex
	client          Publisher
	sequence        atomic.Uint64
	bootID          string
	reportErrors    prometheus.Counter
	reportLastError prometheus.Gauge
}

func NewHandler(cfg Config) (*Handler, error) {
	return newHandler(cfg, prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
}

// NewHandlerWithRegistry is the SCF Sentinel mode: the caller owns the
// process-local registry, updates it in a timer handler, and invokes Handle.
// NewHandler remains the long-running service mode using Prometheus defaults.
func NewHandlerWithRegistry(cfg Config, registry *prometheus.Registry) (*Handler, error) {
	if registry == nil {
		return nil, fmt.Errorf("metrics reporter registry is required")
	}
	return newHandler(cfg, registry, registry)
}

func newHandler(cfg Config, registerer prometheus.Registerer, gatherer prometheus.Gatherer) (*Handler, error) {
	cfg = cfg.withDefaults()
	if err := validateModuleName(cfg.Module); err != nil {
		return nil, fmt.Errorf("metrics reporter: %w", err)
	}
	if strings.TrimSpace(cfg.ServiceName) == "" {
		return nil, fmt.Errorf("metrics reporter service name is required")
	}
	if err := cfg.validateIdentity(); err != nil {
		return nil, err
	}
	if _, err := ValidatePipelineEnvironment(); err != nil {
		return nil, err
	}
	if _, err := regexp.Compile(cfg.IncludeRegex); err != nil {
		return nil, fmt.Errorf("include regex: %w", err)
	}
	if _, err := regexp.Compile(cfg.ExcludeRegex); err != nil {
		return nil, fmt.Errorf("exclude regex: %w", err)
	}
	reportErrors, err := registerOrReuseCounter(registerer, prometheus.NewCounter(prometheus.CounterOpts{
		Name: "moox_" + cfg.Module + "_report_errors_total",
		Help: "Metric snapshot reporting failures.",
	}))
	if err != nil {
		return nil, err
	}
	reportLastError, err := registerOrReuseGauge(registerer, prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "moox_" + cfg.Module + "_report_last_error_timestamp_seconds",
		Help: "Unix timestamp of the last metric snapshot reporting failure.",
	}))
	if err != nil {
		return nil, err
	}
	return &Handler{
		cfg: cfg, gatherer: gatherer, connector: connect, bootID: cfg.BootID,
		reportErrors: reportErrors, reportLastError: reportLastError,
	}, nil
}

func (h *Handler) Handle(ctx context.Context) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	snapshot, err := h.BuildSnapshot()
	if err != nil {
		return h.reportError(ctx, err)
	}
	seq := h.sequence.Add(1)
	messageID := fmt.Sprintf("%s-%020d", h.bootID, seq)
	client, err := h.publisher(ctx)
	if err != nil {
		return h.reportError(ctx, err)
	}
	if _, err := client.Publish(ctx, events.ObservabilityMetricsSnapshotReported, &metricspb.MetricReport{ServiceName: h.cfg.ServiceName, InstanceId: h.cfg.InstanceID, NodeId: h.cfg.NodeID, BootId: h.bootID, ServiceVersion: h.cfg.Version, Sequence: seq, Snapshot: snapshot}, events.PublishOptions{EventID: messageID, OccurredAt: time.Now().UTC(), SpaceID: h.cfg.SpaceID, SubjectID: h.cfg.ServiceName + "/" + h.cfg.InstanceID}); err != nil {
		return h.reportError(ctx, fmt.Errorf("publish metrics snapshot: %w", err))
	}
	return nil
}

func (h *Handler) EventReporter(ctx context.Context) (*EventReporter, error) {
	if h == nil {
		return nil, fmt.Errorf("metrics reporter handler is nil")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return &EventReporter{Registry: registry, publisherFn: h.publisher}, nil
}

func (h *Handler) reportError(ctx context.Context, err error) error {
	if err != nil {
		h.reportErrors.Inc()
		h.reportLastError.Set(float64(time.Now().Unix()))
		log.WarnContextf(ctx, "metrics snapshot report failed for %s: %v", h.cfg.ServiceName, err)
	}
	return err
}

func registerOrReuseCounter(registerer prometheus.Registerer, collector prometheus.Counter) (prometheus.Counter, error) {
	if err := registerer.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			return nil, fmt.Errorf("register reporter error counter: %w", err)
		}
		existing, ok := already.ExistingCollector.(prometheus.Counter)
		if !ok {
			return nil, fmt.Errorf("registered reporter error metric has type %T", already.ExistingCollector)
		}
		return existing, nil
	}
	return collector, nil
}

func registerOrReuseGauge(registerer prometheus.Registerer, collector prometheus.Gauge) (prometheus.Gauge, error) {
	if err := registerer.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			return nil, fmt.Errorf("register reporter error gauge: %w", err)
		}
		existing, ok := already.ExistingCollector.(prometheus.Gauge)
		if !ok {
			return nil, fmt.Errorf("registered reporter error metric has type %T", already.ExistingCollector)
		}
		return existing, nil
	}
	return collector, nil
}

func (h *Handler) BuildSnapshot() (*metricspb.MetricSnapshot, error) {
	families, err := h.gatherer.Gather()
	if err != nil {
		return nil, fmt.Errorf("gather prometheus metrics: %w", err)
	}
	include := regexp.MustCompile(h.cfg.IncludeRegex)
	exclude := regexp.MustCompile(h.cfg.ExcludeRegex)
	var raw bytes.Buffer
	encoder := expfmt.NewEncoder(&raw, expfmt.NewFormat(expfmt.TypeTextPlain))
	familyCount, sampleCount := 0, 0
	for _, family := range families {
		name := family.GetName()
		if !include.MatchString(name) || exclude.MatchString(name) {
			continue
		}
		familyCount++
		if familyCount > h.cfg.MaxMetricFamilies {
			return nil, fmt.Errorf("metric family limit exceeded: %d", h.cfg.MaxMetricFamilies)
		}
		for _, sample := range family.GetMetric() {
			generatedLabels := 0
			switch family.GetType() {
			case io_prometheus_client.MetricType_HISTOGRAM:
				sampleCount += 2 + len(sample.GetHistogram().GetBucket())
				if len(sample.GetHistogram().GetBucket()) > 0 {
					generatedLabels = 1
				}
			case io_prometheus_client.MetricType_SUMMARY:
				sampleCount += 2 + len(sample.GetSummary().GetQuantile())
				if len(sample.GetSummary().GetQuantile()) > 0 {
					generatedLabels = 1
				}
			default:
				sampleCount++
			}
			if len(sample.GetLabel())+generatedLabels > h.cfg.MaxLabelsPerSample {
				return nil, fmt.Errorf("label limit exceeded for %s", name)
			}
			for _, label := range sample.GetLabel() {
				if len(label.GetName()) > h.cfg.MaxLabelNameBytes || len(label.GetValue()) > h.cfg.MaxLabelValueBytes {
					return nil, fmt.Errorf("label byte limit exceeded for %s", name)
				}
			}
		}
		if sampleCount > h.cfg.MaxSamples {
			return nil, fmt.Errorf("sample limit exceeded: %d", h.cfg.MaxSamples)
		}
		if err := encoder.Encode(family); err != nil {
			return nil, fmt.Errorf("encode metric family %s: %w", name, err)
		}
		if raw.Len() > h.cfg.MaxUncompressedBytes {
			return nil, fmt.Errorf("uncompressed metric limit exceeded: %d", h.cfg.MaxUncompressedBytes)
		}
	}
	checksum := sha256.Sum256(raw.Bytes())
	var compressed bytes.Buffer
	gz, err := gzip.NewWriterLevel(&compressed, h.cfg.GzipLevel)
	if err != nil {
		return nil, fmt.Errorf("gzip level: %w", err)
	}
	if _, err := gz.Write(raw.Bytes()); err != nil {
		return nil, fmt.Errorf("gzip metrics: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close gzip metrics: %w", err)
	}
	if compressed.Len() > h.cfg.MaxCompressedBytes {
		return nil, fmt.Errorf("compressed metric limit exceeded: %d", h.cfg.MaxCompressedBytes)
	}
	return &metricspb.MetricSnapshot{
		SchemaVersion:             1,
		CollectionIntervalSeconds: uint32(h.cfg.Interval / time.Second),
		Format:                    metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT,
		Compression:               metricspb.Compression_COMPRESSION_GZIP,
		Data:                      compressed.Bytes(), MetricFamilyCount: uint32(familyCount), SampleCount: uint32(sampleCount), UncompressedSha256: checksum[:],
	}, nil
}

func (h *Handler) publisher(ctx context.Context) (Publisher, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != nil {
		return h.client, nil
	}
	p, err := h.connector(ctx, h.cfg)
	if err != nil {
		log.WarnContextf(ctx, "metrics eventbus unavailable for %s: %v", h.cfg.ServiceName, err)
		return nil, err
	}
	h.client = p
	return p, nil
}

func connect(ctx context.Context, cfg Config) (Publisher, error) {
	jsConfig := jetstream.ConfigFromEnv(strings.Split(cfg.EventBusURL, ","), "moox-"+cfg.ServiceName+"-metrics")
	if strings.TrimSpace(cfg.CredentialFile) != "" {
		if err := jsConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, fmt.Errorf("load metrics publisher credential: %w", err)
		}
	}
	client, err := jetstream.Connect(ctx, jsConfig)
	if err != nil {
		return nil, err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return events.NewPublisher(client, registry)
}

// Keep the generated client_model import in this module-owned implementation;
// it documents that families are encoded as complete protobuf metric families.
var _ = &io_prometheus_client.Metric{}
