package pebble

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

func TestBuildDatasetFieldsChangedMessageUsesFieldsChangedEnvelopeContract(t *testing.T) {
	data, err := BuildDatasetFieldsChangedMessage("node-1", "foo", "bar", []*pb.RowFieldUpsert{{}})
	if err != nil {
		t.Fatal(err)
	}

	message := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	if message.GetTopic() != "moox.storage.fields_changed.v1.mzxw6.mjqxe" {
		t.Fatalf("topic = %q, want fields_changed subject with encoded space and dataset", message.GetTopic())
	}
	if message.GetContentType() != "application/x-protobuf; message=trpc.moox.storage.DatasetFieldsChanged" {
		t.Fatalf("content type = %q", message.GetContentType())
	}
	if message.GetMessageType() != "moox.storage.fields_changed.v1" {
		t.Fatalf("message type = %q", message.GetMessageType())
	}
}
