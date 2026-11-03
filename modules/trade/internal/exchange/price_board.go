package exchange

import (
	"context"
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

// Book 是一个标的的买一卖一。
type Book struct {
	Bid shared.Decimal
	Ask shared.Decimal
}

// PriceBoard 缓存全市场最新价。零值可用，每个交易所适配器一份。
type PriceBoard struct{ board[shared.Decimal] }

// Get 返回标的的最新价和取得时间。全市场接口失败或没有这个标的时 ok 为 false，调用方退回单标的接口。
func (b *PriceBoard) Get(
	ctx context.Context,
	symbol string,
	fetch func(context.Context) (map[string]shared.Decimal, error),
) (shared.Decimal, time.Time, bool) {
	price, at, ok := b.get(ctx, priceBoardTTL, symbol, fetch)
	return price, at, ok && price.Cmp(shared.Zero()) > 0
}

// BookBoard 缓存全市场买一卖一。零值可用，每个交易所适配器一份。
type BookBoard struct{ board[Book] }

// Get 返回标的的买一卖一和取得时间。全市场接口失败、没有这个标的或报价为空时 ok 为 false，调用方退回单标的接口。
func (b *BookBoard) Get(
	ctx context.Context,
	symbol string,
	fetch func(context.Context) (map[string]Book, error),
) (Book, time.Time, bool) {
	book, at, ok := b.get(ctx, bookBoardTTL, symbol, fetch)
	return book, at, ok && book.Bid.Cmp(shared.Zero()) > 0 && book.Ask.Cmp(shared.Zero()) > 0
}

// board 是按标的查询的全市场快照。
type board[V any] struct {
	once     sync.Once
	snapshot *cache.Snapshot[map[string]V]
}

func (b *board[V]) get(
	ctx context.Context,
	ttl time.Duration,
	symbol string,
	fetch func(context.Context) (map[string]V, error),
) (V, time.Time, bool) {
	b.once.Do(func() { b.snapshot = cache.NewSnapshot[map[string]V](ttl) })
	var zero V
	values, at, err := b.snapshot.Get(ctx, fetch)
	if err != nil {
		return zero, time.Time{}, false
	}
	value, ok := values[symbol]
	return value, at, ok
}
