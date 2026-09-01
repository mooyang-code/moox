package marketdata

import (
	"fmt"
	"time"
)

type SessionSegmentKind string

const (
	SessionRegular    SessionSegmentKind = "regular"
	SessionAuction    SessionSegmentKind = "auction"
	SessionPreMarket  SessionSegmentKind = "pre_market"
	SessionPostMarket SessionSegmentKind = "post_market"
)

// SessionSegment uses wall-clock offsets in the market's Location. Auction
// and extended-hours segments are represented explicitly but are not included
// in continuous-session gap buckets by default.
type SessionSegment struct {
	Open  time.Duration
	Close time.Duration
	Kind  SessionSegmentKind
}

type SessionSpec struct {
	Location *time.Location
	Segments []SessionSegment
}

func (spec SessionSpec) Validate() error {
	if spec.Location == nil {
		return fmt.Errorf("session timezone is required")
	}
	if len(spec.Segments) == 0 {
		return fmt.Errorf("session segments are required")
	}
	var previousClose time.Duration
	for index, segment := range spec.Segments {
		if segment.Open < 0 || segment.Close <= segment.Open || segment.Close > 24*time.Hour {
			return fmt.Errorf("session segment %d is invalid", index)
		}
		if index > 0 && segment.Open < previousClose {
			return fmt.Errorf("session segment %d overlaps the previous segment", index)
		}
		switch segment.Kind {
		case "", SessionRegular, SessionAuction, SessionPreMarket, SessionPostMarket:
		default:
			return fmt.Errorf("session segment %d kind %q is invalid", index, segment.Kind)
		}
		previousClose = segment.Close
	}
	return nil
}

// ExpectedMinuteBuckets returns only regular continuous-session buckets.
// It intentionally leaves auction, pre-market and post-market rows out of
// strict gap calculations until a market policy opts into those semantics.
func (spec SessionSpec) ExpectedMinuteBuckets(date CivilDate) ([]time.Time, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := date.Validate(); err != nil {
		return nil, err
	}
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, spec.Location)
	result := make([]time.Time, 0, 390)
	for _, segment := range spec.Segments {
		if segment.Kind != "" && segment.Kind != SessionRegular {
			continue
		}
		for offset := segment.Open; offset < segment.Close; offset += time.Minute {
			result = append(result, start.Add(offset))
		}
	}
	return result, nil
}
