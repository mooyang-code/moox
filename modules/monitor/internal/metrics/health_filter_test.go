package metrics

import "testing"

func TestFilterHealthSamplesKeepsBusinessFactsOnly(t *testing.T) {
	keep := []string{
		"moox_collector_market_fetch_timer_available",
		"moox_collector_market_fetch_coordination_failure",
		"moox_collector_market_fetch_assignment_errors_total",
		"moox_collector_dataset_output_watermark_timestamp_seconds",
		"moox_factor_dataset_output_watermark_timestamp_seconds",
		"moox_trade_balance_sync_consecutive_failures",
		"moox_storage_view_ack_errors_total",
		"moox_storage_outbox_pending_entries",
		"moox_storage_outbox_oldest_age_seconds",
		"moox_doctor_runs_total",
		"moox_collector_kline_resample_claims_total",
		"moox_collector_kline_resample_writes_total",
		"moox_collector_kline_resample_retries_total",
		"moox_collector_kline_resample_errors_total",
		ViewDatasetOutputLastDataTimeMetric,
	}
	drop := []string{
		"go_gc_duration_seconds",
		"process_cpu_seconds_total",
		"trpc_client_requests_total",
		"http_requests_total",
		"moox_http_requests_total",
		"moox_collector_market_fetch_unrelated_debug_value",
	}
	for _, name := range keep {
		if !IsHealthMetric(name) {
			t.Errorf("business metric %q was filtered", name)
		}
	}
	for _, name := range drop {
		if IsHealthMetric(name) {
			t.Errorf("technical metric %q was retained", name)
		}
	}
}

func TestFilterHealthSamplesForKlineViewsDropsUnconfiguredViewSeries(t *testing.T) {
	samples := []Sample{
		{MetricName: ViewDatasetOutputLastDataTimeMetric, Labels: map[string]string{
			"space_id": "crypto", "view_id": "view_crypto_spot_kline_1m", "dataset_id": "dataset_binance_spot_kline_1m", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "venue:binance",
		}},
		{MetricName: ViewDatasetOutputLastDataTimeMetric, Labels: map[string]string{
			"space_id": "mooxsys", "view_id": "view_mooxsys_service_metrics", "dataset_id": "dataset_mooxsys_service_metrics", "subject_id": "service/metric", "freq": "30s", "series_tag": "default",
		}},
		{MetricName: "moox_storage_outbox_pending_entries"},
	}

	got := FilterHealthSamplesForKlineViews(samples, []ViewMetricScope{{
		SpaceID: "crypto", ViewID: "view_crypto_spot_kline_1m", DatasetID: "dataset_binance_spot_kline_1m", Frequency: "1m",
	}})
	if len(got) != 2 {
		t.Fatalf("filtered samples = %+v, want configured View metric and operational metric", got)
	}
	if got[0].MetricName != ViewDatasetOutputLastDataTimeMetric || got[1].MetricName != "moox_storage_outbox_pending_entries" {
		t.Fatalf("filtered samples = %+v", got)
	}
}
