package dao

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
)

// ListFundFlows 分页查询资金流水（按账户/币种/业务类型/时间过滤）。
func (g *GormStore) ListFundFlows(ctx context.Context, spaceID string, f service.FundFlowFilter, page service.Page) ([]*service.FundFlow, int, error) {
	q := g.db.WithContext(ctx).Model(&service.FundFlow{}).
		Where("c_space_id = ?", spaceID)
	if f.AccountID != "" {
		q = q.Where("c_account_id = ?", f.AccountID)
	}
	if f.Currency != "" {
		q = q.Where("c_currency = ?", f.Currency)
	}
	if f.BizType != "" {
		q = q.Where("c_biz_type = ?", f.BizType)
	}
	if f.StartTime > 0 {
		q = q.Where("c_ctime >= ?", f.StartTime)
	}
	if f.EndTime > 0 {
		q = q.Where("c_ctime <= ?", f.EndTime)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []*service.FundFlow
	if err := q.Order("c_ctime DESC").Offset(page.Offset()).Limit(page.PageSize).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}

// AppendFundFlows 在单个事务内追加流水并按 direction 调整余额（available+total），
// 用 c_version 乐观锁防并发超扣。若余额行不存在则按金额新建。
// frozen 维度的变更（下单冻结/撤单解冻）由阶段4的业务方法显式调用，不在本方法内处理。
func (g *GormStore) AppendFundFlows(ctx context.Context, spaceID string, flows []*service.FundFlow) error {
	if len(flows) == 0 {
		return nil
	}
	for _, fl := range flows {
		fl.SpaceID = spaceID
	}
	return g.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, fl := range flows {
			// 读取当前余额行（加行锁由 SQLite WAL + 单写者近似；乐观锁兜底）。
			var bal service.Balance
			err := tx.Where("c_space_id = ? AND c_account_id = ? AND c_currency = ? AND "+notDeleted(),
				spaceID, fl.AccountID, fl.Currency).First(&bal).Error
			newTotal := "0"
			if err == gorm.ErrRecordNotFound {
				// 不存在：以本次变动为初始余额。
				delta, derr := applyDirection("0", fl.Amount, fl.Direction)
				if derr != nil {
					return derr
				}
				newTotal = delta
				bal = service.Balance{
					SpaceID:   spaceID,
					AccountID: fl.AccountID,
					Currency:  fl.Currency,
					Available: newTotal,
					Frozen:    "0",
					Total:     newTotal,
					Version:   1,
					IsDeleted: service.IsDeletedFalse,
				}
				if err := tx.Create(&bal).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				// 已存在：按 direction 叠加 available 与 total，乐观锁 version+1。
				next, derr := applyDirection(bal.Total, fl.Amount, fl.Direction)
				if derr != nil {
					return derr
				}
				newTotal = next
				nextAvail, derr := applyDirection(bal.Available, fl.Amount, fl.Direction)
				if derr != nil {
					return derr
				}
				res := tx.Model(&service.Balance{}).
					Where("c_id = ? AND c_version = ?", bal.ID, bal.Version).
					Updates(map[string]interface{}{
						"c_available": nextAvail,
						"c_total":     newTotal,
						"c_version":   bal.Version + 1,
					})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return service.ErrConflict // 乐观锁冲突，调用方可重试
				}
			}
			fl.BalanceAfter = newTotal
			if err := tx.Create(fl).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// AdjustFrozen 在事务内调整 frozen 与 available（total 不变），用于下单冻结/撤单解冻/成交结算。
// delta>0 冻结（frozen+=delta, available-=delta）；delta<0 解冻反向。乐观锁 c_version。
func (g *GormStore) AdjustFrozen(ctx context.Context, spaceID, accountID, currency, delta string) error {
	return g.adjustFrozen(ctx, spaceID, accountID, currency, delta)
}

// adjustFrozen 在事务内调整 frozen 与 total（available 不变），用于下单冻结/撤单解冻。
// delta>0 表示冻结（frozen+=delta, available-=delta）；delta<0 表示解冻反向。
// 暴露给阶段4业务层使用。
func (g *GormStore) adjustFrozen(ctx context.Context, spaceID, accountID, currency string, delta string) error {
	return g.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bal service.Balance
		err := tx.Where("c_space_id = ? AND c_account_id = ? AND c_currency = ? AND "+notDeleted(),
			spaceID, accountID, currency).First(&bal).Error
		if err != nil {
			return fmt.Errorf("balance not found: %w", err)
		}
		nextFrozen, err := addDec(bal.Frozen, delta)
		if err != nil {
			return err
		}
		// available 反向
		negDelta, err := applyDirection("0", delta, -1)
		if err != nil {
			return err
		}
		nextAvail, err := addDec(bal.Available, negDelta)
		if err != nil {
			return err
		}
		// total 不变（frozen+available 总和不变）
		res := tx.Model(&service.Balance{}).
			Where("c_id = ? AND c_version = ?", bal.ID, bal.Version).
			Updates(map[string]interface{}{
				"c_available": nextAvail,
				"c_frozen":    nextFrozen,
				"c_version":   bal.Version + 1,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return service.ErrConflict
		}
		return nil
	})
}
