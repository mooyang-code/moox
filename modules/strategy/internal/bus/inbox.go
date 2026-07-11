package bus

import (
	"context"
	"gorm.io/gorm"
)

func AcceptOnce(ctx context.Context, db *gorm.DB, consumer, messageID string, fn func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Table("t_strategy_inbox").Where("c_consumer=? AND c_message_id=?", consumer, messageID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Table("t_strategy_inbox").Create(map[string]any{"c_consumer": consumer, "c_message_id": messageID}).Error
	})
}
