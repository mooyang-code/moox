package rpc

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
)

func noopMonitorFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

type monitorMgrStub struct{}
func (s *monitorMgrStub) ListChecks(context.Context, *pb.ListChecksReq) (*pb.ListChecksRsp, error) { return &pb.ListChecksRsp{}, nil }
func (s *monitorMgrStub) GetCheck(context.Context, *pb.GetCheckReq) (*pb.GetCheckRsp, error) { return &pb.GetCheckRsp{}, nil }
func (s *monitorMgrStub) CreateCheck(context.Context, *pb.CreateCheckReq) (*pb.CreateCheckRsp, error) { return &pb.CreateCheckRsp{}, nil }
func (s *monitorMgrStub) UpdateCheck(context.Context, *pb.UpdateCheckReq) (*pb.UpdateCheckRsp, error) { return &pb.UpdateCheckRsp{}, nil }
func (s *monitorMgrStub) DeleteCheck(context.Context, *pb.DeleteCheckReq) (*pb.DeleteCheckRsp, error) { return &pb.DeleteCheckRsp{}, nil }
func (s *monitorMgrStub) RunCheckOnce(context.Context, *pb.RunCheckOnceReq) (*pb.RunCheckOnceRsp, error) { return &pb.RunCheckOnceRsp{}, nil }
func (s *monitorMgrStub) ListResults(context.Context, *pb.ListResultsReq) (*pb.ListResultsRsp, error) { return &pb.ListResultsRsp{}, nil }
func (s *monitorMgrStub) GetOverview(context.Context, *pb.GetOverviewReq) (*pb.GetOverviewRsp, error) { return &pb.GetOverviewRsp{}, nil }
func (s *monitorMgrStub) ListWebhookChannels(context.Context, *pb.ListWebhookChannelsReq) (*pb.ListWebhookChannelsRsp, error) { return &pb.ListWebhookChannelsRsp{}, nil }
func (s *monitorMgrStub) CreateWebhookChannel(context.Context, *pb.CreateWebhookChannelReq) (*pb.CreateWebhookChannelRsp, error) { return &pb.CreateWebhookChannelRsp{}, nil }
func (s *monitorMgrStub) UpdateWebhookChannel(context.Context, *pb.UpdateWebhookChannelReq) (*pb.UpdateWebhookChannelRsp, error) { return &pb.UpdateWebhookChannelRsp{}, nil }
func (s *monitorMgrStub) DeleteWebhookChannel(context.Context, *pb.DeleteWebhookChannelReq) (*pb.DeleteWebhookChannelRsp, error) { return &pb.DeleteWebhookChannelRsp{}, nil }
func (s *monitorMgrStub) ListAlertRules(context.Context, *pb.ListAlertRulesReq) (*pb.ListAlertRulesRsp, error) { return &pb.ListAlertRulesRsp{}, nil }
func (s *monitorMgrStub) CreateAlertRule(context.Context, *pb.CreateAlertRuleReq) (*pb.CreateAlertRuleRsp, error) { return &pb.CreateAlertRuleRsp{}, nil }
func (s *monitorMgrStub) UpdateAlertRule(context.Context, *pb.UpdateAlertRuleReq) (*pb.UpdateAlertRuleRsp, error) { return &pb.UpdateAlertRuleRsp{}, nil }
func (s *monitorMgrStub) DeleteAlertRule(context.Context, *pb.DeleteAlertRuleReq) (*pb.DeleteAlertRuleRsp, error) { return &pb.DeleteAlertRuleRsp{}, nil }
func (s *monitorMgrStub) ListAlertEvents(context.Context, *pb.ListAlertEventsReq) (*pb.ListAlertEventsRsp, error) { return &pb.ListAlertEventsRsp{}, nil }
func (s *monitorMgrStub) ListMonitorInstances(context.Context, *pb.ListMonitorInstancesReq) (*pb.ListMonitorInstancesRsp, error) { return &pb.ListMonitorInstancesRsp{}, nil }
func (s *monitorMgrStub) SyncSystemChecks(context.Context, *pb.SyncSystemChecksReq) (*pb.SyncSystemChecksRsp, error) { return &pb.SyncSystemChecksRsp{}, nil }
func (s *monitorMgrStub) ListHostAgents(context.Context, *pb.ListHostAgentsReq) (*pb.ListHostAgentsRsp, error) { return &pb.ListHostAgentsRsp{}, nil }
func (s *monitorMgrStub) QueryHostMetricHistory(context.Context, *pb.QueryHostMetricHistoryReq) (*pb.QueryHostMetricHistoryRsp, error) { return &pb.QueryHostMetricHistoryRsp{}, nil }
func (s *monitorMgrStub) ListMetricServices(context.Context, *pb.ListMetricServicesReq) (*pb.ListMetricServicesRsp, error) { return &pb.ListMetricServicesRsp{}, nil }
func (s *monitorMgrStub) ListMetricNames(context.Context, *pb.ListMetricNamesReq) (*pb.ListMetricNamesRsp, error) { return &pb.ListMetricNamesRsp{}, nil }
func (s *monitorMgrStub) ListMetricSeries(context.Context, *pb.ListMetricSeriesReq) (*pb.ListMetricSeriesRsp, error) { return &pb.ListMetricSeriesRsp{}, nil }
func (s *monitorMgrStub) GetMetricLatest(context.Context, *pb.GetMetricLatestReq) (*pb.GetMetricLatestRsp, error) { return &pb.GetMetricLatestRsp{}, nil }
func (s *monitorMgrStub) QueryMetricHistory(context.Context, *pb.QueryMetricHistoryReq) (*pb.QueryMetricHistoryRsp, error) { return &pb.QueryMetricHistoryRsp{}, nil }
func (s *monitorMgrStub) ListMetricRules(context.Context, *pb.ListMetricRulesReq) (*pb.ListMetricRulesRsp, error) { return &pb.ListMetricRulesRsp{}, nil }
func (s *monitorMgrStub) GetMetricRule(context.Context, *pb.GetMetricRuleReq) (*pb.GetMetricRuleRsp, error) { return &pb.GetMetricRuleRsp{}, nil }
func (s *monitorMgrStub) CreateMetricRule(context.Context, *pb.CreateMetricRuleReq) (*pb.CreateMetricRuleRsp, error) { return &pb.CreateMetricRuleRsp{}, nil }
func (s *monitorMgrStub) UpdateMetricRule(context.Context, *pb.UpdateMetricRuleReq) (*pb.UpdateMetricRuleRsp, error) { return &pb.UpdateMetricRuleRsp{}, nil }
func (s *monitorMgrStub) DeleteMetricRule(context.Context, *pb.DeleteMetricRuleReq) (*pb.DeleteMetricRuleRsp, error) { return &pb.DeleteMetricRuleRsp{}, nil }
func (s *monitorMgrStub) PreviewMetricRule(context.Context, *pb.PreviewMetricRuleReq) (*pb.PreviewMetricRuleRsp, error) { return &pb.PreviewMetricRuleRsp{}, nil }
func (s *monitorMgrStub) ListMetricRuleEvaluations(context.Context, *pb.ListMetricRuleEvaluationsReq) (*pb.ListMetricRuleEvaluationsRsp, error) { return &pb.ListMetricRuleEvaluationsRsp{}, nil }
func (s *monitorMgrStub) GetMetricRuleState(context.Context, *pb.GetMetricRuleStateReq) (*pb.GetMetricRuleStateRsp, error) { return &pb.GetMetricRuleStateRsp{}, nil }

func TestMonitorMgrServiceHandlers_ShouldDispatch(t *testing.T) {
	stub := &monitorMgrStub{}
	ctx := context.Background()
	rsp, err := pb.MonitorMgrService_ListChecks_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListChecksRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_GetCheck_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetCheckRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_CreateCheck_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateCheckRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_UpdateCheck_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateCheckRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_DeleteCheck_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeleteCheckRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_RunCheckOnce_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.RunCheckOnceRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListResults_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListResultsRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_GetOverview_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetOverviewRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListWebhookChannels_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListWebhookChannelsRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_CreateWebhookChannel_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateWebhookChannelRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_UpdateWebhookChannel_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateWebhookChannelRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_DeleteWebhookChannel_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeleteWebhookChannelRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListAlertRules_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListAlertRulesRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_CreateAlertRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateAlertRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_UpdateAlertRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateAlertRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_DeleteAlertRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeleteAlertRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListAlertEvents_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListAlertEventsRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListMonitorInstances_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListMonitorInstancesRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_SyncSystemChecks_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.SyncSystemChecksRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListHostAgents_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListHostAgentsRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_QueryHostMetricHistory_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.QueryHostMetricHistoryRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListMetricServices_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListMetricServicesRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListMetricNames_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListMetricNamesRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListMetricSeries_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListMetricSeriesRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_GetMetricLatest_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetMetricLatestRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_QueryMetricHistory_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.QueryMetricHistoryRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListMetricRules_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListMetricRulesRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_GetMetricRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetMetricRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_CreateMetricRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.CreateMetricRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_UpdateMetricRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.UpdateMetricRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_DeleteMetricRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.DeleteMetricRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_PreviewMetricRule_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.PreviewMetricRuleRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_ListMetricRuleEvaluations_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListMetricRuleEvaluationsRsp{}, rsp)
	rsp, err = pb.MonitorMgrService_GetMetricRuleState_Handler(stub, ctx, noopMonitorFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetMetricRuleStateRsp{}, rsp)
}

func TestUnimplementedMonitorMgr_AllMethods(t *testing.T) {
	svc := &pb.UnimplementedMonitorMgr{}
	ctx := context.Background()
	_, err := svc.ListChecks(ctx, &pb.ListChecksReq{})
	assert.Error(t, err)
	_, err = svc.GetCheck(ctx, &pb.GetCheckReq{})
	assert.Error(t, err)
	_, err = svc.CreateCheck(ctx, &pb.CreateCheckReq{})
	assert.Error(t, err)
	_, err = svc.UpdateCheck(ctx, &pb.UpdateCheckReq{})
	assert.Error(t, err)
	_, err = svc.DeleteCheck(ctx, &pb.DeleteCheckReq{})
	assert.Error(t, err)
	_, err = svc.RunCheckOnce(ctx, &pb.RunCheckOnceReq{})
	assert.Error(t, err)
	_, err = svc.ListResults(ctx, &pb.ListResultsReq{})
	assert.Error(t, err)
	_, err = svc.GetOverview(ctx, &pb.GetOverviewReq{})
	assert.Error(t, err)
	_, err = svc.ListWebhookChannels(ctx, &pb.ListWebhookChannelsReq{})
	assert.Error(t, err)
	_, err = svc.CreateWebhookChannel(ctx, &pb.CreateWebhookChannelReq{})
	assert.Error(t, err)
	_, err = svc.UpdateWebhookChannel(ctx, &pb.UpdateWebhookChannelReq{})
	assert.Error(t, err)
	_, err = svc.DeleteWebhookChannel(ctx, &pb.DeleteWebhookChannelReq{})
	assert.Error(t, err)
	_, err = svc.ListAlertRules(ctx, &pb.ListAlertRulesReq{})
	assert.Error(t, err)
	_, err = svc.CreateAlertRule(ctx, &pb.CreateAlertRuleReq{})
	assert.Error(t, err)
	_, err = svc.UpdateAlertRule(ctx, &pb.UpdateAlertRuleReq{})
	assert.Error(t, err)
	_, err = svc.DeleteAlertRule(ctx, &pb.DeleteAlertRuleReq{})
	assert.Error(t, err)
	_, err = svc.ListAlertEvents(ctx, &pb.ListAlertEventsReq{})
	assert.Error(t, err)
	_, err = svc.ListMonitorInstances(ctx, &pb.ListMonitorInstancesReq{})
	assert.Error(t, err)
	_, err = svc.SyncSystemChecks(ctx, &pb.SyncSystemChecksReq{})
	assert.Error(t, err)
	_, err = svc.ListHostAgents(ctx, &pb.ListHostAgentsReq{})
	assert.Error(t, err)
	_, err = svc.QueryHostMetricHistory(ctx, &pb.QueryHostMetricHistoryReq{})
	assert.Error(t, err)
	_, err = svc.ListMetricServices(ctx, &pb.ListMetricServicesReq{})
	assert.Error(t, err)
	_, err = svc.ListMetricNames(ctx, &pb.ListMetricNamesReq{})
	assert.Error(t, err)
	_, err = svc.ListMetricSeries(ctx, &pb.ListMetricSeriesReq{})
	assert.Error(t, err)
	_, err = svc.GetMetricLatest(ctx, &pb.GetMetricLatestReq{})
	assert.Error(t, err)
	_, err = svc.QueryMetricHistory(ctx, &pb.QueryMetricHistoryReq{})
	assert.Error(t, err)
	_, err = svc.ListMetricRules(ctx, &pb.ListMetricRulesReq{})
	assert.Error(t, err)
	_, err = svc.GetMetricRule(ctx, &pb.GetMetricRuleReq{})
	assert.Error(t, err)
	_, err = svc.CreateMetricRule(ctx, &pb.CreateMetricRuleReq{})
	assert.Error(t, err)
	_, err = svc.UpdateMetricRule(ctx, &pb.UpdateMetricRuleReq{})
	assert.Error(t, err)
	_, err = svc.DeleteMetricRule(ctx, &pb.DeleteMetricRuleReq{})
	assert.Error(t, err)
	_, err = svc.PreviewMetricRule(ctx, &pb.PreviewMetricRuleReq{})
	assert.Error(t, err)
	_, err = svc.ListMetricRuleEvaluations(ctx, &pb.ListMetricRuleEvaluationsReq{})
	assert.Error(t, err)
	_, err = svc.GetMetricRuleState(ctx, &pb.GetMetricRuleStateReq{})
	assert.Error(t, err)
}
