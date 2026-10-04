package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestUpdateRunnerProtectsPendingSession(t *testing.T) {
	for _, pending := range []bool{true, false} {
		t.Run(map[bool]string{true: "pending", false: "legacy_disabled"}[pending], func(t *testing.T) {
			repo := openCurrentStore(t)
			ctx := context.Background()
			at := time.UnixMilli(1)
			runner := domain.StrategyRunner{ID: "runner", StrategyID: "original", SpaceID: "space", SourceViewID: "source", Frequency: "1m", Status: domain.RunnerStatusDisabled, CreatedAt: at, UpdatedAt: at}
			if err := repo.CreateRunner(ctx, runner); err != nil {
				t.Fatal(err)
			}
			if pending {
				session := "claim-pending"
				if err := repo.SetInstanceEnabled(ctx, runner.ID, false, &session, at); err != nil {
					t.Fatal(err)
				}
			}
			before, err := repo.GetInstance(ctx, runner.ID)
			if err != nil {
				t.Fatal(err)
			}
			runner.StrategyID, runner.SourceViewID = "replacement", "other-source"
			runner.UpdatedAt = time.UnixMilli(2)
			err = repo.UpdateRunner(ctx, runner)
			if pending && (err == nil || !strings.Contains(err.Error(), "unfinished")) {
				t.Fatalf("pending mutation error = %v", err)
			}
			if !pending && err != nil {
				t.Fatalf("ordinary disabled legacy update failed: %v", err)
			}
			after, err := repo.GetInstance(ctx, runner.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pending && !reflect.DeepEqual(before, after) {
				t.Fatalf("pending identity changed: before=%+v after=%+v", before, after)
			}
			if !pending && after.StrategyID != "replacement" {
				t.Fatalf("legacy update not applied: %+v", after)
			}
		})
	}
}
