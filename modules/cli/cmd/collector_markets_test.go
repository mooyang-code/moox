package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPublishCollectorMarketsSkipsFailClosedManifests(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sourceRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "collector", "config", "markets"))
	root := t.TempDir()
	for _, market := range []string{"stock_cn", "stock_us", "crypto_binance", "crypto_okx"} {
		if err := os.MkdirAll(filepath.Join(root, market), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"market.yaml", "provider-validation.yaml"} {
			raw, err := os.ReadFile(filepath.Join(sourceRoot, market, name))
			if err != nil {
				t.Fatal(err)
			}
			if name == "market.yaml" {
				raw = []byte(strings.Replace(string(raw), "runtime_enabled: true", "runtime_enabled: false", 1))
			}
			if err := os.WriteFile(filepath.Join(root, market, name), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
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
