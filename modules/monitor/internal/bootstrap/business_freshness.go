package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
)

type businessFreshnessItem struct {
	spaceID, checkID, name, reason, diagnostic string
	success                                    bool
}

func buildBusinessFreshnessReporter(
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	hook func(context.Context, domain.Check, domain.CheckResult),
) func(context.Context) error {
	if builder == nil || repositories == nil {
		return nil
	}
	return func(ctx context.Context) error {
		overview, err := builder.Build(ctx, "")
		if err != nil {
			return err
		}
		items := make(map[string]businessFreshnessItem, len(overview.Services)+len(overview.Datasets)+len(overview.BusinessChecks)+1)
		suppressed := make(map[string]struct{})
		for _, service := range overview.Services {
			if service.ReporterStatus == "" {
				continue
			}
			expected, err := reporterDeploymentExpected(ctx, repositories.Checks, service)
			if err != nil {
				return err
			}
			if !expected {
				continue
			}
			checkID := strings.Join([]string{
				"reporter", service.NodeID, service.ServiceName, service.InstanceID,
			}, ":")
			item := businessFreshnessItem{
				spaceID: monmetrics.InternalMetricSpaceID,
				checkID: checkID,
				name:    strings.Join([]string{"Reporter", service.ServiceName, service.NodeID, service.InstanceID}, " "),
				success: service.ReporterStatus == "healthy",
				reason:  serviceReporterReason(service.ReporterStatus),
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		factorExpected, factorExpectedKnown := false, false
		storageScopes := make(map[string]struct{})
		for _, dataset := range overview.Datasets {
			if dataset.Producer == "storage" {
				storageScopes[dataset.SpaceID+"\x00"+dataset.DatasetID+"\x00"+dataset.Freq] = struct{}{}
			}
		}
		for _, dataset := range overview.Datasets {
			if dataset.Producer == "collector" {
				// Collector's Timer path intentionally has no per-run observer.
				// Once Storage has a fact row, its write watermark is authoritative
				// and the inventory row must not create a duplicate stale alert. If
				// Storage has never reported, retain this expectation so a newly
				// enabled but never-running Dataset is still visible as unhealthy.
				if _, exists := storageScopes[dataset.SpaceID+"\x00"+dataset.DatasetID+"\x00"+dataset.Freq]; exists {
					continue
				}
			}
			checkID := strings.Join([]string{"dataset", dataset.Producer, dataset.DatasetID, dataset.Freq}, ":")
			if dataset.Producer == "factor" {
				if !factorExpectedKnown {
					factorExpected, err = serviceDeploymentExpected(ctx, repositories.Checks, "moox_factor")
					if err != nil {
						return err
					}
					factorExpectedKnown = true
				}
				if !factorExpected {
					continue
				}
			}
			if dataset.Reason == "producer stale" {
				suppressed[dataset.SpaceID+"\x00"+checkID] = struct{}{}
				continue
			}
			item := businessFreshnessItem{
				spaceID: dataset.SpaceID,
				checkID: checkID,
				name:    strings.Join([]string{"Dataset", dataset.Producer, dataset.DatasetID, dataset.Freq}, " "),
				success: dataset.Status == "healthy",
				reason:  dataset.Reason,
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		for _, business := range overview.BusinessChecks {
			if business.Kind != "balance" && business.Kind != "market_fetch" && business.Kind != "data_delivery" {
				continue
			}
			spaceID := business.SpaceID
			if spaceID == "" {
				spaceID = "crypto"
			}
			checkID, name := "balance:"+business.Module, "Balance freshness "+business.Module
			if business.Kind == "market_fetch" {
				checkID, name = "market_fetch:"+business.Module, "行情采集 "+business.Module+" 协调"
			} else if business.Kind == "data_delivery" {
				checkID, name = "pipeline:"+business.Module, "数据投递队列"
			}
			item := businessFreshnessItem{spaceID: spaceID, checkID: checkID, name: name, success: business.Status == "healthy", reason: business.Reason}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		enabled := true
		existing := make([]domain.Check, 0, 500)
		for page := 1; len(existing) < 1000; page++ {
			batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{
				Source: domain.CheckSourceObservability, Enabled: &enabled,
				Page: store.Page{Page: page, PageSize: 500},
			})
			if err != nil {
				return err
			}
			existing = append(existing, batch...)
			if len(batch) < 500 {
				break
			}
		}
		if len(existing) >= 1000 {
			total, err := repositories.Checks.Count(ctx, store.ListChecksOptions{
				Source: domain.CheckSourceObservability, Enabled: &enabled,
			})
			if err != nil {
				return err
			}
			if total > 1000 {
				return fmt.Errorf("business freshness checks exceed limit 1000")
			}
		}
		for _, check := range existing {
			// Market canaries are evaluated by the dedicated watchdog below the
			// scheduler. Business freshness must not synthesize a success for an
			// absent overview item, otherwise a real canary failure is immediately
			// resolved by a concurrent no_longer_expected result.
			if strings.HasPrefix(check.CheckID, "market_canary:") {
				continue
			}
			key := check.SpaceID + "\x00" + check.CheckID
			if _, frozen := suppressed[key]; frozen {
				// Suppression avoids duplicating a producer-stale signal, but the
				// old business check still needs a successful result so its alert
				// state can resolve instead of remaining permanently firing.
				items[key] = businessFreshnessItem{
					spaceID: check.SpaceID, checkID: check.CheckID, name: check.Name,
					success: true, reason: "no_longer_expected",
				}
				continue
			}
			if _, ok := items[key]; !ok {
				items[key] = businessFreshnessItem{
					spaceID: check.SpaceID, checkID: check.CheckID, name: check.Name,
					success: true, reason: "no_longer_expected",
				}
			}
		}
		if len(items) > 1000 {
			return fmt.Errorf("business freshness checks exceed limit 1000")
		}

		checks := make(map[string]domain.Check, len(items))
		for key, item := range items {
			check, err := repositories.Checks.Get(ctx, item.spaceID, item.checkID)
			switch {
			case err == nil:
				if !check.Enabled {
					delete(items, key)
					continue
				}
				checks[key] = *check
			case errors.Is(err, gorm.ErrRecordNotFound):
				check := domain.Check{
					SpaceID: item.spaceID, CheckID: item.checkID, Name: item.name,
					GroupName: "business", Kind: domain.CheckKindExternal,
					Source: domain.CheckSourceObservability, Enabled: true,
					IntervalSeconds: 30, TimeoutMS: 20000,
				}
				if err := repositories.Checks.Create(ctx, &check); err != nil {
					return err
				}
				checks[key] = check
			default:
				return err
			}
		}
		if err := ensureDefaultCheckAlertRules(ctx, repositories); err != nil {
			return err
		}

		now := time.Now().UTC()
		var errs []error
		for key, item := range items {
			check, ok := checks[key]
			if !ok {
				continue
			}
			status := domain.CheckStatusDown
			if item.success {
				status = domain.CheckStatusOK
			}
			result := domain.CheckResult{
				ResultID: fmt.Sprintf("%s-%d", item.checkID, now.UnixNano()),
				SpaceID:  item.spaceID, CheckID: item.checkID, InstanceID: "monitor",
				Success: item.success, Connected: item.success, Status: status,
				ErrorMessage: item.reason, BodyExcerpt: item.diagnostic, CheckedAt: now, CreatedAt: now,
			}
			inserted, err := repositories.Results.InsertIfAbsent(ctx, &result)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !inserted {
				continue
			}
			check.LastCheckedAt = &now
			if err := repositories.Checks.Update(ctx, &check); err != nil {
				errs = append(errs, err)
				continue
			}
			if hook != nil {
				hook(ctx, check, result)
			}
		}
		return errors.Join(errs...)
	}
}

func serviceDeploymentExpected(
	ctx context.Context,
	checks *store.CheckRepository,
	serviceName string,
) (bool, error) {
	if checks == nil || strings.TrimSpace(serviceName) == "" {
		return true, nil
	}
	const (
		pageSize  = 500
		maxChecks = 1500
	)
	opts := store.ListChecksOptions{Source: domain.CheckSourceSysDeploy}
	total, err := checks.Count(ctx, opts)
	if err != nil {
		return false, err
	}
	if total > maxChecks {
		return false, fmt.Errorf("sysdeploy checks exceed limit %d", maxChecks)
	}
	found := false
	for page := 1; int64((page-1)*pageSize) < total; page++ {
		opts.Page = store.Page{Page: page, PageSize: pageSize}
		rows, err := checks.List(ctx, opts)
		if err != nil {
			return false, err
		}
		for _, check := range rows {
			if strings.HasSuffix(check.CheckID, ":"+serviceName) {
				found = true
				if check.Enabled {
					return true, nil
				}
			}
		}
		if len(rows) < pageSize {
			return !found, nil
		}
	}
	return !found, nil
}

func reporterDeploymentExpected(
	ctx context.Context,
	checks *store.CheckRepository,
	service monitorobservability.ServiceStatus,
) (bool, error) {
	if checks == nil || strings.TrimSpace(service.NodeID) == "" || strings.TrimSpace(service.ServiceName) == "" {
		return true, nil
	}
	checkID := strings.Join([]string{"sysdeploy", service.NodeID, service.ServiceName}, ":")
	check, err := checks.Get(ctx, "", checkID)
	switch {
	case err == nil:
		return check.Enabled, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		// External reporters, such as SCF nodes, do not have SysDeploy checks.
		return true, nil
	default:
		return false, err
	}
}

func serviceReporterReason(status string) string {
	switch status {
	case "healthy":
		return "reporter fresh"
	case "stale":
		return "producer stale"
	case "missing":
		return "reporter missing"
	default:
		return "reporter has not reported"
	}
}
