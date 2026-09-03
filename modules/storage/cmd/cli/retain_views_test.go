package main

import (
	"strings"
	"testing"
)

func TestValidateRetainViewsRequiresExactConfirmedInventory(t *testing.T) {
	keep := []string{
		"crypto/view_crypto_spot_kline_1m",
		"crypto/view_crypto_swap_kline_1h",
		"crypto/view_crypto_spot_kline_1h",
		"mooxsys/view_mooxsys_host_resource",
		"mooxsys/view_mooxsys_host_fs",
		"mooxsys/view_mooxsys_host_disk",
		"mooxsys/view_mooxsys_host_net",
		"mooxsys/view_mooxsys_service_metrics",
	}
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: keep, yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: keep}); err == nil {
		t.Fatal("missing --yes was accepted")
	}
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: keep[:7], yes: true}); err == nil {
		t.Fatal("incomplete keep inventory was accepted")
	}
	duplicate := append([]string(nil), keep...)
	duplicate[7] = duplicate[0]
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: duplicate, yes: true}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate keep inventory error = %v", err)
	}
}
