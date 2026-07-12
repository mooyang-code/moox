package store

import (
	"context"
	"math/big"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) WritePerformancePoint(ctx context.Context, point domain.PerformancePoint) error {
	if err := point.Validate(); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pointConflict := clause.OnConflict{
			Columns: []clause.Column{{Name: "c_binding_id"}, {Name: "c_performance_source"}, {Name: "c_point_time"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"c_nav", "c_cumulative_return", "c_drawdown", "c_gross_exposure", "c_net_exposure", "c_turnover", "c_fees", "c_data_revision", "c_calculated_at",
			}),
		}
		if err := tx.Clauses(pointConflict).Create(&point).Error; err != nil {
			return err
		}
		date := point.PointTime.UTC().Format("2006-01-02")
		var dayPoints []domain.PerformancePoint
		if err := tx.Where("c_binding_id=? AND c_performance_source=? AND substr(c_point_time, 1, 10)=?", point.BindingID, point.Source, date).Order("c_point_time ASC").Find(&dayPoints).Error; err != nil {
			return err
		}
		if len(dayPoints) == 0 {
			dayPoints = []domain.PerformancePoint{point}
		}
		first, last := dayPoints[0], dayPoints[len(dayPoints)-1]
		maxDrawdown := first.Drawdown
		for _, candidate := range dayPoints[1:] {
			if lessDecimal(candidate.Drawdown, maxDrawdown) {
				maxDrawdown = candidate.Drawdown
			}
		}
		daily := domain.PerformanceDaily{BindingID: point.BindingID, Source: point.Source, TradeDate: date, StartNAV: first.NAV, EndNAV: last.NAV, Return: last.CumulativeReturn, MaxDrawdown: maxDrawdown, Turnover: last.Turnover, Fees: last.Fees, SampleCount: int64(len(dayPoints)), DataRevision: last.DataRevision, CalculatedAt: point.CalculatedAt}
		dailyConflict := clause.OnConflict{
			Columns: []clause.Column{{Name: "c_binding_id"}, {Name: "c_performance_source"}, {Name: "c_trade_date"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"c_end_nav", "c_return", "c_max_drawdown", "c_turnover", "c_fees", "c_sample_count", "c_data_revision", "c_calculated_at",
			}),
		}
		return tx.Clauses(dailyConflict).Create(&daily).Error
	})
}

func lessDecimal(left, right string) bool {
	l, lok := new(big.Rat).SetString(left)
	r, rok := new(big.Rat).SetString(right)
	return lok && rok && l.Cmp(r) < 0
}
