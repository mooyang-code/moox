package tencentscf

import (
	"testing"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

func TestGetFunctionResponseMapsNestedInstanceConcurrency(t *testing.T) {
	response := &scf.GetFunctionResponseParams{
		InstanceConcurrencyConfig: &scf.InstanceConcurrencyConfig{
			MaxConcurrency: common.Uint64Ptr(1),
		},
	}

	if got := functionMaxInstanceConcurrency(response); got != 1 {
		t.Fatalf("MaxConcurrency = %d, want 1", got)
	}
	if got := functionMaxInstanceConcurrency(&scf.GetFunctionResponseParams{}); got != 0 {
		t.Fatalf("missing MaxConcurrency = %d, want 0", got)
	}
}

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
		COSBucket:       "moox-scf-1255382561",
		COSRegion:       "ap-guangzhou",
		COSObject:       "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip",
		Type:            "Event",
		PublicNetStatus: "ENABLE",
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
	if req.ClsLogsetId != nil || req.ClsTopicId != nil {
		t.Fatalf("native CLS must not be configured: %q/%q", deref(req.ClsLogsetId), deref(req.ClsTopicId))
	}
	if got := deref(req.AutoCreateClsTopic); got != "FALSE" {
		t.Fatalf("AutoCreateClsTopic = %q, want FALSE", got)
	}
	if got := deref(req.AutoDeployClsTopicIndex); got != "FALSE" {
		t.Fatalf("AutoDeployClsTopicIndex = %q, want FALSE", got)
	}
	if req.PublicNetConfig == nil || deref(req.PublicNetConfig.PublicNetStatus) != "ENABLE" || req.PublicNetConfig.EipConfig == nil || deref(req.PublicNetConfig.EipConfig.EipStatus) != "DISABLE" {
		t.Fatalf("PublicNetConfig = %#v, want ENABLE", req.PublicNetConfig)
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

// 2026-11-14 真机实测：从没配置过异步重试的函数返回默认值 RetryConfig=[{2},{-1}]、MsgTTL=21600（不是 ResourceNotFound），
// 解析成“已配置、RetryNum=2”（只看第一项），Ensure 据此判为不一致而写一次。
func TestEventInvokeConfigDefaultIsParsedAsConfiguredWithTheFirstRetry(t *testing.T) {
	response := scf.NewGetFunctionEventInvokeConfigResponse()
	response.Response = &scf.GetFunctionEventInvokeConfigResponseParams{AsyncTriggerConfig: &scf.AsyncTriggerConfig{
		RetryConfig: []*scf.RetryConfig{{RetryNum: common.Int64Ptr(2)}, {RetryNum: common.Int64Ptr(-1)}},
		MsgTTL:      common.Int64Ptr(21600),
	}}
	got := eventInvokeConfigFromResponse(response)
	if !got.Configured || got.RetryNum != 2 || got.MsgTTL != 21600 {
		t.Fatalf("默认异步重试解析成 %+v，期望已配置、RetryNum=2、MsgTTL=21600", got)
	}
	if empty := eventInvokeConfigFromResponse(scf.NewGetFunctionEventInvokeConfigResponse()); empty.Configured {
		t.Fatalf("没有返回内容时不能算已配置：%+v", empty)
	}
}

// 真机实测：没配置过预留并发时返回成功、ReservedMem 为 null，判为没配置（0）。
func TestReservedMemNullIsNotConfigured(t *testing.T) {
	response := scf.NewGetReservedConcurrencyConfigResponse()
	response.Response = &scf.GetReservedConcurrencyConfigResponseParams{}
	if got := reservedMemFromResponse(response); got != 0 {
		t.Fatalf("ReservedMem 为 null 时得到 %d，期望 0", got)
	}
	response.Response.ReservedMem = common.Uint64Ptr(320)
	if got := reservedMemFromResponse(response); got != 320 {
		t.Fatalf("ReservedMem=320 时得到 %d", got)
	}
}
