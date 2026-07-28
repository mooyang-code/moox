package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"gorm.io/gorm"
)

type Options struct {
	InstanceID            string
	Runner                probe.Runner
	OnResult              func(context.Context, domain.Check, domain.CheckResult)
	SyncSystem            func(context.Context) (int, error)
	MetricsQuery          *monmetrics.QueryService
	MetricRules           *monmetrics.MetricRuleStore
	MetricEvaluator       *monmetrics.MetricEvaluator
	HostStore             *hostmetrics.Store
	HostReader            *hostmetrics.StorageReader
	HostStorageReady      func() bool
	DoctorContext         *monitordoctor.Builder
	ObservabilityOverview *monitorobservability.Builder
}

type Service struct {
	checks                *store.CheckRepository
	results               *store.ResultRepository
	alerts                *store.AlertRepository
	runner                probe.Runner
	onResult              func(context.Context, domain.Check, domain.CheckResult)
	syncSystem            func(context.Context) (int, error)
	metricsQuery          *monmetrics.QueryService
	metricRules           *monmetrics.MetricRuleStore
	metricEvaluator       *monmetrics.MetricEvaluator
	hostStore             *hostmetrics.Store
	hostReader            *hostmetrics.StorageReader
	hostStorageReady      func() bool
	doctorContext         *monitordoctor.Builder
	observabilityOverview *monitorobservability.Builder
	instance              string
}

func New(repos *store.Repositories, opts Options) *Service {
	instance := opts.InstanceID
	if instance == "" {
		instance = "monitor"
	}
	runner := opts.Runner
	if runner == nil {
		runner = probe.DefaultRunner()
	}
	if repos == nil {
		repos = store.NewRepositories(nil)
	}
	return &Service{
		checks:                repos.Checks,
		results:               repos.Results,
		alerts:                repos.Alerts,
		runner:                runner,
		onResult:              opts.OnResult,
		syncSystem:            opts.SyncSystem,
		metricsQuery:          opts.MetricsQuery,
		metricRules:           opts.MetricRules,
		metricEvaluator:       opts.MetricEvaluator,
		hostStore:             opts.HostStore,
		hostReader:            opts.HostReader,
		hostStorageReady:      opts.HostStorageReady,
		doctorContext:         opts.DoctorContext,
		observabilityOverview: opts.ObservabilityOverview,
		instance:              instance,
	}
}

func (s *Service) CreateCheck(ctx context.Context, req *monitorpb.CreateCheckReq) (*monitorpb.CreateCheckRsp, error) {
	check, err := normalizeCheck(req.GetCheck(), true)
	if err != nil {
		return &monitorpb.CreateCheckRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.checks.Create(ctx, check); err != nil {
		return &monitorpb.CreateCheckRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.CreateCheckRsp{RetInfo: success(), Check: checkToPB(*check)}, nil
}

func (s *Service) UpdateCheck(ctx context.Context, req *monitorpb.UpdateCheckReq) (*monitorpb.UpdateCheckRsp, error) {
	check, err := normalizeCheck(req.GetCheck(), false)
	if err != nil {
		return &monitorpb.UpdateCheckRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.checks.Update(ctx, check); err != nil {
		return &monitorpb.UpdateCheckRsp{RetInfo: inner(err)}, nil
	}
	got, err := s.checks.Get(ctx, check.SpaceID, check.CheckID)
	if err != nil {
		return &monitorpb.UpdateCheckRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.UpdateCheckRsp{RetInfo: success(), Check: checkToPB(*got)}, nil
}

func (s *Service) GetCheck(ctx context.Context, req *monitorpb.GetCheckReq) (*monitorpb.GetCheckRsp, error) {
	if req.GetCheckId() == "" {
		return &monitorpb.GetCheckRsp{RetInfo: invalid(fmt.Errorf("check_id is required"))}, nil
	}
	check, err := s.checks.Get(ctx, req.GetSpaceId(), req.GetCheckId())
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return &monitorpb.GetCheckRsp{RetInfo: notFound("check not found")}, nil
		}
		return &monitorpb.GetCheckRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetCheckRsp{RetInfo: success(), Check: checkToPB(*check)}, nil
}

func (s *Service) ListChecks(ctx context.Context, req *monitorpb.ListChecksReq) (*monitorpb.ListChecksRsp, error) {
	page := pageFromPB(req.GetPage())
	opts := store.ListChecksOptions{
		SpaceID:   req.GetSpaceId(),
		GroupName: req.GetGroupName(),
		Source:    req.GetSource(),
		Page:      page,
	}
	total, err := s.checks.Count(ctx, opts)
	if err != nil {
		return &monitorpb.ListChecksRsp{RetInfo: inner(err)}, nil
	}
	checks, err := s.checks.List(ctx, opts)
	if err != nil {
		return &monitorpb.ListChecksRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.MonitorCheck, 0, len(checks))
	for _, check := range checks {
		out = append(out, checkToPB(check))
	}
	return &monitorpb.ListChecksRsp{
		RetInfo:    success(),
		Checks:     out,
		PageResult: pageResult(page, int(total)),
	}, nil
}

func (s *Service) DeleteCheck(ctx context.Context, req *monitorpb.DeleteCheckReq) (*monitorpb.DeleteCheckRsp, error) {
	if req.GetCheckId() == "" {
		return &monitorpb.DeleteCheckRsp{RetInfo: invalid(fmt.Errorf("check_id is required"))}, nil
	}
	if err := s.checks.Delete(ctx, req.GetSpaceId(), req.GetCheckId()); err != nil {
		if errors.Is(err, store.ErrResourceReferenced) {
			return &monitorpb.DeleteCheckRsp{RetInfo: invalid(err)}, nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.DeleteCheckRsp{RetInfo: notFound("check not found")}, nil
		}
		return &monitorpb.DeleteCheckRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.DeleteCheckRsp{RetInfo: success()}, nil
}

func (s *Service) RunCheckOnce(ctx context.Context, req *monitorpb.RunCheckOnceReq) (*monitorpb.RunCheckOnceRsp, error) {
	if req.GetCheckId() == "" {
		return &monitorpb.RunCheckOnceRsp{RetInfo: invalid(fmt.Errorf("check_id is required"))}, nil
	}
	check, err := s.checks.Get(ctx, req.GetSpaceId(), req.GetCheckId())
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return &monitorpb.RunCheckOnceRsp{RetInfo: notFound("check not found")}, nil
		}
		return &monitorpb.RunCheckOnceRsp{RetInfo: inner(err)}, nil
	}
	result := s.runner.Run(ctx, *check)
	normalizeResult(&result, *check, s.instance)
	if err := s.results.Insert(ctx, &result); err != nil {
		return &monitorpb.RunCheckOnceRsp{RetInfo: inner(err)}, nil
	}
	if s.onResult != nil {
		s.onResult(ctx, *check, result)
	}
	return &monitorpb.RunCheckOnceRsp{RetInfo: success(), Result: resultToPB(result)}, nil
}

func (s *Service) ListResults(ctx context.Context, req *monitorpb.ListResultsReq) (*monitorpb.ListResultsRsp, error) {
	results, err := s.results.Recent(ctx, req.GetSpaceId(), req.GetCheckId(), int(req.GetLimit()))
	if err != nil {
		return &monitorpb.ListResultsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.CheckResult, 0, len(results))
	for _, result := range results {
		out = append(out, resultToPB(result))
	}
	return &monitorpb.ListResultsRsp{RetInfo: success(), Results: out}, nil
}

func (s *Service) GetOverview(ctx context.Context, req *monitorpb.GetOverviewReq) (*monitorpb.GetOverviewRsp, error) {
	enabled := true
	checks, err := s.checks.List(ctx, store.ListChecksOptions{SpaceID: req.GetSpaceId(), Enabled: &enabled, Page: store.Page{PageSize: 500}})
	if err != nil {
		return &monitorpb.GetOverviewRsp{RetInfo: inner(err)}, nil
	}
	overview := &monitorpb.Overview{TotalChecks: int32(len(checks))}
	groups := map[string]*monitorpb.GroupSummary{}
	for _, check := range checks {
		groupName := check.GroupName
		if groupName == "" {
			groupName = "default"
		}
		group := groups[groupName]
		if group == nil {
			group = &monitorpb.GroupSummary{GroupName: groupName}
			groups[groupName] = group
		}
		group.TotalChecks++
		results, err := s.results.Recent(ctx, check.SpaceID, check.CheckID, 1)
		if err != nil {
			return &monitorpb.GetOverviewRsp{RetInfo: inner(err)}, nil
		}
		status := domain.CheckStatusDegraded
		if len(results) > 0 {
			status = results[0].Status
		}
		switch status {
		case domain.CheckStatusOK:
			overview.HealthyChecks++
			group.HealthyChecks++
		case domain.CheckStatusDegraded:
			overview.DegradedChecks++
			group.DegradedChecks++
		default:
			overview.DownChecks++
			group.DownChecks++
		}
	}
	for _, group := range groups {
		overview.Groups = append(overview.Groups, group)
	}
	sort.Slice(overview.Groups, func(i, j int) bool { return overview.Groups[i].GroupName < overview.Groups[j].GroupName })
	overview.SuccessRate_24H, overview.P95LatencyMs = s.resultStats(ctx, req.GetSpaceId(), time.Now().Add(-24*time.Hour))
	firing, _ := s.alerts.CountFiring(ctx, req.GetSpaceId())
	overview.FiringAlerts = int32(firing)
	return &monitorpb.GetOverviewRsp{
		RetInfo:  success(),
		Overview: overview,
	}, nil
}

func (s *Service) resultStats(ctx context.Context, spaceID string, since time.Time) (float64, int64) {
	successRate, p95, err := s.results.Stats(ctx, spaceID, since)
	if err != nil {
		return 0, 0
	}
	return successRate, p95
}

func (s *Service) CreateWebhookChannel(ctx context.Context, req *monitorpb.CreateWebhookChannelReq) (*monitorpb.CreateWebhookChannelRsp, error) {
	webhook, err := normalizeWebhook(req.GetChannel(), true)
	if err != nil {
		return &monitorpb.CreateWebhookChannelRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.alerts.CreateWebhook(ctx, webhook); err != nil {
		return &monitorpb.CreateWebhookChannelRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.CreateWebhookChannelRsp{RetInfo: success(), Channel: webhookToPB(*webhook)}, nil
}

func (s *Service) UpdateWebhookChannel(ctx context.Context, req *monitorpb.UpdateWebhookChannelReq) (*monitorpb.UpdateWebhookChannelRsp, error) {
	webhook, err := normalizeWebhook(req.GetChannel(), false)
	if err != nil {
		return &monitorpb.UpdateWebhookChannelRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.alerts.UpdateWebhook(ctx, webhook); err != nil {
		return &monitorpb.UpdateWebhookChannelRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.UpdateWebhookChannelRsp{RetInfo: success(), Channel: req.GetChannel()}, nil
}

func (s *Service) ListWebhookChannels(ctx context.Context, req *monitorpb.ListWebhookChannelsReq) (*monitorpb.ListWebhookChannelsRsp, error) {
	webhooks, err := s.alerts.ListWebhooks(ctx, req.GetSpaceId())
	if err != nil {
		return &monitorpb.ListWebhookChannelsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.WebhookChannel, 0, len(webhooks))
	for _, webhook := range webhooks {
		out = append(out, webhookToPB(webhook))
	}
	return &monitorpb.ListWebhookChannelsRsp{RetInfo: success(), Channels: out}, nil
}

func (s *Service) DeleteWebhookChannel(ctx context.Context, req *monitorpb.DeleteWebhookChannelReq) (*monitorpb.DeleteWebhookChannelRsp, error) {
	if req.GetWebhookId() == "" {
		return &monitorpb.DeleteWebhookChannelRsp{RetInfo: invalid(fmt.Errorf("webhook_id is required"))}, nil
	}
	if err := s.alerts.DeleteWebhook(ctx, req.GetSpaceId(), req.GetWebhookId()); err != nil {
		if errors.Is(err, store.ErrResourceReferenced) {
			return &monitorpb.DeleteWebhookChannelRsp{RetInfo: invalid(err)}, nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.DeleteWebhookChannelRsp{RetInfo: notFound("webhook not found")}, nil
		}
		return &monitorpb.DeleteWebhookChannelRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.DeleteWebhookChannelRsp{RetInfo: success()}, nil
}

func (s *Service) CreateAlertRule(ctx context.Context, req *monitorpb.CreateAlertRuleReq) (*monitorpb.CreateAlertRuleRsp, error) {
	rule, err := normalizeRule(req.GetRule(), true)
	if err != nil {
		return &monitorpb.CreateAlertRuleRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.alerts.CreateRule(ctx, rule); err != nil {
		if errors.Is(err, store.ErrInvalidReference) {
			return &monitorpb.CreateAlertRuleRsp{RetInfo: invalid(err)}, nil
		}
		return &monitorpb.CreateAlertRuleRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.CreateAlertRuleRsp{RetInfo: success(), Rule: ruleToPB(*rule)}, nil
}

func (s *Service) UpdateAlertRule(ctx context.Context, req *monitorpb.UpdateAlertRuleReq) (*monitorpb.UpdateAlertRuleRsp, error) {
	rule, err := normalizeRule(req.GetRule(), false)
	if err != nil {
		return &monitorpb.UpdateAlertRuleRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.alerts.UpdateRule(ctx, rule); err != nil {
		if errors.Is(err, store.ErrInvalidReference) {
			return &monitorpb.UpdateAlertRuleRsp{RetInfo: invalid(err)}, nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.UpdateAlertRuleRsp{RetInfo: notFound("alert rule not found")}, nil
		}
		return &monitorpb.UpdateAlertRuleRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.UpdateAlertRuleRsp{RetInfo: success(), Rule: req.GetRule()}, nil
}

func (s *Service) ListAlertRules(ctx context.Context, req *monitorpb.ListAlertRulesReq) (*monitorpb.ListAlertRulesRsp, error) {
	spaceID := req.GetSpaceId()
	if _, _, ok := hostmetrics.ParseHostRuleKey(req.GetCheckId()); ok {
		spaceID = hostmetrics.SpaceID
	}
	var (
		rules []domain.AlertRule
		err   error
	)
	if req.GetCheckId() != "" {
		rules, err = s.alerts.ListRulesForCheck(ctx, spaceID, req.GetCheckId())
	} else {
		rules, err = s.alerts.ListRules(ctx, spaceID)
	}
	if err != nil {
		return &monitorpb.ListAlertRulesRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.AlertRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, ruleToPB(rule))
	}
	return &monitorpb.ListAlertRulesRsp{RetInfo: success(), Rules: out}, nil
}

func (s *Service) DeleteAlertRule(ctx context.Context, req *monitorpb.DeleteAlertRuleReq) (*monitorpb.DeleteAlertRuleRsp, error) {
	if req.GetRuleId() == "" {
		return &monitorpb.DeleteAlertRuleRsp{RetInfo: invalid(fmt.Errorf("rule_id is required"))}, nil
	}
	spaceID := req.GetSpaceId()
	if rule, err := s.alerts.GetRuleByID(ctx, req.GetRuleId()); err == nil && rule != nil {
		if rule.SpaceID == hostmetrics.SpaceID {
			if _, _, ok := hostmetrics.ParseHostRuleKey(rule.CheckID); ok {
				spaceID = hostmetrics.SpaceID
			}
		}
	}
	if err := s.alerts.DeleteRule(ctx, spaceID, req.GetRuleId()); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.DeleteAlertRuleRsp{RetInfo: notFound("alert rule not found")}, nil
		}
		return &monitorpb.DeleteAlertRuleRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.DeleteAlertRuleRsp{RetInfo: success()}, nil
}

func (s *Service) ListAlertEvents(ctx context.Context, req *monitorpb.ListAlertEventsReq) (*monitorpb.ListAlertEventsRsp, error) {
	events, err := s.alerts.ListEvents(ctx, req.GetSpaceId(), int(req.GetLimit()))
	if err != nil {
		return &monitorpb.ListAlertEventsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.AlertEvent, 0, len(events))
	for _, event := range events {
		out = append(out, eventToPB(event))
	}
	return &monitorpb.ListAlertEventsRsp{RetInfo: success(), Events: out}, nil
}

func (s *Service) SyncSystemChecks(ctx context.Context, req *monitorpb.SyncSystemChecksReq) (*monitorpb.SyncSystemChecksRsp, error) {
	if s.syncSystem == nil {
		return &monitorpb.SyncSystemChecksRsp{RetInfo: success(), Synced: 0}, nil
	}
	n, err := s.syncSystem(ctx)
	if err != nil {
		return &monitorpb.SyncSystemChecksRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.SyncSystemChecksRsp{RetInfo: success(), Synced: int32(n)}, nil
}

func normalizeCheck(in *monitorpb.MonitorCheck, create bool) (*domain.Check, error) {
	if in == nil {
		return nil, fmt.Errorf("check is required")
	}
	kind := kindToString(in.GetKind())
	if kind == "" {
		return nil, fmt.Errorf("check kind must be http or tcp")
	}
	checkID := strings.TrimSpace(in.GetCheckId())
	if checkID == "" {
		if create {
			checkID = newID("check")
		} else {
			return nil, fmt.Errorf("check_id is required")
		}
	}
	name := strings.TrimSpace(in.GetName())
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	interval := int(in.GetIntervalSeconds())
	if interval == 0 {
		interval = 60
	}
	if interval < 5 {
		return nil, fmt.Errorf("interval_seconds must be at least 5")
	}
	timeout := int(in.GetTimeoutMs())
	if timeout == 0 {
		timeout = 3000
	}
	if timeout < 100 || timeout > 60000 {
		return nil, fmt.Errorf("timeout_ms must be between 100 and 60000")
	}
	method := strings.ToUpper(strings.TrimSpace(in.GetMethod()))
	if method == "" {
		method = "GET"
	}
	expectedStatus := strings.TrimSpace(in.GetExpectedStatus())
	if expectedStatus == "" {
		expectedStatus = "200-299"
	}
	if kind == domain.CheckKindHTTP && strings.TrimSpace(in.GetUrl()) == "" {
		return nil, fmt.Errorf("http check url is required")
	}
	if kind == domain.CheckKindTCP {
		if strings.TrimSpace(in.GetTcpHost()) == "" {
			return nil, fmt.Errorf("tcp check host is required")
		}
		if in.GetTcpPort() < 1 || in.GetTcpPort() > 65535 {
			return nil, fmt.Errorf("tcp port must be between 1 and 65535")
		}
	}
	source := in.GetSource()
	if source == "" {
		source = domain.CheckSourceManual
	}
	headers := in.GetHeadersJson()
	if headers == "" {
		headers = "{}"
	}
	labels := in.GetLabelsJson()
	if labels == "" {
		labels = "{}"
	}
	return &domain.Check{
		SpaceID:         in.GetSpaceId(),
		CheckID:         checkID,
		Name:            name,
		GroupName:       in.GetGroupName(),
		Kind:            kind,
		URL:             strings.TrimSpace(in.GetUrl()),
		Method:          method,
		Headers:         headers,
		Body:            in.GetBody(),
		TCPHost:         strings.TrimSpace(in.GetTcpHost()),
		TCPPort:         int(in.GetTcpPort()),
		IntervalSeconds: interval,
		TimeoutMS:       timeout,
		ExpectedStatus:  expectedStatus,
		MaxResponseMS:   int(in.GetMaxResponseMs()),
		BodyContains:    in.GetBodyContains(),
		Enabled:         in.GetEnabled() || create,
		Source:          source,
		Labels:          labels,
		Description:     in.GetDescription(),
	}, nil
}

func normalizeWebhook(in *monitorpb.WebhookChannel, create bool) (*domain.WebhookChannel, error) {
	if in == nil {
		return nil, fmt.Errorf("channel is required")
	}
	webhookID := strings.TrimSpace(in.GetWebhookId())
	if webhookID == "" {
		if create {
			webhookID = newID("webhook")
		} else {
			return nil, fmt.Errorf("webhook_id is required")
		}
	}
	if strings.TrimSpace(in.GetName()) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(in.GetUrl()) == "" {
		return nil, fmt.Errorf("url is required")
	}
	method := strings.ToUpper(strings.TrimSpace(in.GetMethod()))
	if method == "" {
		method = "POST"
	}
	headers := in.GetHeadersJson()
	if headers == "" {
		headers = "{}"
	}
	body := in.GetBodyTemplate()
	if body == "" {
		body = "{}"
	}
	return &domain.WebhookChannel{
		SpaceID:      in.GetSpaceId(),
		WebhookID:    webhookID,
		Name:         in.GetName(),
		URL:          in.GetUrl(),
		Method:       method,
		Headers:      headers,
		BodyTemplate: body,
		Enabled:      in.GetEnabled() || create,
	}, nil
}

func normalizeRule(in *monitorpb.AlertRule, create bool) (*domain.AlertRule, error) {
	if in == nil {
		return nil, fmt.Errorf("rule is required")
	}
	ruleID := strings.TrimSpace(in.GetRuleId())
	if ruleID == "" {
		if create {
			ruleID = newID("rule")
		} else {
			return nil, fmt.Errorf("rule_id is required")
		}
	}
	if strings.TrimSpace(in.GetCheckId()) == "" {
		return nil, fmt.Errorf("check_id is required")
	}
	if strings.TrimSpace(in.GetWebhookId()) == "" {
		return nil, fmt.Errorf("webhook_id is required")
	}
	failure := int(in.GetFailureThreshold())
	if failure == 0 {
		failure = 3
	}
	successes := int(in.GetSuccessThreshold())
	if successes == 0 {
		successes = 2
	}
	reminder := int(in.GetMinimumReminderIntervalSeconds())
	if reminder > 0 && reminder < 300 {
		return nil, fmt.Errorf("minimum_reminder_interval_seconds must be 0 or at least 300")
	}
	spaceID := in.GetSpaceId()
	if _, _, ok := hostmetrics.ParseHostRuleKey(in.GetCheckId()); ok {
		spaceID = hostmetrics.SpaceID
	}
	return &domain.AlertRule{
		SpaceID:                        spaceID,
		RuleID:                         ruleID,
		CheckID:                        in.GetCheckId(),
		WebhookID:                      in.GetWebhookId(),
		FailureThreshold:               failure,
		SuccessThreshold:               successes,
		MinimumReminderIntervalSeconds: reminder,
		SendOnResolved:                 in.GetSendOnResolved() || create,
		Enabled:                        in.GetEnabled() || create,
		Description:                    in.GetDescription(),
	}, nil
}

func pageFromPB(in *commonpb.Page) store.Page {
	if in == nil {
		return store.Page{Page: 1, PageSize: 50}
	}
	return store.Page{Page: int(in.GetPage()), PageSize: int(in.GetSize())}
}

func pageResult(page store.Page, n int) *commonpb.PageResult {
	return &commonpb.PageResult{
		Page:    uint32(max(page.Page, 1)),
		Size:    uint32(page.Limit()),
		Total:   uint32(n),
		HasMore: n == page.Limit(),
	}
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func normalizeResult(result *domain.CheckResult, check domain.Check, instanceID string) {
	if result.ResultID == "" {
		result.ResultID = newID("result")
	}
	if result.SpaceID == "" {
		result.SpaceID = check.SpaceID
	}
	if result.CheckID == "" {
		result.CheckID = check.CheckID
	}
	if result.InstanceID == "" {
		result.InstanceID = instanceID
	}
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	if result.Status == "" {
		if result.Success {
			result.Status = domain.CheckStatusOK
		} else {
			result.Status = domain.CheckStatusDown
		}
	}
}
