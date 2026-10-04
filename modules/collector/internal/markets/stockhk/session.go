package stockhk

import (
	"fmt"
	"time"
)

type Segment struct {
	Open  time.Duration
	Close time.Duration
}

type Session struct {
	Location *time.Location
	Segments []Segment
}

func RegularSession() (Session, error) {
	location, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		return Session{}, err
	}
	return Session{Location: location, Segments: []Segment{{Open: 9*time.Hour + 30*time.Minute, Close: 12 * time.Hour}, {Open: 13 * time.Hour, Close: 16 * time.Hour}}}, nil
}

func (session Session) ExpectedMinuteBuckets(date time.Time) ([]time.Time, error) {
	if session.Location == nil || len(session.Segments) == 0 {
		return nil, fmt.Errorf("hong kong session is not configured")
	}
	local := date.In(session.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, session.Location)
	result := make([]time.Time, 0, 330)
	for _, segment := range session.Segments {
		if segment.Open < 0 || segment.Close <= segment.Open || segment.Close > 24*time.Hour {
			return nil, fmt.Errorf("invalid hong kong session segment")
		}
		for offset := segment.Open; offset < segment.Close; offset += time.Minute {
			result = append(result, start.Add(offset))
		}
	}
	return result, nil
}
