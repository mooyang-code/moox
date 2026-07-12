// Package store owns Strategy persistence adapters.
package store

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"gorm.io/gorm"
)

var ErrStateConflict = errors.New("strategy: state conflict")
var ErrIdempotencyConflict = errors.New("strategy: idempotency conflict")

// Store is the persistence boundary for the strategy module. Callers must use
// domain methods instead of reaching into the underlying database.
type Store struct {
	db       *gorm.DB
	commitMu sync.Mutex
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func isRetryableLock(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") || strings.Contains(message, "deadlocked")
}
