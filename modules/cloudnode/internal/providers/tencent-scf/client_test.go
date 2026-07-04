package tencentscf

import "testing"

func TestBuildCreateFunctionRequestUsesCOSPackage(t *testing.T) {
	req := buildCreateFunctionRequest(CreateFunctionRequest{
		FunctionRef: FunctionRef{
			Region:       "ap-guangzhou",
			FunctionName: "moox-collector-ap-guangzhou-0",
			Namespace:    "collector",
		},
		Runtime:     "CustomRuntime",
		Handler:     "main",
		Description: "collector runtime",
		MemorySize:  256,
		Timeout:     60,
		Environment: map[string]string{
			"MOOX_ENV": "prod",
		},
		COSBucket:   "moox-scf-1255382561",
		COSRegion:   "ap-guangzhou",
		COSObject:   "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip",
		ClsLogsetID: "logset-a",
		ClsTopicID:  "topic-a",
		Type:        "Event",
	})

	if req.GetAction() == "" {
		t.Fatalf("request action must be initialized")
	}
	if got := deref(req.FunctionName); got != "moox-collector-ap-guangzhou-0" {
		t.Fatalf("FunctionName = %q", got)
	}
	if got := deref(req.Namespace); got != "collector" {
		t.Fatalf("Namespace = %q", got)
	}
	if got := deref(req.Runtime); got != "CustomRuntime" {
		t.Fatalf("Runtime = %q", got)
	}
	if got := deref(req.Handler); got != "main" {
		t.Fatalf("Handler = %q", got)
	}
	if got := deref(req.CodeSource); got != "Cos" {
		t.Fatalf("CodeSource = %q, want Cos", got)
	}
	if req.Code == nil {
		t.Fatalf("Code must be set")
	}
	if got := deref(req.Code.CosBucketName); got != "moox-scf-1255382561" {
		t.Fatalf("CosBucketName = %q", got)
	}
	if got := deref(req.Code.CosBucketRegion); got != "ap-guangzhou" {
		t.Fatalf("CosBucketRegion = %q", got)
	}
	if got := deref(req.Code.CosObjectName); got != "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip" {
		t.Fatalf("CosObjectName = %q", got)
	}
	if req.Environment == nil || len(req.Environment.Variables) != 1 {
		t.Fatalf("Environment variables = %#v, want one", req.Environment)
	}
	if got := deref(req.Environment.Variables[0].Key); got != "MOOX_ENV" {
		t.Fatalf("env key = %q", got)
	}
	if got := deref(req.Environment.Variables[0].Value); got != "prod" {
		t.Fatalf("env value = %q", got)
	}
	if got := deref(req.ClsLogsetId); got != "logset-a" {
		t.Fatalf("ClsLogsetId = %q", got)
	}
	if got := deref(req.ClsTopicId); got != "topic-a" {
		t.Fatalf("ClsTopicId = %q", got)
	}
}

func TestBuildUpdateFunctionCodeRequestUsesCOSPackage(t *testing.T) {
	req := buildUpdateFunctionCodeRequest(UpdateFunctionCodeRequest{
		FunctionRef: FunctionRef{
			Region:       "ap-guangzhou",
			FunctionName: "moox-collector-ap-guangzhou-0",
			Namespace:    "collector",
		},
		Handler:   "main",
		COSBucket: "moox-scf-1255382561",
		COSRegion: "ap-guangzhou",
		COSObject: "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip",
	})

	if got := deref(req.FunctionName); got != "moox-collector-ap-guangzhou-0" {
		t.Fatalf("FunctionName = %q", got)
	}
	if got := deref(req.Namespace); got != "collector" {
		t.Fatalf("Namespace = %q", got)
	}
	if got := deref(req.Handler); got != "main" {
		t.Fatalf("Handler = %q", got)
	}
	if got := deref(req.CosBucketName); got != "moox-scf-1255382561" {
		t.Fatalf("CosBucketName = %q", got)
	}
	if got := deref(req.CosBucketRegion); got != "ap-guangzhou" {
		t.Fatalf("CosBucketRegion = %q", got)
	}
	if got := deref(req.CosObjectName); got != "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip" {
		t.Fatalf("CosObjectName = %q", got)
	}
}
