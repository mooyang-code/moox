package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestRunnerStoreRoundTripPreservesNullableFields(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	logicalAccountID := "logical-account-1"
	want := domain.StrategyRunner{
		ID:                 "runner-1",
		StrategyID:         "strategy-1",
		SpaceID:            "space-1",
		ViewID:             "view-1",
		Frequency:          "1m",
		ParamsJSON:         json.RawMessage(`{"fast":12}`),
		LogicalAccountID:   &logicalAccountID,
		Status:             domain.RunnerStatusDisabled,
		CurrentTargetsJSON: json.RawMessage(`[]`),
		CommandSequence:    0,
		CreatedAt:          time.UnixMilli(2000).UTC(),
		UpdatedAt:          time.UnixMilli(3000).UTC(),
	}
	if err := repo.CreateRunner(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunner(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LogicalAccountID == nil || *got.LogicalAccountID != logicalAccountID {
		t.Fatalf("LogicalAccountID = %v", got.LogicalAccountID)
	}
	if got.LastResultID != nil || got.LastSuccessAt != nil || got.LastError != nil {
		t.Fatalf("nullable runner health fields = %+v", got)
	}
}

func seedStrategy(t *testing.T, repo *Store, id string) {
	t.Helper()
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{
		ID: id, Name: id, ManifestYAML: "{}", SourceCode: "pass", SourceHash: id,
		CreatedAt: time.UnixMilli(1000).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}
