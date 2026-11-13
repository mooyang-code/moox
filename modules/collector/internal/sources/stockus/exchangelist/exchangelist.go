// Package exchangelist 给一组美股成分代码对上交易所（MIC），数据来自东方财富美股列表（clist）。
//
// 它只做一件事：输入成分代码，返回 {代码: MIC}，不是任何意义上的“取全部标的”来源，也不登记进行情源目录，
// 只被成分来源（标普 500 的 datahub）调用。
//
// 东方财富把美股分在三个市场：105 纳斯达克、106 纽交所、107 其他。映射：105→XNAS、106→XNYS、107→XASE。
// 107 是纽交所、纳斯达克以外的交易所合在一起，含 ETF 和 Cboe 上市的 CBOE，记为 XASE 并不准确，
// 是已接受的取舍（与 markets/stockus 的包注释一致）。
//
// 对照规则：
//   - 单向查找：对每个成分代码，候选写法依次是原样、点换成下划线（BRK.B→BRK_B、BF.B→BF_B），
//     先按候选的顺序、再按 105、106、107 的顺序，第一个命中的取它的市场。
//     横线写法不做：美股标的规则不允许代码里有横线，带横线的代码在到这里之前就被拒掉了。
//     不会把整张列表反过来逐个校验：列表里有 SUPN_、XFLHr、AAPL42 这类代码，与成分无关。
//   - 同一代码出现在两个市场时，取 105、106、107 顺序里的第一个。
//   - 没有对上的代码不在返回值里，由调用方决定怎么处理。
//
// 请求的节奏与上限（东方财富对本机出口 IP 做过限流，翻页必须有节制）：
//   - 请求节奏由 packages/cnstock 的进程级限流器保证（所有 *.eastmoney.com 的请求合计每秒 2 个、顺序发出，
//     连续失败或 429、403 会让主机冷却，见 docs/模块/采集.md 6a），所以相邻两页之间默认不再额外等待；
//     PageInterval 仍可设置（相邻两页含相邻两个市场之间最少间隔，默认 0），再叠加只会变慢；
//   - 每个市场取到的行数必须等于接口自报的 total（total 为 0 也算失败），否则整次报错，不会悄悄跳过失败页；
//   - 每个市场最多读 MaxPages 页（默认 80，当前最大的 105 市场约 61 页），没有无界循环。请求总数的最坏值：
//     每个市场每页最多 PageRetries+1 次，每个市场的第一页还会在主备两个地址上各试一遍（多 PageRetries+1 次），
//     默认值下三个市场合计 3 × (80 × 3 + 3) = 729 次；
//   - 主备地址的选择带到后面的市场：第一个市场读完后，实际用的地址（push2 或 push2test）就是后两个市场的起点，
//     不会每个市场都从 push2 起步、各自在第 1 页先失败一次或白等一个超时。起点是 push2test 时后面的市场只用它，
//     不再回退 push2（已知差别：中途 push2test 也坏了就整次失败，下一轮同步重试）。上面的 729 仍是上限；
//   - 某页失败（超时、连接失败、5xx）最多重试 PageRetries 次（默认 2），每次重试前等的时间逐次增加；429 不重试；
//   - ctx 取消或到期立即退出，错误里带 ctx 的错误；整个对照的时间预算由调用方通过 ctx 给定。
//
// 已知差别（不处理）：
//   - 429 限流在这里只是带 cnstock 错误类别的普通错误，没有映射成 marketdata.ErrRateLimited。
//   - 标签同步（subjectsync.TagRunner）遇到确定性失败（例如有成分对不上交易所）也会整次重跑 3 遍，
//     每遍重新翻一次三个市场的列表（约 140 页），重跑期间的请求量与失败原因无关。
//   - 标签的 sync.cron 缺省是 0 * * * *（UTC，每小时一次）：美股标签的种子必须写成 sync.timezone
//     America/New_York、sync.cron 纽约 08:00，否则每小时都会翻一遍东方财富列表。
package exchangelist

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockus"
	"github.com/mooyang-code/moox/packages/cnstock"
)

const (
	defaultPageRetries    = 2
	defaultRetryBackoff   = time.Second
	defaultMaxPages       = 80
	defaultRequestTimeout = 15 * time.Second
)

// market 是东方财富的一个美股市场。
type market struct {
	filter   string
	exchange string
}

// markets 的顺序就是同一代码出现在多个市场时的优先顺序。
var markets = []market{
	{filter: "m:105", exchange: stockus.ExchangeNasdaq},
	{filter: "m:106", exchange: stockus.ExchangeNYSE},
	{filter: "m:107", exchange: stockus.ExchangeOther},
}

// Config 是对照的请求参数，零值都有默认值。
type Config struct {
	// BaseURL 是 clist 的地址，默认东方财富主地址（超时或 5xx 时 cnstock 会改用备用地址）。
	// 只是第一个市场的起点：之后每个市场都从上一个市场实际用的地址起步。
	BaseURL    string
	HTTPClient *http.Client
	// RequestTimeout 是单次 HTTP 请求的超时，默认 15 秒；只在没有传 HTTPClient 时用。
	RequestTimeout time.Duration
	// PageInterval 是相邻两页之间额外的最小间隔，默认 0：请求节奏由 cnstock 的进程级限流器保证（每秒 2 个、顺序发出），
	// 再叠加间隔只会变慢。限流器只管 *.eastmoney.com，测试里换成本地服务时没有节奏，要自己设间隔；负数按 0 处理。
	PageInterval time.Duration
	// PageRetries 是单页失败后最多再试的次数，默认 2；负数表示不重试。
	PageRetries int
	// RetryBackoff 是重试等待的基数，第 k 次重试前等 k×RetryBackoff，默认 1 秒。
	RetryBackoff time.Duration
	// MaxPages 是每个市场的页数上限，默认 80。
	MaxPages int
	// Sleep 替换等待的实现，测试用。
	Sleep func(ctx context.Context, d time.Duration) error
}

func (c Config) withDefaults() Config {
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = defaultRequestTimeout
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.RequestTimeout}
	}
	if c.PageInterval < 0 {
		c.PageInterval = 0
	}
	switch {
	case c.PageRetries == 0:
		c.PageRetries = defaultPageRetries
	case c.PageRetries < 0:
		c.PageRetries = 0
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = defaultRetryBackoff
	}
	if c.MaxPages <= 0 {
		c.MaxPages = defaultMaxPages
	}
	return c
}

// candidates 返回一个成分代码在东方财富列表里可能的写法，按优先顺序：原样；带点时换成下划线。
func candidates(code string) []string {
	if alt := strings.ReplaceAll(code, ".", "_"); alt != code {
		return []string{code, alt}
	}
	return []string{code}
}

// Lookup 给每个成分代码对上交易所，返回 {代码: MIC}，只含对上的代码；对不上的由调用方处理。
// 任何一个市场没有完整读完（页失败、条数对不上总数、超过页数上限、ctx 结束）都整次返回错误，不返回部分结果。
func Lookup(ctx context.Context, cfg Config, codes []string) (map[string]string, error) {
	cfg = cfg.withDefaults()
	// wanted 是全部候选写法；found 只记命中 wanted 的行，按 105、106、107 的顺序第一次出现为准。
	wanted := make(map[string]struct{})
	for _, code := range codes {
		if strings.TrimSpace(code) == "" {
			return nil, fmt.Errorf("对照交易所：成分代码不能为空")
		}
		for _, candidate := range candidates(code) {
			wanted[candidate] = struct{}{}
		}
	}
	found := make(map[string]string, len(codes))
	// baseURL 是当前市场读第 1 页的起点；每读完一个市场就换成它实际在用的地址。
	baseURL := cfg.BaseURL
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	for i, m := range markets {
		if i > 0 && cfg.PageInterval > 0 {
			if err := sleep(ctx, cfg.PageInterval); err != nil {
				return nil, fmt.Errorf("对照交易所：读 %s 之前等待时被中断：%w", m.filter, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("对照交易所：读 %s 之前已超时或取消：%w", m.filter, err)
		}
		rows := 0
		result, err := cnstock.FetchClist(ctx, cnstock.ClistQuery{
			BaseURL:      baseURL,
			Filter:       m.filter,
			PageSize:     cnstock.EastMoneyClistMaxPageSize,
			HTTPClient:   cfg.HTTPClient,
			UserAgent:    "Mozilla/5.0",
			PageInterval: cfg.PageInterval,
			PageRetries:  cfg.PageRetries,
			RetryBackoff: cfg.RetryBackoff,
			MaxPages:     cfg.MaxPages,
			Sleep:        cfg.Sleep,
		}, func(item cnstock.ClistItem) error {
			rows++
			if _, ok := wanted[item.Code]; ok {
				if _, exists := found[item.Code]; !exists {
					found[item.Code] = m.exchange
				}
			}
			return nil
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("对照交易所：读东方财富美股列表 %s 时已超时或取消：%w（%v）", m.filter, ctxErr, err)
			}
			return nil, fmt.Errorf("对照交易所：读东方财富美股列表 %s：%w", m.filter, err)
		}
		if result.Total <= 0 || rows != result.Total {
			return nil, fmt.Errorf("对照交易所：东方财富美股列表 %s 取到 %d 行，接口自报总数 %d，列表不完整", m.filter, rows, result.Total)
		}
		if result.Base != "" {
			baseURL = result.Base
		}
	}
	exchanges := make(map[string]string, len(codes))
	for _, code := range codes {
		for _, candidate := range candidates(code) {
			if exchange, ok := found[candidate]; ok {
				exchanges[code] = exchange
				break
			}
		}
	}
	return exchanges, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
