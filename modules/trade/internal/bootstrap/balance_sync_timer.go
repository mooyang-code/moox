package bootstrap

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tradeobservability "github.com/mooyang-code/moox/modules/trade/internal/observability"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-go/server"
)

const tradeBalanceSyncTimerService = "trpc.moox.trade.balance_sync.timer"

type balanceSyncer interface {
	Sync(context.Context) (float64, error)
}

type balanceSyncTimer struct {
	syncer  balanceSyncer
	metrics *tradeobservability.BalanceMetrics
	now     func() time.Time
}

func (t *balanceSyncTimer) Handle(ctx context.Context) error {
	difference, err := t.syncer.Sync(ctx)
	t.metrics.Observe(t.now(), difference, err)
	return err
}

func registerBalanceSyncTimer(s *server.Server, svc *service.Service, spaceIDs []string) error {
	if s == nil || svc == nil || svc.Account == nil {
		return fmt.Errorf("trade balance sync timer requires server and account service")
	}
	metrics, err := tradeobservability.DefaultBalanceMetrics()
	if err != nil {
		return fmt.Errorf("register trade balance metrics: %w", err)
	}
	timerHandler := &balanceSyncTimer{
		syncer:  serviceBalanceSyncer{account: svc.Account, spaceIDs: normalizeSpaceIDs(spaceIDs)},
		metrics: metrics,
		now:     time.Now,
	}
	job, err := timerjob.New("trade_balance_sync", 4*time.Minute, timerHandler.Handle)
	if err != nil {
		return err
	}
	return registerKernelTimer(s, tradeBalanceSyncTimerService, job)
}

type serviceBalanceSyncer struct {
	account  *service.AccountService
	spaceIDs []string
}

func (s serviceBalanceSyncer) Sync(ctx context.Context) (float64, error) {
	var maximum float64
	for _, spaceID := range s.spaceIDs {
		accounts, _, err := s.account.ListAccounts(ctx, spaceID, service.AccountFilter{}, service.Page{PageNo: 1, PageSize: 1000})
		if err != nil {
			return maximum, err
		}
		for _, account := range accounts {
			if account == nil || account.Status == service.AccountDisabled {
				continue
			}
			before, err := s.account.GetBalances(ctx, spaceID, account.AccountID, nil)
			if err != nil {
				return maximum, err
			}
			venue, err := s.account.SyncBalances(ctx, spaceID, account.AccountID)
			if err != nil {
				return maximum, err
			}
			maximum = max(maximum, maxBalanceDifference(before, venue))
		}
	}
	return maximum, nil
}

func normalizeSpaceIDs(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func maxBalanceDifference(before, after []*service.Balance) float64 {
	previous := make(map[string]float64, len(before))
	for _, balance := range before {
		if balance != nil {
			previous[balance.Currency] = parseBalance(balance.Total)
		}
	}
	var maximum float64
	for _, balance := range after {
		if balance == nil {
			continue
		}
		current := parseBalance(balance.Total)
		old := previous[balance.Currency]
		denominator := math.Max(math.Abs(current), 1e-12)
		maximum = max(maximum, math.Abs(current-old)/denominator)
		delete(previous, balance.Currency)
	}
	for _, old := range previous {
		if old != 0 {
			maximum = max(maximum, 1)
		}
	}
	return maximum
}

func parseBalance(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	return parsed
}
