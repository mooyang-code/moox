package bootstrap

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/delist"
	"github.com/mooyang-code/moox/modules/collector/internal/eventbus"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	binanceclient "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
)

// eventBusRuntime 是 Collector 级的 EventBus 接入：共享连接和独立的发件箱中继。
// K 线合成、下架检测等事件生产者都往同一个发件箱写，由中继发布；中继不依赖 K 线合成是否开启。
type eventBusRuntime struct {
	hub       *eventbus.Hub
	relay     *eventbus.Relay
	attention *attention.Attention
}

// relayInterval 是中继轮询发件箱的间隔；新事件封存后会立即唤醒它。
const relayInterval = 10 * time.Second

// newEventBusRuntime 在配置了 event_bus 时创建 EventBus 接入，否则返回 nil（不发布任何事件）。
func newEventBusRuntime(cfg *Config, dbm *store.Store) (*eventBusRuntime, error) {
	if !cfg.EventBus.Enabled() {
		return nil, nil
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	hub := eventbus.NewHub(cfg.EventBus, registry)
	attn := attention.New(prometheus.DefaultRegisterer)
	hub.Attention = attn
	relay := eventbus.NewRelay(dbm.Resample(), registry, hub, relayInterval)
	relay.Attention = attn
	return &eventBusRuntime{hub: hub, relay: relay, attention: attn}, nil
}

// start 启动中继循环（连接在循环里建立并自动重试）。必须在各生产者注册完连接成功后的回调之后调用。
func (b *eventBusRuntime) start(process *Runtime) {
	if b == nil {
		return
	}
	process.beforeClose = append(process.beforeClose, b.hub.Close)
	process.launch(func() { _ = b.relay.Run(process.ctx) })
}

// registerDelistDetection 启动下架检测：定时轮询币安现货下架计划、币安 U 本位合约信息和 OKX 永续合约信息，
// 并让 CollectMgr 接受手工登记。没有配置 EventBus 时不检测（发现了也发布不了），手工登记返回明确的错误。
// delist.auto_detect 为 false（没有出口代理，三个来源都连不上）时只接手工登记，不轮询来源。
func registerDelistDetection(cfg *Config, dbm *store.Store, process *Runtime, bus *eventBusRuntime, svc *collectsvc.Service) error {
	if bus == nil {
		log.Warn("collector 没有配置 event_bus，下架检测不启动")
		return nil
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	registrar := &delist.Registrar{Repo: dbm.DelistNotices(), Registry: registry, Wake: bus.relay.Wake}
	svc.SetDelistRegistrar(registrar)
	if !cfg.Delist.AutoDetect {
		log.Info("collector 的 delist.auto_detect 已关闭（没有出口代理），不自动检测下架，手工登记照常可用")
		return nil
	}

	http, err := httpclient.NewEgressHTTPClient(process.gateway, cfg.EgressProxy.Domains)
	if err != nil {
		return fmt.Errorf("initialize delist HTTP client: %w", err)
	}
	process.beforeClose = append(process.beforeClose, func() error { http.Close(); return nil })
	detector := &delist.Detector{
		Sources: []delist.Source{
			&delist.BinanceSpot{Client: binanceclient.NewClient(http), Keys: &delist.GatewayKeys{Gateway: process.gateway}},
			&delist.BinanceSwap{Client: http},
			&delist.OKXSwap{Client: http},
		},
		Registrar: registrar,
		Attention: bus.attention,
	}
	process.launch(func() { _ = detector.Run(process.ctx) })
	return nil
}
