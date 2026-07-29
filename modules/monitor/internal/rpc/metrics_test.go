package rpc

import (
	"context"
	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestMetricPageAndResult(t *testing.T) {
	offset, limit := metricPage(nil)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 50, limit)
	offset, limit = metricPage(&commonpb.Page{Page: 2, Size: 1000})
	assert.Equal(t, 500, offset)
	assert.Equal(t, 500, limit)
	result := metricPageResult(0, 50, 120)
	assert.True(t, result.GetHasMore())
	assert.Equal(t, uint32(120), result.GetTotal())
}

func TestMetricPBConverters(t *testing.T) {
	now := time.Now().UTC()
	series := seriesToPB(monmetrics.MetricSeries{SeriesID: "s1", ServiceName: "svc", MetricName: "cpu", LastSeenAt: now, IsStale: true})
	assert.Equal(t, "s1", series.GetSeriesId())
	assert.True(t, series.GetStale())
	latest := latestToPB(monmetrics.MetricLatest{SeriesID: "s1", Value: 1.5, ObservedAt: now, IntervalSeconds: 30})
	assert.InDelta(t, 1.5, latest.GetValue(), 0.001)
	eval := evaluationToPB(&monmetrics.RuleEvaluation{
		EvaluationID: "e1", SpaceID: "moox_system", RuleID: "r1", EvaluatedAt: now,
		Result: true, Status: domain.AlertStatusFiring,
		Conditions: []monmetrics.ConditionResult{{ConditionID: "c1", SelectedSeriesCount: 2, Value: 1, Threshold: 0.5, HasData: true, Result: true}},
	})
	require.NotNil(t, eval)
	assert.Len(t, eval.GetConditions(), 1)
	state := stateToPB(monmetrics.MetricRuleStateRow{SpaceID: "moox_system", RuleID: "r1", Status: domain.AlertStatusOK, TriggerCount: 1})
	assert.Equal(t, monitorpb.AlertStatus_ALERT_STATUS_OK, state.GetStatus())
}

func openMonitorDB(t *testing.T) *store.Store {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	return mgr
}

func seedMetricCatalog(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&monmetrics.MetricService{
		ServiceName: "api", InstanceID: "i1", BootID: "b1", NodeID: "n1", Version: "v1", LastSeenAt: now,
	}).Error)
	require.NoError(t, db.Create(&monmetrics.MetricSeries{
		ServiceName: "api", InstanceID: "i1", SeriesID: "series-1", MetricName: "requests",
		MetricType: "gauge", LabelsJSON: "{}", LastSeenAt: now,
	}).Error)
	require.NoError(t, db.Create(&monmetrics.MetricLatest{
		SeriesID: "series-1", ServiceName: "api", InstanceID: "i1", MetricName: "requests",
		MetricType: "gauge", LabelsJSON: "{}", Value: 42, ObservedAt: now, IntervalSeconds: 30, MessageID: "m1",
	}).Error)
}

func TestMetricRPCWithInjectedQueryService(t *testing.T) {
	mgr := openMonitorDB(t)
	query, err := store.WithDatabase(mgr, func(db *gorm.DB) *monmetrics.QueryService {
		seedMetricCatalog(t, db)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	svc := New(mgr.Repositories(), Options{InstanceID: "monitor-test", MetricsQuery: query})
	ctx := context.Background()

	services, err := svc.ListMetricServices(ctx, &monitorpb.ListMetricServicesReq{SpaceId: "moox_system", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, services.GetRetInfo().GetCode())
	require.Len(t, services.GetServices(), 1)
	assert.Equal(t, "api", services.GetServices()[0].GetServiceName())

	names, err := svc.ListMetricNames(ctx, &monitorpb.ListMetricNamesReq{SpaceId: "moox_system", ServiceName: "api"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, names.GetRetInfo().GetCode())
	require.Len(t, names.GetNames(), 1)
	assert.Equal(t, "requests", names.GetNames()[0].GetMetricName())
	assert.NotEmpty(t, names.GetNames()[0].GetLastSeenAt())

	series, err := svc.ListMetricSeries(ctx, &monitorpb.ListMetricSeriesReq{SpaceId: "moox_system", ServiceName: "api", MetricName: "requests"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, series.GetRetInfo().GetCode())
	require.Len(t, series.GetSeries(), 1)

	latest, err := svc.GetMetricLatest(ctx, &monitorpb.GetMetricLatestReq{SpaceId: "moox_system", SeriesId: "series-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, latest.GetRetInfo().GetCode())
	assert.InDelta(t, 42, latest.GetLatest().GetValue(), 0.001)

	missing, err := svc.GetMetricLatest(ctx, &monitorpb.GetMetricLatestReq{SpaceId: "moox_system", SeriesId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())

	emptySeries, err := svc.GetMetricLatest(ctx, &monitorpb.GetMetricLatestReq{SpaceId: "moox_system"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, emptySeries.GetRetInfo().GetCode())

	history, err := svc.QueryMetricHistory(ctx, &monitorpb.QueryMetricHistoryReq{
		SpaceId: "moox_system", SeriesId: "series-1",
		StartAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		EndAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Limit:   10,
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, history.GetRetInfo().GetCode())
}

func TestMetricRuleRPCWithInjectedStores(t *testing.T) {
	mgr := openMonitorDB(t)
	ruleStore, err := store.WithDatabase(mgr, monmetrics.NewMetricRuleStore)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, mgr.Repositories().Alerts.CreateWebhook(ctx, &domain.WebhookChannel{
		SpaceID: "moox_system", WebhookID: "ops", Name: "ops", URL: "http://example.invalid/hook", Enabled: true,
	}))

	svc := New(mgr.Repositories(), Options{InstanceID: "monitor-test", MetricRules: ruleStore})
	rule := &monitorpb.MetricRule{
		SpaceId: "moox_system", RuleId: "rule-1", Name: "high",
		Conditions: []*monitorpb.MetricCondition{{
			ConditionId: "A",
			Query: &monitorpb.MetricQuery{
				Selector:      &monitorpb.MetricSelector{ServiceName: "api", MetricName: "requests"},
				TimeReducer:   monitorpb.TimeReducer_TIME_REDUCER_CURRENT,
				SeriesReducer: monitorpb.SeriesReducer_SERIES_REDUCER_MAX,
			},
			Compare:      monitorpb.CompareOperator_COMPARE_OPERATOR_GT,
			Threshold:    10,
			NoDataPolicy: monitorpb.NoDataPolicy_NO_DATA_POLICY_OK,
		}},
		Connector:                 monitorpb.LogicalOperator_LOGICAL_OPERATOR_AND,
		ConsecutiveTriggerCount:   2,
		ConsecutiveRecoveryCount:  2,
		EvaluationIntervalSeconds: 30,
		WebhookIds:                []string{"ops"},
		Enabled:                   true,
	}

	created, err := svc.CreateMetricRule(ctx, &monitorpb.CreateMetricRuleReq{Rule: rule})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())

	listed, err := svc.ListMetricRules(ctx, &monitorpb.ListMetricRulesReq{SpaceId: "moox_system"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, listed.GetRetInfo().GetCode())
	require.Len(t, listed.GetRules(), 1)

	got, err := svc.GetMetricRule(ctx, &monitorpb.GetMetricRuleReq{SpaceId: "moox_system", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, got.GetRetInfo().GetCode())

	require.NoError(t, ruleStore.UpsertState(ctx, &monmetrics.MetricRuleStateRow{
		SpaceID: "moox_system", RuleID: "rule-1", Status: domain.AlertStatusOK,
	}))
	require.NoError(t, ruleStore.InsertEvaluation(ctx, &monmetrics.MetricRuleEvaluationRow{
		SpaceID: "moox_system", RuleID: "rule-1", EvaluatedAt: time.Now().UTC(), Status: domain.AlertStatusOK, ResultJSON: `{"result":true}`,
	}))

	evals, err := svc.ListMetricRuleEvaluations(ctx, &monitorpb.ListMetricRuleEvaluationsReq{SpaceId: "moox_system", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, evals.GetRetInfo().GetCode())
	require.NotEmpty(t, evals.GetEvaluations())

	state, err := svc.GetMetricRuleState(ctx, &monitorpb.GetMetricRuleStateReq{SpaceId: "moox_system", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, state.GetRetInfo().GetCode())

	updated := protoCloneRule(rule)
	updated.Name = "higher"
	upd, err := svc.UpdateMetricRule(ctx, &monitorpb.UpdateMetricRuleReq{Rule: updated})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, upd.GetRetInfo().GetCode())

	del, err := svc.DeleteMetricRule(ctx, &monitorpb.DeleteMetricRuleReq{SpaceId: "moox_system", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, del.GetRetInfo().GetCode())

	missing, err := svc.GetMetricRule(ctx, &monitorpb.GetMetricRuleReq{SpaceId: "moox_system", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
}

func protoCloneRule(rule *monitorpb.MetricRule) *monitorpb.MetricRule {
	return proto.Clone(rule).(*monitorpb.MetricRule)
}

func TestMetricRPCUnavailableBranches(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	cases := []func() *commonpb.RetInfo{
		func() *commonpb.RetInfo {
			rsp, _ := svc.ListMetricNames(ctx, &monitorpb.ListMetricNamesReq{SpaceId: "moox_system"})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.ListMetricSeries(ctx, &monitorpb.ListMetricSeriesReq{SpaceId: "moox_system"})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.ListMetricRules(ctx, &monitorpb.ListMetricRulesReq{SpaceId: "moox_system"})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.GetMetricRule(ctx, &monitorpb.GetMetricRuleReq{SpaceId: "moox_system", RuleId: "r"})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.CreateMetricRule(ctx, &monitorpb.CreateMetricRuleReq{})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.UpdateMetricRule(ctx, &monitorpb.UpdateMetricRuleReq{})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.DeleteMetricRule(ctx, &monitorpb.DeleteMetricRuleReq{SpaceId: "moox_system", RuleId: "r"})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.PreviewMetricRule(ctx, &monitorpb.PreviewMetricRuleReq{})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.ListMetricRuleEvaluations(ctx, &monitorpb.ListMetricRuleEvaluationsReq{SpaceId: "moox_system"})
			return rsp.GetRetInfo()
		},
		func() *commonpb.RetInfo {
			rsp, _ := svc.GetMetricRuleState(ctx, &monitorpb.GetMetricRuleStateReq{SpaceId: "moox_system", RuleId: "r"})
			return rsp.GetRetInfo()
		},
	}
	for i, fn := range cases {
		ret := fn()
		assert.Equal(t, commonpb.ErrorCode_INNER_ERR, ret.GetCode(), "case %d", i)
	}
}

type hostReaderAccessFake struct {
	snap *hostmetricpb.HostSnapshot
}

func (f *hostReaderAccessFake) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	cfg := monconfig.Default().Metrics.HostStorage
	at := time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339Nano)
	agentID := "agent-1"
	if len(req.GetSelectors()) > 0 {
		agentID = req.GetSelectors()[0].GetSubjectId()
	}
	dataset := cfg.ResourceDatasetID
	if len(req.GetSelectors()) > 0 {
		dataset = req.GetSelectors()[0].GetDatasetId()
	}
	var rows []*storagepb.TimeSeriesRow
	if dataset == cfg.ResourceDatasetID {
		rows = []*storagepb.TimeSeriesRow{resourceRowForTest(cfg, at, f.snap, agentID)}
	}
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    rows,
	}, nil
}

func resourceRowForTest(cfg monconfig.HostStorageConfig, at string, snap *hostmetricpb.HostSnapshot, agentID string) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{SpaceId: cfg.SpaceID, DatasetId: cfg.ResourceDatasetID, SubjectId: agentID, Freq: cfg.Frequency, DataTime: at},
		Fields: []*storagepb.FieldValue{
			{FieldId: "logical_cores", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: int64(snap.GetCpu().GetLogicalCores())}}},
			{FieldId: "cpu_usage_percent", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: snap.GetCpu().GetUsagePercent()}}},
			{FieldId: "cpu_usage_available", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: true}}},
			{FieldId: "memory_total_bytes", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: int64(snap.GetMemory().GetTotalBytes())}}},
			{FieldId: "memory_used_bytes", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: int64(snap.GetMemory().GetUsedBytes())}}},
			{FieldId: "memory_available_bytes", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: int64(snap.GetMemory().GetAvailableBytes())}}},
			{FieldId: "memory_usage_percent", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: snap.GetMemory().GetUsagePercent()}}},
		},
	}
}

func TestHostRPCWithInjectedStoreAndReader(t *testing.T) {
	mgr := openMonitorDB(t)
	hostStore := hostmetrics.NewStore(nil, nil)
	snap := &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsagePercent: 12, UsageAvailable: true},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40},
	}
	cfg := monconfig.Default().Metrics.HostStorage
	hostReader := hostmetrics.NewStorageReader(&hostReaderAccessFake{snap: snap}, cfg)
	svc := New(mgr.Repositories(), Options{
		InstanceID:       "monitor-test",
		HostStore:        hostStore,
		HostReader:       hostReader,
		HostStorageReady: func() bool { return true },
	})
	ctx := context.Background()

	agents, err := svc.ListHostAgents(ctx, &monitorpb.ListHostAgentsReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, agents.GetRetInfo().GetCode())
	assert.True(t, agents.GetStorageAvailable())

	hist, err := svc.QueryHostMetricHistory(ctx, &monitorpb.QueryHostMetricHistoryReq{
		AgentId: "agent-1",
		StartAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		EndAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Limit:   10,
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, hist.GetRetInfo().GetCode())
	require.NotEmpty(t, hist.GetPoints())
}

func TestHostRPCValidationBranches(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	agents, err := svc.ListHostAgents(ctx, &monitorpb.ListHostAgentsReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, agents.GetRetInfo().GetCode())

	empty, err := svc.QueryHostMetricHistory(ctx, &monitorpb.QueryHostMetricHistoryReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, empty.GetRetInfo().GetCode())

	badStart, err := svc.QueryHostMetricHistory(ctx, &monitorpb.QueryHostMetricHistoryReq{AgentId: "a", StartAt: "bad"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, badStart.GetRetInfo().GetCode())

	badEnd, err := svc.QueryHostMetricHistory(ctx, &monitorpb.QueryHostMetricHistoryReq{
		AgentId: "a", StartAt: time.Now().UTC().Format(time.RFC3339Nano), EndAt: "bad",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, badEnd.GetRetInfo().GetCode())

	inverted, err := svc.QueryHostMetricHistory(ctx, &monitorpb.QueryHostMetricHistoryReq{
		AgentId: "a",
		StartAt: time.Now().UTC().Format(time.RFC3339Nano),
		EndAt:   time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, inverted.GetRetInfo().GetCode())

	noReader, err := svc.QueryHostMetricHistory(ctx, &monitorpb.QueryHostMetricHistoryReq{AgentId: "a"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, noReader.GetRetInfo().GetCode())
}

func TestListMetricServicesRejectsInvalidSpace(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.ListMetricServices(context.Background(), &monitorpb.ListMetricServicesReq{SpaceId: "default"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestGetMetricLatestRequiresSeriesID(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.GetMetricLatest(context.Background(), &monitorpb.GetMetricLatestReq{SpaceId: "moox_system"})
	require.NoError(t, err)
	// newTestService 未注入 MetricsQuery，优先返回不可用错误。
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}

func TestQueryMetricHistoryRejectsInvalidTime(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.QueryMetricHistory(context.Background(), &monitorpb.QueryMetricHistoryReq{
		SpaceId: "moox_system", StartAt: "bad", EndAt: "2026-01-02T00:00:00Z",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}
