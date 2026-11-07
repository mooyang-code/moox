package binance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// sessionLikeHandler 模拟会话：OnAccountSnapshot 进来后要先拿 opMu（session.applyEvent），
// 而同步线程是持着 opMu 去调 GetAccountSnapshot 的。
type sessionLikeHandler struct {
	recordingHandler
	opMu    *sync.Mutex
	entered chan struct{}
}

func (h *sessionLikeHandler) OnAccountSnapshot(ctx context.Context, value exchange.AccountSnapshot) error {
	close(h.entered)
	h.opMu.Lock()
	defer h.opMu.Unlock()
	return h.recordingHandler.OnAccountSnapshot(ctx, value)
}

// 私有流线程在回调里等 opMu 时，持着 opMu 的同步线程调 GetAccountSnapshot 不能被借款表的锁卡住。
func TestPortfolioMarginMarginEventDoesNotDeadlockWithSync(t *testing.T) {
	_, server := newPMServer(t, map[string]string{
		"GET /papi/v1/balance": samplePath(t, "papi_balance.json"),
		"GET /papi/v1/account": samplePath(t, "papi_account.json"),
	})
	pm := testPM(server, exchange.MarketTypeSpot)

	opMu := &sync.Mutex{}
	opMu.Lock() // 同步线程先持有 opMu

	handler := &sessionLikeHandler{opMu: opMu, entered: make(chan struct{})}
	event := `{"e":"outboundAccountPosition","E":1564034571105,"u":1564034571073,"B":[{"a":"USDT","f":"90","l":"1"}]}`
	streamDone := make(chan error, 1)
	go func() { streamDone <- pm.dispatchPrivate(context.Background(), []byte(event), handler) }()
	<-handler.entered // 私有流线程正在回调里等 opMu

	syncDone := make(chan error, 1)
	go func() {
		_, err := pm.GetAccountSnapshot(context.Background())
		syncDone <- err
	}()
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("GetAccountSnapshot 失败：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("同步线程卡在借款表的锁上：私有流线程持锁回调")
	}
	opMu.Unlock()
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("私有流事件处理失败：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("私有流线程没有返回")
	}
}
