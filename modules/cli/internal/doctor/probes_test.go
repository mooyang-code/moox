package doctor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPProberRejectsPathsAndOversizedResponses(t *testing.T) {
	prober := HTTPProber{Auth: HealthAuth{AccessKey: "monitor", SecretKey: "secret"}}
	_, err := prober.Get(context.Background(), "http://localhost/admin")
	require.ErrorContains(t, err, "not allowed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxProbeBytes+1)))
	}))
	defer server.Close()
	_, err = prober.Get(context.Background(), server.URL+"/metrics")
	require.ErrorContains(t, err, "exceeds")
}

func TestProbeWritablePathAlwaysCleansUp(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data", "factor")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, ProbeWritablePath(cancelled, root, "data/factor"))
	matches, err := filepath.Glob(filepath.Join(dir, probePrefix+"*"))
	require.NoError(t, err)
	require.Empty(t, matches)
	require.Error(t, ProbeWritablePath(context.Background(), root, "../outside"))
}
