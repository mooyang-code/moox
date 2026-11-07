package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	walletapp "github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
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
	// turnoverRatioLimit 是 1 小时目标成交额相对“目标变化所需成交额”的告警倍数；按目标变化比较，和策略周期无关。
	turnoverRatioLimit = 1.5
	// turnoverSlackRatio 是按权益计的余量，吸收取整、价格变动和未收敛完的上一目标带来的差额。
	turnoverSlackRatio = 0.05
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
		// 划转额度不足每份目标记一次，保留窗口内持续上报。
		telemetry.AttentionTransferShortfall: telemetry.Recent(telemetry.AttentionTransferShortfall, now, rejectionWindow),
		telemetry.AttentionTransferFailing:   telemetry.Recent(telemetry.AttentionTransferFailing, now, rejectionWindow),
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
		if blocked, failing := InspectionAttention(account); blocked != "" || failing != "" {
			if blocked != "" {
				items[telemetry.AttentionInspectionBlocked] = append(items[telemetry.AttentionInspectionBlocked], blocked)
			}
			if failing != "" {
				items[telemetry.AttentionInspectionFailing] = append(items[telemetry.AttentionInspectionFailing], failing)
			}
		}
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
	// 换手检查出错只记日志，不能挡住后面的过期、被挡检查。
	if high, err := w.turnoverHigh(ctx, logical, now); err != nil {
		log.WarnContextf(ctx, "交易关注项：组合 %s 换手检查失败：%v", logical.LogicalAccountID, err)
	} else if high {
		items[telemetry.AttentionTurnoverHigh] = append(items[telemetry.AttentionTurnoverHigh], logical.Name+"：1 小时目标成交额超过目标变化所需的 1.5 倍")
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
		// 资金缺口是持续状态：目标一直 BLOCKED 且带着资金不足的项就一直上报，不依赖 5 分钟的事件窗口
		// （已阻塞的目标不会被定时重新收敛，策略周期长于窗口时事件早就过期了）。固定文字、不带会变的数字。
		for _, item := range target.BlockedTargets {
			if !item.Deferred && walletapp.IsFundsShortage(item.Reason) {
				items[telemetry.AttentionTransferShortfall] = append(items[telemetry.AttentionTransferShortfall],
					logical.Name+"：合约钱包可划转的资金不足，现货买入被缩小或挡下")
				break
			}
		}
		if now.Sub(since) > bar {
			// 合约钱包可划转资金不足同时有 transfer_shortfall 在报，这里跳过它，同一个缺口不发两条告警；
			// 只有别的原因的阻塞才算目标持续阻塞。
			for _, blocked := range target.BlockedTargets {
				if blocked.Deferred || walletapp.IsFundsShortage(blocked.Reason) {
					continue
				}
				items[telemetry.AttentionTargetBlocked] = append(items[telemetry.AttentionTargetBlocked],
					logical.Name+" "+blocked.InstrumentID+"："+blocked.Reason)
				break
			}
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

// turnoverHigh 判断组合账户 1 小时内目标子单的成交额是否明显超过目标变化所需的成交额，用来发现重复下单。
// 所需成交额 = 相邻两个目标之间每条标的 |新数量 − 旧数量| × 参考价之和；窗口内第一个目标和窗口前最后一个比。
// 没收敛完就过期的目标只会让实际成交额偏小，不会误报。
func (w *AttentionWorker) turnoverHigh(ctx context.Context, logical store.LogicalAccountRecord, now time.Time) (bool, error) {
	members, err := w.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return false, err
	}
	// 1 小时内暂停或恢复过（清仓、人工下单都会暂停）：恢复后执行器会把持仓拉回目标，
	// 这部分成交没有对应的目标变化，这一小时不判断。组合账户只在暂停、恢复、改名、认领会话时更新。
	if !logical.UpdatedAt.IsZero() && now.Sub(logical.UpdatedAt) < time.Hour {
		return false, nil
	}
	since := now.Add(-time.Hour).UnixMilli()
	equity, actual := 0.0, 0.0
	for _, member := range members {
		account, err := w.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return false, err
		}
		if value, parseErr := strconv.ParseFloat(account.Snapshot.Equity, 64); parseErr == nil {
			equity += value
		}
		notional, err := w.Store.TargetFillNotionalSince(ctx, logical.SpaceID, member.TradingAccountID, since)
		if err != nil {
			return false, err
		}
		actual += notional
	}
	if actual <= 0 {
		return false, nil
	}

	receipts, err := w.Store.ListTargetReceiptsSince(ctx, logical.SpaceID, logical.LogicalAccountID, since)
	if err != nil {
		return false, err
	}
	required, err := requiredTurnover(receipts, since)
	if err != nil {
		return false, err
	}
	return actual > required*turnoverRatioLimit+equity*turnoverSlackRatio, nil
}

// requiredTurnover 累加相邻目标之间的数量变化额。保持（Hold）的标的沿用上一个已知数量。
func requiredTurnover(receipts []store.TargetReceiptRecord, since int64) (float64, error) {
	previous := map[string]float64{}
	lastPrice := map[string]float64{}
	required := 0.0
	// 窗口前的基准和窗口内第一张相隔超过两个目标有效期（策略停过），持仓不再等于基准，改为从空仓算起。
	if len(receipts) > 1 && receipts[0].AcceptedAt <= since {
		interval := receipts[1].ValidUntil - receipts[1].EffectiveAt
		if interval > 0 && receipts[1].AcceptedAt-receipts[0].AcceptedAt > 2*interval {
			receipts = receipts[1:]
		}
	}
	for index, receipt := range receipts {
		var targets []store.InstrumentTarget
		if err := json.Unmarshal([]byte(receipt.QuantityTargetsJSON), &targets); err != nil {
			return 0, err
		}
		// 回执的参考价按标的存证据对象（targetapp.ReferencePriceEvidence），这里只取价格。
		var prices map[string]struct {
			Price string `json:"price"`
		}
		if err := json.Unmarshal([]byte(receipt.ReferencePricesJSON), &prices); err != nil {
			return 0, err
		}
		current := make(map[string]float64, len(targets))
		for instrumentID, quantity := range previous {
			current[instrumentID] = quantity
		}
		listed := make(map[string]bool, len(targets))
		for _, target := range targets {
			listed[target.InstrumentID] = true
			if target.Hold {
				continue
			}
			quantity, err := strconv.ParseFloat(target.Quantity, 64)
			if err != nil {
				return 0, err
			}
			current[target.InstrumentID] = quantity
		}
		// 全量目标里没列出的标的目标为 0。
		for instrumentID := range current {
			if !listed[instrumentID] {
				current[instrumentID] = 0
			}
		}
		for instrumentID, evidence := range prices {
			if price, err := strconv.ParseFloat(evidence.Price, 64); err == nil && price > 0 {
				lastPrice[instrumentID] = price
			}
		}
		// 窗口前的那条只作基准，不计入。清掉的标的这次没有参考价，沿用最近一次的。
		if !(index == 0 && receipt.AcceptedAt <= since) {
			for instrumentID, quantity := range current {
				required += math.Abs(quantity-previous[instrumentID]) * lastPrice[instrumentID]
			}
		}
		previous = current
	}
	return required, nil
}
