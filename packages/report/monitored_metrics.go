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
	"_dataset_last_partial_timestamp_seconds",
	"_dataset_input_watermark_timestamp_seconds",
	"_dataset_output_watermark_timestamp_seconds",
	"_dataset_output_arrival_timestamp_seconds",
	"_dataset_inventory_last_success_timestamp_seconds",
	"_dataset_info",
	"_runs_total",
	"_last_success_timestamp_seconds",
	"_last_error_timestamp_seconds",
	"_metrics_errors_total",
	"_metrics_last_error_timestamp_seconds",
}

var healthMetricNames = map[string]struct{}{
	AttentionMetric: {},
	"moox_storage_view_output_arrival_timestamp_seconds":         {},
	"moox_storage_view_output_latest_data_time_seconds":          {},
	"moox_storage_view_output_tracked_since_data_time_seconds":   {},
	"moox_storage_view_output_subjects":                          {},
	"moox_storage_view_output_lagging_subjects":                  {},
	"moox_storage_view_output_lagging_subject_data_time_seconds": {},
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
	if strings.HasPrefix(name, "moox_storage_view_") && (strings.HasSuffix(name, "_errors_total") || strings.HasSuffix(name, "_watermark_timestamp_seconds")) {
		return true
	}
	if strings.HasPrefix(name, "moox_factor_") && (strings.HasSuffix(name, "_report_errors_total") || strings.HasSuffix(name, "_report_last_error_timestamp_seconds")) {
		return true
	}
	return false
}
