package metrics

import "testing"

func TestFilterHealthSamplesKeepsBusinessFactsOnly(t *testing.T) {
	keep := []string{
		"moox_collector_dataset_output_watermark_timestamp_seconds",
		"moox_factor_dataset_output_watermark_timestamp_seconds",
		"moox_attention_active",
		ViewOutputLatestMetric,
		ViewOutputLaggingSubjectMetric,
	}
	drop := []string{
		"go_gc_duration_seconds",
		"process_cpu_seconds_total",
		"trpc_client_requests_total",
		"http_requests_total",
		"moox_http_requests_total",
		"moox_doctor_debug_counter",
		"moox_collector_market_fetch_unrelated_debug_value",
		"moox_collector_market_fetch_timer_available",
		"moox_collector_kline_resample_claims_total",
		"moox_storage_outbox_publish_errors_total",
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
