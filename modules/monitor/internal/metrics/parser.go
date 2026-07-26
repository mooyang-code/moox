package metrics

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	DefaultMaxUncompressedBytes int64 = 4 << 20
	DefaultMaxCompressedBytes   int64 = 1 << 20
	DefaultMaxMetricFamilies          = 2000
	DefaultMaxSamples                 = 20000
	DefaultMaxLabelsPerSample         = 20
	DefaultMaxLabelNameBytes          = 128
	DefaultMaxLabelValueBytes         = 512
)

type Limits struct {
	MaxUncompressedBytes int64
	MaxCompressedBytes   int64
	MaxMetricFamilies    int
	MaxSamples           int
	MaxLabelsPerSample   int
	MaxLabelNameBytes    int
	MaxLabelValueBytes   int
}

func DefaultLimits() Limits {
	return Limits{DefaultMaxUncompressedBytes, DefaultMaxCompressedBytes, DefaultMaxMetricFamilies, DefaultMaxSamples, DefaultMaxLabelsPerSample, DefaultMaxLabelNameBytes, DefaultMaxLabelValueBytes}
}

func (l Limits) normalized() Limits {
	d := DefaultLimits()
	if l.MaxUncompressedBytes <= 0 {
		l.MaxUncompressedBytes = d.MaxUncompressedBytes
	}
	if l.MaxCompressedBytes <= 0 {
		l.MaxCompressedBytes = d.MaxCompressedBytes
	}
	if l.MaxMetricFamilies <= 0 {
		l.MaxMetricFamilies = d.MaxMetricFamilies
	}
	if l.MaxSamples <= 0 {
		l.MaxSamples = d.MaxSamples
	}
	if l.MaxLabelsPerSample <= 0 {
		l.MaxLabelsPerSample = d.MaxLabelsPerSample
	}
	if l.MaxLabelNameBytes <= 0 {
		l.MaxLabelNameBytes = d.MaxLabelNameBytes
	}
	if l.MaxLabelValueBytes <= 0 {
		l.MaxLabelValueBytes = d.MaxLabelValueBytes
	}
	return l
}

type Envelope struct {
	ServiceName     string
	InstanceID      string
	MessageID       string
	ProducerNodeID  string
	ProducerVersion string
	ObservedAt      time.Time
}

type Sample struct {
	SeriesID        string
	ServiceName     string
	InstanceID      string
	MetricName      string
	MetricType      string
	Labels          map[string]string
	LabelsJSON      string
	Value           float64
	ObservedAt      time.Time
	Interval        time.Duration
	MessageID       string
	ProducerNodeID  string
	ProducerVersion string
}

// DecodeSnapshot verifies and decompresses a bounded snapshot payload.
func DecodeSnapshot(snapshot *metricspb.MetricSnapshot, limits Limits) ([]byte, error) {
	if snapshot == nil {
		return nil, errors.New("metric snapshot is nil")
	}
	if snapshot.GetSchemaVersion() != 1 {
		return nil, fmt.Errorf("unsupported metric snapshot schema_version %d", snapshot.GetSchemaVersion())
	}
	limits = limits.normalized()
	var raw []byte
	switch snapshot.GetCompression() {
	case metricspb.Compression_COMPRESSION_NONE:
		if int64(len(snapshot.GetData())) > limits.MaxUncompressedBytes {
			return nil, fmt.Errorf("snapshot exceeds %d bytes", limits.MaxUncompressedBytes)
		}
		raw = append([]byte(nil), snapshot.GetData()...)
	case metricspb.Compression_COMPRESSION_GZIP:
		if int64(len(snapshot.GetData())) > limits.MaxCompressedBytes {
			return nil, fmt.Errorf("compressed snapshot exceeds %d bytes", limits.MaxCompressedBytes)
		}
		zr, err := gzip.NewReader(bytes.NewReader(snapshot.GetData()))
		if err != nil {
			return nil, fmt.Errorf("open gzip snapshot: %w", err)
		}
		lr := io.LimitReader(zr, limits.MaxUncompressedBytes+1)
		raw, err = io.ReadAll(lr)
		_ = zr.Close()
		if err != nil {
			return nil, fmt.Errorf("read gzip snapshot: %w", err)
		}
		if int64(len(raw)) > limits.MaxUncompressedBytes {
			return nil, fmt.Errorf("decompressed snapshot exceeds %d bytes", limits.MaxUncompressedBytes)
		}
	default:
		return nil, fmt.Errorf("unsupported snapshot compression %s", snapshot.GetCompression().String())
	}
	if len(snapshot.GetUncompressedSha256()) > 0 {
		sum := sha256.Sum256(raw)
		if !bytes.Equal(sum[:], snapshot.GetUncompressedSha256()) {
			return nil, fmt.Errorf("snapshot checksum mismatch: got %s", hex.EncodeToString(sum[:]))
		}
	}
	return raw, nil
}

// ParseSnapshot decodes, validates, and flattens a snapshot into Storage rows.
func ParseSnapshot(snapshot *metricspb.MetricSnapshot, env Envelope, limits Limits) ([]Sample, error) {
	if snapshot == nil {
		return nil, errors.New("metric snapshot is nil")
	}
	if snapshot.GetFormat() != metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT {
		return nil, fmt.Errorf("unsupported exposition format %s", snapshot.GetFormat().String())
	}
	raw, err := DecodeSnapshot(snapshot, limits)
	if err != nil {
		return nil, err
	}
	limits = limits.normalized()
	if !utf8.Valid(raw) {
		return nil, errors.New("snapshot contains invalid UTF-8")
	}
	p := expfmt.NewTextParser(model.UTF8Validation)
	families, err := p.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse prometheus exposition: %w", err)
	}
	if len(families) > limits.MaxMetricFamilies {
		return nil, fmt.Errorf("metric family limit exceeded: %d > %d", len(families), limits.MaxMetricFamilies)
	}
	if snapshot.GetMetricFamilyCount() != 0 && int(snapshot.GetMetricFamilyCount()) != len(families) {
		return nil, fmt.Errorf("metric family count mismatch: declared %d got %d", snapshot.GetMetricFamilyCount(), len(families))
	}
	interval := time.Duration(snapshot.GetCollectionIntervalSeconds()) * time.Second
	if env.ObservedAt.IsZero() {
		env.ObservedAt = time.Now().UTC()
	}
	var out []Sample
	for name, family := range families {
		if name == "" || family == nil {
			continue
		}
		typeName := metricTypeName(family.GetType())
		for _, metric := range family.GetMetric() {
			if metric == nil {
				continue
			}
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				if pair == nil || pair.GetName() == "" {
					return nil, fmt.Errorf("metric %s has empty label name", name)
				}
				if _, exists := labels[pair.GetName()]; exists {
					return nil, fmt.Errorf("metric %s has duplicate label %q", name, pair.GetName())
				}
				if len(pair.GetName()) > limits.MaxLabelNameBytes || len(pair.GetValue()) > limits.MaxLabelValueBytes {
					return nil, fmt.Errorf("metric %s label exceeds size limit", name)
				}
				labels[pair.GetName()] = pair.GetValue()
			}
			if len(labels) > limits.MaxLabelsPerSample {
				return nil, fmt.Errorf("metric %s label limit exceeded", name)
			}
			if err := appendMetricSamples(&out, family, metric, name, typeName, labels, env, interval, limits.MaxSamples, limits.MaxLabelsPerSample); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MetricName != out[j].MetricName {
			return out[i].MetricName < out[j].MetricName
		}
		return out[i].SeriesID < out[j].SeriesID
	})
	if snapshot.GetSampleCount() != 0 && int(snapshot.GetSampleCount()) != len(out) {
		return nil, fmt.Errorf("sample count mismatch: declared %d got %d", snapshot.GetSampleCount(), len(out))
	}
	return out, nil
}

func metricTypeName(t dto.MetricType) string {
	switch t {
	case dto.MetricType_COUNTER:
		return "counter"
	case dto.MetricType_GAUGE:
		return "gauge"
	case dto.MetricType_HISTOGRAM:
		return "histogram"
	case dto.MetricType_SUMMARY:
		return "summary"
	case dto.MetricType_UNTYPED:
		return "untyped"
	default:
		return "unknown"
	}
}

func appendMetricSamples(out *[]Sample, family *dto.MetricFamily, metric *dto.Metric, name, typeName string, labels map[string]string, env Envelope, interval time.Duration, max, maxLabels int) error {
	add := func(value float64, suffix string, extra map[string]string) error {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("metric %s contains non-finite value", name)
		}
		merged := make(map[string]string, len(labels)+len(extra))
		for k, v := range labels {
			merged[k] = v
		}
		for k, v := range extra {
			if _, ok := merged[k]; ok {
				return fmt.Errorf("metric %s duplicate generated label %q", name, k)
			}
			merged[k] = v
		}
		if len(merged) > maxLabels {
			return fmt.Errorf("metric %s label limit exceeded", name)
		}
		lj, err := CanonicalLabelsJSON(merged)
		if err != nil {
			return err
		}
		*out = append(*out, Sample{SeriesID: SeriesID(env.ServiceName, env.InstanceID, name+suffix, merged), ServiceName: env.ServiceName, InstanceID: env.InstanceID, MetricName: name + suffix, MetricType: typeName, Labels: merged, LabelsJSON: lj, Value: value, ObservedAt: env.ObservedAt, Interval: interval, MessageID: env.MessageID, ProducerNodeID: env.ProducerNodeID, ProducerVersion: env.ProducerVersion})
		if len(*out) > max {
			return fmt.Errorf("sample limit exceeded: %d > %d", len(*out), max)
		}
		return nil
	}
	switch family.GetType() {
	case dto.MetricType_COUNTER:
		return add(metric.GetCounter().GetValue(), "", nil)
	case dto.MetricType_GAUGE:
		return add(metric.GetGauge().GetValue(), "", nil)
	case dto.MetricType_UNTYPED:
		return add(metric.GetUntyped().GetValue(), "", nil)
	case dto.MetricType_HISTOGRAM:
		h := metric.GetHistogram()
		if err := add(h.GetSampleSum(), "_sum", nil); err != nil {
			return err
		}
		if err := add(float64(h.GetSampleCount()), "_count", nil); err != nil {
			return err
		}
		for _, bucket := range h.GetBucket() {
			if bucket == nil {
				continue
			}
			if err := add(float64(bucket.GetCumulativeCount()), "_bucket", map[string]string{"le": fmt.Sprintf("%g", bucket.GetUpperBound())}); err != nil {
				return err
			}
		}
		return nil
	case dto.MetricType_SUMMARY:
		s := metric.GetSummary()
		if err := add(s.GetSampleSum(), "_sum", nil); err != nil {
			return err
		}
		if err := add(float64(s.GetSampleCount()), "_count", nil); err != nil {
			return err
		}
		for _, q := range s.GetQuantile() {
			if q == nil {
				continue
			}
			if err := add(q.GetValue(), "", map[string]string{"quantile": fmt.Sprintf("%g", q.GetQuantile())}); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported metric type %s", family.GetType().String())
	}
}

func EncodeSnapshot(raw []byte, interval time.Duration, gzipLevel int, limits Limits) (*metricspb.MetricSnapshot, error) {
	limits = limits.normalized()
	if int64(len(raw)) > limits.MaxUncompressedBytes {
		return nil, fmt.Errorf("snapshot exceeds %d bytes", limits.MaxUncompressedBytes)
	}
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if len(fams) > limits.MaxMetricFamilies {
		return nil, fmt.Errorf("metric family limit exceeded")
	}
	count := 0
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			if m == nil {
				continue
			}
			switch f.GetType() {
			case dto.MetricType_HISTOGRAM:
				count += 2 + len(m.GetHistogram().GetBucket())
			case dto.MetricType_SUMMARY:
				count += 2 + len(m.GetSummary().GetQuantile())
			default:
				count++
			}
		}
	}
	var compressed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&compressed, gzipLevel)
	if err != nil {
		return nil, err
	}
	if _, err = zw.Write(raw); err != nil {
		return nil, err
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	if int64(compressed.Len()) > limits.MaxCompressedBytes {
		return nil, fmt.Errorf("compressed snapshot exceeds %d bytes", limits.MaxCompressedBytes)
	}
	sum := sha256.Sum256(raw)
	return &metricspb.MetricSnapshot{SchemaVersion: 1, CollectionIntervalSeconds: uint32(interval / time.Second), Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT, Compression: metricspb.Compression_COMPRESSION_GZIP, Data: compressed.Bytes(), MetricFamilyCount: uint32(len(fams)), SampleCount: uint32(count), UncompressedSha256: sum[:]}, nil
}
