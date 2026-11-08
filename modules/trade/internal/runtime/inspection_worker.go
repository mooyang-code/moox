package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// inspectionInterval 是实盘账户复查密钥的间隔。
	inspectionInterval = time.Hour
	// InspectionFailureAlertThreshold 是复查连续没有检测成功多少次后告警；传输错误只说明这次没检测成功，不代表账户有问题。
	InspectionFailureAlertThreshold = 6
)

// InspectionReinspector 重新检测一个账户并写回结果。
type InspectionReinspector interface {
	Reinspect(context.Context, string) (accountapp.ReinspectResult, error)
	// ReinspectUncounted 没检测成功时不计入连续失败次数（会话绑定时的立即复查用）。
	ReinspectUncounted(context.Context, string) (accountapp.ReinspectResult, error)
}

// LogicalAccountPauser 暂停组合账户。
type LogicalAccountPauser interface {
	Pause(ctx context.Context, spaceID, logicalAccountID, reason string) (store.LogicalAccountRecord, error)
}

// InspectionWorker 每小时复查实盘账户的密钥：账户类型变了或出现阻断项就暂停所在组合（自动暂停会告警），
// 传输错误保留上次结果、不触发阻断，连续失败到阈值由关注项扫描告警。
type InspectionWorker struct {
	Store    *store.Store
	Accounts InspectionReinspector
	Logical  LogicalAccountPauser
	Interval time.Duration
}

func (w *InspectionWorker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = inspectionInterval
	}
	// 启动后先等一个间隔：刚启动时会话和密钥服务可能还没就绪，建账户时刚检测过。
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if err := w.CheckAll(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContextf(ctx, "交易账户密钥复查失败：%v", err)
		}
	}
}

// inspectable 报告账户是否需要复查：启用的实盘加密货币账户；币安测试网没有 sapi、papi，不检测。
func inspectable(account store.TradingAccountRecord) bool {
	if account.Status != string(exchange.AccountStatusEnabled) || account.ExecutionMode != string(exchange.ExecutionModeLive) {
		return false
	}
	return !(account.Exchange == string(exchange.ExchangeBinance) && account.Environment == string(exchange.AccountEnvironmentTestnet))
}

// CheckAccount 立即复查一个账户并处理结果（会话绑定时发现密钥内容与检测时不一致用）。
// 这次复查是额外的，没检测成功不计入连续失败次数。
func (w *InspectionWorker) CheckAccount(ctx context.Context, spaceID, accountID string) error {
	result, err := w.Accounts.ReinspectUncounted(ctx, accountID)
	if err != nil {
		return err
	}
	return w.Handle(ctx, spaceID, accountID, result)
}

// CheckAll 复查全部需要复查的账户，单个账户失败不影响其他账户。
func (w *InspectionWorker) CheckAll(ctx context.Context) error {
	accounts, err := w.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, account := range accounts {
		if !inspectable(account) {
			continue
		}
		result, err := w.Accounts.Reinspect(ctx, account.TradingAccountID)
		if err != nil {
			errs = append(errs, fmt.Errorf("账户 %s：%w", account.Name, err))
			continue
		}
		if err := w.Handle(ctx, account.SpaceID, account.TradingAccountID, result); err != nil {
			errs = append(errs, fmt.Errorf("账户 %s：%w", account.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Handle 处理一次复查的结果：账户类型变了或出现阻断项时，暂停账户所在的组合。已经暂停的不覆盖原因。
func (w *InspectionWorker) Handle(ctx context.Context, spaceID, tradingAccountID string, result accountapp.ReinspectResult) error {
	if !result.ModeChanged && !result.Blocked {
		return nil
	}
	logical, _, err := w.Store.FindLogicalAccountMembership(ctx, spaceID, tradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if logical.AutomationState == "PAUSED" {
		return nil
	}
	reason := "交易账户检测发现账户类型变化，请重新检测确认后再恢复"
	if result.Blocked {
		reason = "交易账户检测发现阻断项：" + blockerSummary(result.Inspection)
	}
	_, err = w.Logical.Pause(ctx, spaceID, logical.LogicalAccountID, store.AutoPauseReason(reason))
	return err
}

func blockerSummary(inspection exchange.Inspection) string {
	if len(inspection.Blockers) == 0 {
		return "账户类型或权限变化"
	}
	return inspection.Blockers[0].Message
}

// InspectionAttention 返回账户在关注项里的说明：阻断项，以及复查连续失败到阈值。
func InspectionAttention(account store.TradingAccountRecord) (blocked string, failing string) {
	if !inspectable(account) {
		return "", ""
	}
	inspection, err := exchange.ParseInspection(account.InspectionJSON)
	if err != nil {
		return "", ""
	}
	markets := make([]exchange.MarketType, 0, len(account.Markets))
	for _, market := range account.Markets {
		markets = append(markets, exchange.MarketType(market))
	}
	if blockers := inspection.BlockersFor(markets); len(blockers) != 0 {
		blocked = account.Name + "：" + blockers[0].Message
	}
	if inspection.ConsecutiveFailures >= InspectionFailureAlertThreshold {
		failing = fmt.Sprintf("%s：密钥检测连续失败 %d 次", account.Name, inspection.ConsecutiveFailures)
	}
	return blocked, failing
}
