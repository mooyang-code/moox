package binance

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

const (
	klineInitialLimit   = 1000
	klineCatchupLimit   = 5000
	klineFetchPageLimit = 1000
)

type klineCursor struct {
	initial       bool
	nextStart     time.Time
	remaining     int
	requestLimit  int
	requestStart  time.Time
	requestIssued bool
	done          bool
}

func newKlineCursor(watermark *time.Time) *klineCursor {
	if watermark == nil || watermark.IsZero() {
		return &klineCursor{initial: true, remaining: klineInitialLimit}
	}
	return &klineCursor{nextStart: watermark.UTC().Add(time.Millisecond), remaining: klineCatchupLimit}
}

func (c *klineCursor) NextRequest(symbol, interval string) (*exchange.KlineRequest, bool) {
	if c == nil || c.done || c.requestIssued || c.remaining <= 0 {
		return nil, false
	}
	limit := klineFetchPageLimit
	if c.initial || c.remaining < limit {
		limit = c.remaining
	}
	c.requestLimit = limit
	c.requestStart = c.nextStart
	c.requestIssued = true
	return &exchange.KlineRequest{Symbol: symbol, Interval: interval, Limit: limit, StartTime: c.nextStart}, true
}

func (c *klineCursor) Advance(rows []*exchange.Kline) (bool, error) {
	if c == nil || !c.requestIssued {
		return false, fmt.Errorf("kline cursor request was not issued")
	}
	c.requestIssued = false
	if len(rows) == 0 {
		c.done = true
		return false, nil
	}
	var latest time.Time
	for _, row := range rows {
		if row != nil && row.OpenTime.After(latest) {
			latest = row.OpenTime.UTC()
		}
	}
	if latest.IsZero() || (!c.requestStart.IsZero() && latest.Before(c.requestStart)) {
		c.done = true
		return false, fmt.Errorf("kline page did not advance beyond %s", c.requestStart.Format(time.RFC3339Nano))
	}
	c.remaining -= len(rows)
	if c.initial || len(rows) < c.requestLimit || c.remaining <= 0 {
		c.done = true
		return false, nil
	}
	c.nextStart = latest.Add(time.Millisecond)
	return true, nil
}
