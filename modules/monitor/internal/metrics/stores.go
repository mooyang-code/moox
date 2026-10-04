// Package metrics is a bounded context. Its message store owns metric catalog
// persistence, while internal/store owns monitor control-plane data.
// Keeping this boundary avoids coupling monitor checks and metric ingestion.
package metrics

import "gorm.io/gorm"

type Stores struct {
	Messages *MetricMessageStore
}

func NewStores(db *gorm.DB) *Stores {
	return &Stores{Messages: NewMetricMessageStore(db)}
}
