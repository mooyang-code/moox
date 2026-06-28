// binance-verify 直接用 binance 适配器对币安发真实只读请求，逐接口验证。
//
// 用法：
//   go run ./cmd/binance-verify -key=<API_KEY> -secret=<API_SECRET> [-swap]
//
// 只做只读请求（Ping/GetBalances/GetInstruments/GetTradeFee/ListOrders/ListTrades/ListPositions），
// 不下单/撤单，避免资金影响。若 -swap 给出，则把 key/secret 互换后再试一次。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all"
)

func main() {
	key := flag.String("key", "", "binance api key")
	secret := flag.String("secret", "", "binance api secret")
	symbol := flag.String("symbol", "BTCUSDT", "symbol for fee/orders/trades")
	swap := flag.Bool("swap", false, "交换 key/secret 后再试")
	flag.Parse()

	if *key == "" || *secret == "" {
		fmt.Fprintln(os.Stderr, "usage: binance-verify -key=... -secret=...")
		os.Exit(2)
	}

	cred := exchange.Credential{APIKey: *key, APISecret: *secret}
	runAll(cred, *symbol)

	if *swap {
		fmt.Println("\n===== 交换 key/secret 再试 =====")
		cred = exchange.Credential{APIKey: *secret, APISecret: *key}
		runAll(cred, *symbol)
	}
}

func runAll(cred exchange.Credential, symbol string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := exchange.New("binance")
	if err != nil {
		fmt.Println("ERR exchange.New:", err)
		return
	}

	// 1. Ping -> GET /api/v3/account (签名)
	lat, err := a.Ping(ctx, cred)
	report("Ping(/api/v3/account)", err, fmt.Sprintf("latency_ms=%d", lat))

	// 2. GetBalances -> 现货余额
	bs, err := a.GetBalances(ctx, cred, exchange.MarketSpot, nil)
	if err != nil {
		report("GetBalances", err, "")
	} else {
		nonzero := 0
		for _, b := range bs {
			if b.Available != "0" || b.Frozen != "0" || b.Total != "0" {
				nonzero++
			}
		}
		report("GetBalances", nil, fmt.Sprintf("total=%d nonzero=%d", len(bs), nonzero))
		for _, b := range bs {
			if b.Available != "0" || b.Frozen != "0" {
				fmt.Printf("    %s: avail=%s frozen=%s total=%s\n", b.Currency, b.Available, b.Frozen, b.Total)
			}
		}
	}

	// 3. GetInstruments -> /api/v3/exchangeInfo (公开)
	ins, err := a.GetInstruments(ctx, exchange.MarketSpot)
	if err != nil {
		report("GetInstruments", err, "")
	} else {
		report("GetInstruments", nil, fmt.Sprintf("count=%d", len(ins)))
		for _, i := range ins {
			if i.Symbol == symbol {
				fmt.Printf("    %s: base=%s quote=%s tick=%s lot=%s minQty=%s minNotional=%s status=%s\n",
					i.Symbol, i.BaseCcy, i.QuoteCcy, i.TickSize, i.LotSize, i.MinQty, i.MinNotional, i.Status)
			}
		}
	}

	// 4. GetTradeFee -> /sapi/v1/asset/tradeFee
	fee, err := a.GetTradeFee(ctx, cred, exchange.MarketSpot, symbol)
	if err != nil {
		report("GetTradeFee", err, "")
	} else {
		report("GetTradeFee", nil, fmt.Sprintf("symbol=%s maker=%s taker=%s", fee.Symbol, fee.Maker, fee.Taker))
	}

	// 5. ListOrders (历史) -> /api/v3/allOrders
	os2, err := a.ListOrders(ctx, cred, &exchange.ListOrdersReq{Market: exchange.MarketSpot, Symbol: symbol, Limit: 5})
	if err != nil {
		report("ListOrders", err, "")
	} else {
		report("ListOrders", nil, fmt.Sprintf("count=%d", len(os2)))
	}

	// 6. ListTrades -> /api/v3/myTrades
	tr, err := a.ListTrades(ctx, cred, &exchange.ListTradesReq{Market: exchange.MarketSpot, Symbol: symbol, Limit: 5})
	if err != nil {
		report("ListTrades", err, "")
	} else {
		report("ListTrades", nil, fmt.Sprintf("count=%d", len(tr)))
		for _, t := range tr {
			fmt.Printf("    trade %s: px=%s qty=%s fee=%s%s side=%s\n", t.TradeID, t.Price, t.Quantity, t.Fee, t.FeeCurrency, t.Side)
		}
	}

	// 7. ListOpenOrders -> /api/v3/openOrders
	oo, err := a.ListOpenOrders(ctx, cred, &exchange.ListOrdersReq{Market: exchange.MarketSpot, Symbol: symbol})
	if err != nil {
		report("ListOpenOrders", err, "")
	} else {
		report("ListOpenOrders", nil, fmt.Sprintf("count=%d", len(oo)))
	}

	// 8. ListPositions -> 现货无持仓，返回空
	ps, err := a.ListPositions(ctx, cred, exchange.MarketSpot, "")
	if err != nil {
		report("ListPositions(spot)", err, "")
	} else {
		report("ListPositions(spot)", nil, fmt.Sprintf("count=%d", len(ps)))
	}
}

func report(name string, err error, extra string) {
	status := "OK"
	if err != nil {
		status = "FAIL"
	}
	if extra != "" {
		fmt.Printf("[%s] %s: %s\n", status, name, extra)
	} else {
		fmt.Printf("[%s] %s\n", status, name)
	}
	if err != nil {
		// 截断错误信息
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		fmt.Printf("    error: %s\n", strings.ReplaceAll(msg, "\n", " "))
	}
}
