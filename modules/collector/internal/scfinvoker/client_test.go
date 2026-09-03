package scfinvoker

import (
	"testing"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestIsDeployedRequiresPostDeployMarkerWhenDeploymentIDIsEmpty(t *testing.T) {
	if isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1"}) {
		t.Fatal("package-only node must not be invokable before deploy")
	}
	if !isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1", DeploymentId: "dep-1"}) {
		t.Fatal("node with deployment id should be invokable")
	}
	metadata, err := structpb.NewStruct(map[string]any{"deployment_ready": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1", Metadata: metadata}) {
		t.Fatal("node with local deployment marker should be invokable")
	}
}

func TestIsInstrumentSnapshotNodeUsesFunctionMode(t *testing.T) {
	if !IsInstrumentSnapshotNode(Node{Metadata: map[string]any{"function_mode": "instrument_snapshot"}}) {
		t.Fatal("instrument snapshot function mode must be recognized")
	}
	if IsInstrumentSnapshotNode(Node{Metadata: map[string]any{"function_mode": "kline"}}) {
		t.Fatal("Kline function mode must remain eligible for Timer scheduling")
	}
	if !IsInstrumentSnapshotNode(Node{FunctionName: "moox-fetcher-stockcn-instrument-ap-shanghai-0"}) {
		t.Fatal("instrument snapshot function prefix must remain recognizable for legacy nodes")
	}
}
