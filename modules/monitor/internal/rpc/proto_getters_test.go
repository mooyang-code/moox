package rpc

import (
	"reflect"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func MonitorProtoMessages() []proto.Message {
	return []proto.Message{
		&pb.MonitorCheck{},
		&pb.CheckResult{},
		&pb.WebhookChannel{},
		&pb.AlertRule{},
		&pb.AlertState{},
		&pb.AlertEvent{},
		&pb.MetricServiceInfo{},
		&pb.MetricNameInfo{},
		&pb.MetricSeriesInfo{},
		&pb.MetricLatestPoint{},
		&pb.MetricHistoryPoint{},
		&pb.ListMetricServicesReq{},
		&pb.ListMetricServicesRsp{},
		&pb.ListMetricNamesReq{},
		&pb.ListMetricNamesRsp{},
		&pb.ListMetricSeriesReq{},
		&pb.ListMetricSeriesRsp{},
		&pb.GetMetricLatestReq{},
		&pb.GetMetricLatestRsp{},
		&pb.QueryMetricHistoryReq{},
		&pb.QueryMetricHistoryRsp{},
		&pb.LabelMatcher{},
		&pb.MetricSelector{},
		&pb.MetricQuery{},
		&pb.MetricCondition{},
		&pb.MetricRule{},
		&pb.MetricConditionEvaluation{},
		&pb.MetricRuleEvaluation{},
		&pb.MetricRuleState{},
		&pb.ListMetricRulesReq{},
		&pb.ListMetricRulesRsp{},
		&pb.GetMetricRuleReq{},
		&pb.GetMetricRuleRsp{},
		&pb.CreateMetricRuleReq{},
		&pb.CreateMetricRuleRsp{},
		&pb.UpdateMetricRuleReq{},
		&pb.UpdateMetricRuleRsp{},
		&pb.DeleteMetricRuleReq{},
		&pb.DeleteMetricRuleRsp{},
		&pb.PreviewMetricRuleReq{},
		&pb.PreviewMetricRuleRsp{},
		&pb.ListMetricRuleEvaluationsReq{},
		&pb.ListMetricRuleEvaluationsRsp{},
		&pb.GetMetricRuleStateReq{},
		&pb.GetMetricRuleStateRsp{},
		&pb.MonitorInstance{},
		&pb.GroupSummary{},
		&pb.Overview{},
		&pb.ListChecksReq{},
		&pb.ListChecksRsp{},
		&pb.GetCheckReq{},
		&pb.GetCheckRsp{},
		&pb.CreateCheckReq{},
		&pb.CreateCheckRsp{},
		&pb.UpdateCheckReq{},
		&pb.UpdateCheckRsp{},
		&pb.DeleteCheckReq{},
		&pb.DeleteCheckRsp{},
		&pb.RunCheckOnceReq{},
		&pb.RunCheckOnceRsp{},
		&pb.ListResultsReq{},
		&pb.ListResultsRsp{},
		&pb.GetOverviewReq{},
		&pb.GetOverviewRsp{},
		&pb.ListWebhookChannelsReq{},
		&pb.ListWebhookChannelsRsp{},
		&pb.CreateWebhookChannelReq{},
		&pb.CreateWebhookChannelRsp{},
		&pb.UpdateWebhookChannelReq{},
		&pb.UpdateWebhookChannelRsp{},
		&pb.DeleteWebhookChannelReq{},
		&pb.DeleteWebhookChannelRsp{},
		&pb.ListAlertRulesReq{},
		&pb.ListAlertRulesRsp{},
		&pb.CreateAlertRuleReq{},
		&pb.CreateAlertRuleRsp{},
		&pb.UpdateAlertRuleReq{},
		&pb.UpdateAlertRuleRsp{},
		&pb.DeleteAlertRuleReq{},
		&pb.DeleteAlertRuleRsp{},
		&pb.ListAlertEventsReq{},
		&pb.ListAlertEventsRsp{},
		&pb.ListMonitorInstancesReq{},
		&pb.ListMonitorInstancesRsp{},
		&pb.SyncSystemChecksReq{},
		&pb.SyncSystemChecksRsp{},
		&pb.HostAgentInfo{},
		&pb.ListHostAgentsReq{},
		&pb.ListHostAgentsRsp{},
		&pb.HostMetricHistoryPoint{},
		&pb.QueryHostMetricHistoryReq{},
		&pb.QueryHostMetricHistoryRsp{},
	}
}

func callMonitorProtoGetters(msg proto.Message) {
	rv := reflect.ValueOf(msg)
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		method := rt.Method(i)
		if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
			method.Func.Call([]reflect.Value{rv})
		}
	}
	_ = msg.ProtoReflect()
	if d, ok := msg.(interface {
		Descriptor() ([]byte, []int)
	}); ok {
		_, _ = d.Descriptor()
	}
	if r, ok := msg.(interface{ Reset() }); ok {
		r.Reset()
	}
	if s, ok := msg.(interface{ String() string }); ok {
		_ = s.String()
	}
}

func TestMonitorProtoMessages_ShouldExerciseGetters(t *testing.T) {
	for _, msg := range MonitorProtoMessages() {
		callMonitorProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}

func TestMonitorProtoNilGetters_ShouldNotPanic(t *testing.T) {
	for _, sample := range MonitorProtoMessages() {
		nilMsg := reflect.Zero(reflect.TypeOf(sample)).Interface().(proto.Message)
		rv := reflect.ValueOf(nilMsg)
		rt := rv.Type()
		for i := 0; i < rt.NumMethod(); i++ {
			method := rt.Method(i)
			if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
				method.Func.Call([]reflect.Value{rv})
			}
		}
	}
}
