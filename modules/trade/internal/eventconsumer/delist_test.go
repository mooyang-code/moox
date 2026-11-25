package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

type recordingIntake struct {
	events []delistapp.Event
	err    error
}

func (r *recordingIntake) Intake(_ context.Context, event delistapp.Event) (delistapp.IntakeResult, error) {
	r.events = append(r.events, event)
	return delistapp.IntakeResult{}, r.err
}

func delistDelivery(t *testing.T, delistAt, occurredAt time.Time) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subjectID := events.InstrumentDelistSubjectID("binance", events.DelistMarketSpot, "FOOUSDT")
	raw, err := registry.MarshalMessage(events.InstrumentDelistScheduled, &storagepb.InstrumentDelistScheduled{
		Exchange: "binance", Market: events.DelistMarketSpot, ExchangeSymbol: "FOOUSDT", DelistAt: delistAt.UnixMilli(),
		Source: events.DelistSourceManual, FirstSeenAt: occurredAt.UnixMilli(), Scope: events.DelistScopePaperOnly,
	}, events.PublishOptions{EventID: "delist-1", OccurredAt: occurredAt, SpaceID: events.SystemSpaceID, SubjectID: subjectID})
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.InstrumentDelistScheduled, events.SystemSpaceID, subjectID)
	require.NoError(t, err)
	return &jetstream.Delivery{RawData: raw, Subject: subject, RawMessageID: "delist-1", ContentType: events.ContentType}
}

func TestHandleDelistPassesTheEventToIntake(t *testing.T) {
	delistAt := time.Date(2026, 12, 10, 8, 0, 0, 0, time.UTC)
	occurredAt := delistAt.Add(-72 * time.Hour)
	intake := &recordingIntake{}
	result := HandleDelist(context.Background(), delistDelivery(t, delistAt, occurredAt), intake)
	require.Equal(t, jetstream.ACK, result.Decision, "%v", result.Err)
	require.Len(t, intake.events, 1)
	got := intake.events[0]
	require.Equal(t, "delist-1", got.EventID)
	require.True(t, got.DelistAt.Equal(delistAt))
	require.True(t, got.OccurredAt.Equal(occurredAt))
	require.Equal(t, delistapp.Key{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT"}, got.Key)
	require.Equal(t, events.DelistScopePaperOnly, got.Scope)
}

func TestHandleDelistTermsInvalidAndRetriesStoreErrors(t *testing.T) {
	require.Equal(t, jetstream.TERM, HandleDelist(context.Background(), &jetstream.Delivery{RawData: []byte("bad")}, &recordingIntake{}).Decision)

	delivery := delistDelivery(t, time.Now().Add(time.Hour), time.Now())
	invalid := HandleDelist(context.Background(), delivery, &recordingIntake{err: delistapp.ErrInvalidRequest})
	require.Equal(t, jetstream.TERM, invalid.Decision)

	retry := HandleDelist(context.Background(), delivery, &recordingIntake{err: errors.New("database is locked")})
	require.Equal(t, jetstream.RETRY, retry.Decision)
	require.Equal(t, 5*time.Second, retry.Delay)
}
