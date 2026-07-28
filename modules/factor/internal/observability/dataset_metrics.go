package observability

import (
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// NewDatasetMetrics registers Factor's committed output dataset metrics.
func NewDatasetMetrics(registerer prometheus.Registerer) (*report.DatasetMetrics, error) {
	return report.NewDatasetMetrics(registerer, "factor")
}
