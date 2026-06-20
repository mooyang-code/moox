package factkey

import "testing"

func TestBuildTimeSeriesDataKeyUsesSubjectFreqAndStableDimensionHash(t *testing.T) {
	left := BuildTimeSeriesDataKey("BTC-USDT", "1h", map[string]string{"market": "spot", "adjust": "none"})
	right := BuildTimeSeriesDataKey("BTC-USDT", "1h", map[string]string{"adjust": "none", "market": "spot"})

	if left == "" {
		t.Fatalf("time series data key should not be empty")
	}
	if left != right {
		t.Fatalf("dimension order should not affect data key: %q != %q", left, right)
	}
	if left == BuildTimeSeriesDataKey("BTC-USDT", "1h", nil) {
		t.Fatalf("non-empty dimensions should change data key")
	}
}

func TestBuildObjectDataKeyRequiresObjectID(t *testing.T) {
	got, err := BuildObjectDataKey("news|123")
	if err != nil {
		t.Fatalf("build object data key: %v", err)
	}
	if got != "news%7C123" {
		t.Fatalf("object data key = %q, want escaped object id", got)
	}

	if _, err := BuildObjectDataKey(""); err == nil {
		t.Fatalf("empty object id should fail")
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "_"},
		{name: "rfc3339 utc", input: "2025-03-03T00:05:00Z", want: "2025-03-03T00:05:00.000000000Z"},
		{name: "rfc3339 offset", input: "2025-03-03T08:05:00+08:00", want: "2025-03-03T00:05:00.000000000Z"},
		{name: "rfc3339 nano", input: "2025-03-03T00:05:00.123456789Z", want: "2025-03-03T00:05:00.123456789Z"},
		{name: "custom version", input: "v1|draft", want: "v1%7Cdraft"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeVersion(tt.input); got != tt.want {
				t.Fatalf("NormalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeTimeVersionRejectsNonRFC3339(t *testing.T) {
	got, err := NormalizeTimeVersion("2025-03-03T08:05:00+08:00")
	if err != nil {
		t.Fatalf("normalize time version: %v", err)
	}
	if got != "2025-03-03T00:05:00.000000000Z" {
		t.Fatalf("time version = %q", got)
	}

	if _, err := NormalizeTimeVersion("2025-03-03 00:05:00"); err == nil {
		t.Fatalf("non-RFC3339 time should fail")
	}
}
