package bootstrap

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/client"
)

type newSubjectMetadata struct{ subjects []string }

func (m newSubjectMetadata) GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func (m newSubjectMetadata) ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	rsp := &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}
	for _, id := range m.subjects {
		rsp.DatasetSubjects = append(rsp.DatasetSubjects, &storagepb.DatasetSubject{SubjectId: id, Status: "active"})
	}
	return rsp, nil
}

func (m newSubjectMetadata) ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error) {
	return &storagepb.ListTagsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

type newSubjectFirstSeen map[string]time.Time

func (f newSubjectFirstSeen) ObserveSubjects(context.Context, string, string, map[string]struct{}, time.Time) (map[string]time.Time, error) {
	return f, nil
}

// 新上线对象在等首根 K 线：检查按通过记录（不触发告警），状态是 degraded，页面显示黄色关注；宽限期过了才按失败记录。
func TestBusinessFreshnessRecordsNewSubjectsAsDegradedWithoutFailing(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	labels := `{"space_id":"crypto","view_id":"view","dataset_id":"dataset","freq":"1h"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, row := range []monmetrics.MetricLatest{
			{SeriesID: "latest", MetricName: monmetrics.ViewOutputLatestMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Truncate(time.Hour).Add(-time.Hour).Unix()), ObservedAt: now},
			{SeriesID: "tracked", MetricName: monmetrics.ViewOutputTrackedSinceMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-6 * time.Hour).Unix()), ObservedAt: now},
			{SeriesID: "subjects", MetricName: monmetrics.ViewOutputSubjectsMetric, MetricType: "gauge", LabelsJSON: labels, Value: 2, ObservedAt: now},
		} {
			require.NoError(t, db.Create(&row).Error)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), monmetrics.NewStorageAdapter(newSubjectMetadata{subjects: []string{"BTC", "ETH", "NEW1"}}))
	})
	require.NoError(t, err)
	repositories := manager.Repositories()
	evaluator := monmetrics.NewKlineFreshnessEvaluator(query, []monmetrics.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1h", MarketID: "crypto", Lateness: 5 * time.Minute,
	}}, 20)
	evaluator.SetSubjectFirstSeenStore(newSubjectFirstSeen{"NEW1": time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)})
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results, Now: func() time.Time { return now },
	}, repositories, nil, evaluator)
	require.NoError(t, run(t.Context()))

	results, err := repositories.Results.Recent(t.Context(), "crypto", "kline_freshness:crypto:view:1h", 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Success, "宽限期内不能按失败记录，否则会触发告警")
	require.Equal(t, domain.CheckStatusDegraded, results[0].Status)
	require.True(t, strings.Contains(results[0].ErrorMessage, "1 个新上线对象等待首根K线"), results[0].ErrorMessage)
	require.Contains(t, results[0].ErrorMessage, "22:15", "宽限截止时间用北京时间展示")
}
