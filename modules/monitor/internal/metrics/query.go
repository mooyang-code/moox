package metrics

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrMetricsRepositoryUnavailable = errors.New("metrics repository is unavailable")

type QueryService struct {
	catalog     *MetricCatalog
	storage     *StorageAdapter
	repo        *Repository
	noDataAfter time.Duration
}

func NewQueryService(repo *Repository, storage *StorageAdapter) *QueryService {
	return &QueryService{repo: repo, catalog: NewCatalog(repo), storage: storage, noDataAfter: 2 * time.Minute}
}
func (q *QueryService) Catalog() *MetricCatalog {
	if q == nil {
		return nil
	}
	return q.catalog
}
func (q *QueryService) SetNoDataAfter(d time.Duration) {
	if q != nil && d > 0 {
		q.noDataAfter = d
	}
}

func (q *QueryService) Latest(ctx context.Context, seriesID string) (*MetricLatest, error) {
	if q == nil || q.repo == nil {
		return nil, ErrMetricsRepositoryUnavailable
	}
	return q.repo.GetLatest(ctx, seriesID)
}

func (q *QueryService) History(ctx context.Context, seriesID, serviceName, metricName, labelsJSON string, start, end time.Time, desc bool, limit int) ([]HistoryPoint, error) {
	if q == nil || q.catalog == nil || q.storage == nil {
		return nil, ErrMetricsRepositoryUnavailable
	}
	series, err := q.catalog.FindSeries(ctx, seriesID, serviceName, metricName, labelsJSON, 500)
	if err != nil {
		return nil, err
	}
	if len(series) == 0 {
		return []HistoryPoint{}, nil
	}
	selectors := make([]HistorySelector, 0, len(series))
	for _, s := range series {
		selectors = append(selectors, HistorySelectorForSeries(s))
	}
	return q.storage.QueryHistorySelectors(ctx, selectors, start, end, desc, limit)
}

func parseTimeValue(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}

// ParseTime accepts the RFC3339 form used on the wire and is exported for RPC
// handlers that need to keep parsing semantics identical to history queries.
func ParseTime(raw string) (time.Time, error) { return parseTimeValue(raw) }
