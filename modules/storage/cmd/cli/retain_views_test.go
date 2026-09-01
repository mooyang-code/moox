package main

import (
	"strings"
	"testing"
)

func TestValidateRetainViewsRequiresExactConfirmedInventory(t *testing.T) {
	keep := []string{
		"crypto/binance_spot_kline_1m_view",
		"crypto/perpetual_kline_1h_view",
		"crypto/spot_kline_1h_view",
		"moox_system/host_resource_view",
		"moox_system/host_fs_view",
		"moox_system/host_disk_view",
		"moox_system/host_net_view",
		"moox_system/moox_service_metrics_view",
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
