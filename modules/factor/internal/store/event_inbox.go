package store

import (
	"context"
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

func (s *Store) LoadPendingEvents(ctx context.Context, visit func(string, *storagepb.DatasetFieldsChanged, time.Time) error) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	var records []pendingEventRecord
	if err := s.db.WithContext(ctx).Order("c_received_at ASC, c_message_id ASC").Find(&records).Error; err != nil {
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

func (s *Store) DeletePendingEvents(ctx context.Context, messageIDs []string) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	if len(messageIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("c_message_id IN ?", messageIDs).Delete(&pendingEventRecord{}).Error
}
