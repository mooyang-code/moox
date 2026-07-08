package jobhistory

import (
	"context"
	"fmt"
	"os"
	"time"
)

func (s *Store) EnsureDayDB(ctx context.Context, day time.Time) error {
	db, err := s.openDayDB(ctx, day)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close job history day db: %w", err)
	}
	return nil
}

func (s *Store) MaintainDaily(ctx context.Context, now time.Time) error {
	day := calendarDay(now)
	for _, offset := range []int{1, 2} {
		if err := s.EnsureDayDB(ctx, day.AddDate(0, 0, offset)); err != nil {
			return err
		}
	}
	retentionDays := s.retentionDays
	if retentionDays <= 0 {
		retentionDays = 2
	}
	for _, offset := range []int{-retentionDays, -(retentionDays + 1)} {
		if err := os.Remove(s.dayPath(day.AddDate(0, 0, offset))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old job history db: %w", err)
		}
	}
	return nil
}

func calendarDay(t time.Time) time.Time {
	loc := t.Location()
	if loc == nil {
		loc = time.Local
	}
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
