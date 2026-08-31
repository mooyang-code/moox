package stockus

import (
	"fmt"
	"time"
)

type Session struct {
	Location *time.Location
	Open     time.Duration
	Close    time.Duration
}

func RegularSession() (Session, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return Session{}, err
	}
	return Session{Location: location, Open: 9*time.Hour + 30*time.Minute, Close: 16 * time.Hour}, nil
}

func (session Session) ExpectedMinuteBuckets(date time.Time) ([]time.Time, error) {
	if session.Location == nil || session.Open < 0 || session.Close <= session.Open || session.Close > 24*time.Hour {
		return nil, fmt.Errorf("united states session is not configured")
	}
	local := date.In(session.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, session.Location)
	result := make([]time.Time, 0, int((session.Close-session.Open)/time.Minute))
	for offset := session.Open; offset < session.Close; offset += time.Minute {
		result = append(result, start.Add(offset))
	}
	return result, nil
}
