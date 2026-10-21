package localexec

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	marketserverless "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
)

type nopStorage struct{}

func (nopStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error { return nil }

func backfillEvent(t *testing.T) []byte {
	t.Helper()
	from := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	item := domain.CollectionItem{InstanceID: "backfill|1m|BTC-USDT", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", StartTime: from.Format(time.RFC3339), EndTime: from.Add(10 * time.Minute).Format(time.RFC3339), BarLimit: 10,
		Provider: "binance", SourceID: "spot_http", MarketID: "crypto", InstrumentType: "spot", MarketType: "spot", DataType: "kline", DatasetID: "bars_1m", Frequency: "1m"}
	request := marketfetch.Request{BatchID: "bf|1", BatchKind: domain.BatchKindBackfill, SpaceID: "crypto", MarketID: "crypto", InstrumentType: "spot", DatasetID: "bars_1m", Frequency: "1m",
		Provider: "binance", SourceID: "spot_http", MarketType: "spot", NodeID: "n", FunctionName: "n", RequestID: "bf|1", WriteIDByContent: true, Concurrency: 1,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{{ID: "bars_1m|1m|BTC-USDT", SpaceID: "crypto", InstanceID: item.InstanceID, TaskID: "bars_1m", DatasetID: "bars_1m", Frequency: "1m"}}}
	event, err := marketfetch.MarketFetchEvent(request)
	require.NoError(t, err)
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	return raw
}

func recordingHandler(observed *[]marketfetch.Request, mu *sync.Mutex) func() *marketfetch.Handler {
	return func() *marketfetch.Handler {
		return &marketfetch.Handler{
			NewStorage: func(string, string) (marketfetch.Storage, error) { return nopStorage{}, nil },
			Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
				mu.Lock()
				*observed = append(*observed, request)
				mu.Unlock()
				return &marketfetchpb.MarketFetchBatchCompleted{BatchId: request.BatchID, Status: "succeeded"}, nil
			},
		}
	}
}

// 同一事件经云函数入口与本地入口得到相同的执行请求与响应。
func TestLocalEntryMatchesTheCloudFunctionEntry(t *testing.T) {
	var mu sync.Mutex
	var cloud, local []marketfetch.Request
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_SCF_FUNCTION_NAME", FunctionName("crypto"))
	cloudResponse, err := (&marketserverless.Handler{NewMarketFetch: recordingHandler(&cloud, &mu)}).HandleRequest(context.Background(), backfillEvent(t))
	require.NoError(t, err)

	t.Setenv("MOOX_SPACE_ID", "") // 本地执行端不依赖进程环境
	executor := New("crypto", 2, recordingHandler(&local, &mu))
	rsp, err := executor.Invoke(context.Background(), &pb.InvokeMarketFetchReq{SpaceId: "crypto", Event: backfillEvent(t)})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())

	require.Len(t, cloud, 1)
	require.Len(t, local, 1)
	require.Equal(t, cloud[0], local[0])
	cloudRaw, _ := json.Marshal(cloudResponse)
	var want, got map[string]any
	require.NoError(t, json.Unmarshal(cloudRaw, &want))
	require.NoError(t, json.Unmarshal(rsp.GetResponse(), &got))
	delete(want, "timestamp")
	delete(got, "timestamp")
	require.Equal(t, want, got)

	// 本地入口不接受定时器事件，也不接受别的空间。
	timer, _ := json.Marshal(map[string]any{"Type": "Timer", "TriggerName": "moox-market-fetch-timer", "Time": "2026-10-20T08:00:00Z", "Message": "market_fetch_timer_v1"})
	rsp, err = executor.Invoke(context.Background(), &pb.InvokeMarketFetchReq{SpaceId: "crypto", Event: timer})
	require.NoError(t, err)
	require.Contains(t, string(rsp.GetResponse()), "timer events are only accepted")
	rsp, err = executor.Invoke(context.Background(), &pb.InvokeMarketFetchReq{SpaceId: "stockcn", Event: backfillEvent(t)})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestLocalExecutorLimitsConcurrency(t *testing.T) {
	release := make(chan struct{})
	var running, peak atomic.Int32
	executor := New("crypto", 2, func() *marketfetch.Handler {
		return &marketfetch.Handler{
			NewStorage: func(string, string) (marketfetch.Storage, error) { return nopStorage{}, nil },
			Execute: func(context.Context, marketfetch.Request, marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
				current := running.Add(1)
				for {
					old := peak.Load()
					if current <= old || peak.CompareAndSwap(old, current) {
						break
					}
				}
				<-release
				running.Add(-1)
				return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
			},
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = executor.Invoke(context.Background(), &pb.InvokeMarketFetchReq{SpaceId: "crypto", Event: backfillEvent(t)})
		}()
	}
	require.Eventually(t, func() bool { return executor.InFlight() == 2 }, time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 2, peak.Load())
	close(release)
	wg.Wait()
	require.EqualValues(t, 2, peak.Load())

	// 排队等待超过调用方期限时直接返回繁忙。
	blocked := New("crypto", 1, func() *marketfetch.Handler { return nil })
	blocked.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rsp, err := blocked.Invoke(ctx, &pb.InvokeMarketFetchReq{SpaceId: "crypto", Event: backfillEvent(t)})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}
