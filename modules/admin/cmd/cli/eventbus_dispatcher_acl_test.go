package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	yamlv3 "gopkg.in/yaml.v3"
)

// 全新安装能连上 EventBus：用生成的 users.yaml 起真实的 NATS 服务，事件中心角色按生产用的消费者配置创建全部 durable、
// 拉取并确认消息；它不能发布任何事件主题；Collector 角色能发布下架事件。
func TestGeneratedACLLetsCallbackDispatcherConsumeEveryItsEventAndPublishNothing(t *testing.T) {
	tokens := map[string]string{}
	for index, role := range eventBusRoles {
		tokens[role] = fmt.Sprintf("token-%d-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", index)
	}
	var parsed struct {
		Users []struct {
			Username    string `yaml:"username"`
			Password    string `yaml:"password"`
			Permissions struct {
				Publish   struct{ Allow []string } `yaml:"publish"`
				Subscribe struct{ Allow []string } `yaml:"subscribe"`
			} `yaml:"permissions"`
		} `yaml:"users"`
	}
	require.NoError(t, yamlv3.Unmarshal([]byte(usersYAML(tokens)), &parsed))
	users := make([]*natsserver.User, 0, len(parsed.Users))
	for _, item := range parsed.Users {
		users = append(users, &natsserver.User{
			Username: item.Username, Password: item.Password,
			Permissions: &natsserver.Permissions{
				Publish:   &natsserver.SubjectPermission{Allow: append([]string{}, item.Permissions.Publish.Allow...)},
				Subscribe: &natsserver.SubjectPermission{Allow: item.Permissions.Subscribe.Allow},
			},
		})
	}
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), Users: users})
	require.NoError(t, err)
	server.Start()
	require.True(t, server.ReadyForConnections(5*time.Second))
	t.Cleanup(server.Shutdown)

	admin, err := nats.Connect(server.ClientURL(), nats.UserInfo("eventbus-internal-admin", tokens["eventbus-internal-admin"]))
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	adminJS, err := admin.JetStream()
	require.NoError(t, err)
	for _, stream := range []nats.StreamConfig{
		{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.dataset.period.completed.v1.>", "moox.event.storage.view.data.ready.v2.>"}},
		{Name: "MOOX_COLLECTOR", Subjects: []string{"moox.event.collector.kline.backfilled.v1.>", "moox.event.collector.instrument.delist_scheduled.v1.>"}},
		{Name: "MOOX_TRADE", Subjects: []string{"moox.event.trade.target.weight_requested.v1.>"}, Retention: nats.WorkQueuePolicy},
	} {
		stream := stream
		_, err := adminJS.AddStream(&stream)
		require.NoError(t, err, stream.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)

	// Collector 角色能发布下架事件。
	collectorClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "collector-acl-e2e", Username: "collector-eventbus", Password: tokens["collector-eventbus"]})
	require.NoError(t, err)
	defer collectorClient.Close()
	publisher, err := events.NewPublisher(collectorClient, registry)
	require.NoError(t, err)
	delistAt := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	_, err = publisher.Publish(ctx, events.InstrumentDelistScheduled, &storagepb.InstrumentDelistScheduled{
		Exchange: "binance", Market: events.DelistMarketSpot, ExchangeSymbol: "FOOUSDT", DelistAt: timestamppb.New(delistAt),
		Source: events.DelistSourceManual, FirstSeenAt: timestamppb.New(delistAt.Add(-time.Hour)), Scope: events.DelistScopeAll,
	}, events.PublishOptions{
		EventID:    events.InstrumentDelistEventID("binance", events.DelistMarketSpot, "FOOUSDT", events.DelistScopeAll, delistAt),
		OccurredAt: delistAt.Add(-time.Hour), SpaceID: events.DelistSystemSpaceID, SubjectID: events.InstrumentDelistSubjectID("binance", events.DelistMarketSpot, "FOOUSDT"),
	})
	require.NoError(t, err)

	// 事件中心角色：按事件中心的消费者配置为每个可订阅事件创建自己的 durable。
	dispatcherClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "dispatcher-acl-e2e", Username: "callback-dispatcher-eventbus", Password: tokens["callback-dispatcher-eventbus"]})
	require.NoError(t, err)
	defer dispatcherClient.Close()
	for _, event := range events.CallbackDispatcherEvents {
		policy := nats.DeliverNewPolicy
		if event.Name() == events.InstrumentDelistScheduled.Name() {
			policy = nats.DeliverAllPolicy
		}
		consumer, err := events.NewConsumer(ctx, dispatcherClient, registry, events.ConsumerConfig{
			Name: events.CallbackDispatcherConsumerName(event), Event: event,
			AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: 500 * time.Millisecond, DeliverPolicy: policy, DeliverDecodeErrors: true,
		})
		require.NoError(t, err, event.Name())
		if event.Name() == events.InstrumentDelistScheduled.Name() {
			deliveries, err := consumer.Fetch(ctx, 1)
			require.NoError(t, err)
			require.Len(t, deliveries, 1, "能拉取到 Collector 发布的下架事件")
			require.NoError(t, jetstream.ApplyHandlerResult(ctx, deliveries[0], jetstream.HandlerResult{Decision: jetstream.ACK}), "能确认消息")
		}
		require.NoError(t, consumer.Close(), event.Name())
	}

	// 不属于它的 durable 创建不了；任何事件主题都不能发布。
	_, err = events.NewConsumer(ctx, dispatcherClient, registry, events.ConsumerConfig{
		Name: "callback_dispatcher_not_owned", Event: events.InstrumentDelistScheduled,
		AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: 500 * time.Millisecond, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.Error(t, err)
	raw, err := nats.Connect(server.ClientURL(), nats.UserInfo("callback-dispatcher-eventbus", tokens["callback-dispatcher-eventbus"]))
	require.NoError(t, err)
	t.Cleanup(raw.Close)
	rawJS, err := raw.JetStream()
	require.NoError(t, err)
	for _, subject := range []string{
		"moox.event.collector.instrument.delist_scheduled.v1.mooxsys.x",
		"moox.event.collector.kline.backfilled.v1.crypto.x",
		"moox.event.storage.view.data.ready.v2.crypto.x",
		"moox.event.trade.target.weight_requested.v1.crypto.x",
	} {
		_, err := rawJS.Publish(subject, []byte("x"), nats.AckWait(time.Second))
		require.Error(t, err, subject)
	}
}
