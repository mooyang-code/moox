package marketwiring

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 美股收市后整天重取的端到端用例：假的 CNBC 服务（按合成的 1M 返回，见 usCNBCServer）加假的 Storage，
// 其余都是真实装配（编进二进制的 config/markets/stockus/、行情源目录、标的规则、采集计划生成、补采、云函数处理器）。
// 冬令时、夏令时、半日市各取一天：重取范围是纽约当天零点到次日零点，每只标的 1 个分块、1 次请求，
// 整天的 78 根（半日市 42 根）都要取到；上游的盘前、盘后与前后交易日的 1M 不写。

// usRefetchCase 是一个要整天重取的交易日。
type usRefetchCase struct {
	name string
	// days 是上游有 1M 的交易日：目标日前后各带一个交易日。
	days []usDay
	// day 是目标交易日（纽约日期），bars 是它应有的根数。
	day  string
	bars int
	// closeBar 是收盘那一根的开始时刻（UTC，Storage 里的行键），lastMinute 是它最后一根 1M 的 tradeTime（纽约时间）。
	closeBar, lastMinute string
	// closeAt 是收市时刻（UTC）；重取那一轮在它之后 20 分钟。
	closeAt time.Time
}

func usRefetchCases() []usRefetchCase {
	return []usRefetchCase{
		{
			name: "冬令时周三", days: []usDay{{"2026-11-10", "16:00"}, {"2026-11-11", "16:00"}, {"2026-11-12", "16:00"}},
			day: "2026-11-11", bars: 78, closeBar: "2026-11-11T20:55:00Z", lastMinute: "20261111155900",
			closeAt: time.Date(2026, 11, 11, 21, 0, 0, 0, time.UTC),
		},
		{
			name: "夏令时周四", days: []usDay{{"2026-10-28", "16:00"}, {"2026-10-29", "16:00"}, {"2026-10-30", "16:00"}},
			day: "2026-10-29", bars: 78, closeBar: "2026-10-29T19:55:00Z", lastMinute: "20261029155900",
			closeAt: time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC),
		},
		{
			// 感恩节次日 13:00 收市：42 根，末根是 12:55；前一天（11-26）全天休市，后一个交易日是周一。
			name: "半日市", days: []usDay{{"2026-11-25", "16:00"}, {"2026-11-27", "13:00"}, {"2026-11-30", "16:00"}},
			day: "2026-11-27", bars: 42, closeBar: "2026-11-27T17:55:00Z", lastMinute: "20261127125900",
			closeAt: time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC),
		},
	}
}

// usBackfillFunctions 给补采分块一个函数名，调用交给真实的处理器。
type usBackfillFunctions struct{}

func (usBackfillFunctions) EnsureBackfillFunction(context.Context, string) (string, error) {
	return "moox-fetch-plan-backfill-stockus-equity-nanjing", nil
}

// usRefetchHarness 装好一个美股空间的补采环境：采集计划（真实生成逻辑）、假 Storage、真实的处理器和补采器。
// minutes 按代码给出上游的 1M；now 是可改的当前时间。
type usRefetchHarness struct {
	t          *testing.T
	storage    *hkRowStore
	db         *store.Store
	backfiller *klinecheck.Backfiller
	invoker    *hkFunctionInvoker
	server     *usCNBCServer
	now        time.Time
	mu         sync.Mutex
}

func newUSRefetchHarness(t *testing.T, subjects []string, minutes func(code string) []usMinute) *usRefetchHarness {
	t.Helper()
	t.Setenv("MOOX_SPACE_ID", "stockus")
	h := &usRefetchHarness{t: t, storage: newHKRowStore(), server: newUSCNBCServer(t, minutes)}
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	h.db = db

	// 采集计划：真实的生成逻辑，一个数据集，2026-10-20 开盘起生效（早于所有用例的交易日）。
	desired, err := fetchplan.BuildDesiredState(usPlanOptions(), []fetchplan.TaskGroup{usGroup(subjects...)}, nil)
	require.NoError(t, err)
	content, _, err := fetchplan.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return usTime("2026-10-20 00:00:00").Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	record, err := db.FetchPlans().Create(context.Background(), "stockus", string(raw), "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", "", firstPlanTimeline("stockus", content)...))

	clock := func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	handler := NewHandler()
	handler.Now = clock
	handler.NewStorage = func() (marketfetch.Storage, error) { return h.storage, nil }
	handler.NewKlinePipeline = func(s marketfetch.Storage, market string, instrument marketdata.InstrumentType, frequency, provider string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
		require.Equal(t, "stockus", market)
		require.Equal(t, marketdata.InstrumentEquity, instrument)
		require.Equal(t, "5m", frequency)
		return stockUSPipeline(t, s, clock(), limits, h.server), nil
	}
	h.invoker = &hkFunctionInvoker{t: t, handler: handler}
	checker := &klinecheck.Checker{Timeline: db.SeriesTimeline(), Rows: h.storage, Absences: db.Backfill(), Now: clock, Calendar: markets.TradingBar}
	h.backfiller = &klinecheck.Backfiller{
		Checker: checker, Store: db.Backfill(), Routes: contentRoutes{content}, Invoker: h.invoker, BaseCtx: context.Background(), Now: clock,
		Functions:    func(string) klinecheck.FunctionProvider { return usBackfillFunctions{} },
		CloseRefetch: markets.CloseRefetchDay,
	}
	return h
}

func (h *usRefetchHarness) setNow(at time.Time) { h.mu.Lock(); h.now = at; h.mu.Unlock() }

// runJobs 对账若干轮：每轮认领并执行待执行的分块；强制作业的分块第一次复查仍缺时会退回再跑一次。
func (h *usRefetchHarness) runJobs(rounds int) {
	for i := 0; i < rounds; i++ {
		require.NoError(h.t, h.backfiller.Tick(context.Background(), "stockus"))
		h.backfiller.Wait()
	}
}

// lastPath 返回最后一次发给上游的请求路径。
func (h *usRefetchHarness) lastPath() string {
	paths := h.server.requested()
	return paths[len(paths)-1]
}

// 收市后整天重取，从“到点提交”到“行被覆盖”走一遍（冬令时、夏令时、半日市各一天）：
//  1. 库里先有当天整天的根，补采的整天范围只有 1 个分块；
//  2. 收市后 20 分钟那一轮提交强制补采作业，分块经云函数的处理器取数、写入，发给 CNBC 的是当天交易时段的 1M；
//  3. 收盘那根被覆盖成上游修正后的值，其余各根重写后开高低收量额与原来相同，前后交易日与盘前盘后的 1M 没有写进来。
//
// 上游返回的是合成的 1M，收盘那根的“修正”是构造的：只把它最后一根 1M 的收盘价、成交量改成另一组合法的值。
func TestStockUSRefetchAfterCloseOverwritesTheCloseBar(t *testing.T) {
	for _, tc := range usRefetchCases() {
		t.Run(tc.name, func(t *testing.T) {
			var final bool
			var finalMu sync.Mutex
			base := usSessionMinutes(tc.days...)
			h := newUSRefetchHarness(t, []string{"AAPL.XNAS"}, func(string) []usMinute {
				finalMu.Lock()
				defer finalMu.Unlock()
				if !final {
					return base
				}
				revised := append([]usMinute(nil), base...)
				for i := range revised {
					if revised[i].TradeTime == tc.lastMinute {
						var open float64
						_, err := fmt.Sscanf(revised[i].Open, "%f", &open)
						require.NoError(t, err)
						revised[i].Close = fmt.Sprintf("%.4f", open+0.8)
						revised[i].Volume = 600000
					}
				}
				return revised
			})
			ctx := context.Background()
			dayStart := nyt0(tc.day)
			dayEnd := dayStart.AddDate(0, 0, 1)

			// 第 1 步：库里是空的，按缺口补采把当天整天写进去。还没到重取的时刻，不提交重取。
			h.setNow(tc.closeAt.Add(2 * time.Minute))
			require.NoError(t, h.backfiller.RefetchAfterClose(ctx, "stockus"))
			jobID, chunks, err := h.backfiller.Submit(ctx, klinecheck.JobRequest{SpaceID: "stockus", From: dayStart, To: dayEnd}, store.BackfillSourceManual)
			require.NoError(t, err)
			require.Equal(t, 1, chunks, "没有午休，每只标的整天 1 个分块")
			h.runJobs(3)
			status, err := h.backfiller.Status(ctx, jobID)
			require.NoError(t, err)
			require.Equal(t, store.BackfillSucceeded, status.Job.Status, "%+v", status.Failed)
			before := h.storage.snapshot()
			require.Len(t, before, tc.bars)
			closeKey := hkRowKey("AAPL.XNAS", tc.closeBar)
			require.Contains(t, before, closeKey)
			closeBefore := before[closeKey]

			// 第 2 步：收市后 20 分钟那一轮，上游这时返回的收盘那根已经修正。
			h.setNow(tc.closeAt.Add(20*time.Minute + 10*time.Second))
			finalMu.Lock()
			final = true
			finalMu.Unlock()
			require.NoError(t, h.backfiller.RefetchAfterClose(ctx, "stockus"))
			jobs, _, err := h.db.Backfill().ListJobs(ctx, "stockus", store.BackfillJobFilter{}, 1, 10)
			require.NoError(t, err)
			require.Len(t, jobs, 2)
			refetch := jobs[0]
			require.Equal(t, store.BackfillKindForce, refetch.Kind)
			callsBefore := h.invoker.calls
			requestsBefore := len(h.server.requested())
			h.runJobs(3)
			status, err = h.backfiller.Status(ctx, refetch.JobID)
			require.NoError(t, err)
			require.Equal(t, store.BackfillSucceeded, status.Job.Status, "%+v", status.Failed)
			require.Equal(t, 1, h.invoker.calls-callsBefore, "每只标的整天 1 次函数调用")
			require.Equal(t, 1, len(h.server.requested())-requestsBefore, "整天 1 次请求")
			// 重取范围是纽约当天零点到次日零点，分块收窄到当天的交易时段（09:30 到收盘那根结束）；接口两端都含，终点写收盘那根的最后一分钟。
			require.Equal(t, usBarsPrefix+"AAPL/1M/"+tc.day[:4]+tc.day[5:7]+tc.day[8:]+"093000/"+tc.lastMinute+"/unadjusted/EST5EDT.json", h.lastPath(), "发给 CNBC 的区间")

			// 第 3 步：只有收盘那根的行情值变了；前后交易日与盘前盘后的 1M 没有写进来。
			after := h.storage.snapshot()
			require.Len(t, after, tc.bars)
			var changed []string
			for key, value := range after {
				if value != before[key] {
					changed = append(changed, key)
				}
				require.Equal(t, 2, h.storage.writes[key], "%s：补采一次、重取一次", key)
			}
			sort.Strings(changed)
			require.Equal(t, []string{closeKey}, changed)
			require.InDelta(t, closeBefore[3]+0.3, after[closeKey][3], 1e-9, "收盘价被覆盖成修正后的值")
			require.Greater(t, after[closeKey][4], 500000.0, "成交量变大")
		})
	}
}

// 整天重取不能被停牌的标的卡住：HALT 当天一根都没有（上游只有前后交易日的 1M，区间内为空，CNBC 数据源报“没有已收盘的 K 线”），
// 强制作业的这个分块复查两次缺的一样，记成行情源没有、分块成功结束；同一作业里正常的 AAPL 整天照常写入。
func TestStockUSRefetchAfterCloseDoesNotStickOnASuspendedSubject(t *testing.T) {
	for _, tc := range usRefetchCases() {
		t.Run(tc.name, func(t *testing.T) {
			all := usSessionMinutes(tc.days...)
			var others []usDay
			for _, day := range tc.days {
				if day.date != tc.day {
					others = append(others, day)
				}
			}
			halted := usSessionMinutes(others...)
			h := newUSRefetchHarness(t, []string{"AAPL.XNAS", "HALT.XNAS"}, func(code string) []usMinute {
				if code == "HALT" {
					return halted
				}
				return all
			})
			ctx := context.Background()
			h.setNow(tc.closeAt.Add(20*time.Minute + 10*time.Second))
			require.NoError(t, h.backfiller.RefetchAfterClose(ctx, "stockus"))
			jobs, _, err := h.db.Backfill().ListJobs(ctx, "stockus", store.BackfillJobFilter{}, 1, 10)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.Equal(t, store.BackfillKindForce, jobs[0].Kind)
			h.runJobs(6)
			status, err := h.backfiller.Status(ctx, jobs[0].JobID)
			require.NoError(t, err)
			require.Equal(t, store.BackfillSucceeded, status.Job.Status, "%+v", status.Failed)
			require.Empty(t, status.Failed)

			// 停牌的分块执行两次：第一次复查仍缺，记下缺的退回再跑；第二次缺的完全一样，才认定是行情源没有。
			// 每次执行里流水线对“没有已收盘的 K 线”最多请求 3 次，所以是 2 × 3；去掉这一步复查（第一次就记成没有）时只有 3。
			// 正常的 AAPL 一次就取全。
			require.Equal(t, 6, h.server.requestsFor("HALT"))
			require.Equal(t, 1, h.server.requestsFor("AAPL"))
			after := h.storage.snapshot()
			require.Len(t, after, tc.bars, "只有 AAPL 的整天写入了，HALT 一根都没有")
			for key := range after {
				require.True(t, strings.HasPrefix(key, "AAPL.XNAS|"), key)
			}
		})
	}
}

// nyt0 返回纽约某天零点（UTC）。
func nyt0(day string) time.Time {
	return nyTime(day + " 00:00")
}
