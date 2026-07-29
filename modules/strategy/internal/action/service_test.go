package action

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type resultStoreStub struct {
	commitRequest store.CommitResultRequest
	failure       error
	failureAt     time.Time
}

func (s *resultStoreStub) CommitResult(
	_ context.Context,
	request store.CommitResultRequest,
) (store.CommitResultOutcome, error) {
	s.commitRequest = request
	return store.CommitResultOutcome{Result: request.Result, Created: true}, nil
}

func (s *resultStoreStub) RecordRunnerFailure(
	_ context.Context,
	_ string,
	failure error,
	at time.Time,
) error {
	s.failure = failure
	s.failureAt = at
	return nil
}

func TestCommitDelegatesAcceptedResultToAtomicStore(t *testing.T) {
	repo := &resultStoreStub{}
	service := Service{Repo: repo}
	result := domain.StrategyResult{
		ID: "result-1", RunnerID: "runner-1", Action: domain.ActionHold,
	}
	output := domain.Output{Action: domain.ActionHold}

	outcome, err := service.Commit(context.Background(), result, output)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Created || repo.commitRequest.Result.ID != result.ID ||
		repo.commitRequest.Output.Action != output.Action {
		t.Fatalf("outcome=%+v request=%+v", outcome, repo.commitRequest)
	}
}

func TestRecordFailureUpdatesRunnerHealthWithoutResult(t *testing.T) {
	repo := &resultStoreStub{}
	service := Service{Repo: repo}
	failure := errors.New("worker failed")
	at := time.UnixMilli(1234)

	if err := service.RecordFailure(
		context.Background(),
		"runner-1",
		failure,
		at,
	); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(repo.failure, failure) || !repo.failureAt.Equal(at) {
		t.Fatalf("failure=%v at=%v", repo.failure, repo.failureAt)
	}
}
