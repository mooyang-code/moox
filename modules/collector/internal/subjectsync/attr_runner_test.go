package subjectsync

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeAttributeStore struct {
	space string
	items []*pb.SubjectAttributes
}

func (f *fakeAttributeStore) UpdateSubjectAttributes(_ context.Context, space string, items []*pb.SubjectAttributes) (int, int, error) {
	f.space, f.items = space, items
	return len(items), 0, nil
}

type contextAwareSubjectLister struct {
	items []marketdata.Instrument
}

func (l contextAwareSubjectLister) List(ctx context.Context) ([]marketdata.Instrument, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.items, nil
}

func TestAttributeRunnerWaitsForFirstCron(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	store := &fakeAttributeStore{}
	runner := &AttributeRunner{
		Store:   store,
		Listers: Listers{{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Name: "BTC"}}}},
		Jobs:    []AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, Cron: "0 * * * *", Timezone: "UTC"}},
		Now:     func() time.Time { return now },
	}
	runner.RunOnce(context.Background())
	if len(store.items) != 0 {
		t.Fatal("attribute job ran before its first scheduled time")
	}
	runner.Now = func() time.Time { return now.Add(time.Hour) }
	runner.RunOnce(context.Background())
	if store.space != "crypto" || len(store.items) != 1 || store.items[0].GetAttributes()["base"] != "BTC" || store.items[0].GetAttributes()["quote"] != "USDT" {
		t.Fatalf("attribute update = %q %+v", store.space, store.items)
	}
}

func TestAttributeRunnerUsesDefaultFetchTimeout(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	store := &fakeAttributeStore{}
	runner := &AttributeRunner{
		Store:   store,
		Listers: Listers{{Source: "binance", InstrumentType: "spot"}: contextAwareSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}}},
		Jobs:    []AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, Cron: "0 * * * *", Timezone: "UTC"}},
		Now:     func() time.Time { return now },
	}
	runner.RunOnce(context.Background())
	runner.Now = func() time.Time { return now.Add(time.Hour) }
	runner.RunOnce(context.Background())
	if len(store.items) != 1 {
		t.Fatalf("items = %+v", store.items)
	}
}
