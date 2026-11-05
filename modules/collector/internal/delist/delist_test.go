package delist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

var testNow = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

func readSample(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, target))
}

// 币安现货：官方样例（delistTime 是毫秒时间戳）。只保留 USDT 计价、下架时间在未来的条目。
func TestParseBinanceSpotScheduleOfficialSample(t *testing.T) {
	var schedule binanceDelistSchedule
	readSample(t, "binance_spot_delist_schedule.json", &schedule)

	before := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	notices := parseBinanceSpotSchedule(schedule, before)
	require.Len(t, notices, 2, "BNBBUSD 不是 USDT 计价")
	require.Equal(t, Notice{Exchange: "binance", Market: events.DelistMarketSpot, Symbol: "ADAUSDT", DelistAt: time.UnixMilli(1686161202000).UTC(), Source: events.DelistSourceBinanceSpotSchedule}, notices[0])
	require.Equal(t, "BNBUSDT", notices[1].Symbol)

	require.Empty(t, parseBinanceSpotSchedule(schedule, testNow), "下架时间已过的条目忽略")
}

// 币安 U 本位合约：永续合约的交割时间正常是占位的远期值（官方样例 4133404800000），出现近期的真实时间才是下架。
func TestParseBinanceSwapExchangeInfoOnlyPerpetualUSDTWithRealDeliveryDate(t *testing.T) {
	var info binanceSwapExchangeInfo
	readSample(t, "binance_fapi_exchange_info.json", &info)

	notices := parseBinanceSwapExchangeInfo(info, testNow)
	require.Equal(t, []Notice{{Exchange: "binance", Market: events.DelistMarketSwap, Symbol: "FOOUSDT", DelistAt: time.UnixMilli(1793865600000).UTC(), Source: events.DelistSourceBinanceSwapExchangeInfo}}, notices,
		"占位远期值、季度合约、非 USDT 计价、已经过去的交割时间都不算")
}

// OKX 永续：expTime 为空是正常的永续合约；有值才是计划下架。只要 USDT 本位。
func TestParseOKXInstrumentsOnlyUSDTSwapWithExpTime(t *testing.T) {
	var info okxInstruments
	readSample(t, "okx_instruments_swap.json", &info)
	notices := parseOKXInstruments(info, testNow)
	require.Equal(t, []Notice{{Exchange: "okx", Market: events.DelistMarketSwap, Symbol: "FOO-USDT-SWAP", DelistAt: time.UnixMilli(1793865600000).UTC(), Source: events.DelistSourceOKXSwapInstruments}}, notices)
}

type fakeGetter struct {
	body    any
	err     error
	headers http.Header
	domain  string
	path    string
	query   url.Values
}

func (f *fakeGetter) GetDirectWithHeaders(_ context.Context, domain, path string, query url.Values, headers http.Header, result interface{}) error {
	f.domain, f.path, f.query, f.headers = domain, path, query, headers
	return f.fill(result)
}

func (f *fakeGetter) Get(_ context.Context, domain, path string, query url.Values, result interface{}) error {
	f.domain, f.path, f.query = domain, path, query
	return f.fill(result)
}

func (f *fakeGetter) fill(result interface{}) error {
	if f.err != nil {
		return f.err
	}
	raw, _ := json.Marshal(f.body)
	return json.Unmarshal(raw, result)
}

type fakeKeys struct {
	key string
	err error
}

func (f fakeKeys) ReadonlyKey(context.Context) (string, error) { return f.key, f.err }

func TestBinanceSpotSendsOnlyTheAPIKeyHeaderWithoutSignature(t *testing.T) {
	var schedule binanceDelistSchedule
	readSample(t, "binance_spot_delist_schedule.json", &schedule)
	getter := &fakeGetter{body: schedule}
	source := &BinanceSpot{Client: getter, Keys: fakeKeys{key: "readonly-api-key"}, Now: func() time.Time { return time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC) }}
	notices, err := source.Fetch(context.Background())
	require.NoError(t, err)
	require.Len(t, notices, 2)
	require.Equal(t, "api.binance.com", getter.domain)
	require.Equal(t, "/sapi/v1/spot/delist-schedule", getter.path)
	require.Equal(t, "readonly-api-key", getter.headers.Get("X-MBX-APIKEY"))
	require.Empty(t, getter.query.Get("signature"), "行情数据类接口不签名")

	missing := &BinanceSpot{Client: getter, Keys: fakeKeys{err: ErrReadonlyKeyMissing}}
	_, err = missing.Fetch(context.Background())
	require.ErrorIs(t, err, ErrReadonlyKeyMissing)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newRegistrar(t *testing.T, db *store.Store) (*Registrar, *int) {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	wakes := 0
	return &Registrar{Repo: db.DelistNotices(), Registry: registry, Wake: func() { wakes++ }, Now: func() time.Time { return testNow }}, &wakes
}

func TestRegisterPublishesEachNoticeOnlyOnce(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	registrar, wakes := newRegistrar(t, db)
	notice := Notice{Exchange: "binance", Market: events.DelistMarketSpot, Symbol: "FOOUSDT", DelistAt: time.UnixMilli(1793865600000).UTC(), Source: events.DelistSourceBinanceSpotSchedule}

	created, eventID, err := registrar.Register(ctx, notice)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, events.InstrumentDelistEventID("binance", events.DelistMarketSpot, "FOOUSDT", events.DelistScopeAll, notice.DelistAt), eventID)
	require.Equal(t, 1, *wakes)

	pending, err := db.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	registry, _ := events.DefaultRegistry()
	message, err := registry.UnmarshalMessage(pending[0].Payload)
	require.NoError(t, err)
	require.Equal(t, events.DelistSystemSpaceID, message.GetSpaceId())
	require.Equal(t, "binance:SPOT:FOOUSDT", message.GetSubjectId())
	require.Equal(t, eventID, message.GetEventId())
	event, err := registry.ValidateMessage(message)
	require.NoError(t, err)
	require.Equal(t, events.InstrumentDelistScheduled.Name(), event.Name())
	var payload storagepb.InstrumentDelistScheduled
	require.NoError(t, proto.Unmarshal(message.GetPayload(), &payload))
	require.Equal(t, events.DelistScopeAll, payload.GetScope())
	require.Equal(t, notice.DelistAt, payload.GetDelistAt().AsTime())

	// 同一条（来源换了也一样）再次被报告：只更新最后报告时间，不再发布。
	again := notice
	again.Source = events.DelistSourceManual
	created, _, err = registrar.Register(ctx, again)
	require.NoError(t, err)
	require.False(t, created)
	pending, err = db.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, 1, *wakes)

	// 下架时间变了就是新条目，发布新事件。
	changed := notice
	changed.DelistAt = notice.DelistAt.Add(24 * time.Hour)
	created, changedID, err := registrar.Register(ctx, changed)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, eventID, changedID)

	// 仅模拟盘的演练登记不会挡住同标的同时间的真实下架。
	paper := notice
	paper.Scope = events.DelistScopePaperOnly
	created, paperID, err := registrar.Register(ctx, paper)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, eventID, paperID)
}

func TestRegisterValidatesNotice(t *testing.T) {
	registrar, _ := newRegistrar(t, openStore(t))
	valid := Notice{Exchange: "binance", Market: events.DelistMarketSwap, Symbol: "FOOUSDT", DelistAt: testNow.Add(time.Hour), Source: events.DelistSourceManual}
	for name, mutate := range map[string]func(*Notice){
		"大写交易所":  func(n *Notice) { n.Exchange = "BINANCE" },
		"市场不对":   func(n *Notice) { n.Market = "FUTURES" },
		"代码为空":   func(n *Notice) { n.Symbol = "" },
		"代码带冒号":  func(n *Notice) { n.Symbol = "A:B" },
		"没有下架时间": func(n *Notice) { n.DelistAt = time.Time{} },
		"范围不对":   func(n *Notice) { n.Scope = "SOME" },
	} {
		broken := valid
		mutate(&broken)
		_, _, err := registrar.Register(context.Background(), broken)
		require.Error(t, err, name)
	}
	// 手工登记可以补录过去的下架时间。
	past := valid
	past.DelistAt = testNow.Add(-48 * time.Hour)
	created, _, err := registrar.Register(context.Background(), past)
	require.NoError(t, err)
	require.True(t, created)
}

type scriptedSource struct {
	name    string
	results []sourceResult
	calls   int
}

type sourceResult struct {
	notices []Notice
	err     error
}

func (s *scriptedSource) Name() string { return s.name }
func (s *scriptedSource) Fetch(context.Context) ([]Notice, error) {
	result := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return result.notices, result.err
}

func TestDetectorPublishesOncePerNoticeAndRaisesAttention(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	registrar, _ := newRegistrar(t, db)
	attention := NewAttention(prometheus.NewRegistry())
	notice := Notice{Exchange: "okx", Market: events.DelistMarketSwap, Symbol: "FOO-USDT-SWAP", DelistAt: testNow.Add(72 * time.Hour), Source: events.DelistSourceOKXSwapInstruments}
	flaky := &scriptedSource{name: "OKX", results: []sourceResult{{notices: []Notice{notice}}, {err: errors.New("超时")}, {err: errors.New("超时")}, {err: errors.New("超时")}, {notices: []Notice{notice}}}}
	keyless := &scriptedSource{name: "币安现货", results: []sourceResult{{err: ErrReadonlyKeyMissing}, {notices: nil}}}
	detector := &Detector{Sources: []Source{flaky, keyless}, Registrar: registrar, Attention: attention}

	detector.RunOnce(ctx) // OKX 发现一条；币安现货缺 Key
	pending, err := db.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, 1.0, testutil.ToFloat64(attention.gauge.WithLabelValues(AttentionReadonlyKeyMissing, "币安现货：没有读到只读 Key（exchange-binance-production-readonly），用 setup apply 写入")))
	require.Equal(t, 0.0, testutil.ToFloat64(attention.gauge.WithLabelValues(AttentionSourceFailed, "")))

	detector.RunOnce(ctx) // 币安现货补上 Key 后恢复；OKX 第 1 次失败
	detector.RunOnce(ctx) // 第 2 次失败
	require.Equal(t, 0.0, testutil.ToFloat64(attention.gauge.WithLabelValues(AttentionReadonlyKeyMissing, "")))
	require.Equal(t, 0.0, testutil.ToFloat64(attention.gauge.WithLabelValues(AttentionSourceFailed, "")), "连续失败不到 3 次不告警")
	detector.RunOnce(ctx) // 第 3 次失败
	require.Equal(t, 1.0, testutil.ToFloat64(attention.gauge.WithLabelValues(AttentionSourceFailed, "OKX 连续 3 次失败：超时")))

	detector.RunOnce(ctx) // 恢复，同一条下架不再重复发布
	require.Equal(t, 0.0, testutil.ToFloat64(attention.gauge.WithLabelValues(AttentionSourceFailed, "")))
	pending, err = db.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "同一条只发布一次")
}
