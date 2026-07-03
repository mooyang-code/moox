package rpc

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

func TestNodeStatusCodeValues(t *testing.T) {
	cases := map[pb.NodeStatusCode]int32{
		pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED: 0,
		pb.NodeStatusCode_NODE_STATUS_OFFLINE:     1,
		pb.NodeStatusCode_NODE_STATUS_ONLINE:      2,
		pb.NodeStatusCode_NODE_STATUS_TIMEOUT:     3,
		pb.NodeStatusCode_NODE_STATUS_ABNORMAL:    4,
	}
	for status, want := range cases {
		if int32(status) != want {
			t.Fatalf("NodeStatusCode %v = %d, want %d", status, status, want)
		}
	}
}

func TestPackageStatusValues(t *testing.T) {
	cases := map[pb.PackageStatus]int32{
		pb.PackageStatus_PACKAGE_STATUS_UNSPECIFIED: 0,
		pb.PackageStatus_PACKAGE_STATUS_PENDING:     1,
		pb.PackageStatus_PACKAGE_STATUS_AVAILABLE:   2,
		pb.PackageStatus_PACKAGE_STATUS_FAILED:      3,
		pb.PackageStatus_PACKAGE_STATUS_DELETED:     4,
	}
	for status, want := range cases {
		if int32(status) != want {
			t.Fatalf("PackageStatus %v = %d, want %d", status, status, want)
		}
	}
}

func TestCloudWorkItemHasNoMaxAttemptsField(t *testing.T) {
	item := &pb.CloudWorkItem{}
	item.ProtoReflect().Descriptor().Fields().ByName("max_attempts")
	if item.ProtoReflect().Descriptor().Fields().ByName("max_attempts") != nil {
		t.Fatal("CloudWorkItem should not expose max_attempts")
	}
}
