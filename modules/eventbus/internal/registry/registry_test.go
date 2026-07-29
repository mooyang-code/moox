package registry

import (
	"context"
	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"net"
	"testing"
	"time"
)

func TestReconcileCreateNoOpAndUnsafeChanges(t *testing.T) {
	c := repositoryConfig(t)
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
	r, err := New(js, c)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Streams != len(c.Streams) || result.KV != len(c.KV) {
		t.Fatalf("result = %#v", result)
	}
	if _, err := r.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if _, err := js.AddConsumer("MOOX_CLOUDNODE_EXEC", &nats.ConsumerConfig{Name: "keep", Durable: "keep", FilterSubject: "moox.cloudnode.synthetic.obsolete.v1.>", AckPolicy: nats.AckExplicitPolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile with consumer: %v", err)
	}
	if _, err := js.ConsumerInfo("MOOX_CLOUDNODE_EXEC", "keep"); err != nil {
		t.Fatalf("consumer state was not preserved: %v", err)
	}
	originalTTL := c.KV[0].MaxAge
	c.KV[0].MaxAge = originalTTL / 2
	if _, err := r.Reconcile(ctx); err != nil {
		t.Fatalf("safe KV TTL update: %v", err)
	}
	if info, err := js.StreamInfo("KV_" + c.KV[0].Bucket); err != nil || info.Config.MaxAge != c.KV[0].MaxAge {
		t.Fatalf("KV TTL was not reconciled: %v", err)
	}
	c.KV[0].History = 2
	if _, err := r.Reconcile(ctx); err == nil {
		t.Fatal("KV history change was accepted")
	}
	c.KV[0].History = 1
	c.KV[0].MaxAge = originalTTL
	if _, err := js.Publish("moox.observability.host.snapshot.reported.v1", []byte("metric")); err != nil {
		t.Fatal(err)
	}
	c.Streams[1].Subjects = []string{"moox.observability.other.>"}
	if _, err := r.Reconcile(ctx); err == nil {
		t.Fatal("subject removal was accepted")
	}
}

func TestTopicCoverageAndKVTTL(t *testing.T) {
	c := repositoryConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Streams = append(c.Streams, config.StreamConfig{Name: "MOOX_OBSERVABILITY_DUP", Subjects: []string{"moox.observability.>"}, Retention: "limits", Storage: "file", Replicas: 1})
	if err := c.Validate(); err == nil {
		t.Fatal("overlapping stream accepted")
	}
}

func TestReconcileDoesNotCreateConsumers(t *testing.T) {
	c := repositoryConfig(t)
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
	js, _ := nc.JetStream()
	r, _ := New(js, c)
	if _, err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stream := range c.Streams {
		info, err := js.StreamInfo(stream.Name)
		if err != nil {
			t.Fatal(err)
		}
		if info.State.Consumers != 0 {
			t.Fatalf("stream %s consumers=%d", stream.Name, info.State.Consumers)
		}
	}
}

func TestSubjectPatternOverlapHonorsGreaterThanSemantics(t *testing.T) {
	if subjectPatternsOverlap("foo.>", "foo") {
		t.Fatal("foo.> must not match bare foo")
	}
	if !subjectPatternsOverlap("foo.>", "foo.bar") {
		t.Fatal("foo.> should overlap foo.bar")
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

func TestEnabledTopics(t *testing.T) {
	cfg := repositoryConfig(t)
	assert.Greater(t, enabledTopics(cfg), 0)
}

func TestSubjectRemoved(t *testing.T) {
	assert.True(t, subjectRemoved([]string{"a", "b"}, []string{"a"}))
	assert.False(t, subjectRemoved([]string{"a"}, []string{"a", "b"}))
}

func TestSameStrings(t *testing.T) {
	assert.True(t, sameStrings([]string{"a", "b"}, []string{"a", "b"}))
	assert.False(t, sameStrings([]string{"a"}, []string{"b"}))
}

func TestSubjectPatternsOverlapVariants(t *testing.T) {
	assert.True(t, subjectPatternsOverlap("a.>", "a.b.c"))
	assert.False(t, subjectPatternsOverlap("a.b", "a.c"))
}

func repositoryConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
