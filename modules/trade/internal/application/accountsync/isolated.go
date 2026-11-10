package accountsync

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// FilterIsolatedPositions 拆出交易所账户里的逐仓持仓：moox 只支持全仓单向，逐仓持仓不入投影，
// 账户所在组合暂停自动执行，原因里写明标的（走 auto_paused 关注项上报）。返回其余的全仓持仓。
func (s *Service) FilterIsolatedPositions(
	ctx context.Context,
	tradingAccountID string,
	positions []exchange.Position,
) ([]exchange.Position, error) {
	cross, isolated := exchange.SplitIsolated(positions)
	return cross, s.pauseForIsolated(ctx, tradingAccountID, isolated)
}

// pauseForIsolated 在发现逐仓持仓时暂停组合。已经暂停的不改写原因（可能是风控或用户暂停，不能覆盖）；
// 用户在逐仓持仓还在时恢复，下一次同步会再暂停。自主下单账户没有自动执行，不处理。
func (s *Service) pauseForIsolated(ctx context.Context, tradingAccountID string, isolated []exchange.Position) error {
	if len(isolated) == 0 {
		return nil
	}
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	symbols := make([]string, 0, len(isolated))
	for _, position := range isolated {
		symbols = append(symbols, position.ExchangeSymbol)
	}
	slices.Sort(symbols)
	symbols = slices.Compact(symbols)
	detail := account.Name + "：交易所账户里有逐仓持仓：标的 " + strings.Join(symbols, "、") + "，moox 只支持全仓单向"
	// 关注项不看组合状态：已暂停、自主下单或没加入组合的账户同样要让用户知道这笔持仓不入投影。
	telemetry.Note(telemetry.AttentionIsolatedPosition, detail, s.now())
	logical, _, err := s.Store.FindLogicalAccountByTradingAccount(ctx, account.SpaceID, tradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if logical.ControlMode == "MANUAL" || logical.AutomationState != string(logicalaccount.AutomationActive) {
		return nil
	}
	reason := store.AutoPauseReason("交易所账户里有逐仓持仓：标的 " + strings.Join(symbols, "、") + "，moox 只支持全仓单向")
	log.WarnContextf(ctx, "交易关注项：执行账户 %s 暂停组合 %s：%s", account.Name, logical.Name, reason)
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(logical.SpaceID, logical.LogicalAccountID, string(logicalaccount.AutomationPaused), reason)
	})
}
