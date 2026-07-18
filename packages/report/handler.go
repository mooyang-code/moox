package report

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	SnapshotContentType = "application/vnd.moox.metrics.snapshot+protobuf"
	SnapshotTopic       = DefaultTopic
)

type Publisher interface {
	Publish(context.Context, *messagepb.MooxMessage, ...jetstream.PublishOption) (*jetstream.PublishAck, error)
}

type Connector func(context.Context, Config) (Publisher, error)

type Handler struct {
	cfg        Config
	gatherer   prometheus.Gatherer
	connector  Connector
	mu         sync.Mutex
	client     Publisher
	sequence   atomic.Uint64
	errorCount atomic.Uint64
	bootID     string
}

func NewHandler(cfg Config) (*Handler, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.ServiceName) == "" {
		return nil, fmt.Errorf("metrics reporter service name is required")
	}
	if _, err := regexp.Compile(cfg.IncludeRegex); err != nil {
		return nil, fmt.Errorf("include regex: %w", err)
	}
	if _, err := regexp.Compile(cfg.ExcludeRegex); err != nil {
		return nil, fmt.Errorf("exclude regex: %w", err)
	}
	bootID := cfg.BootID
	if bootID == "" {
		bootID = newID()
	}
	return &Handler{cfg: cfg, gatherer: prometheus.DefaultGatherer, connector: connect, bootID: bootID}, nil
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
	payload, err := proto.Marshal(snapshot)
	if err != nil {
		return h.reportError(ctx, fmt.Errorf("marshal metric snapshot: %w", err))
	}
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       messageID,
		Topic:           h.cfg.Topic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT,
		Producer:        &messagepb.Producer{ServiceName: h.cfg.ServiceName, InstanceId: h.cfg.InstanceID, NodeId: h.cfg.NodeID, BootId: h.bootID, Version: h.cfg.Version},
		SpaceId:         h.cfg.SpaceID,
		Sequence:        seq,
		OccurredAt:      timestamppb.Now(),
		PublishedAt:     timestamppb.Now(),
		ContentType:     SnapshotContentType,
		MessageType:     "moox.metrics.snapshot.reported.v1",
		Payload:         payload,
	}
	client, err := h.publisher(ctx)
	if err != nil {
		return h.reportError(ctx, err)
	}
	if _, err := client.Publish(ctx, msg, jetstream.WithOrderingKey(h.cfg.ServiceName+"/"+h.cfg.InstanceID)); err != nil {
		return h.reportError(ctx, fmt.Errorf("publish metrics snapshot: %w", err))
	}
	return nil
}

func (h *Handler) reportError(ctx context.Context, err error) error {
	if err != nil {
		h.errorCount.Add(1)
		log.WarnContextf(ctx, "metrics snapshot report failed for %s: %v", h.cfg.ServiceName, err)
	}
	return err
}

func (h *Handler) ErrorCount() uint64 {
	if h == nil {
		return 0
	}
	return h.errorCount.Load()
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
	return jetstream.Connect(ctx, jetstream.ConfigFromEnv(strings.Split(cfg.EventBusURL, ","), "moox-"+cfg.ServiceName+"-metrics"))
}

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("boot-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", raw[:])
}

// Keep the generated client_model import in this module-owned implementation;
// it documents that families are encoded as complete protobuf metric families.
var _ = &io_prometheus_client.Metric{}
