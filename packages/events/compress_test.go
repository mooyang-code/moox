package events

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
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
	return &storagepb.DatasetRowsUpserted{SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1, SpaceId: "space", DatasetId: "dataset", Rows: rows}
}

// 压缩发布的信封经 DecodeRaw 透明解压，与未压缩的解码结果一致；小信封与关闭压缩时保持原样。
func TestPublisherCompressionRoundTrip(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	opts := PublishOptions{EventID: "evt-1", OccurredAt: time.Unix(1_700_000_000, 0).UTC(), SpaceID: "space", SubjectID: "dataset"}
	payload := rowsPayload(200)

	plain := &captureClient{}
	publisher, err := NewPublisher(plain, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), DatasetRowsUpserted, payload, opts); err != nil {
		t.Fatal(err)
	}
	if plain.contentType != ContentType {
		t.Fatalf("默认不压缩：%s", plain.contentType)
	}

	packed := &captureClient{}
	compressing, _ := NewPublisher(packed, registry)
	compressing.SetCompression(4096)
	if _, err := compressing.Publish(context.Background(), DatasetRowsUpserted, payload, opts); err != nil {
		t.Fatal(err)
	}
	if packed.contentType != ContentTypeGzip || len(packed.body) >= len(plain.body)/2 {
		t.Fatalf("大信封应被压缩：%s %d/%d", packed.contentType, len(packed.body), len(plain.body))
	}
	messageA, payloadA, errA := DecodeRaw(registry, plain.body, plain.subject, "evt-1", plain.contentType)
	messageB, payloadB, errB := DecodeRaw(registry, packed.body, packed.subject, "evt-1", packed.contentType)
	if errA != nil || errB != nil {
		t.Fatalf("解码失败：%v %v", errA, errB)
	}
	if messageA.GetEventId() != messageB.GetEventId() || !bytes.Equal(messageA.GetPayload(), messageB.GetPayload()) || payloadA.ProtoReflect().Descriptor() != payloadB.ProtoReflect().Descriptor() {
		t.Fatal("压缩与未压缩的解码结果应一致")
	}

	small := &captureClient{}
	smallPublisher, _ := NewPublisher(small, registry)
	smallPublisher.SetCompression(1 << 20)
	if _, err := smallPublisher.Publish(context.Background(), DatasetRowsUpserted, payload, opts); err != nil || small.contentType != ContentType {
		t.Fatalf("低于阈值不压缩：%v %s", err, small.contentType)
	}
}

func TestInflateRejectsGarbage(t *testing.T) {
	if _, err := inflateBody([]byte("not gzip")); err == nil {
		t.Fatal("非 gzip 数据应报错")
	}
}
