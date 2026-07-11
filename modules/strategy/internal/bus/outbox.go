package bus

import (
	"context"
	"gorm.io/gorm"
	"sync"
)

type Publisher interface {
	Publish(context.Context, string, []byte) error
}
type Relay struct {
	DB        *gorm.DB
	Publisher Publisher
	mu        sync.Mutex
}

func (r *Relay) PublishPending(ctx context.Context, limit int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []struct {
		MessageID, Topic string
		Payload          []byte
	}
	if err := r.DB.WithContext(ctx).Table("t_strategy_outbox").Where("c_published=0").Order("c_ctime").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := r.Publisher.Publish(ctx, row.Topic, row.Payload); err != nil {
			return err
		}
		if err := r.DB.WithContext(ctx).Table("t_strategy_outbox").Where("c_message_id=?", row.MessageID).Update("c_published", 1).Error; err != nil {
			return err
		}
	}
	return nil
}
