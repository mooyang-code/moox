package bootstrap

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// factorStorageHealth latches a malformed SQLite error until the Factor
// process is restarted. A successful task must not clear the signal: once
// SQLite reports corruption, readiness should remain failed until the database
// is explicitly rebuilt.
type factorStorageHealth struct {
	malformed   atomic.Bool
	lastError   atomic.Value
	lastErrorAt atomic.Int64
}

func (h *factorStorageHealth) ObserveStorageWriteFailure(err error) {
	if h == nil || !store.IsDatabaseCorruption(err) {
		return
	}
	h.lastError.Store(strings.TrimSpace(err.Error()))
	h.lastErrorAt.Store(time.Now().UTC().Unix())
	// Publish the latch last so a concurrent readiness snapshot always sees
	// the diagnostic fields once malformed becomes true.
	h.malformed.Store(true)
}

func (h *factorStorageHealth) Ready() bool {
	return h == nil || !h.malformed.Load()
}

func (h *factorStorageHealth) Error() (string, time.Time) {
	if h == nil || !h.malformed.Load() {
		return "", time.Time{}
	}
	message, _ := h.lastError.Load().(string)
	if message == "" {
		message = "factor SQLite database is malformed"
	}
	return message, time.Unix(h.lastErrorAt.Load(), 0).UTC()
}

var _ interface {
	ObserveStorageWriteFailure(error)
} = (*factorStorageHealth)(nil)
