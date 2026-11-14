package exchange

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/cache"
)

const (
	// priceBoardTTL 是全市场最新价的复用时间。目标换算要一次取几百个标的的价格，
	// 逐个请求要几十秒，全市场接口一次就够，而且只占很少的限频额度。
	priceBoardTTL = 2 * time.Second
	// bookBoardTTL 是全市场买一卖一的复用时间。模拟盘每笔子单落库和提交前都要取一次可成交价，
	// 逐个请求盘口一批 16 单要几十次往返。
	bookBoardTTL = time.Second
)

type cachedQuotesKey struct{}

// ErrQuoteNotCached 表示只读缓存的取价没有可用的缓存。
var ErrQuoteNotCached = errors.New("报价不在缓存里")

// WithCachedQuotes 返回“只读已有报价缓存”的上下文：用它取价不向交易所发请求，
// 缓存里没有、或比 maxAge 旧时按取不到处理。管理台的查询用它，不为一次页面刷新占交易所的限频额度。
func WithCachedQuotes(ctx context.Context, maxAge time.Duration) context.Context {
	return context.WithValue(ctx, cachedQuotesKey{}, maxAge)
}

// CachedQuotesOnly 报告上下文是否只读报价缓存，以及可接受的最长缓存时间。
func CachedQuotesOnly(ctx context.Context) (time.Duration, bool) {
	maxAge, ok := ctx.Value(cachedQuotesKey{}).(time.Duration)
	return maxAge, ok
}

// Book 是一个标的的买一卖一。
type Book struct {
	Bid shared.Decimal
	Ask shared.Decimal
}

// PriceBoard 缓存全市场最新价，按（市场，交易所代码）查询。零值可用，每个交易所适配器一份。
type PriceBoard struct{ board[shared.Decimal] }

// Get 返回标的的最新价和取得时间。全市场接口失败或没有这个标的时 ok 为 false，调用方退回单标的接口。
func (b *PriceBoard) Get(
	ctx context.Context,
	key SymbolKey,
	fetch func(context.Context) (map[SymbolKey]shared.Decimal, error),
) (shared.Decimal, time.Time, bool) {
	price, at, ok := b.get(ctx, priceBoardTTL, key, fetch)
	return price, at, ok && price.Cmp(shared.Zero()) > 0
}

// BookBoard 缓存全市场买一卖一。零值可用，每个交易所适配器一份。
type BookBoard struct{ board[Book] }

// Get 返回标的的买一卖一和取得时间。全市场接口失败、没有这个标的或报价为空时 ok 为 false，调用方退回单标的接口。
func (b *BookBoard) Get(
	ctx context.Context,
	key SymbolKey,
	fetch func(context.Context) (map[SymbolKey]Book, error),
) (Book, time.Time, bool) {
	book, at, ok := b.get(ctx, bookBoardTTL, key, fetch)
	return book, at, ok && book.Bid.Cmp(shared.Zero()) > 0 && book.Ask.Cmp(shared.Zero()) > 0
}

// board 是按标的查询的全市场快照。
type board[V any] struct {
	once     sync.Once
	snapshot *cache.Snapshot[map[SymbolKey]V]
}

func (b *board[V]) get(
	ctx context.Context,
	ttl time.Duration,
	key SymbolKey,
	fetch func(context.Context) (map[SymbolKey]V, error),
) (V, time.Time, bool) {
	b.once.Do(func() { b.snapshot = cache.NewSnapshot[map[SymbolKey]V](ttl) })
	var zero V
	if maxAge, only := CachedQuotesOnly(ctx); only {
		values, at, ok := b.snapshot.Peek(maxAge)
		if !ok {
			return zero, time.Time{}, false
		}
		value, ok := values[key]
		return value, at, ok
	}
	values, at, err := b.snapshot.Get(ctx, fetch)
	if err != nil {
		return zero, time.Time{}, false
	}
	value, ok := values[key]
	return value, at, ok
}
