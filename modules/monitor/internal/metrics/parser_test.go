package metrics

import (
	"crypto/sha256"
	"testing"
	"time"

	metricspb "github.com/mooyang-code/moox/packages/metricspb"
)

func TestEncodeParseHistogramSummary(t *testing.T) {
	raw := []byte("# TYPE request_duration_seconds histogram\nrequest_duration_seconds_bucket{method=\"GET\",le=\"0.5\"} 2\nrequest_duration_seconds_bucket{method=\"GET\",le=\"+Inf\"} 3\nrequest_duration_seconds_sum{method=\"GET\"} 1.2\nrequest_duration_seconds_count{method=\"GET\"} 3\n# TYPE request_size summary\nrequest_size{quantile=\"0.5\"} 10\nrequest_size_sum 20\nrequest_size_count 2\n")
	snapshot, err := testSnapshot(raw, 30*time.Second)
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
	s := &metricspb.MetricSnapshot{SchemaVersion: 1, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT, Data: raw, DataSha256: sum[:], SampleCount: 1}
	if _, err := DecodeSnapshot(s, Limits{MaxUncompressedBytes: 1}); err == nil {
		t.Fatal("expected snapshot size limit")
	}
	if got, err := DecodeSnapshot(s, Limits{MaxUncompressedBytes: int64(len(raw))}); err != nil || string(got) != string(raw) {
		t.Fatalf("DecodeSnapshot() = %q, %v, want the exposition text at exactly the limit", got, err)
	}
	s.DataSha256 = []byte("bad")
	if _, err := DecodeSnapshot(s, Limits{}); err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestDefaultLimitsAllowFullKlineSnapshot(t *testing.T) {
	limits := DefaultLimits()
	if limits.MaxSamples < 100000 {
		t.Fatalf("MaxSamples = %d, want at least 100000", limits.MaxSamples)
	}
	if limits.MaxUncompressedBytes < 16<<20 {
		t.Fatalf("snapshot byte limits are too small: %+v", limits)
	}
}

func TestParseSnapshotRejectsDeclaredFamilyCountMismatch(t *testing.T) {
	snapshot, err := testSnapshot([]byte("# TYPE x gauge\nx 1\n"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.MetricFamilyCount = 2
	if _, err := ParseSnapshot(snapshot, Envelope{ServiceName: "svc", InstanceID: "i"}, Limits{}); err == nil {
		t.Fatal("ParseSnapshot() error = nil, want family count mismatch")
	}
}

func testSnapshot(raw []byte, interval time.Duration) (*metricspb.MetricSnapshot, error) {
	sum := sha256.Sum256(raw)
	return &metricspb.MetricSnapshot{
		SchemaVersion:             1,
		CollectionIntervalSeconds: uint32(interval / time.Second),
		Format:                    metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT,
		Data:                      raw,
		DataSha256:                sum[:],
	}, nil
}

func TestSeriesIDLengthPrefixes(t *testing.T) {
	if SeriesID("ab", "c", "m", nil) == SeriesID("a", "bc", "m", nil) {
		t.Fatal("length prefixes must disambiguate identity")
	}
}
