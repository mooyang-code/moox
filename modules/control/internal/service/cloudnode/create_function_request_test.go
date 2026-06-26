package cloudnode

import (
	"testing"

	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
)

func TestBuildCreateFunctionRequestPropagatesRuntimeHandlerAndEnvironment(t *testing.T) {
	node := &model.CloudNode{
		NodeID:         "collector-fn",
		Region:         "ap-guangzhou",
		Namespace:      "collector",
		NodeType:       model.NodeTypeSCFEvent,
		CloudAccountID: "acct-1",
	}
	codeConfig := &FunctionCodeConfig{
		Runtime:   "CustomRuntime",
		Handler:   "bootstrap",
		COSBucket: "collector-bucket",
		COSPath:   "packages/collector.zip",
		COSRegion: "ap-guangzhou",
		Environment: map[string]string{
			"MOOX_ENV": "prod",
			"CONFIG":   "collector",
		},
	}

	req := buildCreateFunctionRequest(node, codeConfig)

	if req.Runtime != "CustomRuntime" {
		t.Fatalf("Runtime = %q, want CustomRuntime", req.Runtime)
	}
	if req.Handler != "bootstrap" {
		t.Fatalf("Handler = %q, want bootstrap", req.Handler)
	}
	if req.COSBucket != codeConfig.COSBucket || req.COSPath != codeConfig.COSPath || req.COSRegion != codeConfig.COSRegion {
		t.Fatalf("COS config not propagated: %+v", req)
	}
	if req.Environment["MOOX_ENV"] != "prod" || req.Environment["CONFIG"] != "collector" {
		t.Fatalf("Environment = %+v, want codeConfig environment", req.Environment)
	}
}

func TestBuildCreateFunctionRequestDoesNotDefaultEmptyRuntime(t *testing.T) {
	req := buildCreateFunctionRequest(&model.CloudNode{
		NodeID:    "collector-fn",
		Region:    "ap-guangzhou",
		Namespace: "collector",
		NodeType:  model.NodeTypeSCFEvent,
	}, &FunctionCodeConfig{
		Handler:       "main",
		ZipFileBase64: "zip",
	})

	if req.Runtime != "" {
		t.Fatalf("Runtime = %q, want empty runtime preserved", req.Runtime)
	}
	if req.Handler != "main" {
		t.Fatalf("Handler = %q, want main", req.Handler)
	}
	if req.ZipFile != "zip" {
		t.Fatalf("ZipFile = %q, want zip", req.ZipFile)
	}
}

func TestCreateNodeRequiresRuntimeBeforeProviderCall(t *testing.T) {
	_, err := (&ServiceImpl{}).CreateNode(t.Context(), &CloudNodeDTO{
		CloudAccountID: "acct-1",
		Region:         "ap-guangzhou",
	}, &FunctionCodeConfig{
		ZipFileBase64: "zip",
	})
	if err == nil {
		t.Fatalf("CreateNode should reject missing runtime")
	}
	if err.Error() != "runtime is required" {
		t.Fatalf("CreateNode error = %q, want runtime is required", err.Error())
	}
}

func TestApplyNodeCreateItemPropagatesEnvironmentAndConfig(t *testing.T) {
	codeConfig := &FunctionCodeConfig{}
	applyNodeCreateItemToCodeConfig(codeConfig, NodeCreateItem{
		Runtime: "CustomRuntime",
		Handler: "bootstrap",
		Config: map[string]string{
			"MOOX_ENV": "from-config",
			"REGION":   "ap-guangzhou",
		},
		Environment: map[string]string{
			"MOOX_ENV": "prod",
		},
	})

	if codeConfig.Runtime != "CustomRuntime" || codeConfig.Handler != "bootstrap" {
		t.Fatalf("codeConfig runtime/handler = %q/%q", codeConfig.Runtime, codeConfig.Handler)
	}
	if codeConfig.Environment["MOOX_ENV"] != "prod" || codeConfig.Environment["REGION"] != "ap-guangzhou" {
		t.Fatalf("Environment = %+v, want merged config plus environment override", codeConfig.Environment)
	}
}
