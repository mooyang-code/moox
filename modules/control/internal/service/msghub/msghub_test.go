package msghub

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/types"
)

// TestNewMessage 测试消息创建
func TestNewMessage(t *testing.T) {
	msg := NewMessage("test.subject", []byte("test data"))

	if msg == nil {
		t.Fatal("NewMessage returned nil")
	}

	if msg.ID == "" {
		t.Error("Message ID is empty")
	}

	if msg.Subject != "test.subject" {
		t.Errorf("Expected subject 'test.subject', got '%s'", msg.Subject)
	}

	if string(msg.Data) != "test data" {
		t.Errorf("Expected data 'test data', got '%s'", string(msg.Data))
	}

	if msg.Headers == nil {
		t.Error("Message Headers is nil")
	}
}

// TestMessageHeaders 测试消息头操作
func TestMessageHeaders(t *testing.T) {
	msg := NewMessage("test.subject", []byte("test data"))

	// 测试添加消息头
	msg.AddHeader("X-Test", "test-value")

	// 测试获取消息头
	value := msg.GetHeader("X-Test")
	if value != "test-value" {
		t.Errorf("Expected header value 'test-value', got '%s'", value)
	}

	// 测试不存在的消息头
	value = msg.GetHeader("X-NotExist")
	if value != "" {
		t.Errorf("Expected empty string for non-existent header, got '%s'", value)
	}
}

// TestGenerateMessageID 测试消息ID生成
func TestGenerateMessageID(t *testing.T) {
	id1 := GenerateMessageID()
	id2 := GenerateMessageID()

	if id1 == "" {
		t.Error("GenerateMessageID returned empty string")
	}

	if id2 == "" {
		t.Error("GenerateMessageID returned empty string")
	}

	if id1 == id2 {
		t.Error("GenerateMessageID generated duplicate IDs")
	}

	// xid 生成的ID应该是20个字符
	if len(id1) != 20 {
		t.Errorf("Expected ID length 20, got %d", len(id1))
	}
}

// TestHookChain 测试钩子链
func TestHookChain(t *testing.T) {
	chain := types.NewHookChain()

	if chain.Len() != 0 {
		t.Errorf("Expected empty chain, got length %d", chain.Len())
	}

	// 添加钩子
	executed := false
	hook := func(msg *types.Message) error {
		executed = true
		return nil
	}

	chain.Add(hook)

	if chain.Len() != 1 {
		t.Errorf("Expected chain length 1, got %d", chain.Len())
	}

	// 执行钩子链
	msg := NewMessage("test.subject", []byte("test data"))
	if err := chain.Execute(msg); err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	if !executed {
		t.Error("Hook was not executed")
	}

	// 测试清空
	chain.Clear()
	if chain.Len() != 0 {
		t.Errorf("Expected empty chain after Clear, got length %d", chain.Len())
	}
}

// TestPublisherRegistry 测试Publisher注册表
func TestPublisherRegistry(t *testing.T) {
	reg := newPublisherRegistry()

	// 测试空注册表
	names := reg.List()
	if len(names) != 0 {
		t.Errorf("Expected empty registry, got %d items", len(names))
	}

	// 测试获取不存在的Publisher
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent publisher")
	}
}

// TestConsumerRegistry 测试Consumer注册表
func TestConsumerRegistry(t *testing.T) {
	reg := newConsumerRegistry()

	// 测试空注册表
	names := reg.List()
	if len(names) != 0 {
		t.Errorf("Expected empty registry, got %d items", len(names))
	}

	// 测试获取不存在的Consumer
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent consumer")
	}
}

// TestDefaultConfig 测试默认配置
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	if !config.Server.Enable {
		t.Error("Expected server to be enabled by default")
	}

	if config.Server.Type != "nats" {
		t.Errorf("Expected server type 'nats', got '%s'", config.Server.Type)
	}

	if config.Server.Host != "127.0.0.1" {
		t.Errorf("Expected server host '127.0.0.1', got '%s'", config.Server.Host)
	}

	if config.Server.Port != 4222 {
		t.Errorf("Expected server port 4222, got %d", config.Server.Port)
	}
}

// TestConfigConversion 测试配置转换
func TestConfigConversion(t *testing.T) {
	config := DefaultConfig()

	// 测试转换为ServiceOptions
	svcOpts := config.ToServiceOptions()
	if svcOpts.ServerType != NATSServerType {
		t.Errorf("Expected server type '%s', got '%s'", NATSServerType, svcOpts.ServerType)
	}

	if svcOpts.ServerOpts.Host != config.Server.Host {
		t.Error("Server host mismatch in conversion")
	}

	if svcOpts.ServerOpts.Port != config.Server.Port {
		t.Error("Server port mismatch in conversion")
	}

	// 测试PublisherConfig转换
	pubConfig := PublisherConfig{
		Name:           "test-pub",
		Type:           "nats",
		ServerURL:      "nats://localhost:4222",
		StreamName:     "test-stream",
		StreamSubjects: []string{"test.subject"},
	}

	pubOpts := pubConfig.ToPublisherOptions()
	if pubOpts.ServerURL != pubConfig.ServerURL {
		t.Error("Publisher ServerURL mismatch in conversion")
	}

	if pubOpts.StreamName != pubConfig.StreamName {
		t.Error("Publisher StreamName mismatch in conversion")
	}

	// 测试ConsumerConfig转换
	consumerConfig := ConsumerConfig{
		Name:         "test-consumer",
		Type:         "nats",
		ServerURL:    "nats://localhost:4222",
		StreamName:   "test-stream",
		Subject:      "test.subject",
		ConsumerName: "test-processor",
		MaxInFlight:  100,
		AckWait:      30,
	}

	handler := func(msg *types.Message) error {
		return nil
	}

	consumerOpts := consumerConfig.ToConsumerOptions(handler)
	if consumerOpts.ServerURL != consumerConfig.ServerURL {
		t.Error("Consumer ServerURL mismatch in conversion")
	}

	if consumerOpts.Subject != consumerConfig.Subject {
		t.Error("Consumer Subject mismatch in conversion")
	}

	if consumerOpts.AckWait != 30*time.Second {
		t.Errorf("Expected AckWait 30s, got %v", consumerOpts.AckWait)
	}
}

// BenchmarkGenerateMessageID 测试消息ID生成性能
func BenchmarkGenerateMessageID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = GenerateMessageID()
	}
}

// BenchmarkNewMessage 测试消息创建性能
func BenchmarkNewMessage(b *testing.B) {
	data := []byte("test data")
	for i := 0; i < b.N; i++ {
		_ = NewMessage("test.subject", data)
	}
}

// BenchmarkHookChainExecute 测试钩子链执行性能
func BenchmarkHookChainExecute(b *testing.B) {
	chain := types.NewHookChain()
	chain.Add(func(msg *types.Message) error {
		return nil
	})

	msg := NewMessage("test.subject", []byte("test data"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = chain.Execute(msg)
	}
}
