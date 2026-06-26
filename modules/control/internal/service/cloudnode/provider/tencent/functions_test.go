package tencent

import "testing"

func TestBuildCreateFunctionSDKRequestSetsHandlerWhenProvided(t *testing.T) {
	req, err := buildCreateFunctionSDKRequest(&CreateFunctionRequest{
		Region:       "ap-guangzhou",
		FunctionName: "collector-fn",
		Runtime:      "CustomRuntime",
		Namespace:    "collector",
		Handler:      "bootstrap",
		COSBucket:    "collector-bucket",
		COSPath:      "packages/collector.zip",
		COSRegion:    "ap-guangzhou",
		Environment: map[string]string{
			"MOOX_ENV": "prod",
		},
	})
	if err != nil {
		t.Fatalf("buildCreateFunctionSDKRequest returned error: %v", err)
	}
	if req.Handler == nil || *req.Handler != "bootstrap" {
		t.Fatalf("Handler = %v, want bootstrap", req.Handler)
	}
	if req.Runtime == nil || *req.Runtime != "CustomRuntime" {
		t.Fatalf("Runtime = %v, want CustomRuntime", req.Runtime)
	}
	if req.Code == nil || req.Code.CosObjectName == nil || *req.Code.CosObjectName != "packages/collector.zip" {
		t.Fatalf("COS code config not propagated: %+v", req.Code)
	}
	if req.Environment == nil || len(req.Environment.Variables) != 1 {
		t.Fatalf("Environment = %+v, want one variable", req.Environment)
	}
	if key, value := req.Environment.Variables[0].Key, req.Environment.Variables[0].Value; key == nil || value == nil || *key != "MOOX_ENV" || *value != "prod" {
		t.Fatalf("Environment variable = %+v, want MOOX_ENV=prod", req.Environment.Variables[0])
	}
}
