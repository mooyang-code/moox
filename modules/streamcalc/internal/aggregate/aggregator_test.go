package aggregate

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/marketpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAggregatorClosesFiveMinuteWindowAndIsIdempotent(t *testing.T) {
	a, err := New("1m", "5m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		start := base.Add(time.Duration(i) * time.Minute)
		result, err := a.Apply(fmt.Sprintf("event-%d", i), "crypto", &marketpb.KlineClosed{
			Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(time.Minute)),
			Open: float64(100 + i), High: float64(101 + i), Low: float64(99 + i), Close: float64(100 + i), Volume: 1, QuoteVolume: 100, TradeCount: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 && result.Bar.Closed {
			t.Fatalf("bar closed before target end at %d", i)
		}
		if i == 4 && !result.Bar.Closed {
			t.Fatal("bar did not close at target end")
		}
	}
	duplicate, err := a.Apply("event-4", "crypto", &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(base.Add(4 * time.Minute)), WindowEnd: timestamppb.New(base.Add(5 * time.Minute)), Open: 104, High: 105, Low: 103, Close: 104, Volume: 1, QuoteVolume: 100, TradeCount: 2})
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate result=%+v err=%v", duplicate, err)
	}
}

func TestAggregatorSnapshotRestoresDeduplicationAndClosedWindow(t *testing.T) {
	a, err := New("1m", "2m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		start := base.Add(time.Duration(i) * time.Minute)
		_, err := a.Apply(fmt.Sprintf("event-%d", i), "crypto", &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(time.Minute)), Open: 1, High: 2, Low: 1, Close: 2})
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := New("1m", "2m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(a.Export()); err != nil {
		t.Fatal(err)
	}
	result, err := b.Apply("event-1", "crypto", &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(base.Add(time.Minute)), WindowEnd: timestamppb.New(base.Add(2 * time.Minute)), Open: 1, High: 2, Low: 1, Close: 2})
	if err != nil || !result.Duplicate || !result.Bar.Closed {
		t.Fatalf("restored result=%+v err=%v", result, err)
	}
}

func TestAggregatorUsesEventTimeForOpenAndCloseWhenInputsArriveOutOfOrder(t *testing.T) {
	a, err := New("1m", "2m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id string
		at time.Duration
		op float64
		cl float64
	}{{"late", time.Minute, 20, 21}, {"first", 0, 10, 11}} {
		result, err := a.Apply(item.id, "crypto", &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(base.Add(item.at)), WindowEnd: timestamppb.New(base.Add(item.at + time.Minute)), Open: item.op, High: item.cl, Low: item.op, Close: item.cl})
		if err != nil {
			t.Fatal(err)
		}
		if item.id == "first" && (result.Bar.Open != 10 || result.Bar.Close != 21) {
			t.Fatalf("event-time OHLC = %+v", result.Bar)
		}
	}
}
