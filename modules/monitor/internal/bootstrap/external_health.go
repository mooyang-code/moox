package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability/eventconsumer"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"gorm.io/gorm"
)

const externalSentinelObserver = "scf_sentinel"

var externalSentinelChecks = map[string]string{
	"monitor_ready": "SCF sentinel Monitor readiness",
	"gateway_ready": "SCF sentinel Gateway readiness",
	"market_canary": "SCF sentinel market canary",
}

func registerExternalSentinelChecks(ctx context.Context, repos *store.Repositories) error {
	if repos == nil {
		return fmt.Errorf("monitor repositories are required")
	}
	for rawID, name := range externalSentinelChecks {
		checkID := externalCheckID(externalSentinelObserver, rawID)
		if _, err := repos.Checks.Get(ctx, "crypto", checkID); err == nil {
			continue
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		if err := repos.Checks.Create(ctx, &domain.Check{
			SpaceID: "crypto", CheckID: checkID, Name: name, GroupName: "external",
			Kind: domain.CheckKindExternal, Source: domain.CheckSourceManual,
			Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000,
		}); err != nil {
			return err
		}
	}
	return nil
}

func externalHealthRoute(repos *store.Repositories, evaluator *alerting.Evaluator) func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error {
	return func(ctx context.Context, message *eventpb.EventMessage, report *observabilitypb.HealthCheckReport) error {
		if repos == nil || report == nil || message == nil {
			return eventconsumer.Permanent(fmt.Errorf("external health route is not initialized"))
		}
		observerID := strings.TrimSpace(report.GetObserverId())
		rawCheckID := strings.TrimSpace(report.GetCheckId())
		if observerID != externalSentinelObserver {
			return eventconsumer.Permanent(fmt.Errorf("unknown external observer"))
		}
		if _, ok := externalSentinelChecks[rawCheckID]; !ok {
			return eventconsumer.Permanent(fmt.Errorf("unknown external check"))
		}
		if message.GetSpaceId() != "crypto" || report.GetCheckedAt() == nil {
			return eventconsumer.Permanent(fmt.Errorf("invalid external check scope or checked_at"))
		}
		checkedAt := report.GetCheckedAt().AsTime().UTC()
		if err := report.GetCheckedAt().CheckValid(); err != nil || checkedAt.After(time.Now().UTC().Add(time.Minute)) {
			return eventconsumer.Permanent(fmt.Errorf("invalid external checked_at"))
		}
		checkID := externalCheckID(observerID, rawCheckID)
		check, err := repos.Checks.Get(ctx, message.GetSpaceId(), checkID)
		if err != nil {
			return err
		}
		status := domain.CheckStatusDown
		if report.GetSuccess() {
			status = domain.CheckStatusOK
		}
		result := &domain.CheckResult{
			ResultID: fmt.Sprintf("%s-%d", checkID, checkedAt.UnixNano()),
			SpaceID:  message.GetSpaceId(), CheckID: checkID, InstanceID: observerID,
			Success: report.GetSuccess(), Status: status, HTTPStatus: int(report.GetStatusCode()),
			Connected: report.GetSuccess(), LatencyMS: report.GetLatencyMs(),
			ErrorMessage: strings.TrimSpace(report.GetErrorSummary()), CheckedAt: checkedAt, CreatedAt: time.Now().UTC(),
		}
		inserted, err := repos.Results.InsertIfAbsent(ctx, result)
		if err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		if check.LastCheckedAt != nil && !checkedAt.After(check.LastCheckedAt.UTC()) {
			return nil
		}
		freshFor := 2 * time.Duration(check.IntervalSeconds) * time.Second
		if freshFor <= 0 {
			freshFor = time.Minute
		}
		if checkedAt.Before(time.Now().UTC().Add(-freshFor)) {
			// Durable delivery may replay a long outage after Monitor recovers.
			// Keep the historical fact, but do not reopen or resolve the current
			// alert state from stale evidence.
			return nil
		}
		check.LastCheckedAt = &checkedAt
		if err := repos.Checks.Update(ctx, check); err != nil {
			return err
		}
		if evaluator != nil {
			return evaluator.Evaluate(ctx, *check, *result)
		}
		return nil
	}
}

func externalCheckID(observerID, checkID string) string {
	return "external:" + observerID + ":" + checkID
}
