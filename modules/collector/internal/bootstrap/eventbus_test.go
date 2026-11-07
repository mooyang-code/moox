package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/eventbus"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func delistFixture(t *testing.T) (*store.Store, *Runtime, *eventBusRuntime, *collectsvc.Service) {
	t.Helper()
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = dbm.Close() })
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	process := newRuntime(context.Background(), nil)
	t.Cleanup(process.cancel)
	bus := &eventBusRuntime{
		relay:     eventbus.NewRelay(dbm.Resample(), registry, nil, time.Second),
		attention: attention.New(prometheus.NewRegistry()),
	}
	return dbm, process, bus, collectsvc.New(dbm, collectsvc.Dependencies{})
}

// 没有出口代理时（delist.auto_detect = false）不轮询三个来源，也不建出口 HTTP 客户端；手工登记照常可用。
func TestDelistAutoDetectOffKeepsManualRegistration(t *testing.T) {
	dbm, process, bus, svc := delistFixture(t)
	cfg := Default()
	cfg.Delist.AutoDetect = false
	require.NoError(t, registerDelistDetection(cfg, dbm, process, bus, svc))
	require.Empty(t, process.beforeClose, "没有建出口 HTTP 客户端")

	rsp, err := svc.RegisterDelistNotice(context.Background(), &pb.RegisterDelistNoticeReq{
		Exchange: "binance", Market: "SPOT", ExchangeSymbol: "FOOUSDT", DelistAtMs: time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC).UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.True(t, rsp.GetCreated())
}

// 默认开启自动检测：建出口 HTTP 客户端（关闭时要释放）并启动轮询。进程先取消，轮询不会真的跑起来。
func TestDelistAutoDetectDefaultsOn(t *testing.T) {
	dbm, process, bus, svc := delistFixture(t)
	process.cancel()
	cfg := Default()
	require.True(t, cfg.Delist.AutoDetect)
	require.NoError(t, registerDelistDetection(cfg, dbm, process, bus, svc))
	require.Len(t, process.beforeClose, 1, "建了出口 HTTP 客户端")
}

func TestLoadDelistAutoDetect(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "delist:\n  auto_detect: false\n"))
	require.NoError(t, err)
	require.False(t, cfg.Delist.AutoDetect)
}
