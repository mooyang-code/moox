package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
)

// AppendOrderOperation 追加一次通道操作审计。
func (g *GormStore) AppendOrderOperation(ctx context.Context, spaceID string, op *service.OrderOperation) error {
	op.SpaceID = spaceID
	return g.db.WithContext(ctx).Create(op).Error
}

// UpdateOrderOperation 回填操作结果（状态/响应/耗时/错误）。
func (g *GormStore) UpdateOrderOperation(ctx context.Context, spaceID string, op *service.OrderOperation) error {
	res := g.db.WithContext(ctx).
		Model(&service.OrderOperation{}).
		Where("c_space_id = ? AND c_op_id = ?", spaceID, op.OpID).
		Updates(map[string]any{
			"c_op_status":     op.OpStatus,
			"c_response":      op.Response,
			"c_error_code":    op.ErrorCode,
			"c_error_message": op.ErrorMessage,
			"c_latency_ms":    op.LatencyMS,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}
