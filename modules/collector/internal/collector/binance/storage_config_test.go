package binance

import "testing"

func TestResolveStorageBindingUsesBinanceYAMLAndDefaults(t *testing.T) {
	t.Parallel()

	spot, err := ResolveStorageBinding(InstTypeSPOT)
	if err != nil {
		t.Fatalf("ResolveStorageBinding(SPOT): %v", err)
	}
	assertBinding(t, spot, StorageBinding{
		SpaceID:         "crypto",
		DataSourceID:    "binance",
		SubjectType:     "crypto_pair",
		SubjectMarket:   "spot",
		RecordDatasetID: "binance_spot_symbols",
		KlineDatasetID:  "binance_spot_kline",
		BindDatasetIDs:  []string{"binance_spot_symbols", "binance_spot_kline"},
		AuthInfo: StorageAuthInfo{
			AppID:  "data-collector",
			AppKey: "binance-spot-collector",
		},
	})

	swap, err := ResolveStorageBinding(InstTypeSWAP)
	if err != nil {
		t.Fatalf("ResolveStorageBinding(SWAP): %v", err)
	}
	assertBinding(t, swap, StorageBinding{
		SpaceID:         "crypto",
		DataSourceID:    "binance",
		SubjectType:     "crypto_pair",
		SubjectMarket:   "swap",
		RecordDatasetID: "binance_swap_symbols",
		KlineDatasetID:  "binance_swap_kline",
		BindDatasetIDs:  []string{"binance_swap_symbols", "binance_swap_kline"},
		AuthInfo: StorageAuthInfo{
			AppID:  "data-collector",
			AppKey: "binance-swap-collector",
		},
	})
}

func TestResolveStorageBindingRejectsUnsupportedInstType(t *testing.T) {
	t.Parallel()

	if _, err := ResolveStorageBinding("MARGIN"); err == nil {
		t.Fatalf("expected unsupported inst type error")
	}
}

func TestResolveAPIConfigUsesBinanceYAML(t *testing.T) {
	t.Parallel()

	cfg, err := ResolveAPIConfig()
	if err != nil {
		t.Fatalf("ResolveAPIConfig returned error: %v", err)
	}
	if cfg.SpotBaseURL != "https://data-api.binance.vision" {
		t.Fatalf("SpotBaseURL = %q, want data-api.binance.vision", cfg.SpotBaseURL)
	}
	if cfg.SwapBaseURL != "https://fapi.binance.com" {
		t.Fatalf("SwapBaseURL = %q, want fapi.binance.com", cfg.SwapBaseURL)
	}
}

func assertBinding(t *testing.T, got StorageBinding, want StorageBinding) {
	t.Helper()
	if got.SpaceID != want.SpaceID || got.DataSourceID != want.DataSourceID ||
		got.SubjectType != want.SubjectType || got.SubjectMarket != want.SubjectMarket ||
		got.RecordDatasetID != want.RecordDatasetID ||
		got.KlineDatasetID != want.KlineDatasetID {
		t.Fatalf("binding mismatch:\n got: %+v\nwant: %+v", got, want)
	}
	if got.AuthInfo != want.AuthInfo {
		t.Fatalf("auth_info = %+v, want %+v", got.AuthInfo, want.AuthInfo)
	}
	if len(got.BindDatasetIDs) != len(want.BindDatasetIDs) {
		t.Fatalf("binding dataset count = %d, want %d", len(got.BindDatasetIDs), len(want.BindDatasetIDs))
	}
	for i := range want.BindDatasetIDs {
		if got.BindDatasetIDs[i] != want.BindDatasetIDs[i] {
			t.Fatalf("bind_dataset_ids[%d] = %s, want %s", i, got.BindDatasetIDs[i], want.BindDatasetIDs[i])
		}
	}
}
