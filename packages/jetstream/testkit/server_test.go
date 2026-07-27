package testkit

import (
	"testing"

	"github.com/nats-io/nats.go"
)

func TestServerStartsJetStreamAndAddsStream(t *testing.T) {
	server := Start(t)
	if server.URL() == "" {
		t.Fatal("URL() is empty")
	}
	info := server.AddStream(t, &nats.StreamConfig{
		Name:     "TEST",
		Subjects: []string{"test.>"},
		Storage:  nats.MemoryStorage,
	})
	if info.Config.Name != "TEST" {
		t.Fatalf("stream name = %q, want TEST", info.Config.Name)
	}
	if _, err := server.JetStream().StreamInfo("TEST"); err != nil {
		t.Fatalf("StreamInfo() error = %v", err)
	}
}
