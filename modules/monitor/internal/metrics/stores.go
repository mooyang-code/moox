// Package metrics is a bounded context. Its message/rule stores own metric
// catalog persistence, while internal/store owns monitor control-plane data.
// Keeping this boundary avoids coupling monitor checks and metric ingestion.
package metrics

import "gorm.io/gorm"

// Stores is the persistence graph for the metrics bounded context. It is
// created once during bootstrap so ingestion, queries, and rule evaluation
// share the same explicitly named stores.
type Stores struct {
	Messages *MetricMessageStore
	Rules    *MetricRuleStore
}

func NewStores(db *gorm.DB) *Stores {
	return &Stores{
		Messages: NewMetricMessageStore(db),
		Rules:    NewMetricRuleStore(db),
	}
}
