package report

import "strings"

// Keep this list deliberately narrow. Services register many Prometheus
// metrics for local scraping; only facts consumed by Monitor's health view,
// alerts and Doctor are reported, and Monitor persists nothing else.
var healthMetricSuffixes = []string{
	"_dataset_enabled",
	"_dataset_expected_interval_seconds",
	"_dataset_last_run_timestamp_seconds",
	"_dataset_last_success_timestamp_seconds",
	"_dataset_input_watermark_timestamp_seconds",
	"_dataset_output_watermark_timestamp_seconds",
	"_dataset_inventory_last_success_timestamp_seconds",
	"_runs_total",
	"_last_success_timestamp_seconds",
	"_last_error_timestamp_seconds",
	"_business_watermark_timestamp_seconds",
	"_input_watermark_timestamp_seconds",
	"_metrics_errors_total",
	"_metrics_last_error_timestamp_seconds",
}

var healthMetricNames = map[string]struct{}{
	"moox_collector_market_fetch_assignment_required":                          {},
	"moox_collector_market_fetch_assignment_active":                            {},
	"moox_collector_market_fetch_assignment_last_success_timestamp_seconds":    {},
	"moox_collector_market_fetch_coordination_healthy":                         {},
	"moox_collector_market_fetch_coordination_failure":                         {},
	"moox_collector_market_fetch_coordination_pending":                         {},
	"moox_collector_market_fetch_coordination_pending_since_timestamp_seconds": {},
	"moox_collector_market_fetch_timer_capacity_total":                         {},
	"moox_collector_market_fetch_timer_capacity_required":                      {},
	"moox_collector_market_fetch_timer_capacity_active":                        {},
	"moox_collector_market_fetch_timer_capacity_headroom":                      {},
	"moox_collector_market_fetch_timer_available":                              {},
	"moox_collector_market_fetch_assignment_errors_total":                      {},
	"moox_collector_period_pending_total":                                      {},
	"moox_collector_period_report_retry_total":                                 {},
	"moox_collector_kline_resample_claims_total":                               {},
	"moox_collector_kline_resample_writes_total":                               {},
	"moox_collector_kline_resample_retries_total":                              {},
	"moox_collector_kline_resample_errors_total":                               {},
	"moox_storage_view_ack_errors_total":                                       {},
	"moox_storage_view_in_progress_errors_total":                               {},
	"moox_storage_outbox_publish_errors_total":                                 {},
	"moox_storage_outbox_pending_entries":                                      {},
	"moox_storage_outbox_oldest_age_seconds":                                   {},
	"moox_storage_view_period_waiting_datasets":                                {},
	"moox_storage_view_output_latest_data_time_seconds":                        {},
	"moox_storage_view_output_tracked_since_data_time_seconds":                 {},
	"moox_storage_view_output_subjects":                                        {},
	"moox_storage_view_output_lagging_subjects":                                {},
	"moox_storage_view_output_lagging_subject_data_time_seconds":               {},
}

// IsMonitoredMetric reports whether Monitor consumes a metric family. The
// reporter sends only these families, and Monitor keeps only these.
func IsMonitoredMetric(name string) bool {
	if strings.HasPrefix(name, "moox_timer_job_") {
		return false
	}
	if _, ok := healthMetricNames[name]; ok {
		return true
	}
	if !strings.HasPrefix(name, "moox_") {
		return false
	}
	for _, suffix := range healthMetricSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	if strings.HasPrefix(name, "moox_trade_balance_sync_") || strings.HasPrefix(name, "moox_doctor_") {
		return true
	}
	if strings.HasPrefix(name, "moox_storage_view_") && (strings.HasSuffix(name, "_errors_total") || strings.HasSuffix(name, "_watermark_timestamp_seconds")) {
		return true
	}
	if strings.HasPrefix(name, "moox_factor_") && (strings.HasSuffix(name, "_report_errors_total") || strings.HasSuffix(name, "_report_last_error_timestamp_seconds")) {
		return true
	}
	return false
}
