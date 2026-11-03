package exchange

import (
	"context"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// priceBoardTTL 是全市场报价的复用时间。目标换算要一次取几百个标的的价格，
// 逐个请求要几十秒，全市场接口一次就够，而且只占很少的限频额度。
const priceBoardTTL = 2 * time.Second

// PriceBoard 缓存全市场最新价，并发调用只请求一次。
type PriceBoard struct {
	mu     sync.Mutex
	at     time.Time
	prices map[string]shared.Decimal
}

// Get 返回标的的最新价和读取时间。全市场接口失败或没有这个标的时 ok 为 false，调用方退回单标的接口。
func (b *PriceBoard) Get(
	ctx context.Context,
	symbol string,
	fetch func(context.Context) (map[string]shared.Decimal, error),
) (shared.Decimal, time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.at) >= priceBoardTTL {
		prices, err := fetch(ctx)
		if err != nil {
			return shared.Decimal{}, time.Time{}, false
		}
		b.prices, b.at = prices, time.Now().UTC()
	}
	price, ok := b.prices[symbol]
	return price, b.at, ok && price.Cmp(shared.Zero()) > 0
}

// bookBoardTTL 是全市场买一卖一的复用时间。模拟盘每笔子单落库和提交前都要取一次可成交价，
// 逐个请求盘口一批 16 单要几十次往返。
const bookBoardTTL = time.Second

// Book 是一个标的的买一卖一。
type Book struct {
	Bid shared.Decimal
	Ask shared.Decimal
}

// BookBoard 缓存全市场买一卖一，并发调用只请求一次。
type BookBoard struct {
	mu    sync.Mutex
	at    time.Time
	books map[string]Book
}

// Get 返回标的的买一卖一和读取时间。全市场接口失败或没有这个标的时 ok 为 false，调用方退回单标的接口。
func (b *BookBoard) Get(
	ctx context.Context,
	symbol string,
	fetch func(context.Context) (map[string]Book, error),
) (Book, time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.at) >= bookBoardTTL {
		books, err := fetch(ctx)
		if err != nil {
			// 失败也缓存一个周期，期间直接退回单标的接口，不反复拉全市场。
			b.books, b.at = nil, time.Now().UTC()
			return Book{}, time.Time{}, false
		}
		b.books, b.at = books, time.Now().UTC()
	}
	book, ok := b.books[symbol]
	return book, b.at, ok && book.Bid.Cmp(shared.Zero()) > 0 && book.Ask.Cmp(shared.Zero()) > 0
}
