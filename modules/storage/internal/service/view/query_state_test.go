package view

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

// A delivery holds runtime.mu while it writes the index; a query must still
// resolve the active index and its cached stats without waiting for it.
func TestQueryPathDoesNotWaitForRuntimeLock(t *testing.T) {
	ref := viewRef{spaceID: "crypto", viewID: "factors"}
	runtime := &viewRuntime{}
	runtime.mu.Lock()
	runtime.active = "factors-a"
	runtime.statsIndexID = "factors-a"
	runtime.stats = viewindex.ViewIndexStats{Exists: true, EntryCount: 42}
	runtime.publishReadStateLocked()
	runtime.mu.Unlock()
	svc := &Service{views: map[viewRef]*viewRuntime{ref: runtime}}

	runtime.mu.Lock() // an index write in progress
	defer runtime.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if indexID, got := svc.activeIndex("crypto", "factors"); indexID != "factors-a" || got != runtime {
			t.Errorf("activeIndex = %q, %p", indexID, got)
		}
		if stats, ok := cachedActiveIndexStats(runtime, "factors-a"); !ok || stats.EntryCount != 42 {
			t.Errorf("cachedActiveIndexStats = %+v, %v", stats, ok)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("query path blocked on the runtime lock")
	}
}
