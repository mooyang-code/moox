//go:build legacy_storage

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
)

func TestReadOnlyManagementPaginationAndStableOrdering(t *testing.T) {
	c := config.Default()
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
	list, err := svc.ListTopics(ctx, &eventbuspb.ListTopicsReq{Page: &commonpb.Page{Page: 1, Size: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if list.RetInfo.GetCode() != commonpb.ErrorCode_SUCCESS || len(list.Topics) != 2 || !list.PageResult.GetHasMore() {
		t.Fatalf("unexpected list response: %#v", list)
	}
	if list.Topics[0].GetTopic() >= list.Topics[1].GetTopic() {
		t.Fatalf("topics not sorted: %q, %q", list.Topics[0].GetTopic(), list.Topics[1].GetTopic())
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
	c := config.Default()
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
