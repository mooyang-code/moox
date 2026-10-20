package metrics

import (
	"context"
	"errors"
	"time"
)

var ErrMetricsStoreUnavailable = errors.New("metrics store is unavailable")

type QueryService struct {
	catalog      *MetricCatalog
	storage      *StorageAdapter
	messageStore *MetricMessageStore
	noDataAfter  time.Duration
}

// ListBarArrivals 读取一条数据链路在 [from, to) 内开盘的逐根到达记录。
func (q *QueryService) ListBarArrivals(ctx context.Context, chain BarArrivalChain, from, to time.Time) ([]BarArrival, bool, error) {
	if q == nil {
		return nil, false, ErrMetricsStoreUnavailable
	}
	return q.messageStore.ListBarArrivals(ctx, chain, from, to)
}

func NewQueryService(messageStore *MetricMessageStore, storage *StorageAdapter) *QueryService {
	return &QueryService{messageStore: messageStore, catalog: NewCatalog(messageStore), storage: storage, noDataAfter: 2 * time.Minute}
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
	if q == nil || q.messageStore == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	return q.messageStore.GetLatest(ctx, seriesID)
}

func (q *QueryService) ListLatestByMetricNames(ctx context.Context, names []string, limit int) ([]MetricLatest, error) {
	if q == nil || q.messageStore == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	return q.messageStore.ListLatestByMetricNames(ctx, names, limit)
}

func (q *QueryService) ListLatestByViewScopes(ctx context.Context, metricName string, scopes []ViewMetricScope, limit int) ([]MetricLatest, error) {
	if q == nil || q.messageStore == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	return q.messageStore.ListLatestByViewScopes(ctx, metricName, scopes, limit)
}

func (q *QueryService) ActiveDatasetSubjects(ctx context.Context, spaceID, datasetID string) (map[string]struct{}, error) {
	if q == nil || q.storage == nil {
		// Unit-level metric evaluation can operate on its explicit observations;
		// the production monitor wires the Storage metadata adapter here and gets
		// authoritative lifecycle filtering.
		return nil, nil
	}
	return q.storage.ListActiveDatasetSubjects(ctx, spaceID, datasetID)
}

// DatasetDisplayName returns a dataset's human name, or "" when unknown.
func (q *QueryService) DatasetDisplayName(ctx context.Context, spaceID, datasetID string) string {
	if q == nil || q.storage == nil {
		return ""
	}
	return q.storage.DatasetDisplayName(ctx, spaceID, datasetID)
}

// SeriesLimitStatus 返回新序列限额的使用情况，供健康概览判断指标目录是否在失控增长。
func (q *QueryService) SeriesLimitStatus(ctx context.Context, now time.Time) (SeriesLimitStatus, error) {
	if q == nil || q.messageStore == nil {
		return SeriesLimitStatus{}, ErrMetricsStoreUnavailable
	}
	return q.messageStore.SeriesLimitStatus(ctx, now)
}

// IngestRejects 返回各服务最近被拒收的快照及原因，供 Doctor 诊断。
func (q *QueryService) IngestRejects() []IngestReject {
	if q == nil {
		return nil
	}
	return q.messageStore.IngestRejects()
}
