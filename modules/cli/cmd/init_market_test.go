package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunBuiltinMarketInitRegistersOnlyRequestedMarkets(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"stock_cn", "crypto_okx"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "market.yaml"), []byte("schema_version: 1\nmarket_id: "+id+"\nspace_id: "+id+"\nregister_metadata: true\nruntime_enabled: false\nexecution: {timeout_seconds: 60, job_budget_ms: 30000, report_reserve_ms: 10000}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.seed.yaml"), []byte("spaces:\n  - {space_id: "+id+", name: test, owner: collector, status: active}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var applied []string
	summary, err := runBuiltinMarketInit(context.Background(), root, "stock_cn", func(_ context.Context, marketID string, _ []metadataImportCall) (metadataImportSummary, error) {
		applied = append(applied, marketID)
		return metadataImportSummary{Status: "ok", Applied: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "stock_cn" || summary.Total.Applied != 1 {
		t.Fatalf("applied=%v summary=%+v", applied, summary)
	}
}

func TestBuiltinMarketIDsRejectUnknownAndDuplicate(t *testing.T) {
	if _, err := parseBuiltinMarketIDs("stock_cn,stock_cn"); err == nil {
		t.Fatal("duplicate market accepted")
	}
	if _, err := parseBuiltinMarketIDs("unknown"); err == nil {
		t.Fatal("unknown market accepted")
	}
}
