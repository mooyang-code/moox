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

func TestViewDataReadyBindingsCopyIntoPeriodReady(t *testing.T) {
	payload := &storagepb.ViewDataReady{
		Bindings: []*storagepb.FactorBindingPeriodState{{
			BindingId: "binding-1", Status: "degraded", SourceHash: "hash-1",
			SkippedSubjects: []string{"ETH-USDT"}, FailedSubjects: []string{"SOL-USDT"},
		}},
	}
	statuses, states := periodReadyBindings(payload)
	if statuses["binding-1"] != "degraded" {
		t.Fatalf("binding statuses=%v", statuses)
	}
	state := states["binding-1"]
	if state.SourceHash != "hash-1" || len(state.SkippedSubjects) != 1 || state.SkippedSubjects[0] != "ETH-USDT" || len(state.FailedSubjects) != 1 || state.FailedSubjects[0] != "SOL-USDT" {
		t.Fatalf("binding state=%v", state)
	}
}
