package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

func TestToPBNodePreservesThreeWayPackageFields(t *testing.T) {
	node := repository.CloudNode{
		NodeID:         "node-1",
		PackageID:      "pkg-abc",
		PackageVersion: "v1.0.0",
		DeploymentID:   "dep-xyz",
		Status:         "online",
	}
	out := toPBNode(node)
	if out.GetPackageId() != "pkg-abc" {
		t.Fatalf("package_id = %q", out.GetPackageId())
	}
	if out.GetPackageVersion() != "v1.0.0" {
		t.Fatalf("package_version = %q", out.GetPackageVersion())
	}
	if out.GetDeploymentId() != "dep-xyz" {
		t.Fatalf("deployment_id = %q", out.GetDeploymentId())
	}
}

func TestFromPBNodePreservesThreeWayPackageFields(t *testing.T) {
	in := &pb.CloudNode{
		NodeId:         "node-1",
		PackageId:      "pkg-abc",
		PackageVersion: "v1.0.0",
		DeploymentId:   "dep-xyz",
	}
	out := fromPBNode("space-a", in)
	if out.PackageID != "pkg-abc" || out.PackageVersion != "v1.0.0" || out.DeploymentID != "dep-xyz" {
		t.Fatalf("unexpected mapping: %+v", out)
	}
	if out.SpaceID != "space-a" {
		t.Fatalf("space_id = %q", out.SpaceID)
	}
}
