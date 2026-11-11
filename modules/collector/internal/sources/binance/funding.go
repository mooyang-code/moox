package binance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	_ marketdata.FundingRateSource = (*MarketDataAdapter)(nil)
	_ marketdata.FieldSource       = (*MarketDataAdapter)(nil)
)

// swapFundingCache 是进程内唯一的永续资金费率缓存：每次取数都新建适配器，缓存必须是包级单例，推送与拉取共用。
// 只在第一次用到时构造：控制机上的 collector 不跑拉取管线，不会建。
var (
	swapFundingOnce  sync.Once
	swapFundingCache *marketfetch.FundingCache
)

// FundingCache 返回永续的资金费率缓存；现货没有资金费，返回 nil。
func (a *MarketDataAdapter) FundingCache() *marketfetch.FundingCache {
	if !a.SupportsFundingRates() {
		return nil
	}
	swapFundingOnce.Do(func() { swapFundingCache = marketfetch.NewFundingCache(a, nil) })
	return swapFundingCache
}

// FieldSpecs 按实例返回附加字段：永续提供资金费率，现货没有。
func (a *MarketDataAdapter) FieldSpecs() []marketdata.FieldSpec {
	if !a.SupportsFundingRates() {
		return nil
	}
	return marketfetch.FundingFieldSpecs()
}

// FetchFields 用资金费率缓存回答附加字段取数。
func (a *MarketDataAdapter) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	cache := a.FundingCache()
	if cache == nil {
		return nil, fmt.Errorf("%w: 币安 %s 没有资金费", marketdata.ErrInvalidRequest, a.defaultInstrumentType)
	}
	return cache.FetchFields(ctx, req)
}

const (
	// fundingRequestInterval：资金费接口与 fundingInfo 共用“每 5 分钟 500 次”的配额，进程内每秒最多发 1 次。
	fundingRequestInterval = time.Second
	// fundingBackoffBase、fundingBackoffMax 是收到 429/418 后暂停全部资金费请求的时长（连续失败时翻倍）。
	fundingBackoffBase = 5 * time.Second
	fundingBackoffMax  = 5 * time.Minute
)

// defaultFundingLimiter 是进程内唯一的资金费限速器：每次取数都新建适配器，限速必须是包级单例。
var defaultFundingLimiter = newFundingLimiter(fundingRequestInterval, fundingBackoffBase, fundingBackoffMax)

// fundingLimiter 控制资金费请求的节奏：两次请求至少间隔 interval；有实时请求在等时，补采请求让它先走；
// 收到 429/418 后暂停全部请求，连续触发时退避时间翻倍，成功一次就复位。
type fundingLimiter struct {
	interval, backoffBase, backoffMax time.Duration
	now                               func() time.Time

	mu              sync.Mutex
	next            time.Time
	pausedUntil     time.Time
	backoff         time.Duration
	realtimeWaiting int
	changed         chan struct{}
}

func newFundingLimiter(interval, backoffBase, backoffMax time.Duration) *fundingLimiter {
	return &fundingLimiter{interval: interval, backoffBase: backoffBase, backoffMax: backoffMax, now: time.Now, changed: make(chan struct{})}
}

// broadcast 唤醒全部等待者重新判断（调用方持有锁）。
func (l *fundingLimiter) broadcast() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// Wait 等到可以发下一次请求；realtime 为真时优先于补采请求。
func (l *fundingLimiter) Wait(ctx context.Context, realtime bool) error {
	l.mu.Lock()
	if realtime {
		l.realtimeWaiting++
	}
	leave := func() {
		if realtime {
			l.realtimeWaiting--
			l.broadcast()
		}
	}
	for {
		now := l.now()
		ready := l.next
		if l.pausedUntil.After(ready) {
			ready = l.pausedUntil
		}
		if !now.Before(ready) && (realtime || l.realtimeWaiting == 0) {
			l.next = now.Add(l.interval)
			leave()
			l.mu.Unlock()
			return nil
		}
		delay := ready.Sub(now)
		if delay <= 0 {
			// 配额已空出，但有实时请求在等：等它取走后被唤醒。
			delay = l.interval
		}
		changed := l.changed
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			l.mu.Lock()
			leave()
			l.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		case <-changed:
			timer.Stop()
		}
		l.mu.Lock()
	}
}

// Done 接收一次请求的结果：429/418 时暂停全部请求，成功时复位退避。
func (l *fundingLimiter) Done(err error) {
	var statusErr *httpclient.StatusError
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		l.backoff = 0
		return
	}
	if !errors.As(err, &statusErr) || (statusErr.StatusCode != http.StatusTooManyRequests && statusErr.StatusCode != http.StatusTeapot) {
		return
	}
	if l.backoff == 0 {
		l.backoff = l.backoffBase
	} else {
		l.backoff = min(l.backoff*2, l.backoffMax)
	}
	pause := max(l.backoff, statusErr.RetryAfter)
	if until := l.now().Add(pause); until.After(l.pausedUntil) {
		l.pausedUntil = until
	}
	l.broadcast()
}

// fundingPacer 把一次查询的实时/补采身份带给限速器。
type fundingPacer struct {
	limiter  *fundingLimiter
	realtime bool
}

func (p fundingPacer) Wait(ctx context.Context) error { return p.limiter.Wait(ctx, p.realtime) }
func (p fundingPacer) Done(err error)                 { p.limiter.Done(err) }

// SupportsFundingRates：只有 U 本位永续有资金费。
func (a *MarketDataAdapter) SupportsFundingRates() bool {
	return a != nil && a.defaultInstrumentType == marketdata.InstrumentSwap
}

// FundingRates 返回结算时刻落在 [start, end) 内的资金费记录；symbol 为空时查全部合约。
// 带 symbol 时任何一条记录格式不对都报错；全市场查询里格式不对的记录跳过并记日志，不能让无关合约挡住整个小时。
func (a *MarketDataAdapter) FundingRates(ctx context.Context, symbol string, start, end time.Time) ([]marketdata.FundingRate, error) {
	if !a.SupportsFundingRates() {
		return nil, fmt.Errorf("%w: 币安 %s 没有资金费", marketdata.ErrInvalidRequest, a.defaultInstrumentType)
	}
	return a.fundingRates(ctx, defaultFundingLimiter, symbol, start, end)
}

func (a *MarketDataAdapter) fundingRates(ctx context.Context, limiter *fundingLimiter, symbol string, start, end time.Time) ([]marketdata.FundingRate, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	api := binanceapi.NewSwapAPI(a.klineCollector.client)
	records, err := api.FundingRates(ctx, fundingPacer{limiter: limiter, realtime: marketdata.IsRealtimeFunding(ctx)}, symbol, start, end)
	if err != nil {
		reason := fundingFailureReason(err)
		marketdata.ObserveFundingFetchFailure(reason)
		log.WarnContextf(ctx, "币安资金费率取数失败 symbol=%s start=%s end=%s reason=%s: %v", fundingSymbolLabel(symbol), start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), reason, err)
		return nil, classifyProviderError(err)
	}
	rates := make([]marketdata.FundingRate, 0, len(records))
	skipped := 0
	for _, record := range records {
		rate, parseErr := fundingRateFromRecord(record)
		if parseErr == nil {
			rates = append(rates, rate)
			continue
		}
		if symbol != "" {
			marketdata.ObserveFundingFetchFailure(marketdata.FundingFailureMalformed)
			log.WarnContextf(ctx, "币安资金费率记录格式不对 symbol=%s start=%s end=%s: %v", symbol, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), parseErr)
			return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, parseErr)
		}
		skipped++
	}
	if skipped > 0 {
		marketdata.ObserveFundingFetchFailure(marketdata.FundingFailureMalformed)
		log.WarnContextf(ctx, "币安资金费率全市场查询跳过 %d 条格式不对的记录 start=%s end=%s", skipped, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	}
	return rates, nil
}

func fundingRateFromRecord(record binanceapi.FundingRateRecord) (marketdata.FundingRate, error) {
	if strings.TrimSpace(record.Symbol) == "" || record.FundingTime <= 0 {
		return marketdata.FundingRate{}, fmt.Errorf("资金费记录缺少合约或结算时刻（symbol=%q time=%d）", record.Symbol, record.FundingTime)
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(record.FundingRate), 64)
	if err != nil {
		return marketdata.FundingRate{}, fmt.Errorf("资金费率 %s 不是数字：%q", record.Symbol, record.FundingRate)
	}
	return marketdata.FundingRate{ProviderSymbol: strings.ToUpper(strings.TrimSpace(record.Symbol)), FundingTime: time.UnixMilli(record.FundingTime).UTC(), Rate: rate}, nil
}

func fundingFailureReason(err error) string {
	var statusErr *httpclient.StatusError
	var networkErr net.Error
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return marketdata.FundingFailureCanceled
	case errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode == http.StatusTeapot):
		return marketdata.FundingFailureRateLimited
	case errors.As(err, &statusErr):
		return marketdata.FundingFailureHTTP
	case errors.As(err, &networkErr):
		return marketdata.FundingFailureNetwork
	}
	return marketdata.FundingFailureProtocol
}

func fundingSymbolLabel(symbol string) string {
	if symbol == "" {
		return "全部合约"
	}
	return symbol
}
