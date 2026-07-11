package cmd

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/cobra"
)

func TestPublishCollectorMarketsSkipsFailClosedManifests(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "collector", "config", "markets"))
	old := collectorMarketsFlags
	defer func() { collectorMarketsFlags = old }()
	collectorMarketsFlags.ControlURL, collectorMarketsFlags.CloudAccountID, collectorMarketsFlags.Region, collectorMarketsFlags.ZipPath = "http://control", "account", "ap-test", "/tmp/unused.zip"
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	results, err := publishCollectorMarkets(cmd, root, "development")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("results=%+v", results)
	}
	for _, result := range results {
		if result.Status != "skipped_not_runtime_enabled" {
			t.Fatalf("result=%+v", result)
		}
	}
}
