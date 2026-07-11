package bus

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"sync"
	"time"
)

type Publisher interface {
	Publish(context.Context, string, []byte) error
}
type IdempotentPublisher interface {
	PublishWithID(context.Context, string, string, []byte) error
}
type Relay struct {
	DB        *gorm.DB
	Publisher Publisher
	mu        sync.Mutex
}

func (r *Relay) PublishPending(ctx context.Context, limit int) error {
	if r == nil || r.DB == nil || r.Publisher == nil {
		return fmt.Errorf("strategy outbox relay dependencies are required")
	}
	if limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []struct {
		MessageID string `gorm:"column:c_message_id"`
		Topic     string `gorm:"column:c_topic"`
		Payload   []byte `gorm:"column:c_payload"`
	}
	now := time.Now().UTC()
	if err := r.DB.WithContext(ctx).Table("t_strategy_outbox").Where("c_published=0 AND (c_claimed_until IS NULL OR c_claimed_until < ?)", now).Order("c_ctime").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		token := fmt.Sprintf("relay-%d", time.Now().UnixNano())
		claimed := r.DB.WithContext(ctx).Table("t_strategy_outbox").Where("c_message_id=? AND c_published=0 AND (c_claimed_until IS NULL OR c_claimed_until < ?)", row.MessageID, now).Updates(map[string]any{"c_claimed_until": now.Add(30 * time.Second), "c_claim_token": token})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			continue
		}
		var err error
		if p, ok := r.Publisher.(IdempotentPublisher); ok {
			err = p.PublishWithID(ctx, row.MessageID, row.Topic, row.Payload)
		} else {
			err = r.Publisher.Publish(ctx, row.Topic, row.Payload)
		}
		if err != nil {
			_ = r.DB.WithContext(ctx).Table("t_strategy_outbox").Where("c_message_id=? AND c_claim_token=?", row.MessageID, token).Updates(map[string]any{"c_claimed_until": nil, "c_claim_token": ""}).Error
			return err
		}
		if err := r.DB.WithContext(ctx).Table("t_strategy_outbox").Where("c_message_id=? AND c_claim_token=?", row.MessageID, token).Updates(map[string]any{"c_published": 1, "c_claimed_until": nil, "c_claim_token": ""}).Error; err != nil {
			return err
		}
	}
	return nil
}
