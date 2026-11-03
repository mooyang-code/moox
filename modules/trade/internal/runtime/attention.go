package runtime

import (
	"context"
	"errors"
	"strconv"
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
	// turnoverMultiple 是 1 小时成交额相对组合账户权益的告警倍数：总权重 2 倍时约等于每小时调仓 3 轮。
	turnoverMultiple = 6
	// unknownResultsLimit 是 1 分钟内结果未知下单的告警笔数。
	unknownResultsLimit = 5
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
		telemetry.AttentionTargetRejected: telemetry.Recent(telemetry.AttentionTargetRejected, now, rejectionWindow),
		telemetry.AttentionTargetReversal: telemetry.Recent(telemetry.AttentionTargetReversal, now, rejectionWindow),
		telemetry.AttentionRateLimited:    telemetry.Recent(telemetry.AttentionRateLimited, now, rejectionWindow),
	}
	names := make(map[string]string)
	accounts, err := w.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return err
	}
	spaces := make(map[string]struct{})
	notReady := make(map[string]time.Time, len(accounts))
	for _, account := range accounts {
		spaces[account.SpaceID] = struct{}{}
		names[account.TradingAccountID] = account.Name
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
				account.Name+"："+notReadyReason(account.LastError))
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
	for accountID, count := range telemetry.UnknownResultsSince(now.Add(-time.Minute)) {
		if count > unknownResultsLimit {
			name := names[accountID]
			if name == "" {
				name = accountID
			}
			telemetry.Note(telemetry.AttentionUnknownResults, name+"：1 分钟内多笔下单结果未知", now)
		}
	}
	items[telemetry.AttentionUnknownResults] = telemetry.Recent(telemetry.AttentionUnknownResults, now, rejectionWindow)
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
	if high, err := w.turnoverHigh(ctx, logical, now); err != nil {
		return err
	} else if high {
		items[telemetry.AttentionTurnoverHigh] = append(items[telemetry.AttentionTurnoverHigh], logical.Name+"：1 小时成交额超过权益的 6 倍")
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

func notReadyReason(lastError string) string {
	if lastError == "" {
		return "账户未就绪"
	}
	return telemetry.StableReason(lastError)
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

// turnoverHigh 判断组合账户 1 小时成交额是否超过权益的 turnoverMultiple 倍，用来发现下单量异常。
func (w *AttentionWorker) turnoverHigh(ctx context.Context, logical store.LogicalAccountRecord, now time.Time) (bool, error) {
	members, err := w.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return false, err
	}
	since := now.Add(-time.Hour).UnixMilli()
	equity, turnover := 0.0, 0.0
	for _, member := range members {
		account, err := w.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return false, err
		}
		if value, parseErr := strconv.ParseFloat(account.Snapshot.Equity, 64); parseErr == nil {
			equity += value
		}
		notional, err := w.Store.FillNotionalSince(ctx, logical.SpaceID, member.TradingAccountID, since)
		if err != nil {
			return false, err
		}
		turnover += notional
	}
	return equity > 0 && turnover > equity*turnoverMultiple, nil
}
