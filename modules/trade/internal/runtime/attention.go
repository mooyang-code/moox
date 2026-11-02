package runtime

import (
	"context"
	"errors"
	"strings"
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
	blockedSince  map[string]time.Time
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
			items[telemetry.AttentionAccountNotReady] = append(items[telemetry.AttentionAccountNotReady],
				account.Name+"："+stableReason(account.LastError))
		}
	}
	w.notReadySince = notReady
	blocked := make(map[string]time.Time, len(w.blockedSince))
	var scanErrors []error
	for spaceID := range spaces {
		logicals, listErr := w.Store.ListLogicalAccounts(ctx, spaceID)
		if listErr != nil {
			scanErrors = append(scanErrors, listErr)
			continue
		}
		for _, logical := range logicals {
			if err := w.scanLogicalAccount(ctx, logical, now, items, blocked); err != nil {
				scanErrors = append(scanErrors, err)
			}
		}
	}
	w.blockedSince = blocked
	telemetry.PublishAttention(items)
	return errors.Join(scanErrors...)
}

func (w *AttentionWorker) scanLogicalAccount(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	now time.Time,
	items map[string][]string,
	blocked map[string]time.Time,
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
	return w.noteTarget(ctx, logical, target, now, items, blocked)
}

func (w *AttentionWorker) noteTarget(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	target store.LogicalAccountTargetRecord,
	now time.Time,
	items map[string][]string,
	blocked map[string]time.Time,
) error {
	switch target.Status {
	case targetapp.StatusBlocked:
		// 目标有效期是 2 根 K 线。
		bar := time.Duration(target.ValidUntil-target.EffectiveAt) * time.Millisecond / 2
		// 策略每根 K 线都发新目标，起始时间要跨目标保留，不能用目标的接收时间。
		since, seen := w.blockedSince[logical.LogicalAccountID]
		if !seen {
			since = now
		}
		blocked[logical.LogicalAccountID] = since
		if now.Sub(since) > bar && len(target.BlockedTargets) > 0 {
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

// stableReason 去掉错误里的地址和时间戳：错误原文带签名参数，每次都不同，会让指标序列不断增长。
func stableReason(reason string) string {
	if reason == "" {
		return "账户未就绪"
	}
	if index := strings.Index(reason, "http"); index > 0 {
		reason = strings.TrimRight(reason[:index], " :\"")
	}
	if runes := []rune(reason); len(runes) > 60 {
		reason = string(runes[:60])
	}
	return reason
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
