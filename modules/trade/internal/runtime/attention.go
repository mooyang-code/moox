package runtime

import (
	"context"
	"errors"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	attentionInterval = 30 * time.Second
	// rejectionWindow 内持续上报被拒绝的目标，保证 Monitor 至少采到三次。
	rejectionWindow = 5 * time.Minute
	notReadyGrace   = 2 * time.Minute
	// expiredStreak 是连续过期且有差额的目标数，1 分钟策略偶尔过期不告警。
	expiredStreak = 3
)

// AttentionWorker 定期扫描需要人看的情况并上报，告警由 Monitor 统一发送。
type AttentionWorker struct {
	Store    *store.Store
	Executor *targetapp.Executor
	Now      func() time.Time

	notReadySince map[string]time.Time
	lastExpired   map[string]string
	expiredCounts map[string]int
}

func (w *AttentionWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(attentionInterval)
	defer ticker.Stop()
	for {
		if err := w.Scan(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContextf(ctx, "交易关注项扫描失败：%v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Scan 扫描一轮。部分失败时也上报已得到的结果，错误留给下一轮。
func (w *AttentionWorker) Scan(ctx context.Context) error {
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	items := map[string][]string{
		telemetry.AttentionTargetRejected: telemetry.RecentTargetRejections(now, rejectionWindow),
	}
	accounts, err := w.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return err
	}
	spaces := make(map[string]struct{})
	notReady := make(map[string]time.Time, len(accounts))
	for _, account := range accounts {
		spaces[account.SpaceID] = struct{}{}
		if account.Status != string(exchange.AccountStatusEnabled) || account.Ready {
			continue
		}
		since, seen := w.notReadySince[account.TradingAccountID]
		if !seen {
			since = now
		}
		notReady[account.TradingAccountID] = since
		if now.Sub(since) >= notReadyGrace {
			reason := account.LastError
			if reason == "" {
				reason = "账户未就绪"
			}
			items[telemetry.AttentionAccountNotReady] = append(items[telemetry.AttentionAccountNotReady], account.Name+"："+reason)
		}
	}
	w.notReadySince = notReady
	var scanErrors []error
	for spaceID := range spaces {
		logicals, listErr := w.Store.ListLogicalAccounts(ctx, spaceID)
		if listErr != nil {
			scanErrors = append(scanErrors, listErr)
			continue
		}
		for _, logical := range logicals {
			if err := w.scanLogicalAccount(ctx, logical, now, items); err != nil {
				scanErrors = append(scanErrors, err)
			}
		}
	}
	telemetry.PublishAttention(items)
	return errors.Join(scanErrors...)
}

func (w *AttentionWorker) scanLogicalAccount(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	now time.Time,
	items map[string][]string,
) error {
	if store.IsAutoPauseReason(logical.PauseReason) {
		items[telemetry.AttentionAutoPaused] = append(items[telemetry.AttentionAutoPaused], logical.Name+"："+logical.PauseReason)
	}
	target, err := w.Store.GetLogicalAccountTarget(ctx, logical.SpaceID, logical.LogicalAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch target.Status {
	case targetapp.StatusBlocked:
		// 目标有效期是 2 根 K 线。
		bar := time.Duration(target.ValidUntil-target.EffectiveAt) * time.Millisecond / 2
		if now.Sub(time.UnixMilli(target.AcceptedAt)) > bar && len(target.BlockedTargets) > 0 {
			blocked := target.BlockedTargets[0]
			items[telemetry.AttentionTargetBlocked] = append(items[telemetry.AttentionTargetBlocked],
				logical.Name+" "+blocked.InstrumentID+"："+blocked.Reason)
		}
	case targetapp.StatusConverged:
		delete(w.expiredCounts, logical.LogicalAccountID)
	case targetapp.StatusExpired:
		if err := w.countExpired(ctx, logical, target); err != nil {
			return err
		}
	}
	if w.expiredCounts[logical.LogicalAccountID] >= expiredStreak {
		items[telemetry.AttentionTargetExpired] = append(items[telemetry.AttentionTargetExpired],
			logical.Name+"：连续多个目标过期且持仓未到位")
	}
	return nil
}

// countExpired 每个过期目标只计一次，没有差额的过期不算。
func (w *AttentionWorker) countExpired(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	target store.LogicalAccountTargetRecord,
) error {
	if w.lastExpired[logical.LogicalAccountID] == target.TargetID {
		return nil
	}
	hasDifference, err := w.Executor.HasDifference(ctx, target)
	if err != nil {
		return err
	}
	if w.lastExpired == nil {
		w.lastExpired = make(map[string]string)
		w.expiredCounts = make(map[string]int)
	}
	w.lastExpired[logical.LogicalAccountID] = target.TargetID
	if hasDifference {
		w.expiredCounts[logical.LogicalAccountID]++
	} else {
		delete(w.expiredCounts, logical.LogicalAccountID)
	}
	return nil
}
