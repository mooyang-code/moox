//go:build e2e_external

package test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestExternalGatewayOwnerTrade(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer db.Close()
	logicalSrv := &rpc.LogicalAccountServer{Store: db, LogicalAccounts: &logicalapp.Service{Store: db}}
	console := &rpc.ConsoleServer{
		Store:                db,
		AccountServer:        &rpc.AccountServer{Store: db, Logical: logicalSrv, Accounts: &accountapp.Service{Store: accountapp.Repository{Store: db}}},
		LogicalAccountServer: logicalSrv,
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:11200")
	require.NoError(t, err)
	var submits, closes atomic.Int32
	svc := server.New(server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()), server.WithServiceName("trpc.moox.trade.TradeConsoleService"), server.WithListener(listener), server.WithFilter(filter.GetServer(spacecontext.SpaceFilterName)), server.WithFilter(func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		switch req.(type) {
		case *tradepb.SubmitOrderReq:
			submits.Add(1)
		case *tradepb.CloseTargetSessionReq:
			closes.Add(1)
		}
		return next(ctx, req)
	}))
	tradepb.RegisterTradeConsoleServiceService(svc, console)
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	defer func() { _ = svc.Close(nil); <-done }()
	proxy := tradepb.NewTradeConsoleServiceClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(3*time.Second))
	var created *tradepb.CreateTradingAccountRsp
	require.Eventually(t, func() bool {
		created, err = proxy.CreateTradingAccount(context.Background(), &tradepb.CreateTradingAccountReq{
			Name: "gateway-paper", LogicalAccountName: "gateway-strategy", Exchange: "binance", Markets: []string{"spot"}, SettlementAsset: "USDT",
			Paper:       &tradepb.PaperConfig{InitialBalance: "100000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0"},
			ControlMode: tradepb.ControlMode_CONTROL_MODE_STRATEGY,
		}, client.WithMetaData("X-Space-Id", []byte("space-gateway-e2e")))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created)
	id := created.GetLogicalAccount().GetLogicalAccountId()
	require.NotEmpty(t, id)
	require.NoError(t, os.WriteFile(filepath.Join(coord, "logical-id"), []byte(id), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "trade-ready"), []byte(listener.Addr().String()), 0600))
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(coord, "gateway-done")); return e == nil }, 45*time.Second, 25*time.Millisecond)
	require.Zero(t, submits.Load(), "SubmitOrder must never reach Trade")
	require.Positive(t, closes.Load())
	account, err := db.GetLogicalAccount(context.Background(), "space-gateway-e2e", id)
	require.NoError(t, err)
	require.Equal(t, int64(1_700_000_000_000), account.ClosedThrough, "关闭会话推进了组合的关闭到")
	t.Logf("real native RPC -> Paper Store: account=%s closes=%d SubmitOrder=%d; closed_through=%d", id, closes.Load(), submits.Load(), account.ClosedThrough)
}
