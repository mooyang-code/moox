package monitorpb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func monitorProtoMessages() []proto.Message {
	return []proto.Message{
		&MonitorCheck{},
		&CheckResult{},
		&WebhookChannel{},
		&AlertRule{},
		&AlertState{},
		&AlertEvent{},
		&MetricServiceInfo{},
		&MetricNameInfo{},
		&MetricSeriesInfo{},
		&MetricLatestPoint{},
		&MetricHistoryPoint{},
		&ListMetricServicesReq{},
		&ListMetricServicesRsp{},
		&ListMetricNamesReq{},
		&ListMetricNamesRsp{},
		&ListMetricSeriesReq{},
		&ListMetricSeriesRsp{},
		&GetMetricLatestReq{},
		&GetMetricLatestRsp{},
		&QueryMetricHistoryReq{},
		&QueryMetricHistoryRsp{},
		&LabelMatcher{},
		&MetricSelector{},
		&MetricQuery{},
		&MetricCondition{},
		&MetricRule{},
		&MetricConditionEvaluation{},
		&MetricRuleEvaluation{},
		&MetricRuleState{},
		&ListMetricRulesReq{},
		&ListMetricRulesRsp{},
		&GetMetricRuleReq{},
		&GetMetricRuleRsp{},
		&CreateMetricRuleReq{},
		&CreateMetricRuleRsp{},
		&UpdateMetricRuleReq{},
		&UpdateMetricRuleRsp{},
		&DeleteMetricRuleReq{},
		&DeleteMetricRuleRsp{},
		&PreviewMetricRuleReq{},
		&PreviewMetricRuleRsp{},
		&ListMetricRuleEvaluationsReq{},
		&ListMetricRuleEvaluationsRsp{},
		&GetMetricRuleStateReq{},
		&GetMetricRuleStateRsp{},
		&MonitorInstance{},
		&GroupSummary{},
		&Overview{},
		&ListChecksReq{},
		&ListChecksRsp{},
		&GetCheckReq{},
		&GetCheckRsp{},
		&CreateCheckReq{},
		&CreateCheckRsp{},
		&UpdateCheckReq{},
		&UpdateCheckRsp{},
		&DeleteCheckReq{},
		&DeleteCheckRsp{},
		&RunCheckOnceReq{},
		&RunCheckOnceRsp{},
		&ListResultsReq{},
		&ListResultsRsp{},
		&GetOverviewReq{},
		&GetOverviewRsp{},
		&ListWebhookChannelsReq{},
		&ListWebhookChannelsRsp{},
		&CreateWebhookChannelReq{},
		&CreateWebhookChannelRsp{},
		&UpdateWebhookChannelReq{},
		&UpdateWebhookChannelRsp{},
		&DeleteWebhookChannelReq{},
		&DeleteWebhookChannelRsp{},
		&ListAlertRulesReq{},
		&ListAlertRulesRsp{},
		&CreateAlertRuleReq{},
		&CreateAlertRuleRsp{},
		&UpdateAlertRuleReq{},
		&UpdateAlertRuleRsp{},
		&DeleteAlertRuleReq{},
		&DeleteAlertRuleRsp{},
		&ListAlertEventsReq{},
		&ListAlertEventsRsp{},
		&ListMonitorInstancesReq{},
		&ListMonitorInstancesRsp{},
		&SyncSystemChecksReq{},
		&SyncSystemChecksRsp{},
		&HostAgentInfo{},
		&ListHostAgentsReq{},
		&ListHostAgentsRsp{},
		&HostMetricHistoryPoint{},
		&QueryHostMetricHistoryReq{},
		&QueryHostMetricHistoryRsp{},
	}
}

func callProtoGetters(msg proto.Message) {
	rv := reflect.ValueOf(msg)
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		method := rt.Method(i)
		if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
			method.Func.Call([]reflect.Value{rv})
		}
	}
	_ = msg.ProtoReflect()
	if r, ok := msg.(interface{ Reset() }); ok {
		r.Reset()
	}
	if s, ok := msg.(interface{ String() string }); ok {
		_ = s.String()
	}
}

func TestMonitorProtoMessages_ShouldExerciseGetters(t *testing.T) {
	for _, msg := range monitorProtoMessages() {
		callProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}

