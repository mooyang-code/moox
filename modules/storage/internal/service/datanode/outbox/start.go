package outbox

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

// StartWithEventBus 启动 Outbox 转发，EventBus 连没连上都先跑起来：积压采样不依赖连接，
// 发布失败时由转发协程自己重连。返回的函数停止转发并等待退出。
func StartWithEventBus(ctx context.Context, store *pebble.Store, config jetstream.Config) (func(), error) {
	client := jetstream.NewDisconnected(config)
	relay, err := NewRelay(store, NewJetStreamPublisher(client), RelayOptions{})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	relay.Start(ctx)
	connectCtx, cancel := context.WithCancel(ctx)
	connected := make(chan struct{})
	go func() {
		defer close(connected)
		// 先连一次，空闲时也能尽快就绪；连不上就交给转发协程在发布失败后重连。
		if err := client.Reconnect(connectCtx); err != nil {
			log.Warnf("storage primary 连接 EventBus 失败，Outbox 转发会在发布时重连: %v", err)
		}
	}()
	return func() {
		cancel()
		<-connected
		relay.Close()
		_ = client.Close()
	}, nil
}
