package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type recordingResultStore struct {
	rows  []store.Result
	limit int
	// cancelled 模拟列出之后被更新的 ok 结果取消的行：准备时返回 ErrNotFound。
	cancelled map[string]bool
}

func (s *recordingResultStore) ListPendingResults(_ context.Context, limit int) ([]store.Result, error) {
	s.limit = limit
	rows := make([]store.Result, 0, limit)
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

func (s *recordingResultStore) PreparePendingResult(_ context.Context, resultID string, _ time.Time) (store.Result, bool, error) {
	if s.cancelled[resultID] {
		return store.Result{}, false, store.ErrNotFound
	}
	for _, row := range s.rows {
		if row.ResultID == resultID {
			return row, true, nil
		}
	}
	return store.Result{}, false, nil
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
	rows   []store.Result
	failID string
}

func (p *recordingResultPublisher) PublishResult(_ context.Context, row store.Result) error {
	if row.ResultID == p.failID {
		return &PermanentPublishError{Err: errors.New("unknown event type")}
	}
	p.rows = append(p.rows, row)
	return nil
}

func TestRelayHonorsResultBatchLimit(t *testing.T) {
	resultStore := &recordingResultStore{rows: []store.Result{
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
	resultStore := &recordingResultStore{rows: []store.Result{
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

// 列出之后被更新结果取消的行是正常竞争：跳过它继续投递，不返回错误（否则运行时会断开重连）。
func TestRelaySkipsRowsCancelledAfterListing(t *testing.T) {
	resultStore := &recordingResultStore{rows: []store.Result{
		{ResultID: "old", PublishStatus: store.PublishPending},
		{ResultID: "new", PublishStatus: store.PublishPending},
	}, cancelled: map[string]bool{"old": true}}
	publisher := &recordingResultPublisher{}
	relay := &Relay{Store: resultStore, Publisher: publisher}
	if err := relay.PublishPending(context.Background(), 10); err != nil {
		t.Fatalf("被取消的行不应报错：%v", err)
	}
	if len(publisher.rows) != 1 || publisher.rows[0].ResultID != "new" {
		t.Fatalf("应只投递未取消的行：%+v", publisher.rows)
	}
}

// 只有发布失败才标记为 PublishFailure（运行时据此断开重连）；存储错误不是连接问题。
func TestRelayDistinguishesPublishAndStoreFailures(t *testing.T) {
	resultStore := &recordingResultStore{rows: []store.Result{{ResultID: "r1", PublishStatus: store.PublishPending}}}
	relay := &Relay{Store: resultStore, Publisher: &failingPublisher{err: errors.New("nats: connection closed")}}
	err := relay.PublishPending(context.Background(), 10)
	var publishFailure *PublishFailure
	if !errors.As(err, &publishFailure) {
		t.Fatalf("发布失败应标记为 PublishFailure：%v", err)
	}
	broken := &Relay{Store: &brokenResultStore{}, Publisher: &recordingResultPublisher{}}
	err = broken.PublishPending(context.Background(), 10)
	if err == nil || errors.As(err, &publishFailure) {
		t.Fatalf("存储错误不应标记为 PublishFailure：%v", err)
	}
}

type failingPublisher struct{ err error }

func (p *failingPublisher) PublishResult(context.Context, store.Result) error { return p.err }

type brokenResultStore struct{}

func (brokenResultStore) ListPendingResults(context.Context, int) ([]store.Result, error) {
	return nil, errors.New("database is locked")
}

func (brokenResultStore) PreparePendingResult(context.Context, string, time.Time) (store.Result, bool, error) {
	return store.Result{}, false, nil
}

func (brokenResultStore) TransitionPublishStatus(context.Context, string, store.PublishStatus, store.PublishStatus) error {
	return nil
}
