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
