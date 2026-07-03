package binance

import "testing"

func TestNormalizeStorageTarget(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		defaultPort string
		want        string
	}{
		{name: "empty", raw: "", defaultPort: "20102", want: "ip://127.0.0.1:20102"},
		{name: "host port", raw: "127.0.0.1:20102", defaultPort: "20102", want: "ip://127.0.0.1:20102"},
		{name: "ip target", raw: "ip://10.0.0.1:20102/", defaultPort: "20102", want: "ip://10.0.0.1:20102"},
		{name: "http target not converted", raw: "http://10.0.0.2:20201", defaultPort: "20102", want: "http://10.0.0.2:20201"},
		{name: "service discovery", raw: "polaris://moox.storage.Access", defaultPort: "20102", want: "polaris://moox.storage.Access"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeStorageTarget(tt.raw, tt.defaultPort); got != tt.want {
				t.Fatalf("normalizeStorageTarget(%q, %q) = %q, want %q", tt.raw, tt.defaultPort, got, tt.want)
			}
		})
	}
}
