package domain

import (
	"strings"
	"time"
)

// PeriodStorageState records the authoritative Storage observation for one
// immutable Collector period snapshot.
type PeriodStorageState struct {
	Key           PeriodKey
	SeriesHash    string
	ExpectedCount uint32
	DeadlineAt    time.Time
	Status        string
	ConfirmedAt   time.Time
}

func (s *PeriodStorageState) Normalize() {
	if s == nil {
		return
	}
	s.Key.SpaceID = strings.TrimSpace(s.Key.SpaceID)
	s.Key.DatasetID = strings.TrimSpace(s.Key.DatasetID)
	s.Key.Frequency = strings.TrimSpace(s.Key.Frequency)
	s.Key.PeriodTime = s.Key.PeriodTime.UTC()
	s.SeriesHash = strings.ToLower(strings.TrimSpace(s.SeriesHash))
	s.DeadlineAt = s.DeadlineAt.UTC()
	s.Status = strings.ToLower(strings.TrimSpace(s.Status))
	s.ConfirmedAt = s.ConfirmedAt.UTC()
}
