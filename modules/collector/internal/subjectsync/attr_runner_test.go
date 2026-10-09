package subjectsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeAttributeStore struct {
	calls int
	space string
	items []*pb.SubjectAttributes
	err   error
}

func (f *fakeAttributeStore) UpdateSubjectAttributes(_ context.Context, space string, items []*pb.SubjectAttributes) (int, int, error) {
	f.calls++
	f.space, f.items = space, items
	return len(items), 0, f.err
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

func cryptoAttributeRunner(store AttributeStore, lister Lister) *AttributeRunner {
	return &AttributeRunner{
		Store:   store,
		Listers: Listers{{Source: "binance", InstrumentType: "spot"}: lister},
		// 北京时间每天 08:10，与生产配置一致。
		Jobs: []AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "10 8 * * *", Timezone: "Asia/Shanghai"}},
	}
}

func TestAttributeRunnerRunsOnceAtBeijingTimeRegardlessOfHostTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// 定时器每分钟第 45 秒触发；把一整天的触发时刻分别放在 UTC 和 Asia/Shanghai 两种“主机时区”下模拟。
	for _, host := range []*time.Location{time.UTC, shanghai} {
		store := &fakeAttributeStore{}
		runner := cryptoAttributeRunner(store, fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT", BaseAsset: "btc", QuoteAsset: "usdt", Name: "BTC"}}})
		start := time.Date(2026, 10, 9, 0, 0, 45, 0, host)
		var ranAt []time.Time
		for minute := 0; minute < 24*60; minute++ {
			trigger := start.Add(time.Duration(minute) * time.Minute)
			before := store.calls
			if err := runner.RunDue(context.Background(), trigger); err != nil {
				t.Fatal(err)
			}
			if store.calls != before {
				ranAt = append(ranAt, trigger)
			}
		}
		if len(ranAt) != 1 {
			t.Fatalf("主机时区 %s：一天内执行 %d 次（%v），应当只执行一次", host, len(ranAt), ranAt)
		}
		if got := ranAt[0].In(shanghai).Format("15:04:05"); got != "08:10:45" {
			t.Fatalf("主机时区 %s：执行时刻为北京时间 %s，应当是 08:10:45", host, got)
		}
		if store.space != "crypto" || store.items[0].GetAttributes()["base"] != "BTC" || store.items[0].GetAttributes()["quote"] != "USDT" {
			t.Fatalf("属性更新 = %q %+v", store.space, store.items)
		}
	}
}

func TestAttributeRunnerUsesDefaultFetchTimeout(t *testing.T) {
	store := &fakeAttributeStore{}
	runner := cryptoAttributeRunner(store, contextAwareSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}})
	trigger := time.Date(2026, 10, 9, 0, 10, 45, 0, time.UTC)
	if err := runner.RunDue(context.Background(), trigger); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("items = %+v", store.items)
	}
}

func TestAttributeRunnerReportsFailures(t *testing.T) {
	trigger := time.Date(2026, 10, 9, 0, 10, 45, 0, time.UTC)
	listFailure := cryptoAttributeRunner(&fakeAttributeStore{}, fakeSubjectLister{err: errors.New("出口代理不可用")})
	if err := listFailure.RunDue(context.Background(), trigger); err == nil {
		t.Fatal("拉取失败应当返回错误")
	}
	store := &fakeAttributeStore{err: errors.New("storage unavailable")}
	writeFailure := cryptoAttributeRunner(store, fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}})
	if err := writeFailure.RunDue(context.Background(), trigger); err == nil {
		t.Fatal("写入失败应当返回错误")
	}
}
