package events

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

type captureClient struct {
	subject, contentType string
	body                 []byte
}

func (c *captureClient) PublishRaw(_ context.Context, subject, _ string, body []byte, contentType string) (*jetstream.PublishAck, error) {
	c.subject, c.contentType, c.body = subject, contentType, append([]byte(nil), body...)
	return &jetstream.PublishAck{}, nil
}

// rowsPayload 构造包含 count 行记录的行事件；行内容高度重复，和线上因子结果行类似，压缩收益明显。
func rowsPayload(count int) *storagepb.DatasetRowsUpserted {
	rows := make([]*storagepb.RowUpsert, 0, count)
	for i := 0; i < count; i++ {
		rows = append(rows, &storagepb.RowUpsert{Key: &storagepb.RowKey{
			SpaceId: "space", DatasetId: "dataset",
			Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-" + strings.Repeat("x", 40), Version: "v1"}},
		}})
	}
	return &storagepb.DatasetRowsUpserted{NodeId: "node", StoreId: "store", Sequence: 1, SpaceId: "space", DatasetId: "dataset", Rows: rows}
}

// 发布器默认压缩：压缩发布的信封经 DecodeRaw 透明解压，与未压缩的解码结果一致；低于阈值的小信封保持原样。
func TestPublisherCompressionRoundTrip(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	opts := PublishOptions{EventID: "evt-1", OccurredAt: time.Unix(1_700_000_000, 0).UTC(), SpaceID: "space", SubjectID: "dataset"}

	packed := &captureClient{}
	publisher, err := NewPublisher(packed, registry)
	if err != nil {
		t.Fatal(err)
	}
	payload := rowsPayload(200)
	if _, err := publisher.Publish(context.Background(), DatasetRowsUpserted, payload, opts); err != nil {
		t.Fatal(err)
	}
	if packed.contentType != ContentTypeZstd {
		t.Fatalf("发布器默认压缩大信封：%s", packed.contentType)
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(mustEncode(t, registry, payload, opts))
	if err != nil {
		t.Fatal(err)
	}
	if len(packed.body) >= len(raw)/2 {
		t.Fatalf("大信封应明显变小：%d/%d", len(packed.body), len(raw))
	}
	messageA, payloadA, errA := DecodeRaw(registry, raw, packed.subject, "evt-1", ContentType)
	messageB, payloadB, errB := DecodeRaw(registry, packed.body, packed.subject, "evt-1", packed.contentType)
	if errA != nil || errB != nil {
		t.Fatalf("解码失败：%v %v", errA, errB)
	}
	if messageA.GetEventId() != messageB.GetEventId() || !bytes.Equal(messageA.GetPayload(), messageB.GetPayload()) || payloadA.ProtoReflect().Descriptor() != payloadB.ProtoReflect().Descriptor() {
		t.Fatal("压缩与未压缩的解码结果应一致")
	}

	// 小于阈值的信封保持原样。
	small := &captureClient{}
	smallPublisher, _ := NewPublisher(small, registry)
	if _, err := smallPublisher.Publish(context.Background(), DatasetRowsUpserted, rowsPayload(1), opts); err != nil || small.contentType != ContentType {
		t.Fatalf("低于阈值不压缩：%v %s", err, small.contentType)
	}
}

func mustEncode(t *testing.T, registry *Registry, payload proto.Message, opts PublishOptions) *eventpb.EventMessage {
	t.Helper()
	encoded, err := registry.Encode(DatasetRowsUpserted, payload, opts)
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Message
}

// 不是 zstd 的数据、gzip 内容类型、未知的 +xxx 内容类型都必须被拒绝。
func TestDecodeRejectsGarbageAndOldContentTypes(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inflateBody([]byte("not zstd")); err == nil {
		t.Fatal("非 zstd 数据应报错")
	}
	for _, contentType := range []string{ContentType + "+gzip", ContentType + "+br"} {
		if _, _, err := DecodeRaw(registry, []byte("x"), "subject", "evt", contentType); err == nil {
			t.Fatalf("内容类型 %s 应被拒绝", contentType)
		}
	}
}
