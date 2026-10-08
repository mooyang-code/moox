package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/jetstream"
)

func TestDecideMapsHandlerOutcomes(t *testing.T) {
	if got := decide(nil); got.Decision != jetstream.ACK {
		t.Fatalf("全部落库应 ACK：%+v", got)
	}
	if got := decide(errors.New("sqlite busy")); got.Decision != jetstream.RETRY || got.Delay <= 0 {
		t.Fatalf("可重试错误应带退避的 RETRY：%+v", got)
	}
	if got := decide(fmt.Errorf("%w：事件为空", trigger.ErrInvalidEvent)); got.Decision != jetstream.TERM {
		t.Fatalf("事件不可处理应 TERM：%+v", got)
	}
}

func TestHandleDeliveryRejectsUndecodableMessages(t *testing.T) {
	if got := HandleDelivery(context.Background(), nil, &trigger.Handler{}); got.Decision != jetstream.TERM {
		t.Fatalf("空投递应 TERM：%+v", got)
	}
	got := HandleDelivery(context.Background(), &jetstream.Delivery{Subject: "moox.event.storage.view.data_ready.v1.space", RawData: []byte("not-protobuf"), RawMessageID: "m1"}, &trigger.Handler{})
	if got.Decision != jetstream.TERM || got.Err == nil {
		t.Fatalf("无法解码的投递应 TERM：%+v", got)
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	consumer := New(ConsumerConfig{}, &trigger.Handler{})
	if consumer.cfg.ConsumerName != trigger.ViewDataReadyConsumerName || consumer.cfg.AckWait <= 0 || consumer.cfg.MaxAckPending <= 0 || consumer.cfg.BatchSize <= 0 {
		t.Fatalf("默认配置不符：%+v", consumer.cfg)
	}
	if consumer.Ready() {
		t.Fatal("未启动的消费者不应就绪")
	}
	if err := consumer.Start(context.Background()); err == nil {
		t.Fatal("没有 EventBus 客户端时启动应失败")
	}
}
