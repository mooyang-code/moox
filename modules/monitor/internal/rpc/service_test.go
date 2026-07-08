package rpc

import (
	"context"
	"path/filepath"
	"testing"

	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func TestMonitorRPCValidation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rsp, err := svc.CreateCheck(ctx, &monitorpb.CreateCheckReq{Check: &monitorpb.MonitorCheck{
		SpaceId:         "space-a",
		CheckId:         "bad-kind",
		Name:            "bad",
		Kind:            monitorpb.CheckKind_CHECK_KIND_UNSPECIFIED,
		IntervalSeconds: 30,
		TimeoutMs:       1000,
	}})
	if err != nil {
		t.Fatalf("CreateCheck error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}

	rsp, _ = svc.CreateCheck(ctx, &monitorpb.CreateCheckReq{Check: &monitorpb.MonitorCheck{
		SpaceId:         "space-a",
		CheckId:         "http-no-url",
		Name:            "bad http",
		Kind:            monitorpb.CheckKind_CHECK_KIND_HTTP,
		IntervalSeconds: 30,
		TimeoutMs:       1000,
	}})
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}

	rsp, _ = svc.CreateCheck(ctx, &monitorpb.CreateCheckReq{Check: &monitorpb.MonitorCheck{
		SpaceId:         "space-a",
		CheckId:         "tcp-no-host",
		Name:            "bad tcp",
		Kind:            monitorpb.CheckKind_CHECK_KIND_TCP,
		TcpPort:         70000,
		IntervalSeconds: 30,
		TimeoutMs:       1000,
	}})
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
}

func TestMonitorRPCCRUD(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	create, err := svc.CreateCheck(ctx, &monitorpb.CreateCheckReq{Check: &monitorpb.MonitorCheck{
		SpaceId:         "space-a",
		CheckId:         "api-health",
		Name:            "API Health",
		GroupName:       "api",
		Kind:            monitorpb.CheckKind_CHECK_KIND_HTTP,
		Url:             "http://127.0.0.1:11000/healthz",
		IntervalSeconds: 30,
		TimeoutMs:       1000,
	}})
	if err != nil || create.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("create ret=%+v err=%v", create.GetRetInfo(), err)
	}
	list, err := svc.ListChecks(ctx, &monitorpb.ListChecksReq{SpaceId: "space-a"})
	if err != nil || list.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS || len(list.GetChecks()) != 1 {
		t.Fatalf("list len=%d ret=%+v err=%v", len(list.GetChecks()), list.GetRetInfo(), err)
	}
	get, err := svc.GetCheck(ctx, &monitorpb.GetCheckReq{SpaceId: "space-a", CheckId: "api-health"})
	if err != nil || get.GetCheck().GetName() != "API Health" {
		t.Fatalf("get=%+v err=%v", get.GetCheck(), err)
	}
	update, err := svc.UpdateCheck(ctx, &monitorpb.UpdateCheckReq{Check: &monitorpb.MonitorCheck{
		SpaceId:         "space-a",
		CheckId:         "api-health",
		Name:            "API Health Updated",
		GroupName:       "api",
		Kind:            monitorpb.CheckKind_CHECK_KIND_HTTP,
		Url:             "http://127.0.0.1:11000/healthz",
		IntervalSeconds: 60,
		TimeoutMs:       2000,
	}})
	if err != nil || update.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("update ret=%+v err=%v", update.GetRetInfo(), err)
	}
	if update.GetCheck().GetName() != "API Health Updated" {
		t.Fatalf("updated name = %q", update.GetCheck().GetName())
	}
	del, err := svc.DeleteCheck(ctx, &monitorpb.DeleteCheckReq{SpaceId: "space-a", CheckId: "api-health"})
	if err != nil || del.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("delete ret=%+v err=%v", del.GetRetInfo(), err)
	}
	list, _ = svc.ListChecks(ctx, &monitorpb.ListChecksReq{SpaceId: "space-a"})
	if len(list.GetChecks()) != 0 {
		t.Fatalf("deleted check still listed")
	}
}

func TestMonitorRPCWebhookAndRuleCRUD(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	webhook, err := svc.CreateWebhookChannel(ctx, &monitorpb.CreateWebhookChannelReq{Channel: &monitorpb.WebhookChannel{
		SpaceId:   "space-a",
		WebhookId: "ops",
		Name:      "Ops",
		Url:       "http://127.0.0.1/webhook",
	}})
	if err != nil || webhook.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("webhook ret=%+v err=%v", webhook.GetRetInfo(), err)
	}
	webhooks, _ := svc.ListWebhookChannels(ctx, &monitorpb.ListWebhookChannelsReq{SpaceId: "space-a"})
	if len(webhooks.GetChannels()) != 1 {
		t.Fatalf("webhooks len = %d", len(webhooks.GetChannels()))
	}
	rule, err := svc.CreateAlertRule(ctx, &monitorpb.CreateAlertRuleReq{Rule: &monitorpb.AlertRule{
		SpaceId:   "space-a",
		RuleId:    "rule-a",
		CheckId:   "api-health",
		WebhookId: "ops",
		Enabled:   true,
	}})
	if err != nil || rule.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("rule ret=%+v err=%v", rule.GetRetInfo(), err)
	}
	rules, _ := svc.ListAlertRules(ctx, &monitorpb.ListAlertRulesReq{SpaceId: "space-a"})
	if len(rules.GetRules()) != 1 {
		t.Fatalf("rules len = %d", len(rules.GetRules()))
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()

	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return New(mgr.DB(), Options{InstanceID: "monitor-test"})
}
