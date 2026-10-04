package eventconsumer

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
)

func TestPeriodTimeUsesUnixSecondsContract(t *testing.T) {
	want := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if got := periodTime(want.Unix()); !got.Equal(want) {
		t.Fatalf("period time = %s, want %s", got, want)
	}
}

func TestViewDataReadyFactorsCopyIntoPeriodReady(t *testing.T) {
	payload := &storagepb.ViewDataReady{
		Factors: []*storagepb.FactorPeriodState{{
			FactorId: "factor-1", Status: "degraded", SourceHash: "hash-1",
			FailedSubjects: []string{"SOL-USDT"},
		}},
	}
	states := periodReadyFactors(payload)
	if states["factor-1"].Status != "degraded" {
		t.Fatalf("factor states=%v", states)
	}
	state := states["factor-1"]
	if state.SourceHash != "hash-1" || len(state.FailedSubjects) != 1 || state.FailedSubjects[0] != "SOL-USDT" {
		t.Fatalf("factor state=%v", state)
	}
}
