package hostmetrics

import (
	"errors"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
)

func TestIdleFetchTimeoutKeepsConsumerRunning(t *testing.T) {
	if !isIdleFetchError(nats.ErrTimeout) {
		t.Fatal("NATS idle fetch timeout must not restart the durable consumer")
	}
	if isIdleFetchError(errors.New("connection closed")) {
		t.Fatal("non-timeout consumer errors must still be returned")
	}
}

func TestValidateHostMetricContract(t *testing.T) {
	payload, err := proto.Marshal(&hostmetricpb.HostMetric{Snapshot: &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1, UsageAvailable: true, UsagePercent: 20}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40}}})
	if err != nil {
		t.Fatal(err)
	}
	now := timestamppb.Now()
	msg := &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", Topic: Topic, Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, Producer: &messagepb.Producer{ServiceName: "moox-host-agent", InstanceId: "0190f4d0-7b1c-4f45-9a3e-7c28f6479a73", NodeId: "host", BootId: "boot"}, SpaceId: SpaceID, OccurredAt: now, PublishedAt: now, ContentType: ContentType, Payload: payload}
	if _, err := ValidateMessage(msg); err != nil {
		t.Fatal(err)
	}
	msg.SpaceId = "crypto"
	if _, err := ValidateMessage(msg); err == nil {
		t.Fatal("non-system space accepted")
	}
}
