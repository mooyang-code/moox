package pebble

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

const (
	outboxPrefix   = "\x00moox-outbox/"
	outboxSequence = "\x00moox-outbox-sequence"
)

func (s *Store) WriteRowsWithOutbox(ctx context.Context, rows []*pb.PrimaryStoreRow, entry *device.OutboxEntry) error {
	if entry == nil || len(entry.Data) == 0 {
		return errors.New("outbox entry is required")
	}
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(entry.Data, msg); err != nil {
		return fmt.Errorf("decode outbox message: %w", err)
	}
	if err := jetstream.ValidateMessage(msg, 16<<20); err != nil {
		return err
	}
	if entry.MessageID != "" && entry.MessageID != msg.GetMessageId() {
		return errors.New("outbox message_id mismatch")
	}
	if entry.Topic != "" && entry.Topic != msg.GetTopic() {
		return errors.New("outbox topic mismatch")
	}
	entry.MessageID, entry.Topic = msg.GetMessageId(), msg.GetTopic()
	return s.writeRows(ctx, rows, entry)
}

func (s *Store) stageOutbox(batch *cpebble.Batch, entry *device.OutboxEntry) error {
	seq, err := s.nextOutboxSequence()
	if err != nil {
		return err
	}
	entry.Sequence = seq
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	key := []byte(outboxKey(seq))
	if err := batch.Set(key, append([]byte(nil), entry.Data...), s.writeOptions); err != nil {
		return err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	return batch.Set([]byte(outboxSequence), buf[:], s.writeOptions)
}

func (s *Store) nextOutboxSequence() (uint64, error) {
	data, closer, err := s.db.Get([]byte(outboxSequence))
	if errors.Is(err, cpebble.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(data) == 8 {
		return binary.BigEndian.Uint64(data) + 1, nil
	}
	seq, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decode outbox sequence: %w", err)
	}
	return seq + 1, nil
}

func outboxKey(seq uint64) string { return fmt.Sprintf("%s%020d", outboxPrefix, seq) }

func (s *Store) ListOutbox(ctx context.Context, after uint64, maxItems int, maxBytes int) ([]*device.OutboxEntry, error) {
	_ = ctx
	if maxItems <= 0 {
		maxItems = 100
	}
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: []byte(outboxPrefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := make([]*device.OutboxEntry, 0, maxItems)
	bytesRead := 0
	for valid := iter.First(); valid; valid = iter.Next() {
		seq, err := strconv.ParseUint(strings.TrimPrefix(string(iter.Key()), outboxPrefix), 10, 64)
		if err != nil || seq <= after {
			continue
		}
		data := append([]byte(nil), iter.Value()...)
		if len(out) > 0 && bytesRead+len(data) > maxBytes {
			break
		}
		msg := &messagepb.MooxMessage{}
		if err := proto.Unmarshal(data, msg); err != nil {
			return nil, fmt.Errorf("decode outbox message %d: %w", seq, err)
		}
		created := time.Now().UTC()
		if msg.GetPublishedAt() != nil {
			created = msg.GetPublishedAt().AsTime()
		}
		out = append(out, &device.OutboxEntry{Sequence: seq, MessageID: msg.GetMessageId(), Topic: msg.GetTopic(), Data: data, CreatedAt: created})
		bytesRead += len(data)
		if len(out) >= maxItems {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteOutbox(ctx context.Context, sequences []uint64) error {
	_ = ctx
	if len(sequences) == 0 {
		return nil
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, seq := range sequences {
		if seq == 0 {
			continue
		}
		if err := batch.Delete([]byte(outboxKey(seq)), s.writeOptions); err != nil {
			return err
		}
	}
	return batch.Commit(s.writeOptions)
}
