package store

import (
	"context"
	"errors"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pendingEventRecord struct {
	MessageID  string    `gorm:"column:c_message_id;primaryKey"`
	Payload    []byte    `gorm:"column:c_payload;not null"`
	ReceivedAt time.Time `gorm:"column:c_received_at;not null"`
}

func (pendingEventRecord) TableName() string { return "t_factor_event_inbox" }

type processedEventRecord struct {
	MessageID   string    `gorm:"column:c_message_id;primaryKey"`
	ProcessedAt time.Time `gorm:"column:c_processed_at;not null"`
}

func (processedEventRecord) TableName() string { return "t_factor_event_processed" }

func (s *Store) PutPendingEvent(ctx context.Context, messageID string, event *storagepb.DatasetFieldsChanged, receivedAt time.Time) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	payload, err := proto.Marshal(event)
	if err != nil {
		return err
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&pendingEventRecord{MessageID: messageID, Payload: payload, ReceivedAt: receivedAt}).Error
}

func (s *Store) IsProcessedEvent(ctx context.Context, messageID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, gorm.ErrInvalidDB
	}
	var record processedEventRecord
	err := s.db.WithContext(ctx).Where("c_message_id = ?", messageID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) LoadPendingEvents(ctx context.Context, visit func(string, *storagepb.DatasetFieldsChanged, time.Time) error) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	var records []pendingEventRecord
	query := s.db.WithContext(ctx).
		Where("NOT EXISTS (SELECT 1 FROM t_factor_event_processed p WHERE p.c_message_id = t_factor_event_inbox.c_message_id)").
		Order("c_received_at ASC, c_message_id ASC")
	if err := query.Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		event := new(storagepb.DatasetFieldsChanged)
		if err := proto.Unmarshal(record.Payload, event); err != nil {
			return err
		}
		if err := visit(record.MessageID, event, record.ReceivedAt); err != nil {
			return err
		}
	}
	return nil
}

// CommitPendingEvents atomically records successful scheduler acceptance and
// removes the corresponding inbox rows. A failed delete rolls back the
// processed marker, so a later redelivery remains recoverable.
func (s *Store) CommitPendingEvents(ctx context.Context, messageIDs []string) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	if len(messageIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		processed := make([]processedEventRecord, 0, len(messageIDs))
		for _, messageID := range messageIDs {
			processed = append(processed, processedEventRecord{MessageID: messageID, ProcessedAt: now})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&processed).Error; err != nil {
			return err
		}
		return tx.Where("c_message_id IN ?", messageIDs).Delete(&pendingEventRecord{}).Error
	})
}
