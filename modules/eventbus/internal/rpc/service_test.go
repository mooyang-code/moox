package rpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	eventbuspb "github.com/mooyang-code/moox/modules/eventbus/proto/eventbusgen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestReadOnlyManagementPaginationAndStableOrdering(t *testing.T) {
	c := rpcTestConfig(t)
	for i := range c.Streams {
		c.Streams[i].MaxBytes = 1 << 20
	}
	c.Broker.StoreDir = t.TempDir()
	c.Broker.Port = freePort(t)
	b, err := broker.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown(context.Background())
	nc, err := nats.Connect(b.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(js, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	svc := New(js, c, Options{Ready: func() bool { return true }, Connections: b.Connections})
	list, err := svc.ListEvents(ctx, &eventbuspb.ListEventsReq{Page: &commonpb.Page{Page: 1, Size: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if list.RetInfo.GetCode() != commonpb.ErrorCode_SUCCESS || len(list.Events) != 2 || !list.PageResult.GetHasMore() {
		t.Fatalf("unexpected list response: %#v", list)
	}
	if list.Events[0].GetSubjectPattern() >= list.Events[1].GetSubjectPattern() {
		t.Fatalf("events not sorted: %q, %q", list.Events[0].GetSubjectPattern(), list.Events[1].GetSubjectPattern())
	}
	for _, event := range list.GetEvents() {
		if event.GetOwner() == "" {
			t.Fatalf("event %q is missing owner", event.GetEventName())
		}
	}
	streams, err := svc.ListStreams(ctx, &eventbuspb.ListStreamsReq{})
	if err != nil || len(streams.Streams) != len(c.Streams) {
		t.Fatalf("streams response: %v %#v", err, streams)
	}
	if _, err := svc.GetConsumer(ctx, &eventbuspb.GetConsumerReq{Stream: "MOOX_STORAGE", Name: "missing"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetConsumer(ctx, &eventbuspb.GetConsumerReq{Stream: "MOOX_STORAGE", Name: "missing"})
	if got.RetInfo.GetCode() != commonpb.ErrorCode_NOT_FOUND {
		t.Fatalf("missing consumer code: %#v", got.RetInfo)
	}
}

func TestGetOverviewAndListConsumers(t *testing.T) {
	c := rpcTestConfig(t)
	for i := range c.Streams {
		c.Streams[i].MaxBytes = 1 << 20
	}
	c.Broker.StoreDir = t.TempDir()
	c.Broker.Port = freePort(t)
	b, err := broker.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown(context.Background())
	nc, err := nats.Connect(b.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(js, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	svc := nativeManagement(t, New(js, c, Options{Ready: func() bool { return true }, Connections: b.Connections}))
	overview, err := svc.GetOverview(ctx, &eventbuspb.GetOverviewReq{})
	if err != nil || overview.RetInfo.GetCode() != commonpb.ErrorCode_SUCCESS || overview.Overview.GetStreams() == 0 {
		t.Fatalf("overview=%#v err=%v", overview, err)
	}
	consumers, err := svc.ListConsumers(ctx, &eventbuspb.ListConsumersReq{})
	if err != nil || consumers.RetInfo.GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("consumers=%#v err=%v", consumers, err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func rpcTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func nativeManagement(t *testing.T, service *Service) eventbuspb.EventBusMgrClientProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	svc := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()))
	eventbuspb.RegisterEventBusMgrService(svc, service)
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	t.Cleanup(func() {
		_ = svc.Close(nil)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("EventBus native listener did not stop")
		}
	})
	return eventbuspb.NewEventBusMgrClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithProtocol("trpc"), client.WithNetwork("tcp"), client.WithTimeout(3*time.Second), client.WithTransport(transport.NewClientTransport()), client.WithDisableConnectionPool())
}
