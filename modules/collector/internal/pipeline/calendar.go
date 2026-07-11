package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

type CalendarStore interface {
	WriteCalendarDays(context.Context, string, time.Time, []markets.CalendarDay) error
}
type CalendarPipeline struct {
	Policy     markets.CalendarPolicy
	Store      CalendarStore
	DatasetID  string
	Generation time.Time
}
type CalendarRequest struct {
	Start, End time.Time
	Limit      int
	Cursor     string
}
type CalendarPipelineResult struct {
	Rows       int
	Complete   bool
	NextCursor string
}

func (p CalendarPipeline) Materialize(ctx context.Context, request CalendarRequest) (CalendarPipelineResult, error) {
	if p.Policy == nil || p.Store == nil || p.DatasetID == "" || p.Generation.IsZero() {
		return CalendarPipelineResult{}, fmt.Errorf("calendar policy, store, dataset and generation are required")
	}
	days, err := p.Policy.TradingDays(request.Start, request.End)
	if err != nil {
		return CalendarPipelineResult{}, err
	}
	start := 0
	if request.Cursor != "" {
		start, err = strconv.Atoi(request.Cursor)
		if err != nil || start < 0 {
			return CalendarPipelineResult{}, fmt.Errorf("invalid calendar cursor")
		}
	}
	if start > len(days) {
		start = len(days)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = len(days)
	}
	end := start + limit
	if end > len(days) {
		end = len(days)
	}
	page := days[start:end]
	if len(page) > 0 {
		if err := p.Store.WriteCalendarDays(ctx, p.DatasetID, p.Generation, page); err != nil {
			return CalendarPipelineResult{}, err
		}
	}
	result := CalendarPipelineResult{Rows: len(page), Complete: end == len(days)}
	if !result.Complete {
		result.NextCursor = strconv.Itoa(end)
	}
	return result, nil
}
