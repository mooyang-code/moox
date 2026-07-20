package rpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func TestQueryHostMetricHistoryOutsideRetentionReturnsGap(t *testing.T) {
	svc := newTestService(t)
	svc.hostReader = hostmetrics.NewStorageReader(nil, monconfig.Default().Metrics.HostStorage)
	now := time.Now().UTC()
	rsp, err := svc.QueryHostMetricHistory(context.Background(), &monitorpb.QueryHostMetricHistoryReq{
		AgentId: "agent-1", StartAt: now.Add(-11 * 24 * time.Hour).Format(time.RFC3339Nano), EndAt: now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano), Limit: 10,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS || !rsp.GetDataGap() || len(rsp.GetPoints()) != 0 {
		t.Fatalf("rsp=%+v err=%v, want successful empty gap", rsp, err)
	}
}

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
	if list.GetPageResult().GetTotal() != 1 {
		t.Fatalf("list total = %d", list.GetPageResult().GetTotal())
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
	run, err := svc.RunCheckOnce(ctx, &monitorpb.RunCheckOnceReq{SpaceId: "space-a", CheckId: "api-health"})
	if err != nil || run.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS || !run.GetResult().GetSuccess() {
		t.Fatalf("run ret=%+v result=%+v err=%v", run.GetRetInfo(), run.GetResult(), err)
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
	disabledRule, err := svc.CreateAlertRule(ctx, &monitorpb.CreateAlertRuleReq{Rule: &monitorpb.AlertRule{
		SpaceId:   "space-a",
		RuleId:    "rule-disabled",
		CheckId:   "api-health",
		WebhookId: "ops",
		Enabled:   false,
	}})
	if err != nil || disabledRule.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("disabled rule ret=%+v err=%v", disabledRule.GetRetInfo(), err)
	}
	rules, _ := svc.ListAlertRules(ctx, &monitorpb.ListAlertRulesReq{SpaceId: "space-a"})
	if len(rules.GetRules()) != 2 {
		t.Fatalf("rules len = %d", len(rules.GetRules()))
	}
	rulesByCheck, _ := svc.ListAlertRules(ctx, &monitorpb.ListAlertRulesReq{SpaceId: "space-a", CheckId: "api-health"})
	if len(rulesByCheck.GetRules()) != 2 {
		t.Fatalf("rules by check len = %d", len(rulesByCheck.GetRules()))
	}
}

func TestGetOverview(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for _, check := range []*monitorpb.MonitorCheck{
		{SpaceId: "space-a", CheckId: "sys-ok", Name: "sys ok", GroupName: "moox-system", Kind: monitorpb.CheckKind_CHECK_KIND_HTTP, Url: "http://ok", IntervalSeconds: 30, TimeoutMs: 1000},
		{SpaceId: "space-a", CheckId: "api-down", Name: "api down", GroupName: "api", Kind: monitorpb.CheckKind_CHECK_KIND_HTTP, Url: "http://down", IntervalSeconds: 30, TimeoutMs: 1000},
	} {
		rsp, err := svc.CreateCheck(ctx, &monitorpb.CreateCheckReq{Check: check})
		if err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			t.Fatalf("create check ret=%+v err=%v", rsp.GetRetInfo(), err)
		}
	}
	now := time.Now()
	_ = svc.results.Insert(ctx, &domain.CheckResult{ResultID: "r1", SpaceID: "space-a", CheckID: "sys-ok", InstanceID: "monitor-test", Success: true, Status: domain.CheckStatusOK, LatencyMS: 10, CheckedAt: now})
	_ = svc.results.Insert(ctx, &domain.CheckResult{ResultID: "r2", SpaceID: "space-a", CheckID: "api-down", InstanceID: "monitor-test", Success: false, Status: domain.CheckStatusDown, LatencyMS: 50, CheckedAt: now})

	rsp, err := svc.GetOverview(ctx, &monitorpb.GetOverviewReq{SpaceId: "space-a"})
	if err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("overview ret=%+v err=%v", rsp.GetRetInfo(), err)
	}
	overview := rsp.GetOverview()
	if overview.GetTotalChecks() != 2 || overview.GetHealthyChecks() != 1 || overview.GetDownChecks() != 1 {
		t.Fatalf("overview = %+v", overview)
	}
	if overview.GetSuccessRate_24H() != 0.5 || overview.GetP95LatencyMs() != 10 {
		t.Fatalf("overview stats = %+v", overview)
	}
	foundSystem := false
	for _, group := range overview.GetGroups() {
		if group.GetGroupName() == "moox-system" {
			foundSystem = true
		}
	}
	if !foundSystem {
		t.Fatalf("moox-system group missing: %+v", overview.GetGroups())
	}

	create, err := svc.CreateCheck(ctx, &monitorpb.CreateCheckReq{Check: &monitorpb.MonitorCheck{
		SpaceId: "space-b", CheckId: "other-ok", Name: "other ok", GroupName: "other",
		Kind: monitorpb.CheckKind_CHECK_KIND_HTTP, Url: "http://other", IntervalSeconds: 30, TimeoutMs: 1000,
	}})
	if err != nil || create.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("create space-b ret=%+v err=%v", create.GetRetInfo(), err)
	}
	_ = svc.results.Insert(ctx, &domain.CheckResult{ResultID: "r3", SpaceID: "space-b", CheckID: "other-ok", InstanceID: "monitor-test", Success: true, Status: domain.CheckStatusOK, LatencyMS: 100, CheckedAt: now})
	all, err := svc.GetOverview(ctx, &monitorpb.GetOverviewReq{})
	if err != nil || all.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("all overview ret=%+v err=%v", all.GetRetInfo(), err)
	}
	allOverview := all.GetOverview()
	if allOverview.GetTotalChecks() != 3 || allOverview.GetHealthyChecks() != 2 || allOverview.GetDownChecks() != 1 {
		t.Fatalf("all overview = %+v", allOverview)
	}
	if allOverview.GetSuccessRate_24H() != float64(2)/float64(3) {
		t.Fatalf("all overview success rate = %v", allOverview.GetSuccessRate_24H())
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()

	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return New(mgr.Repositories(), Options{
		InstanceID: "monitor-test",
		Runner: runnerFunc(func(ctx context.Context, check domain.Check) domain.CheckResult {
			return domain.CheckResult{
				SpaceID:   check.SpaceID,
				CheckID:   check.CheckID,
				Success:   true,
				Status:    domain.CheckStatusOK,
				CheckedAt: time.Now().UTC(),
			}
		}),
	})
}

type runnerFunc func(context.Context, domain.Check) domain.CheckResult

func (f runnerFunc) Run(ctx context.Context, check domain.Check) domain.CheckResult {
	return f(ctx, check)
}
