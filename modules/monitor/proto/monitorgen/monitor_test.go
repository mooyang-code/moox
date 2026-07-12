package monitorpb

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"trpc.group/trpc-go/trpc-go/filter"
)

func exerciseMessage(t *testing.T, msg interface {
	Reset()
	String() string
	ProtoMessage()
}) {
	t.Helper()
	msg.Reset()
	_ = msg.String()
	msg.ProtoMessage()
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func TestProtoMessages_ShouldSupportResetAndString(t *testing.T) {
	exerciseMessage(t, &MonitorCheck{})
	exerciseMessage(t, &CheckResult{})
	exerciseMessage(t, &WebhookChannel{})
	exerciseMessage(t, &AlertRule{})
	exerciseMessage(t, &AlertState{})
	exerciseMessage(t, &AlertEvent{})
	exerciseMessage(t, &MetricServiceInfo{})
	exerciseMessage(t, &MetricNameInfo{})
	exerciseMessage(t, &MetricSeriesInfo{})
	exerciseMessage(t, &MetricLatestPoint{})
	exerciseMessage(t, &MetricHistoryPoint{})
	exerciseMessage(t, &ListMetricServicesReq{})
	exerciseMessage(t, &ListMetricServicesRsp{})
	exerciseMessage(t, &ListMetricNamesReq{})
	exerciseMessage(t, &ListMetricNamesRsp{})
	exerciseMessage(t, &ListMetricSeriesReq{})
	exerciseMessage(t, &ListMetricSeriesRsp{})
	exerciseMessage(t, &GetMetricLatestReq{})
	exerciseMessage(t, &GetMetricLatestRsp{})
	exerciseMessage(t, &QueryMetricHistoryReq{})
	exerciseMessage(t, &QueryMetricHistoryRsp{})
	exerciseMessage(t, &LabelMatcher{})
	exerciseMessage(t, &MetricSelector{})
	exerciseMessage(t, &MetricQuery{})
	exerciseMessage(t, &MetricCondition{})
	exerciseMessage(t, &MetricRule{})
	exerciseMessage(t, &MetricConditionEvaluation{})
	exerciseMessage(t, &MetricRuleEvaluation{})
	exerciseMessage(t, &MetricRuleState{})
	exerciseMessage(t, &ListMetricRulesReq{})
	exerciseMessage(t, &ListMetricRulesRsp{})
	exerciseMessage(t, &GetMetricRuleReq{})
	exerciseMessage(t, &GetMetricRuleRsp{})
	exerciseMessage(t, &CreateMetricRuleReq{})
	exerciseMessage(t, &CreateMetricRuleRsp{})
	exerciseMessage(t, &UpdateMetricRuleReq{})
	exerciseMessage(t, &UpdateMetricRuleRsp{})
	exerciseMessage(t, &DeleteMetricRuleReq{})
	exerciseMessage(t, &DeleteMetricRuleRsp{})
	exerciseMessage(t, &PreviewMetricRuleReq{})
	exerciseMessage(t, &PreviewMetricRuleRsp{})
	exerciseMessage(t, &ListMetricRuleEvaluationsReq{})
	exerciseMessage(t, &ListMetricRuleEvaluationsRsp{})
	exerciseMessage(t, &GetMetricRuleStateReq{})
	exerciseMessage(t, &GetMetricRuleStateRsp{})
	exerciseMessage(t, &MonitorInstance{})
	exerciseMessage(t, &GroupSummary{})
	exerciseMessage(t, &Overview{})
	exerciseMessage(t, &ListChecksReq{})
	exerciseMessage(t, &ListChecksRsp{})
	exerciseMessage(t, &GetCheckReq{})
	exerciseMessage(t, &GetCheckRsp{})
	exerciseMessage(t, &CreateCheckReq{})
	exerciseMessage(t, &CreateCheckRsp{})
	exerciseMessage(t, &UpdateCheckReq{})
	exerciseMessage(t, &UpdateCheckRsp{})
	exerciseMessage(t, &DeleteCheckReq{})
	exerciseMessage(t, &DeleteCheckRsp{})
	exerciseMessage(t, &RunCheckOnceReq{})
	exerciseMessage(t, &RunCheckOnceRsp{})
	exerciseMessage(t, &ListResultsReq{})
	exerciseMessage(t, &ListResultsRsp{})
	exerciseMessage(t, &GetOverviewReq{})
	exerciseMessage(t, &GetOverviewRsp{})
	exerciseMessage(t, &ListWebhookChannelsReq{})
	exerciseMessage(t, &ListWebhookChannelsRsp{})
	exerciseMessage(t, &CreateWebhookChannelReq{})
	exerciseMessage(t, &CreateWebhookChannelRsp{})
	exerciseMessage(t, &UpdateWebhookChannelReq{})
	exerciseMessage(t, &UpdateWebhookChannelRsp{})
	exerciseMessage(t, &DeleteWebhookChannelReq{})
	exerciseMessage(t, &DeleteWebhookChannelRsp{})
	exerciseMessage(t, &ListAlertRulesReq{})
	exerciseMessage(t, &ListAlertRulesRsp{})
	exerciseMessage(t, &CreateAlertRuleReq{})
	exerciseMessage(t, &CreateAlertRuleRsp{})
	exerciseMessage(t, &UpdateAlertRuleReq{})
	exerciseMessage(t, &UpdateAlertRuleRsp{})
	exerciseMessage(t, &DeleteAlertRuleReq{})
	exerciseMessage(t, &DeleteAlertRuleRsp{})
	exerciseMessage(t, &ListAlertEventsReq{})
	exerciseMessage(t, &ListAlertEventsRsp{})
	exerciseMessage(t, &ListMonitorInstancesReq{})
	exerciseMessage(t, &ListMonitorInstancesRsp{})
	exerciseMessage(t, &SyncSystemChecksReq{})
	exerciseMessage(t, &SyncSystemChecksRsp{})
	exerciseMessage(t, &HostAgentInfo{})
	exerciseMessage(t, &ListHostAgentsReq{})
	exerciseMessage(t, &ListHostAgentsRsp{})
	exerciseMessage(t, &HostMetricHistoryPoint{})
	exerciseMessage(t, &QueryHostMetricHistoryReq{})
	exerciseMessage(t, &QueryHostMetricHistoryRsp{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	callGetters := func(msg proto.Message) {
		t.Helper()
		rv := reflect.ValueOf(msg)
		if !rv.IsValid() {
			return
		}
		rt := rv.Type()
		for i := 0; i < rt.NumMethod(); i++ {
			method := rt.Method(i)
			if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
				method.Func.Call([]reflect.Value{rv})
			}
		}
	}
	// Typed-nil receivers exercise generated nil-guard getter branches.
	var (
		a *MonitorCheck
		b *CheckResult
		c *WebhookChannel
		d *AlertRule
		e *AlertState
		f *AlertEvent
		g *MetricServiceInfo
		h *MetricNameInfo
		i *MetricSeriesInfo
		j *MetricLatestPoint
		k *MetricHistoryPoint
		l *MetricRule
		m *HostAgentInfo
		n *Overview
		o *MonitorInstance
		p *ListChecksRsp
		q *CreateMetricRuleReq
		r *QueryHostMetricHistoryRsp
	)
	for _, msg := range []proto.Message{a, b, c, d, e, f, g, h, i, j, k, l, m, n, o, p, q, r} {
		callGetters(msg)
	}
	// Also call getters on every concrete message type via typed-nil through ProtoReflect type.
	for _, sample := range monitorProtoMessages() {
		rt := reflect.TypeOf(sample)
		nilMsg := reflect.Zero(rt).Interface().(proto.Message)
		callGetters(nilMsg)
		pr := sample.ProtoReflect()
		pr.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool { return true })
		_ = pr.Descriptor()
		assert.NotNil(t, sample)
	}
}

type MonitorMgrServiceStub struct{}
func (s *MonitorMgrServiceStub) ListChecks(context.Context, *ListChecksReq) (*ListChecksRsp, error) {
	return &ListChecksRsp{}, nil
}
func (s *MonitorMgrServiceStub) GetCheck(context.Context, *GetCheckReq) (*GetCheckRsp, error) {
	return &GetCheckRsp{}, nil
}
func (s *MonitorMgrServiceStub) CreateCheck(context.Context, *CreateCheckReq) (*CreateCheckRsp, error) {
	return &CreateCheckRsp{}, nil
}
func (s *MonitorMgrServiceStub) UpdateCheck(context.Context, *UpdateCheckReq) (*UpdateCheckRsp, error) {
	return &UpdateCheckRsp{}, nil
}
func (s *MonitorMgrServiceStub) DeleteCheck(context.Context, *DeleteCheckReq) (*DeleteCheckRsp, error) {
	return &DeleteCheckRsp{}, nil
}
func (s *MonitorMgrServiceStub) RunCheckOnce(context.Context, *RunCheckOnceReq) (*RunCheckOnceRsp, error) {
	return &RunCheckOnceRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListResults(context.Context, *ListResultsReq) (*ListResultsRsp, error) {
	return &ListResultsRsp{}, nil
}
func (s *MonitorMgrServiceStub) GetOverview(context.Context, *GetOverviewReq) (*GetOverviewRsp, error) {
	return &GetOverviewRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListWebhookChannels(context.Context, *ListWebhookChannelsReq) (*ListWebhookChannelsRsp, error) {
	return &ListWebhookChannelsRsp{}, nil
}
func (s *MonitorMgrServiceStub) CreateWebhookChannel(context.Context, *CreateWebhookChannelReq) (*CreateWebhookChannelRsp, error) {
	return &CreateWebhookChannelRsp{}, nil
}
func (s *MonitorMgrServiceStub) UpdateWebhookChannel(context.Context, *UpdateWebhookChannelReq) (*UpdateWebhookChannelRsp, error) {
	return &UpdateWebhookChannelRsp{}, nil
}
func (s *MonitorMgrServiceStub) DeleteWebhookChannel(context.Context, *DeleteWebhookChannelReq) (*DeleteWebhookChannelRsp, error) {
	return &DeleteWebhookChannelRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListAlertRules(context.Context, *ListAlertRulesReq) (*ListAlertRulesRsp, error) {
	return &ListAlertRulesRsp{}, nil
}
func (s *MonitorMgrServiceStub) CreateAlertRule(context.Context, *CreateAlertRuleReq) (*CreateAlertRuleRsp, error) {
	return &CreateAlertRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) UpdateAlertRule(context.Context, *UpdateAlertRuleReq) (*UpdateAlertRuleRsp, error) {
	return &UpdateAlertRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) DeleteAlertRule(context.Context, *DeleteAlertRuleReq) (*DeleteAlertRuleRsp, error) {
	return &DeleteAlertRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListAlertEvents(context.Context, *ListAlertEventsReq) (*ListAlertEventsRsp, error) {
	return &ListAlertEventsRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListMonitorInstances(context.Context, *ListMonitorInstancesReq) (*ListMonitorInstancesRsp, error) {
	return &ListMonitorInstancesRsp{}, nil
}
func (s *MonitorMgrServiceStub) SyncSystemChecks(context.Context, *SyncSystemChecksReq) (*SyncSystemChecksRsp, error) {
	return &SyncSystemChecksRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListHostAgents(context.Context, *ListHostAgentsReq) (*ListHostAgentsRsp, error) {
	return &ListHostAgentsRsp{}, nil
}
func (s *MonitorMgrServiceStub) QueryHostMetricHistory(context.Context, *QueryHostMetricHistoryReq) (*QueryHostMetricHistoryRsp, error) {
	return &QueryHostMetricHistoryRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListMetricServices(context.Context, *ListMetricServicesReq) (*ListMetricServicesRsp, error) {
	return &ListMetricServicesRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListMetricNames(context.Context, *ListMetricNamesReq) (*ListMetricNamesRsp, error) {
	return &ListMetricNamesRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListMetricSeries(context.Context, *ListMetricSeriesReq) (*ListMetricSeriesRsp, error) {
	return &ListMetricSeriesRsp{}, nil
}
func (s *MonitorMgrServiceStub) GetMetricLatest(context.Context, *GetMetricLatestReq) (*GetMetricLatestRsp, error) {
	return &GetMetricLatestRsp{}, nil
}
func (s *MonitorMgrServiceStub) QueryMetricHistory(context.Context, *QueryMetricHistoryReq) (*QueryMetricHistoryRsp, error) {
	return &QueryMetricHistoryRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListMetricRules(context.Context, *ListMetricRulesReq) (*ListMetricRulesRsp, error) {
	return &ListMetricRulesRsp{}, nil
}
func (s *MonitorMgrServiceStub) GetMetricRule(context.Context, *GetMetricRuleReq) (*GetMetricRuleRsp, error) {
	return &GetMetricRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) CreateMetricRule(context.Context, *CreateMetricRuleReq) (*CreateMetricRuleRsp, error) {
	return &CreateMetricRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) UpdateMetricRule(context.Context, *UpdateMetricRuleReq) (*UpdateMetricRuleRsp, error) {
	return &UpdateMetricRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) DeleteMetricRule(context.Context, *DeleteMetricRuleReq) (*DeleteMetricRuleRsp, error) {
	return &DeleteMetricRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) PreviewMetricRule(context.Context, *PreviewMetricRuleReq) (*PreviewMetricRuleRsp, error) {
	return &PreviewMetricRuleRsp{}, nil
}
func (s *MonitorMgrServiceStub) ListMetricRuleEvaluations(context.Context, *ListMetricRuleEvaluationsReq) (*ListMetricRuleEvaluationsRsp, error) {
	return &ListMetricRuleEvaluationsRsp{}, nil
}
func (s *MonitorMgrServiceStub) GetMetricRuleState(context.Context, *GetMetricRuleStateReq) (*GetMetricRuleStateRsp, error) {
	return &GetMetricRuleStateRsp{}, nil
}

func TestMonitorMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &MonitorMgrServiceStub{}
	ctx := context.Background()
	rsp, err := MonitorMgrService_ListChecks_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListChecksRsp{}, rsp)
	rsp, err = MonitorMgrService_GetCheck_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetCheckRsp{}, rsp)
	rsp, err = MonitorMgrService_CreateCheck_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateCheckRsp{}, rsp)
	rsp, err = MonitorMgrService_UpdateCheck_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateCheckRsp{}, rsp)
	rsp, err = MonitorMgrService_DeleteCheck_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeleteCheckRsp{}, rsp)
	rsp, err = MonitorMgrService_RunCheckOnce_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &RunCheckOnceRsp{}, rsp)
	rsp, err = MonitorMgrService_ListResults_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListResultsRsp{}, rsp)
	rsp, err = MonitorMgrService_GetOverview_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetOverviewRsp{}, rsp)
	rsp, err = MonitorMgrService_ListWebhookChannels_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListWebhookChannelsRsp{}, rsp)
	rsp, err = MonitorMgrService_CreateWebhookChannel_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateWebhookChannelRsp{}, rsp)
	rsp, err = MonitorMgrService_UpdateWebhookChannel_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateWebhookChannelRsp{}, rsp)
	rsp, err = MonitorMgrService_DeleteWebhookChannel_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeleteWebhookChannelRsp{}, rsp)
	rsp, err = MonitorMgrService_ListAlertRules_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListAlertRulesRsp{}, rsp)
	rsp, err = MonitorMgrService_CreateAlertRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateAlertRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_UpdateAlertRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateAlertRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_DeleteAlertRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeleteAlertRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_ListAlertEvents_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListAlertEventsRsp{}, rsp)
	rsp, err = MonitorMgrService_ListMonitorInstances_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListMonitorInstancesRsp{}, rsp)
	rsp, err = MonitorMgrService_SyncSystemChecks_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &SyncSystemChecksRsp{}, rsp)
	rsp, err = MonitorMgrService_ListHostAgents_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListHostAgentsRsp{}, rsp)
	rsp, err = MonitorMgrService_QueryHostMetricHistory_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &QueryHostMetricHistoryRsp{}, rsp)
	rsp, err = MonitorMgrService_ListMetricServices_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListMetricServicesRsp{}, rsp)
	rsp, err = MonitorMgrService_ListMetricNames_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListMetricNamesRsp{}, rsp)
	rsp, err = MonitorMgrService_ListMetricSeries_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListMetricSeriesRsp{}, rsp)
	rsp, err = MonitorMgrService_GetMetricLatest_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetMetricLatestRsp{}, rsp)
	rsp, err = MonitorMgrService_QueryMetricHistory_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &QueryMetricHistoryRsp{}, rsp)
	rsp, err = MonitorMgrService_ListMetricRules_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListMetricRulesRsp{}, rsp)
	rsp, err = MonitorMgrService_GetMetricRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetMetricRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_CreateMetricRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &CreateMetricRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_UpdateMetricRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &UpdateMetricRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_DeleteMetricRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &DeleteMetricRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_PreviewMetricRule_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &PreviewMetricRuleRsp{}, rsp)
	rsp, err = MonitorMgrService_ListMetricRuleEvaluations_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListMetricRuleEvaluationsRsp{}, rsp)
	rsp, err = MonitorMgrService_GetMetricRuleState_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetMetricRuleStateRsp{}, rsp)
}

func TestUnimplementedMonitorMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedMonitorMgr{}
	ctx := context.Background()
	_, err := svc.ListChecks(ctx, &ListChecksReq{})
	assert.Error(t, err)
	_, err = svc.GetCheck(ctx, &GetCheckReq{})
	assert.Error(t, err)
	_, err = svc.CreateCheck(ctx, &CreateCheckReq{})
	assert.Error(t, err)
	_, err = svc.UpdateCheck(ctx, &UpdateCheckReq{})
	assert.Error(t, err)
	_, err = svc.DeleteCheck(ctx, &DeleteCheckReq{})
	assert.Error(t, err)
	_, err = svc.RunCheckOnce(ctx, &RunCheckOnceReq{})
	assert.Error(t, err)
}

func TestRegisterMonitorMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &MonitorMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterMonitorMgrService(s, stub)
	})
	assert.True(t, s.registered)
}

type fakeTRPCService struct { registered bool }

func (f *fakeTRPCService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}

func (f *fakeTRPCService) Serve() error { return nil }

func (f *fakeTRPCService) Close(chan struct{}) error { return nil }

