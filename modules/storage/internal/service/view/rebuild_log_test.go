package view

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestAppendRebuildPhasePreservesHistoryAndTerminal(t *testing.T) {
	details := ""
	for _, phase := range []string{"maintenance", "prepare", "backfill", "catch_up", "activate"} {
		details = appendRebuildPhase(details, phase)
	}
	details = appendRebuildPhase(details, "completed")
	var payload struct {
		Phase        string   `json:"phase"`
		PhaseHistory []string `json:"phase_history"`
	}
	if err := json.Unmarshal([]byte(details), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Phase != "completed" {
		t.Fatalf("terminal phase = %q, want completed", payload.Phase)
	}
	want := []string{"maintenance", "prepare", "backfill", "catch_up", "activate", "completed"}
	if len(payload.PhaseHistory) != len(want) {
		t.Fatalf("phase history = %#v, want %#v", payload.PhaseHistory, want)
	}
	for i := range want {
		if payload.PhaseHistory[i] != want[i] {
			t.Fatalf("phase history = %#v, want %#v", payload.PhaseHistory, want)
		}
	}
}

func TestRebuildErrorSummaryRedactsAndBoundsDetails(t *testing.T) {
	message := `password=secret token=abc {"api_key":"sekret","token":"json-token"} Authorization: Basic dXNlcjpwYXNz ` + strings.Repeat("x", 3000)
	got := rebuildErrorSummary(errors.New(message))
	for _, value := range []string{"secret", "abc", "sekret", "json-token", "dXNlcjpwYXNz"} {
		if strings.Contains(got, value) {
			t.Fatalf("sensitive values were not redacted: %q", got)
		}
	}
	if len(got) > 2048 {
		t.Fatalf("summary length = %d, want <= 2048", len(got))
	}
}

func TestRebuildTriggerReasonRecognizesRevisionScopedManualRequest(t *testing.T) {
	view := &pb.View{
		ActiveIndexId:       "index-a",
		DesiredViewRevision: 8,
		ActiveViewRevision:  7,
		Attributes:          map[string]string{coremetadata.ManualRebuildRevisionAttribute: "8"},
	}
	if got := rebuildTriggerReason(view, viewindex.ViewIndexStats{Exists: true}, false, false); got != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_MANUAL_REPAIR {
		t.Fatalf("trigger reason = %v, want manual repair", got)
	}
	view.Attributes[coremetadata.ManualRebuildRevisionAttribute] = "7"
	if got := rebuildTriggerReason(view, viewindex.ViewIndexStats{Exists: true}, false, false); got != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_DEFINITION_CHANGE {
		t.Fatalf("stale marker trigger reason = %v, want definition change", got)
	}
}

func TestRebuildTriggerReasonPrioritizesSingleSeriesCapacity(t *testing.T) {
	view := &pb.View{SpaceId: "crypto", ViewId: "view_crypto_spot_kline_1m", ActiveIndexId: "index-a", DesiredViewRevision: 1, ActiveViewRevision: 1}
	if got := rebuildTriggerReason(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1}, true, true); got != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SERIES_CAPACITY {
		t.Fatalf("trigger reason=%v, want series capacity", got)
	}
}
