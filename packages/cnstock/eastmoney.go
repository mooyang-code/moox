package cnstock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 东方财富 clist 列表接口（/api/qt/clist/get）。
const (
	// EastMoneyPushBase 是 clist 的主地址；EastMoneyPushFallbackBase 是主地址不可用（超时、5xx）时的备用地址。
	EastMoneyPushBase         = "https://push2.eastmoney.com"
	EastMoneyPushFallbackBase = "https://push2test.eastmoney.com"
	eastMoneyPushHost         = "push2.eastmoney.com"
	// EastMoneyClistMaxPageSize 是 clist 每页的实际上限：请求更大的 pz 也只返回 100 条（2026-11-04 实测）。
	EastMoneyClistMaxPageSize = 100
	// EastMoneyAllAShareFilter 是沪深北全部 A 股：深主板、创业板、沪主板、科创板、北交所。
	EastMoneyAllAShareFilter = "m:0 t:6,m:0 t:80,m:1 t:2,m:1 t:23,m:0 t:81 s:2048"
	// eastMoneyClistFields 是 clist 请求的字段：代码、市场、名称等。
	eastMoneyClistFields = "f12,f13,f14,f115,f152,f103,f128,f129"
	maxClistPages        = 128
)

// ClistItem 是 clist 列表里的一条，字段名兼容几种返回写法。
type ClistItem struct {
	Code   string
	Symbol string
	Market string
	Name   string
	Status string
}

// ClistQuery 是一次完整列表请求。
type ClistQuery struct {
	// BaseURL 是 clist 的地址；主地址是 push2 时，超时或 5xx 会改用 push2test，之后各页固定用第一个成功的地址。
	BaseURL string
	// Filter 是筛选条件 fs，例如 EastMoneyAllAShareFilter、"b:BK0500"（沪深 300 成分）。
	Filter     string
	PageSize   int
	HTTPClient *http.Client
	UserAgent  string
	// 下面几项都是可选的节奏与上限控制，零值保持原有行为（不等待、不重试、页数上限 maxClistPages）。
	//
	// PageInterval 是相邻两页请求之间的最小间隔（第一页之前不等）。
	PageInterval time.Duration
	// PageRetries 是某一页请求失败（超时、连接失败、5xx）后最多再试的次数，负数按 0 处理；429 不重试，直接失败。
	// 第 k 次重试前等 k×RetryBackoff（RetryBackoff 不大于 0 时按 1 秒）。重试只包住一次 HTTP 请求，
	// 页内的行只在请求成功后才交给 visit，不会重复访问。
	PageRetries  int
	RetryBackoff time.Duration
	// MaxPages 是翻页的页数上限；不大于 0 或大于 maxClistPages 时取 maxClistPages。
	MaxPages int
	// Sleep 替换等待的实现，测试用；为空时按真实时间等待，ctx 结束时立即返回错误。
	Sleep func(ctx context.Context, d time.Duration) error
}

// ClistResult 是翻页结束后的统计：Total 是接口自报的总数（没报时为 0），Pages 是读过的页数（含收尾的空页）。
type ClistResult struct {
	Total int
	Pages int
}

// FetchClist 按筛选条件翻页读完整的列表，每条交给 visit；visit 返回错误时立即停止并返回这个错误。
// 条数必须和接口自报的总数一致；没有总数、页数或“还有下一页”标记时，以一页空结果收尾，不把不满一页当成最后一页。
func FetchClist(ctx context.Context, query ClistQuery, visit func(ClistItem) error) (ClistResult, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = EastMoneyClistMaxPageSize
	}
	result := ClistResult{}
	declaredTotal := 0
	itemsSeen := 0
	sawUnverifiedShortPage := false
	activeBase := ""
	maxPages := query.MaxPages
	if maxPages <= 0 || maxPages > maxClistPages {
		maxPages = maxClistPages
	}
	for page := 1; ; page++ {
		if page > maxPages {
			return ClistResult{}, fmt.Errorf("%w: eastmoney instrument pagination exceeded %d pages", ErrProtocol, maxPages)
		}
		if page > 1 && query.PageInterval > 0 {
			if err := query.sleep(ctx, query.PageInterval); err != nil {
				return ClistResult{}, fmt.Errorf("等待 clist 第 %d 页时已取消：%w", page, err)
			}
		}
		values := url.Values{
			"pn":     {strconv.Itoa(page)},
			"pz":     {strconv.Itoa(pageSize)},
			"fs":     {query.Filter},
			"fid":    {"f12"},
			"fields": {eastMoneyClistFields},
		}
		body, base, err := fetchClistPageWithRetry(ctx, query, activeBase, page, values)
		if err != nil {
			return ClistResult{}, err
		}
		activeBase = base
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return ClistResult{}, fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		data := objectAt(payload, "data")
		if data == nil {
			return ClistResult{}, fmt.Errorf("%w: eastmoney instrument payload missing data", ErrProtocol)
		}
		if total, ok := intField(data, "total", "count", "total_count", "totalnum", "total_nums"); ok && total > 0 {
			if declaredTotal == 0 {
				declaredTotal = total
			} else if declaredTotal != total {
				return ClistResult{}, fmt.Errorf("%w: eastmoney instrument total changed from %d to %d on page %d", ErrProtocol, declaredTotal, total, page)
			}
		}
		items := itemSlice(data, "diff", "clist", "list")
		if len(items) == 0 {
			if diff := objectAt(data, "diff"); len(diff) > 0 {
				keys := make([]string, 0, len(diff))
				for key := range diff {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					if item, ok := diff[key].(map[string]any); ok {
						items = append(items, item)
					}
				}
			}
		}
		totalPages := pageLimit(data, pageSize)
		hasMore, hasMoreKnown := boolField(data, "hasnext", "has_next", "hasMore", "has_more")
		if declaredTotal == 0 && len(items) > 0 && len(items) < pageSize {
			sawUnverifiedShortPage = true
			if (totalPages > 0 && page < totalPages) || (hasMoreKnown && hasMore) {
				return ClistResult{}, fmt.Errorf("%w: eastmoney instrument page %d is short (%d < %d) before a declared terminal page", ErrProtocol, page, len(items), pageSize)
			}
		}
		itemsSeen += len(items)
		if declaredTotal > 0 && itemsSeen > declaredTotal {
			return ClistResult{}, fmt.Errorf("%w: eastmoney instrument items %d exceed declared total %d on page %d", ErrProtocol, itemsSeen, declaredTotal, page)
		}
		if len(items) == 0 {
			if declaredTotal > 0 && itemsSeen != declaredTotal {
				return ClistResult{}, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items on terminal page %d", ErrProtocol, declaredTotal, itemsSeen, page)
			}
			if declaredTotal == 0 && sawUnverifiedShortPage {
				return ClistResult{}, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", ErrProtocol)
			}
			if page == 1 || (totalPages > 0 && page < totalPages) || (hasMoreKnown && hasMore) {
				return ClistResult{}, fmt.Errorf("%w: eastmoney instrument page %d is empty before terminal evidence", ErrProtocol, page)
			}
			// 没有总数也没有“还有下一页”标记时，多读的这一页空结果就是收尾；最后一页不满是正常的。
			result.Pages++
			break
		}
		for _, item := range items {
			if err := visit(ClistItem{
				Code:   stringField(item, "f12", "code", "symbol"),
				Symbol: stringField(item, "symbol", "provider_symbol"),
				Market: stringField(item, "f13", "market", "exchange"),
				Name:   stringField(item, "f14", "name", "f14_name"),
				Status: stringField(item, "f107", "status", "state"),
			}); err != nil {
				return ClistResult{}, err
			}
		}
		result.Pages++
		if totalPages > 0 {
			if page >= totalPages {
				// 没有显式页数时，最后一页是按 总数 ÷ 请求的每页条数 推出来的；东方财富把每页条数封顶在 100，
				// 所以非空页仍不足总数时继续读。显式给了页数就以它为准。
				_, hasExplicitPageCount := intField(data, "pagecount", "page_count", "total_pages", "totalpage", "pages")
				if !hasExplicitPageCount && declaredTotal > 0 && itemsSeen < declaredTotal {
					continue
				}
				if declaredTotal > 0 && itemsSeen != declaredTotal {
					return ClistResult{}, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items after page %d", ErrProtocol, declaredTotal, itemsSeen, page)
				}
				if declaredTotal == 0 && sawUnverifiedShortPage {
					return ClistResult{}, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", ErrProtocol)
				}
				break
			}
			continue
		}
		if hasMoreKnown {
			if !hasMore {
				if declaredTotal > 0 && itemsSeen != declaredTotal {
					return ClistResult{}, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items after page %d", ErrProtocol, declaredTotal, itemsSeen, page)
				}
				if declaredTotal == 0 && sawUnverifiedShortPage {
					return ClistResult{}, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", ErrProtocol)
				}
				break
			}
			continue
		}
		// 不满一页本身不能证明是最后一页，除非响应里还有总数、页数或“还有下一页”标记：继续读下一页，要求以空结果收尾。
	}
	result.Total = declaredTotal
	return result, nil
}

// fetchClistPageWithRetry 取一页：先按主备地址顺序试一遍；失败且可重试（超时、连接失败、5xx）时，
// 按 PageRetries 再整体重试，每次重试前等待的时间逐次增加。ctx 结束后不再重试。
// 零重试时只走一遍主备地址，行为与加重试之前一致。返回响应内容和成功的那个地址。
func fetchClistPageWithRetry(ctx context.Context, query ClistQuery, activeBase string, page int, values url.Values) ([]byte, string, error) {
	var lastErr error
	retries := max(query.PageRetries, 0) // 负数按 0 处理，至少请求一次
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			backoff := query.RetryBackoff
			if backoff <= 0 {
				backoff = time.Second
			}
			if err := query.sleep(ctx, time.Duration(attempt)*backoff); err != nil {
				return nil, "", fmt.Errorf("第 %d 页重试前等待时已取消（上次错误：%v）：%w", page, lastErr, err)
			}
		}
		bases := []string{activeBase}
		if activeBase == "" {
			bases = clistBases(query.BaseURL)
		}
		for i, base := range bases {
			body, err := fetchClistPage(ctx, query, base, values)
			if err == nil {
				return body, base, nil
			}
			lastErr = err
			if i == len(bases)-1 || !clistHostRetryable(err) {
				break
			}
		}
		// 取消后 client.Do 的错误会被包成 ErrTimeout，必须先看 ctx，否则会把取消当成可重试的超时。
		if ctxErr := ctx.Err(); ctxErr != nil && retries > 0 {
			return nil, "", fmt.Errorf("读 clist 第 %d 页时已取消（%v）：%w", page, lastErr, ctxErr)
		}
		if retries == 0 || !clistHostRetryable(lastErr) {
			return nil, "", lastErr
		}
	}
	return nil, "", fmt.Errorf("clist 第 %d 页重试 %d 次后仍失败：%w", page, retries, lastErr)
}

// sleep 等 d；ctx 先结束时立即返回 ctx 的错误。
func (q ClistQuery) sleep(ctx context.Context, d time.Duration) error {
	if q.Sleep != nil {
		return q.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func clistBases(base string) []string {
	if strings.TrimSpace(base) == "" {
		base = EastMoneyPushBase
	}
	base = strings.TrimRight(base, "/")
	bases := []string{base}
	if parsed, err := url.Parse(base); err == nil && parsed.Hostname() == eastMoneyPushHost {
		bases = append(bases, EastMoneyPushFallbackBase)
	}
	return bases
}

func fetchClistPage(ctx context.Context, query ClistQuery, base string, values url.Values) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/qt/clist/get?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	agent := query.UserAgent
	if agent == "" {
		agent = "Mozilla/5.0"
	}
	request.Header.Set("User-Agent", agent)
	client := query.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response body: %v", ErrProtocol, err)
	}
	return body, nil
}

func clistHostRetryable(err error) bool {
	if errors.Is(err, ErrTimeout) {
		return true
	}
	var statusErr *HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.Status >= 500
}

func valueAt(root any, path ...string) any {
	current := root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[key]
		if !ok {
			return nil
		}
	}
	return current
}

func objectAt(root map[string]any, path ...string) map[string]any {
	object, _ := valueAt(root, path...).(map[string]any)
	return object
}

func itemSlice(data map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		rawItems, ok := valueAt(data, key).([]any)
		if !ok {
			continue
		}
		items := make([]map[string]any, 0, len(rawItems))
		for _, rawItem := range rawItems {
			if item, ok := rawItem.(map[string]any); ok {
				items = append(items, item)
			}
		}
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func pageLimit(data map[string]any, pageSize int) int {
	if limit, ok := intField(data, "pagecount", "page_count", "total_pages", "totalpage", "pages"); ok && limit > 0 {
		return limit
	}
	if total, ok := intField(data, "total", "count", "total_count", "totalnum", "total_nums"); ok && total > 0 && pageSize > 0 {
		return (total + pageSize - 1) / pageSize
	}
	return 0
}

func stringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			if text := toString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func intField(object map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case int:
			return v, true
		case int32:
			return int(v), true
		case int64:
			return int(v), true
		case float64:
			return int(v), true
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n), true
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func boolField(object map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			return v, true
		case float64:
			return v != 0, true
		case int:
			return v != 0, true
		case int64:
			return v != 0, true
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n != 0, true
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes", "y":
				return true, true
			case "false", "0", "no", "n":
				return false, true
			}
		}
	}
	return false, false
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		if math.Trunc(v) == v {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// Stock 是 A 股标的列表里的一只股票。
type Stock struct {
	InstrumentID string
	Name         string
}

// ListAllStocks 读沪深北全部 A 股（含科创板、北交所）的标的列表，约 60 页。
func ListAllStocks(ctx context.Context, client *http.Client) ([]Stock, error) {
	var stocks []Stock
	seen := make(map[string]struct{})
	_, err := FetchClist(ctx, ClistQuery{Filter: EastMoneyAllAShareFilter, HTTPClient: client}, func(item ClistItem) error {
		instrumentID, err := InstrumentID(item.Code)
		if err != nil {
			return fmt.Errorf("%w：%v", ErrProtocol, err)
		}
		if _, duplicate := seen[instrumentID]; duplicate {
			return fmt.Errorf("%w：标的 %s 重复出现", ErrProtocol, instrumentID)
		}
		seen[instrumentID] = struct{}{}
		stocks = append(stocks, Stock{InstrumentID: instrumentID, Name: item.Name})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(stocks) == 0 {
		return nil, fmt.Errorf("%w：标的列表为空", ErrProtocol)
	}
	return stocks, nil
}
