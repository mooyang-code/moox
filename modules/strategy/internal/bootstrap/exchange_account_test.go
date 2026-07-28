package bootstrap

import (
	"context"
	"net"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/server"
)

type exchangeAccountServiceStub struct {
	tradepb.UnimplementedExchangeAccountService
	spaceID chan string
}

func (s *exchangeAccountServiceStub) GetAccount(
	ctx context.Context,
	req *tradepb.GetAccountReq,
) (*tradepb.GetAccountRsp, error) {
	spaceID := ""
	if request := thttp.Request(ctx); request != nil {
		spaceID = request.Header.Get("X-Space-Id")
	}
	s.spaceID <- spaceID
	return &tradepb.GetAccountRsp{
		RetInfo: &tradepb.RetInfo{},
		Account: &tradepb.ExchangeAccount{
			ExchangeAccountId: req.GetExchangeAccountId(),
			ExecutionMode:     tradepb.ExecutionMode_EXECUTION_MODE_PAPER,
		},
	}, nil
}

func TestExchangeAccountModeClientUsesTradeHTTPProtocolAndBindingSpace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &exchangeAccountServiceStub{spaceID: make(chan string, 1)}
	tradeServer := server.New(
		server.WithNetwork("tcp"),
		server.WithProtocol("http"),
		server.WithServiceName("trpc.moox.trade.ExchangeAccountService"),
		server.WithListener(listener),
	)
	tradepb.RegisterExchangeAccountServiceService(tradeServer, service)
	serveErr := make(chan error, 1)
	go func() { serveErr <- tradeServer.Serve() }()
	t.Cleanup(func() {
		tradeServer.Close(nil)
		select {
		case <-serveErr:
		case <-time.After(time.Second):
		}
	})

	mode, err := newExchangeAccountModeClient("ip://"+listener.Addr().String()).
		ExecutionMode(context.Background(), "space-1", "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if mode != "paper" {
		t.Fatalf("mode = %q, want paper", mode)
	}
	select {
	case spaceID := <-service.spaceID:
		if spaceID != "space-1" {
			t.Fatalf("X-Space-Id = %q, want space-1", spaceID)
		}
	case <-time.After(time.Second):
		t.Fatal("Trade service did not receive request")
	}
}
