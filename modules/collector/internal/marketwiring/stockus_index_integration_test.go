package marketwiring

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/datahub"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/exchangelist"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/nasdaq"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

var (
	ndxKey = subjectsync.IndexListerKey{MarketID: "stockus", Source: "nasdaq", Index: "NDX"}
	spxKey = subjectsync.IndexListerKey{MarketID: "stockus", Source: "datahub", Index: "SPX"}
)

func usTestdata(t *testing.T, parts ...string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(append([]string{"..", "sources", "stockus"}, parts...)...))
	require.NoError(t, err)
	return body
}

// usFakeWeb 是美股成分同步用到的三个站点的假实现：纳斯达克接口、jsdelivr 的 CSV、东方财富 clist。
// 请求不出网，数据是录下的真实返回（ndx.json、sp500.csv）；clist 由 CSV 里的代码生成：
// 带点的代码按东方财富的写法换成下划线放 106，CBOE 放 107，其余放 105，外加几个干扰代码（SUPN_、AAPL42 一类）。
type usFakeWeb struct {
	ndx       []byte
	csv       []byte
	hideCode  string // 不放进 clist 的成分代码（模拟新上市、东方财富还没有）
	clistReqs int
}

func (w *usFakeWeb) clistRows() map[string][]string {
	records, err := csv.NewReader(strings.NewReader(string(w.csv))).ReadAll()
	if err != nil {
		panic(err)
	}
	rows := map[string][]string{"m:105": {"SUPN_", "AAPL42"}, "m:106": {"XFLHr"}, "m:107": {"SPY"}}
	for _, record := range records[1:] {
		code := record[0]
		if code == w.hideCode || strings.ReplaceAll(code, ".", "_") == w.hideCode {
			continue
		}
		switch {
		case code == "CBOE":
			rows["m:107"] = append(rows["m:107"], code)
		case strings.Contains(code, "."):
			rows["m:106"] = append(rows["m:106"], strings.ReplaceAll(code, ".", "_"))
		default:
			rows["m:105"] = append(rows["m:105"], code)
		}
	}
	for fs := range rows {
		sort.Strings(rows[fs])
	}
	return rows
}

func (w *usFakeWeb) client() *http.Client {
	lists := w.clistRows()
	return &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		switch req.URL.Host {
		case "api.nasdaq.com":
			if req.URL.Path != "/api/quote/list-type/nasdaq100" {
				recorder.WriteHeader(http.StatusNotFound)
				break
			}
			_, _ = recorder.Write(w.ndx)
		case "cdn.jsdelivr.net":
			if req.URL.Path != "/gh/datasets/s-and-p-500-companies@main/data/constituents.csv" {
				recorder.WriteHeader(http.StatusNotFound)
				break
			}
			_, _ = recorder.Write(w.csv)
		case "push2.eastmoney.com", "push2test.eastmoney.com":
			w.clistReqs++
			query := req.URL.Query()
			codes, ok := lists[query.Get("fs")]
			if !ok || req.URL.Path != "/api/qt/clist/get" {
				recorder.WriteHeader(http.StatusNotFound)
				break
			}
			page, _ := strconv.Atoi(query.Get("pn"))
			start := (page - 1) * 100
			var diff []map[string]any
			for i := start; i < min(start+100, len(codes)); i++ {
				diff = append(diff, map[string]any{"f12": codes[i], "f14": codes[i] + " name"})
			}
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"total": len(codes), "diff": diff}})
			_, _ = recorder.Write(body)
		default:
			recorder.WriteHeader(http.StatusNotFound)
		}
		return recorder.Result(), nil
	})}
}

// usIndexListers 先断言装配出的指数成分表里有 NDX、SPX 两项且全部标的表里没有美股，再把这两项换成用假客户端构造的
// 同一个数据源：不替换全局 http.DefaultTransport，交易所对照也走假 clist（不等待页间隔）。
func usIndexListers(t *testing.T, web *usFakeWeb) (subjectsync.Listers, subjectsync.IndexListers) {
	t.Helper()
	listers, indexes, err := NewSubjectListers()
	require.NoError(t, err)
	require.Contains(t, indexes, ndxKey)
	require.Contains(t, indexes, spxKey)
	for key := range listers {
		require.NotEqual(t, "stockus", key.MarketID, "美股不提供全部标的")
	}
	client := web.client()
	indexes[ndxKey] = indexLister{fetcher: nasdaq.New(nasdaq.Config{HTTPClient: client}), index: "NDX"}
	indexes[spxKey] = indexLister{fetcher: datahub.New(datahub.Config{HTTPClient: client, Exchanges: func(ctx context.Context, codes []string) (map[string]string, error) {
		return exchangelist.Lookup(ctx, exchangelist.Config{HTTPClient: client, PageInterval: -1, RetryBackoff: time.Millisecond}, codes)
	}}), index: "SPX"}
	return listers, indexes
}

func newUSWeb(t *testing.T) *usFakeWeb {
	return &usFakeWeb{ndx: usTestdata(t, "nasdaq", "testdata", "ndx.json"), csv: usTestdata(t, "datahub", "testdata", "sp500.csv")}
}

// usTag 是美股指数标签的写法：sync.source 是采集用的行情源（东方财富），成分来源另写 sync.index_source。
func usTag(tagID, indexSource, index string) *pb.Tag {
	return &pb.Tag{SpaceId: "stockus", TagId: tagID, Mode: "auto", Attributes: map[string]string{
		"market_type": "equity", "sync.source": "eastmoney", "sync.index_source": indexSource, "sync.index": index,
	}}
}

func syncUSTag(t *testing.T, indexes subjectsync.IndexListers, storage *hkTagStorage) subjectsync.TagResult {
	t.Helper()
	runner := &subjectsync.TagRunner{Store: storage, IndexListers: indexes, RetryDelays: make([]time.Duration, 0)}
	results, err := runner.SyncTags(context.Background(), subjectsync.Filter{SpaceID: "stockus"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	return results[0]
}

// 标签同步对（stockus, nasdaq, NDX）能取到 101 个成员，并创建还不存在的标的；交易所都是 XNAS，不请求东方财富。
func TestTagSyncAppliesNDXConstituentsFromNasdaq(t *testing.T) {
	web := newUSWeb(t)
	_, indexes := usIndexListers(t, web)
	storage := &hkTagStorage{
		tags:     []*pb.Tag{usTag("us_ndx", "nasdaq", "NDX")},
		subjects: map[string]*pb.Subject{"AAPL.XNAS": {SpaceId: "stockus", SubjectId: "AAPL.XNAS", Name: "苹果"}},
		members:  map[string][]string{},
	}
	result := syncUSTag(t, indexes, storage)
	require.Equal(t, subjectsync.TagResult{SpaceID: "stockus", TagID: "us_ndx", Status: subjectsync.StatusSuccess, Subjects: 101}, result)
	require.Zero(t, web.clistReqs, "纳指 100 全部是纳斯达克上市，不翻东方财富列表")

	require.Len(t, storage.members["us_ndx"], 101)
	require.Len(t, storage.subjects, 101, "不存在的 100 只在应用快照时创建")
	created := storage.subjects["MSFT.XNAS"]
	require.NotNil(t, created)
	require.Equal(t, "stockus", created.GetSpaceId())
	require.Contains(t, created.GetName(), "Microsoft")
	require.Equal(t, "America/New_York", created.GetTimezone())
	require.Equal(t, map[string]string{"subject_type": "stock", "market": "XNAS", "currency": "USD", "exchange": "XNAS"}, created.GetAttributes())
	require.Equal(t, "苹果", storage.subjects["AAPL.XNAS"].GetName(), "已存在的标的不改")

	// 成员的标的 ID 都能按美股的标的规则换成传给数据源的代码。
	for _, subjectID := range storage.members["us_ndx"] {
		code, err := marketfetch.DefaultProviderSymbol("stockus", "equity", subjectID)
		require.NoError(t, err, subjectID)
		require.Equal(t, strings.TrimSuffix(subjectID, ".XNAS"), code)
	}
}

// 标签同步对（stockus, datahub, SPX）能取到 503 个成员，交易所由假东方财富列表对上，并创建还不存在的标的。
func TestTagSyncAppliesSPXConstituentsFromDatahub(t *testing.T) {
	web := newUSWeb(t)
	_, indexes := usIndexListers(t, web)
	storage := &hkTagStorage{tags: []*pb.Tag{usTag("us_spx", "datahub", "SPX")}, subjects: map[string]*pb.Subject{}, members: map[string][]string{}}
	result := syncUSTag(t, indexes, storage)
	require.Equal(t, subjectsync.TagResult{SpaceID: "stockus", TagID: "us_spx", Status: subjectsync.StatusSuccess, Subjects: 503}, result)
	require.Positive(t, web.clistReqs)

	require.Len(t, storage.members["us_spx"], 503)
	require.Len(t, storage.subjects, 503)
	for _, id := range []string{"BRK.B.XNYS", "BF.B.XNYS", "CBOE.XASE", "AAPL.XNAS"} {
		require.Contains(t, storage.members["us_spx"], id)
	}
	berkshire := storage.subjects["BRK.B.XNYS"]
	require.Equal(t, "Berkshire Hathaway", berkshire.GetName())
	require.Equal(t, "America/New_York", berkshire.GetTimezone())
	require.Equal(t, map[string]string{"subject_type": "stock", "market": "XNYS", "currency": "USD", "exchange": "XNYS"}, berkshire.GetAttributes())
	require.Equal(t, "XASE", storage.subjects["CBOE.XASE"].GetAttributes()["exchange"])

	// 带点的代码：传给数据源的代码保留点（换成下划线是东方财富数据源的事）。
	code, err := marketfetch.DefaultProviderSymbol("stockus", "equity", "BRK.B.XNYS")
	require.NoError(t, err)
	require.Equal(t, "BRK.B", code)
}

// 有一只成分在东方财富列表里对不上交易所：同步失败，不应用快照，标签保留旧成员；错误里写出对不上的代码。
func TestTagSyncKeepsSPXMembersWhenExchangeUnmatched(t *testing.T) {
	web := newUSWeb(t)
	web.hideCode = "OKTA"
	_, indexes := usIndexListers(t, web)
	storage := &hkTagStorage{tags: []*pb.Tag{usTag("us_spx", "datahub", "SPX")}, subjects: map[string]*pb.Subject{}, members: map[string][]string{"us_spx": {"AAPL.XNAS"}}}
	result := syncUSTag(t, indexes, storage)
	require.Equal(t, subjectsync.StatusFailed, result.Status)
	require.Contains(t, result.Message, "对不上交易所")
	require.Contains(t, result.Message, "OKTA")
	require.Equal(t, []string{"AAPL.XNAS"}, storage.members["us_spx"])
	require.Empty(t, storage.subjects)
}

// 纳斯达克名单不完整（行数对不上自报的总数）：同步失败，标签保留旧成员。
func TestTagSyncKeepsNDXMembersWhenConstituentsIncomplete(t *testing.T) {
	web := newUSWeb(t)
	web.ndx = []byte(strings.Replace(string(web.ndx), `"totalrecords":101`, `"totalrecords":102`, 1))
	_, indexes := usIndexListers(t, web)
	storage := &hkTagStorage{tags: []*pb.Tag{usTag("us_ndx", "nasdaq", "NDX")}, subjects: map[string]*pb.Subject{}, members: map[string][]string{"us_ndx": {"AAPL.XNAS"}}}
	result := syncUSTag(t, indexes, storage)
	require.Equal(t, subjectsync.StatusFailed, result.Status)
	require.Contains(t, result.Message, "取到 101 行，接口报的总数是 102")
	require.Equal(t, []string{"AAPL.XNAS"}, storage.members["us_ndx"])
	require.Empty(t, storage.subjects)
}

// 不写 sync.index_source 时成分来源等于 sync.source：东方财富没有登记美股指数成分，同步失败，不会去用别的来源。
func TestTagSyncUSIndexNeedsIndexSource(t *testing.T) {
	_, indexes := usIndexListers(t, newUSWeb(t))
	tag := usTag("us_spx", "datahub", "SPX")
	delete(tag.Attributes, "sync.index_source")
	storage := &hkTagStorage{tags: []*pb.Tag{tag}, subjects: map[string]*pb.Subject{}, members: map[string][]string{}}
	result := syncUSTag(t, indexes, storage)
	require.Equal(t, subjectsync.StatusFailed, result.Status)
	require.Contains(t, result.Message, "eastmoney")
	require.Empty(t, storage.members)
}
