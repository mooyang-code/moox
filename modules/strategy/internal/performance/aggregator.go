package performance

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Input struct {
	BindingID        string
	Source           string
	PointTime        time.Time
	DataRevision     string
	NAV              string
	CumulativeReturn string
	Drawdown         string
	GrossExposure    string
	NetExposure      string
	Turnover         string
	Fees             string
}

type PointStore interface {
	WritePerformancePoint(context.Context, domain.PerformancePoint) error
}

type Aggregator struct{ Store PointStore }

func (a *Aggregator) WritePoint(ctx context.Context, in Input) error {
	if a == nil || a.Store == nil {
		return errors.New("performance database is unavailable")
	}
	point := domain.PerformancePoint{BindingID: in.BindingID, Source: in.Source, PointTime: in.PointTime.UTC(), NAV: in.NAV, CumulativeReturn: in.CumulativeReturn, Drawdown: in.Drawdown, GrossExposure: in.GrossExposure, NetExposure: in.NetExposure, Turnover: in.Turnover, Fees: in.Fees, DataRevision: in.DataRevision, CalculatedAt: time.Now().UTC()}
	if err := point.Validate(); err != nil {
		return err
	}
	return a.Store.WritePerformancePoint(ctx, point)
}
