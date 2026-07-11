package pebble

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type recordJournalCursor struct {
	Version  uint32 `json:"v"`
	SourceID string `json:"source_id"`
	After    uint64 `json:"after"`
	Through  uint64 `json:"through"`
	Last     uint64 `json:"last"`
}

// ScanRecordJournal returns committed events in the exclusive/inclusive range
// (after, through]. The cursor is bound to the source and both range bounds.
func (s *Store) ScanRecordJournal(ctx context.Context, after, through uint64, page *pb.Page) ([]*pb.RecordRowsCommittedEvent, uint64, *pb.PageResult, error) {
	_ = ctx
	if page == nil {
		page = &pb.Page{}
	}
	var decoded *recordJournalCursor
	if cursor := page.GetCursor(); cursor != "" {
		value, err := s.decodeRecordJournalCursor(cursor)
		if err != nil {
			return nil, after, nil, err
		}
		decoded = &value
		if through == 0 {
			through = value.Through
		}
	}
	_, watermark, err := s.RecordWatermark(ctx)
	if err != nil {
		return nil, after, nil, err
	}
	if through == 0 || through > watermark {
		through = watermark
	}
	rangeAfter := after
	rangeThrough := through
	if decoded != nil {
		if decoded.After != rangeAfter || decoded.Through != rangeThrough {
			return nil, after, nil, errors.New("record journal cursor bounds mismatch")
		}
		after = decoded.Last
	}
	if after >= through {
		return nil, after, &pb.PageResult{Size: pageSize(page), HasMore: false}, nil
	}
	lower := encodeRecordJournalKey(after + 1)
	upper := nextPrefix(encodeRecordJournalKey(through))
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, after, nil, err
	}
	defer iter.Close()
	size := pageSize(page)
	events := make([]*pb.RecordRowsCommittedEvent, 0, size)
	last := after
	for valid := iter.First(); valid; valid = iter.Next() {
		seq := parseUint(string(iter.Key()[len(recordJournalPrefix):]))
		if seq == 0 {
			return nil, last, nil, fmt.Errorf("invalid journal key %q", iter.Key())
		}
		if seq != last+1 {
			return nil, last, nil, fmt.Errorf("record journal sequence gap: got %d after %d", seq, last)
		}
		if uint32(len(events)) >= size {
			return events, last, &pb.PageResult{Size: size, HasMore: true, NextCursor: s.encodeRecordJournalCursor(recordJournalCursor{Version: 1, SourceID: s.recordSource, After: rangeAfter, Through: rangeThrough, Last: last})}, nil
		}
		event := &pb.RecordRowsCommittedEvent{}
		if err := proto.Unmarshal(iter.Value(), event); err != nil {
			return nil, last, nil, err
		}
		events = append(events, event)
		last = seq
	}
	if err := iter.Error(); err != nil {
		return nil, last, nil, err
	}
	if len(events) == 0 {
		last = through
	}
	return events, last, &pb.PageResult{Size: size, HasMore: false}, nil
}

func (s *Store) encodeRecordJournalCursor(cursor recordJournalCursor) string {
	payload, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(payload)
	signed := append(payload, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(signed)
}

func (s *Store) decodeRecordJournalCursor(encoded string) (recordJournalCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) <= sha256.Size {
		return recordJournalCursor{}, errors.New("invalid record journal cursor")
	}
	payload, signature := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return recordJournalCursor{}, errors.New("record journal cursor signature mismatch")
	}
	var cursor recordJournalCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.SourceID != s.recordSource {
		return recordJournalCursor{}, errors.New("record journal cursor binding mismatch")
	}
	return cursor, nil
}
