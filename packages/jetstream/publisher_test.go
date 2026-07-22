package jetstream

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
)

func TestPublishBatchRunsIndependentMessagesConcurrently(t *testing.T) {
	messages := make([]*messagepb.MooxMessage, 4)
	for i := range messages {
		messages[i] = validTestMessage(fmt.Sprintf("concurrent-%d", i), "moox.test.events.v1")
	}
	started := make(chan string, len(messages))
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	updateMax := func(value int32) {
		for current := maxActive.Load(); value > current && !maxActive.CompareAndSwap(current, value); current = maxActive.Load() {
		}
	}
	publish := func(_ context.Context, message *messagepb.MooxMessage, _ ...PublishOption) (*PublishAck, error) {
		current := active.Add(1)
		updateMax(current)
		started <- message.GetMessageId()
		<-release
		active.Add(-1)
		return &PublishAck{Sequence: uint64(current)}, nil
	}

	done := make(chan []PublishResult, 1)
	go func() { done <- publishBatch(context.Background(), messages, nil, 4, publish) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("PublishBatch did not start independent messages concurrently")
		}
	}
	close(release)
	results := <-done
	if maxActive.Load() < 2 {
		t.Fatalf("max concurrent publications = %d, want at least 2", maxActive.Load())
	}
	for i, result := range results {
		if result.Err != nil || result.Ack == nil {
			t.Fatalf("result[%d] = %+v, want successful independent publication", i, result)
		}
	}
}

func TestValidateMessageRejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		msg  *messagepb.MooxMessage
	}{
		{name: "nil", msg: nil},
		{name: "protocol version", msg: validTestMessage("id", "moox.test.events.v1")},
		{name: "kind", msg: validTestMessage("id", "moox.test.events.v1")},
		{name: "message id", msg: validTestMessage("", "moox.test.events.v1")},
		{name: "topic", msg: validTestMessage("id", "")},
		{name: "producer", msg: validTestMessage("id", "moox.test.events.v1")},
		{name: "timestamps", msg: validTestMessage("id", "moox.test.events.v1")},
		{name: "content type", msg: validTestMessage("id", "moox.test.events.v1")},
		{name: "message type", msg: validTestMessage("id", "moox.test.events.v1")},
		{name: "payload", msg: validTestMessage("id", "moox.test.events.v1")},
	}
	cases[1].msg.ProtocolVersion = 0
	cases[2].msg.Kind = messagepb.MessageKind_MESSAGE_KIND_UNSPECIFIED
	cases[5].msg.Producer = nil
	cases[6].msg.OccurredAt = nil
	cases[7].msg.ContentType = ""
	cases[8].msg.MessageType = ""
	cases[9].msg.Payload = nil
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateMessage(tc.msg, 1024); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("ValidateMessage() error = %v, want ErrInvalidMessage", err)
			}
		})
	}
}

func TestValidateMessageRejectsWildcardsAndOversizedPayload(t *testing.T) {
	msg := validTestMessage("id", "moox.test.>")
	if err := ValidateMessage(msg, 1024); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("wildcard topic error = %v", err)
	}
	msg.Topic = "moox.test.events.v1"
	msg.Payload = make([]byte, 1025)
	if err := ValidateMessage(msg, 1024); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("oversized payload error = %v", err)
	}
	for _, topic := range []string{" moox.test.events.v1", "moox.test.events.v1 ", "moox.test.\x00events.v1"} {
		msg.Topic = topic
		if err := ValidateMessage(msg, 1024); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("topic %q error = %v, want ErrInvalidMessage", topic, err)
		}
	}
}

func TestMarshalMessageRejectsOversizedMessage(t *testing.T) {
	msg := validTestMessage("envelope", "moox.test.events.v1")
	msg.Payload = []byte("ok")
	msg.Attributes = map[string]string{"large": string(make([]byte, 512))}
	if _, err := marshalMessage(msg, 256); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("marshalMessage() error = %v, want ErrInvalidMessage", err)
	}
}

func TestPublishOptionOrderingKeyAndCodecRoundTrip(t *testing.T) {
	msg := validTestMessage("id", "moox.test.events.v1")
	msg.Payload = []byte{0, 1, 2, 3}
	encoded, err := marshalMessage(msg, 1024)
	if err != nil {
		t.Fatalf("marshalMessage() error = %v", err)
	}
	decoded, err := unmarshalMessage(encoded, 1024)
	if err != nil {
		t.Fatalf("unmarshalMessage() error = %v", err)
	}
	decoded.Payload[0] = 99
	if msg.Payload[0] != 0 {
		t.Fatal("decoded payload aliases source payload")
	}
	var opts publishOptions
	WithOrderingKey("series-1")(&opts)
	if opts.orderingKey != "series-1" {
		t.Fatalf("ordering key = %q", opts.orderingKey)
	}
}

func TestPublishPreservesCancellationAndDeadlineErrors(t *testing.T) {
	client := &Client{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Publish(cancelled, validTestMessage("cancelled", "moox.test.events.v1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() canceled error = %v, want context.Canceled", err)
	}
	deadline, stop := context.WithTimeout(context.Background(), 1)
	defer stop()
	<-deadline.Done()
	if _, err := client.Publish(deadline, validTestMessage("deadline", "moox.test.events.v1")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Publish() deadline error = %v, want context.DeadlineExceeded", err)
	}
}

func TestDecodeRejectsTopicSubjectAndMessageIDMismatch(t *testing.T) {
	msg := validTestMessage("id", "moox.test.events.v1")
	raw, err := marshalMessage(msg, 1024)
	if err != nil {
		t.Fatalf("marshalMessage() error = %v", err)
	}
	for name, natsMsg := range map[string]*nats.Msg{
		"subject": {
			Subject: "moox.other.events.v1",
			Data:    raw,
			Header:  nats.Header{nats.MsgIdHdr: []string{"id"}, "Content-Type": []string{OuterContentType}},
		},
		"message id": {
			Subject: msg.Topic,
			Data:    raw,
			Header:  nats.Header{nats.MsgIdHdr: []string{"other"}, "Content-Type": []string{OuterContentType}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeNATSMessage(natsMsg, 1024); !errors.Is(err, ErrDecode) {
				t.Fatalf("decodeNATSMessage() error = %v, want ErrDecode", err)
			}
		})
	}
}
