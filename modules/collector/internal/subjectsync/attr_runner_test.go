package subjectsync

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeAttributeStore struct {
	space string
	items []*pb.SubjectAttributes
	calls int
}

func (f *fakeAttributeStore) UpdateSubjectAttributes(_ context.Context, space string, items []*pb.SubjectAttributes) (int, int, error) {
	f.space, f.items = space, items
	f.calls++
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

func TestAttributeRunnerUsesTimezoneAndStatelessMinuteWindow(t *testing.T) {
	if os.Getenv("MOOX_SUBJECT_TIMEZONE_CHILD") != "1" {
		for _, hostZone := range []string{"UTC", "Asia/Shanghai"} {
			t.Run(hostZone, func(t *testing.T) {
				command := exec.Command(os.Args[0], "-test.run=^TestAttributeRunnerUsesTimezoneAndStatelessMinuteWindow$", "-test.count=1")
				command.Env = append(os.Environ(), "TZ="+hostZone, "MOOX_SUBJECT_TIMEZONE_CHILD=1")
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("timezone %s: %v\n%s", hostZone, err, output)
				}
			})
		}
		return
	}
	if time.Local.String() != os.Getenv("TZ") {
		t.Fatalf("host timezone=%s", time.Local)
	}
	now := time.Date(2026, 10, 9, 0, 9, 45, 0, time.UTC)
	store := &fakeAttributeStore{}
	runner := &AttributeRunner{
		Store:   store,
		Listers: Listers{{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Name: "BTC"}}}},
		Jobs:    []AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, Cron: "10 8 * * *", Timezone: "Asia/Shanghai"}},
		Now:     func() time.Time { return now.In(time.Local) },
	}
	for i := 0; i < 3; i++ {
		if err := runner.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if i == 0 && store.calls != 0 {
			t.Fatal("ran before schedule")
		}
		if i == 1 && store.calls != 1 {
			t.Fatal("missed Beijing 08:10:45")
		}
		now = now.Add(time.Minute)
	}
	if store.calls != 1 || store.space != "crypto" || store.items[0].GetAttributes()["base"] != "BTC" || store.items[0].GetAttributes()["quote"] != "USDT" {
		t.Fatalf("calls=%d space=%s items=%+v", store.calls, store.space, store.items)
	}
}

func TestAttributeRunnerFirstTriggerAndWindowBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name           string
		minute, second int
		want           int
	}{
		{"before", 9, 59, 0}, {"inclusive right", 10, 0, 1},
		{"first trigger", 10, 45, 1}, {"exclusive left", 11, 0, 0},
		{"next trigger", 11, 45, 0}, {"no downtime catchup", 20, 45, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeAttributeStore{}
			runner := &AttributeRunner{
				Store:   store,
				Listers: Listers{{Source: "binance", InstrumentType: "spot"}: contextAwareSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}}},
				Jobs:    []AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, Cron: "10 8 * * *", Timezone: "Asia/Shanghai"}},
				Now:     func() time.Time { return time.Date(2026, 10, 9, 0, tt.minute, tt.second, 0, time.UTC) },
			}
			if err := runner.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.calls != tt.want {
				t.Fatalf("calls=%d want=%d", store.calls, tt.want)
			}
		})
	}
}

func TestAttributeRunnerRejectsImpossibleSchedule(t *testing.T) {
	store := &fakeAttributeStore{}
	runner := &AttributeRunner{Store: store, Jobs: []AttributeJob{{SpaceID: "crypto", Cron: "0 0 31 2 *", Timezone: "UTC"}}}
	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("impossible schedule was treated as due")
	}
	if store.calls != 0 {
		t.Fatal("impossible schedule performed an update")
	}
}
