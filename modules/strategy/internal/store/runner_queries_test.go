package store

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestListRunnersFiltersByStatus(t *testing.T) {
	repo := openCurrentStore(t)
	seedStrategy(t, repo, "strategy-1")
	seedRunner(t, repo, "disabled-runner", "strategy-1", domain.RunnerStatusDisabled)
	seedRunner(t, repo, "enabled-runner", "strategy-1", domain.RunnerStatusEnabled)

	runners, err := repo.ListRunners(context.Background(), RunnerFilter{Status: domain.RunnerStatusEnabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || runners[0].ID != "enabled-runner" {
		t.Fatalf("ListRunners() = %+v", runners)
	}
}
