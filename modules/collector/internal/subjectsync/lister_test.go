package subjectsync

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type fakeSubjectLister struct {
	items []marketdata.Instrument
	err   error
}

func (f fakeSubjectLister) List(context.Context) ([]marketdata.Instrument, error) {
	return f.items, f.err
}

func TestFetchSnapshotUnion(t *testing.T) {
	listers := Listers{
		{Source: "sina", InstrumentType: "equity"}:      fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG", Name: "浦发银行"}}},
		{Source: "eastmoney", InstrumentType: "equity"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG", Name: "浦发"}, {SubjectID: "920000.XBSE"}}},
	}
	items, err := FetchSnapshot(context.Background(), listers, []string{"sina", "eastmoney"}, "equity")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SubjectID != "600000.XSHG" || items[0].Name != "浦发银行" {
		t.Fatalf("items = %+v", items)
	}
}

func TestFetchSnapshotFailsOnAnySourceError(t *testing.T) {
	listers := Listers{{Source: "sina", InstrumentType: "equity"}: fakeSubjectLister{err: errors.New("timeout")}}
	if _, err := FetchSnapshot(context.Background(), listers, []string{"sina"}, "equity"); err == nil {
		t.Fatal("want error")
	}
}

func TestFetchSnapshotEmptyAndUnsupported(t *testing.T) {
	listers := Listers{{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{}}
	if _, err := FetchSnapshot(context.Background(), listers, []string{"binance"}, "spot"); !errors.Is(err, ErrEmptySnapshot) {
		t.Fatalf("err = %v", err)
	}
	if _, err := FetchSnapshot(context.Background(), listers, []string{"okx"}, "spot"); err == nil {
		t.Fatal("unsupported source must fail")
	}
}
