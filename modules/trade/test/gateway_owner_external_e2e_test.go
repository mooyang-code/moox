//go:build e2e_external

package test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/filter"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestExternalGatewayOwnerTrade(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer db.Close()
	console := &rpc.ConsoleServer{Store: db, Paper: &papersimulation.Service{Store: db}, LogicalAccountServer: &rpc.LogicalAccountServer{Store: db, LogicalAccounts: &logicalapp.Service{Store: db}}}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	var submits, claims, releases atomic.Int32
	svc := server.New(server.WithNetwork("tcp"), server.WithProtocol("http"), server.WithServiceName("trpc.moox.trade.TradeConsoleService"), server.WithListener(listener), server.WithFilter(filter.GetServer(spacecontext.SpaceFilterName)), server.WithFilter(func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		switch req.(type) {
		case *tradepb.SubmitOrderReq:
			submits.Add(1)
		case *tradepb.ClaimLogicalAccountOwnerReq:
			claims.Add(1)
		case *tradepb.ReleaseLogicalAccountOwnerReq:
			releases.Add(1)
		}
		return next(ctx, req)
	}))
	tradepb.RegisterTradeConsoleServiceService(svc, console)
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	defer func() { _ = svc.Close(nil); <-done }()
	proxy := tradepb.NewTradeConsoleServiceClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("http"), client.WithTimeout(3*time.Second))
	header := &thttp.ClientReqHeader{Header: make(http.Header)}
	header.Header.Set("X-Space-Id", "space-gateway-e2e")
	var created *tradepb.CreatePaperSimulationRsp
	require.Eventually(t, func() bool {
		created, err = proxy.CreatePaperSimulation(context.Background(), &tradepb.CreatePaperSimulationReq{AccountName: "gateway-paper", LogicalAccountName: "gateway-strategy", Exchange: tradepb.Exchange_EXCHANGE_BINANCE, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT", InitialBalance: "100000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0", ControlMode: tradepb.ControlMode_CONTROL_MODE_STRATEGY}, client.WithReqHead(header))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created)
	id := created.GetLogicalAccount().GetLogicalAccountId()
	require.NotEmpty(t, id)
	require.NoError(t, os.WriteFile(filepath.Join(coord, "logical-id"), []byte(id), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "trade-ready"), []byte(listener.Addr().String()), 0600))
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(coord, "strategy-done")); return e == nil }, 45*time.Second, 25*time.Millisecond)
	require.Zero(t, submits.Load(), "SubmitOrder must never reach Trade")
	require.Positive(t, claims.Load())
	require.Positive(t, releases.Load())
	account, err := db.GetLogicalAccount(context.Background(), "space-gateway-e2e", id)
	require.NoError(t, err)
	require.Empty(t, account.OwnerInstanceID)
	require.Empty(t, account.OwnerSessionID)
	t.Logf("real HTTP RPC -> Paper Store: account=%s claims=%d releases=%d SubmitOrder=%d; owner released", id, claims.Load(), releases.Load(), submits.Load())
}
