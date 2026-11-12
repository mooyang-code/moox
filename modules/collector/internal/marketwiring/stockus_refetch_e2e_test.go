package marketwiring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 美股收市后整天重取的端到端用例：假的东方财富服务（按日期区间规则的模型返回，见 usModelUpstream）加假的 Storage，
// 其余都是真实装配（编进二进制的 config/markets/stockus/、行情源目录、标的规则、采集计划生成、补采、云函数处理器）。
// 冬令时、夏令时、半日市各取一天：重取范围是纽约当天零点到次日零点，每只标的 1 个分块，区间规则让 beg 提前一天或带出前后一天的根，
// 整天的 78 根（半日市 42 根）都要取到，多带的根不写。

// usDay 是上游里的一个纽约交易日：收市时刻 16:00，半日市 13:00。
type usDay struct{ date, closeClock string }

// usUniverse 按交易日生成上游有的全部收盘标签（北京时间），时间升序：每个交易日从 09:35 到收市，每 5 分钟一根。
func usUniverse(days ...usDay) []string {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	var labels []string
	for _, day := range days {
		open, err := time.ParseInLocation("2006-01-02 15:04", day.date+" 09:30", newYork)
		if err != nil {
			panic(err)
		}
		closeAt, err := time.ParseInLocation("2006-01-02 15:04", day.date+" "+day.closeClock, newYork)
		if err != nil {
			panic(err)
		}
		for at := open.Add(5 * time.Minute); !at.After(closeAt); at = at.Add(5 * time.Minute) {
			labels = append(labels, at.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04"))
		}
	}
	return labels
}

// usRefetchCase 是一个要整天重取的交易日。
type usRefetchCase struct {
	name string
	// universe 是上游有的全部标签：目标日前后各带一个交易日，beg、end 规则多带的根要有东西可带。
	universe []usDay
	// day 是目标交易日（纽约日期），bars 是它应有的根数。
	day  string
	bars int
	// closeLabel 是收盘那一根的标签（北京时间），closeBar 是它的开始时刻（UTC，Storage 里的行键）。
	closeLabel, closeBar string
	// beg、end 是整天重取发给东方财富的日期区间（日期区间规则：beg 取首根标签减 22:30 的日期，end 取末根标签的日期加一天）。
	beg, end string
	// closeAt 是收市时刻（UTC）；重取那一轮在它之后 15 分钟。
	closeAt time.Time
}

func usRefetchCases() []usRefetchCase {
	return []usRefetchCase{
		{
			name:     "冬令时周三",
			universe: []usDay{{"2026-11-10", "16:00"}, {"2026-11-11", "16:00"}, {"2026-11-12", "16:00"}},
			day:      "2026-11-11", bars: 78,
			closeLabel: "2026-11-12 05:00", closeBar: "2026-11-11T20:55:00Z", beg: "20261111", end: "20261113",
			closeAt: time.Date(2026, 11, 11, 21, 0, 0, 0, time.UTC),
		},
		{
			// 夏令时的首根标签是 21:35，减 22:30 落在前一天：beg 要提前一天才取得到前 11 根。
			name:     "夏令时周四",
			universe: []usDay{{"2026-10-28", "16:00"}, {"2026-10-29", "16:00"}, {"2026-10-30", "16:00"}},
			day:      "2026-10-29", bars: 78,
			closeLabel: "2026-10-30 04:00", closeBar: "2026-10-29T19:55:00Z", beg: "20261028", end: "20261031",
			closeAt: time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC),
		},
		{
			// 感恩节次日 13:00 收市：42 根，末根标签是北京时间次日 02:00；前一天（11-26）全天休市，后一个交易日是周一。
			name:     "半日市",
			universe: []usDay{{"2026-11-25", "16:00"}, {"2026-11-27", "13:00"}, {"2026-11-30", "16:00"}},
			day:      "2026-11-27", bars: 42,
			closeLabel: "2026-11-28 02:00", closeBar: "2026-11-27T17:55:00Z", beg: "20261127", end: "20261129",
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
// serve 按 secid 返回上游内容；now 是可改的当前时间。
type usRefetchHarness struct {
	t           *testing.T
	storage     *hkRowStore
	db          *store.Store
	backfiller  *klinecheck.Backfiller
	invoker     *hkFunctionInvoker
	now         time.Time
	mu          sync.Mutex
	queries     []map[string]string
	serveByCode func(code string) http.HandlerFunc
}

func newUSRefetchHarness(t *testing.T, subjects []string, serveByCode func(code string) http.HandlerFunc) *usRefetchHarness {
	t.Helper()
	t.Setenv("MOOX_SPACE_ID", "stockus")
	h := &usRefetchHarness{t: t, storage: newHKRowStore(), serveByCode: serveByCode}
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	h.db = db

	// 采集计划：真实的生成逻辑，一个数据集，2026-10-20 开盘起生效（早于所有用例的交易日）。
	desired, err := marketfetch.BuildDesiredState(usPlanOptions(), []marketfetch.TaskGroup{usGroup(subjects...)}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return usTime("2026-10-20 00:00:00").Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	record, err := db.FetchPlans().Create(context.Background(), "stockus", string(raw), "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", ""))

	clock := func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	serve := func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		h.mu.Lock()
		h.queries = append(h.queries, map[string]string{"secid": query.Get("secid"), "beg": query.Get("beg"), "end": query.Get("end")})
		h.mu.Unlock()
		code, found := strings.CutPrefix(query.Get("secid"), "105.")
		if !found {
			http.Error(w, "unexpected secid "+query.Get("secid"), http.StatusBadRequest)
			return
		}
		h.serveByCode(code)(w, r)
	}
	handler := NewHandler()
	handler.Now = clock
	handler.NewStorage = func(string) (marketfetch.Storage, error) { return h.storage, nil }
	handler.NewKlinePipeline = func(s marketfetch.Storage, market string, instrument marketdata.InstrumentType, frequency, provider string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
		require.Equal(t, "stockus", market)
		require.Equal(t, marketdata.InstrumentEquity, instrument)
		require.Equal(t, "5m", frequency)
		return stockUSPipeline(t, s, clock(), limits, serve), nil
	}
	h.invoker = &hkFunctionInvoker{t: t, handler: handler}
	checker := &klinecheck.Checker{Plans: db.FetchPlans(), Rows: h.storage, Absences: db.Backfill(), Now: clock, Calendar: markets.TradingBar}
	h.backfiller = &klinecheck.Backfiller{
		Checker: checker, Store: db.Backfill(), Plans: db.FetchPlans(), Invoker: h.invoker, BaseCtx: context.Background(), Now: clock,
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

// requestsFor 返回发给上游的、某个 secid 的请求次数。
func (h *usRefetchHarness) requestsFor(secid string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, query := range h.queries {
		if query["secid"] == secid {
			count++
		}
	}
	return count
}

func (h *usRefetchHarness) lastQuery() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.queries[len(h.queries)-1]
}

// usModelCutoffs 是日期区间规则里不确定的那个切点（end 日 05:00 之后、21:35 之前）的两个极端。
var usModelCutoffs = []struct {
	name      string
	offset    time.Duration
	inclusive bool
}{{"切点 05:00 含", 5 * time.Hour, true}, {"切点 21:30 不含", 21*time.Hour + 30*time.Minute, false}}

// 收市后整天重取，从“到点提交”到“行被覆盖”走一遍（冬令时、夏令时、半日市各一天，切点取两个极端）：
//  1. 库里先有当天整天的根，收盘那根是连续交易时段的值，补采的整天范围只有 1 个分块；
//  2. 收市后 15 分钟那一轮提交强制补采作业，分块经云函数的处理器取数、写入，发给上游的 beg、end 符合日期区间规则；
//  3. 收盘那根被覆盖成含收盘竞价的值，其余各根重写后开高低收量额与原来相同，上游多带的前后日的根没有写进来。
//
// 上游返回的是模型（合成内容），收盘那一根的“最终值”是构造的：只把成交量、成交额、收盘价改成另一组合法的值。
func TestStockUSRefetchAfterCloseOverwritesTheCloseBar(t *testing.T) {
	for _, tc := range usRefetchCases() {
		for _, cutoff := range usModelCutoffs {
			t.Run(tc.name+"/"+cutoff.name, func(t *testing.T) {
				var final bool
				var finalMu sync.Mutex
				line := func(index int, label string) string {
					open := 100 + float64(index%50)
					closePrice, volume := open+0.5, 1000+index
					finalMu.Lock()
					settled := final && label == tc.closeLabel
					finalMu.Unlock()
					if settled {
						closePrice, volume = open+0.8, 600000+index
					}
					// 东方财富的字段顺序是 时间、开、收、高、低、量、额。
					return fmt.Sprintf("%s,%.3f,%.3f,%.3f,%.3f,%d,%d.000", label, open, closePrice, open+1, open-1, volume, volume*100)
				}
				universe := usUniverse(tc.universe...)
				h := newUSRefetchHarness(t, []string{"AAPL.XNAS"}, func(string) http.HandlerFunc {
					return usModelUpstream(t, universe, cutoff.offset, cutoff.inclusive, nil, line)
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

				// 第 2 步：收市后 15 分钟那一轮，上游这时返回的收盘那根已并入收盘竞价的成交。
				h.setNow(tc.closeAt.Add(15*time.Minute + 10*time.Second))
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
				h.runJobs(3)
				status, err = h.backfiller.Status(ctx, refetch.JobID)
				require.NoError(t, err)
				require.Equal(t, store.BackfillSucceeded, status.Job.Status, "%+v", status.Failed)
				require.Equal(t, 1, h.invoker.calls-callsBefore, "每只标的整天 1 次函数调用")
				require.Equal(t, map[string]string{"secid": "105.AAPL", "beg": tc.beg, "end": tc.end}, h.lastQuery(), "发给上游的日期区间")

				// 第 3 步：只有收盘那根的行情值变了；上游多带的前后日的根没有写进来。
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
				open := closeBefore[0]
				require.Equal(t, open+0.8, after[closeKey][3], "收盘价被覆盖成含收盘竞价的值")
				require.Greater(t, after[closeKey][4], 500000.0, "成交量变大")
			})
		}
	}
}

// 整天重取不能被停牌的标的卡住：HALT 当天一根都没有（上游只返回日期规则多带的前后日的根，区间内为空，美股数据源报“没有已收盘的 K 线”），
// 强制作业的这个分块复查两次缺的一样，记成行情源没有、分块成功结束；同一作业里正常的 AAPL 整天照常写入。
func TestStockUSRefetchAfterCloseDoesNotStickOnASuspendedSubject(t *testing.T) {
	for _, tc := range usRefetchCases() {
		for _, cutoff := range usModelCutoffs {
			t.Run(tc.name+"/"+cutoff.name, func(t *testing.T) {
				universe := usUniverse(tc.universe...)
				suspended := func(label time.Time) bool {
					// 目标交易日的全部根：标签落在 [目标日首根, 目标日末根] 之内。
					return !label.Before(usTime(firstLabel(universe, tc.closeLabel, tc.bars)+":00")) && !label.After(usTime(tc.closeLabel+":00"))
				}
				h := newUSRefetchHarness(t, []string{"AAPL.XNAS", "HALT.XNAS"}, func(code string) http.HandlerFunc {
					if code == "HALT" {
						return usModelUpstream(t, universe, cutoff.offset, cutoff.inclusive, suspended, nil)
					}
					return usModelUpstream(t, universe, cutoff.offset, cutoff.inclusive, nil, nil)
				})
				ctx := context.Background()
				h.setNow(tc.closeAt.Add(15*time.Minute + 10*time.Second))
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
				require.Equal(t, 6, h.requestsFor("105.HALT"))
				require.Equal(t, 1, h.requestsFor("105.AAPL"))
				after := h.storage.snapshot()
				require.Len(t, after, tc.bars, "只有 AAPL 的整天写入了，HALT 一根都没有")
				for key := range after {
					require.True(t, strings.HasPrefix(key, "AAPL.XNAS|"), key)
				}
			})
		}
	}
}

// nyt0 返回纽约某天零点（UTC）。
func nyt0(day string) time.Time {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	parsed, err := time.ParseInLocation("2006-01-02", day, newYork)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// firstLabel 返回以 closeLabel 收尾、共 bars 根的那一天的首根标签（北京时间）。
func firstLabel(universe []string, closeLabel string, bars int) string {
	for i, label := range universe {
		if label == closeLabel {
			return universe[i-bars+1]
		}
	}
	panic("上游里没有标签 " + closeLabel)
}
