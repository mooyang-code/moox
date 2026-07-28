package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
)

type businessFreshnessItem struct {
	spaceID, checkID, name, reason string
	success                        bool
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
		items := make(map[string]businessFreshnessItem, len(overview.Datasets)+len(overview.BusinessChecks)+1)
		for _, dataset := range overview.Datasets {
			item := businessFreshnessItem{
				spaceID: dataset.SpaceID,
				checkID: strings.Join([]string{"dataset", dataset.Producer, dataset.DatasetID, dataset.Freq}, ":"),
				name:    strings.Join([]string{"Dataset", dataset.Producer, dataset.DatasetID, dataset.Freq}, " "),
				success: dataset.Status == "healthy",
				reason:  dataset.Reason,
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		for _, business := range overview.BusinessChecks {
			if business.Kind != "balance" || business.Module == domain.CheckSourceObservability {
				continue
			}
			item := businessFreshnessItem{
				spaceID: "crypto", checkID: "balance:" + business.Module,
				name:    "Balance freshness " + business.Module,
				success: business.Status == "healthy", reason: business.Reason,
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		scfTotal := overview.SCF.OnlineCount + overview.SCF.TimeoutCount + overview.SCF.UnknownCount
		if scfTotal > 0 {
			item := businessFreshnessItem{
				spaceID: "crypto", checkID: "scf:heartbeat", name: "SCF heartbeat freshness",
				success: overview.SCF.TimeoutCount == 0 && overview.SCF.UnknownCount == 0,
				reason:  fmt.Sprintf("online=%d timeout=%d unknown=%d", overview.SCF.OnlineCount, overview.SCF.TimeoutCount, overview.SCF.UnknownCount),
			}
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
			key := check.SpaceID + "\x00" + check.CheckID
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
				ErrorMessage: item.reason, CheckedAt: now, CreatedAt: now,
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
