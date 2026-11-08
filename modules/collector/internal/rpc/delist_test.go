package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/delist"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

type fakeDelistRegistrar struct {
	notices []delist.Notice
	created bool
	err     error
}

func (f *fakeDelistRegistrar) Register(_ context.Context, notice delist.Notice) (bool, string, error) {
	f.notices = append(f.notices, notice)
	return f.created, "delist-event-1", f.err
}

func TestRegisterDelistNotice(t *testing.T) {
	service, _ := newReregisterFixture(t)
	ctx := context.Background()
	delistAt := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)

	// 没有配置 EventBus：明确报错，而不是悄悄登记了发不出去。
	rsp, err := service.RegisterDelistNotice(ctx, &pb.RegisterDelistNoticeReq{Exchange: "binance", Market: "SPOT", ExchangeSymbol: "FOOUSDT", DelistAtMs: delistAt.UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "event_bus")

	registrar := &fakeDelistRegistrar{created: true}
	service.SetDelistRegistrar(registrar)
	for name, req := range map[string]*pb.RegisterDelistNoticeReq{
		"没有下架时间":  {Exchange: "binance", Market: "SPOT", ExchangeSymbol: "FOOUSDT"},
		"大写交易所":   {Exchange: "BINANCE", Market: "SPOT", ExchangeSymbol: "FOOUSDT", DelistAtMs: delistAt.UnixMilli()},
		"市场不对":    {Exchange: "binance", Market: "FUTURES", ExchangeSymbol: "FOOUSDT", DelistAtMs: delistAt.UnixMilli()},
		"代码为空":    {Exchange: "binance", Market: "SWAP", DelistAtMs: delistAt.UnixMilli()},
		"不支持的交易所": {Exchange: "kraken", Market: "SPOT", ExchangeSymbol: "FOOUSDT", DelistAtMs: delistAt.UnixMilli()},
	} {
		rsp, err := service.RegisterDelistNotice(ctx, req)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
	}
	require.Empty(t, registrar.notices)

	rsp, err = service.RegisterDelistNotice(ctx, &pb.RegisterDelistNoticeReq{Exchange: " binance ", Market: "SWAP", ExchangeSymbol: "FOOUSDT", DelistAtMs: delistAt.UnixMilli(), PaperOnly: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.True(t, rsp.GetCreated())
	require.Equal(t, "delist-event-1", rsp.GetEventId())
	require.Equal(t, []delist.Notice{{Exchange: "binance", Market: "SWAP", Symbol: "FOOUSDT", DelistAt: delistAt, Source: events.DelistSourceManual, Scope: events.DelistScopePaperOnly}}, registrar.notices)

	// 手工登记时代码写成小写：统一成大写再登记，下游按大写判断“同一个标的”。
	registrar.notices = nil
	rsp, err = service.RegisterDelistNotice(ctx, &pb.RegisterDelistNoticeReq{Exchange: "okx", Market: "SWAP", ExchangeSymbol: " foo-usdt-swap ", DelistAtMs: delistAt.UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, "FOO-USDT-SWAP", registrar.notices[0].Symbol)

	registrar.err = errors.New("数据库错误细节")
	rsp, err = service.RegisterDelistNotice(ctx, &pb.RegisterDelistNoticeReq{Exchange: "okx", Market: "SWAP", ExchangeSymbol: "FOO-USDT-SWAP", DelistAtMs: delistAt.UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.NotContains(t, rsp.GetRetInfo().GetMsg(), "数据库错误细节")
}
