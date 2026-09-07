package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type recordingResultStore struct {
	rows  []store.StrategyResult
	limit int
}

func (s *recordingResultStore) ListPendingResults(_ context.Context, limit int) ([]store.StrategyResult, error) {
	s.limit = limit
	rows := make([]store.StrategyResult, 0, limit)
	for _, row := range s.rows {
		if row.PublishStatus != store.PublishPending {
			continue
		}
		rows = append(rows, row)
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
}

func (s *recordingResultStore) PreparePendingResult(_ context.Context, resultID string, _ time.Time) (store.StrategyResult, bool, error) {
	for _, row := range s.rows {
		if row.ResultID == resultID {
			return row, true, nil
		}
	}
	return store.StrategyResult{}, false, nil
}

func (s *recordingResultStore) TransitionPublishStatus(_ context.Context, resultID string, from, to store.PublishStatus) error {
	for index := range s.rows {
		if s.rows[index].ResultID == resultID && (from == "" || s.rows[index].PublishStatus == from || s.rows[index].PublishStatus == store.PublishNone) {
			s.rows[index].PublishStatus = to
			return nil
		}
	}
	return nil
}

type recordingResultPublisher struct {
	rows   []store.StrategyResult
	failID string
}

func (p *recordingResultPublisher) PublishResult(_ context.Context, row store.StrategyResult) error {
	if row.ResultID == p.failID {
		return &PermanentPublishError{Err: errors.New("unknown event type")}
	}
	p.rows = append(p.rows, row)
	return nil
}

func TestRelayHonorsResultBatchLimit(t *testing.T) {
	resultStore := &recordingResultStore{rows: []store.StrategyResult{
		{ResultID: "r1", PublishStatus: store.PublishPending},
		{ResultID: "r2", PublishStatus: store.PublishPending},
		{ResultID: "r3", PublishStatus: store.PublishPending},
	}}
	publisher := &recordingResultPublisher{}
	relay := &Relay{Store: resultStore, Publisher: publisher}
	if err := relay.PublishPending(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if resultStore.limit != 2 || len(publisher.rows) != 2 {
		t.Fatalf("result batch limit=%d published=%d", resultStore.limit, len(publisher.rows))
	}
}

func TestRelayQuarantinesPermanentResultAndAdvancesPrefix(t *testing.T) {
	resultStore := &recordingResultStore{rows: []store.StrategyResult{
		{ResultID: "bad", PublishStatus: store.PublishPending},
		{ResultID: "good", PublishStatus: store.PublishPending},
	}}
	publisher := &recordingResultPublisher{failID: "bad"}
	relay := &Relay{Store: resultStore, Publisher: publisher}
	if err := relay.PublishPending(context.Background(), 1); err == nil {
		t.Fatal("permanent publish error was swallowed")
	}
	if resultStore.rows[0].PublishStatus != store.PublishCancelled {
		t.Fatalf("permanent row status=%q, want cancelled", resultStore.rows[0].PublishStatus)
	}
	publisher.failID = ""
	if err := relay.PublishPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(publisher.rows) != 1 || publisher.rows[0].ResultID != "good" {
		t.Fatalf("published rows=%+v", publisher.rows)
	}
}
