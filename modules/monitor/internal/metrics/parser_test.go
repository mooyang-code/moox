package metrics

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"testing"
	"time"

	metricspb "github.com/mooyang-code/moox/packages/metricspb"
)

func TestEncodeParseHistogramSummary(t *testing.T) {
	raw := []byte("# TYPE request_duration_seconds histogram\nrequest_duration_seconds_bucket{method=\"GET\",le=\"0.5\"} 2\nrequest_duration_seconds_bucket{method=\"GET\",le=\"+Inf\"} 3\nrequest_duration_seconds_sum{method=\"GET\"} 1.2\nrequest_duration_seconds_count{method=\"GET\"} 3\n# TYPE request_size summary\nrequest_size{quantile=\"0.5\"} 10\nrequest_size_sum 20\nrequest_size_count 2\n")
	snapshot, err := EncodeSnapshot(raw, 30*time.Second, gzip.BestSpeed, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSnapshot(snapshot, Envelope{ServiceName: "svc", InstanceID: "i", ObservedAt: time.Unix(1, 0)}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("flattened samples=%d, want 7", len(got))
	}
	if got[0].SeriesID == got[len(got)-1].SeriesID {
		t.Fatal("series IDs must differ")
	}
}

func TestDecodeSnapshotChecksumAndLimits(t *testing.T) {
	raw := []byte("# TYPE x gauge\nx 1\n")
	sum := sha256.Sum256(raw)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	s := &metricspb.MetricSnapshot{SchemaVersion: 1, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT, Compression: metricspb.Compression_COMPRESSION_GZIP, Data: compressed.Bytes(), UncompressedSha256: sum[:], SampleCount: 1}
	if _, err := DecodeSnapshot(s, Limits{MaxUncompressedBytes: 1}); err == nil {
		t.Fatal("expected decompressed size limit")
	}
	s.UncompressedSha256 = []byte("bad")
	if _, err := DecodeSnapshot(s, Limits{}); err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestSeriesIDLengthPrefixes(t *testing.T) {
	if SeriesID("ab", "c", "m", nil) == SeriesID("a", "bc", "m", nil) {
		t.Fatal("length prefixes must disambiguate identity")
	}
}
