// Package tencentscf wraps Tencent SCF APIs for CloudNode.
package tencentscf

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
	"trpc.group/trpc-go/trpc-go/log"
)

// Client is a Tencent SCF provider client.
type Client struct {
	secretID  string
	secretKey string
}

// FunctionRef identifies a Tencent SCF function.
type FunctionRef struct {
	Region       string
	FunctionName string
	Namespace    string
}

// FunctionInfo describes the current Tencent SCF function state.
type FunctionInfo struct {
	RequestID   string
	Status      string
	Runtime     string
	ModTime     string
	CodeSize    int64
	MemorySize  int64
	Timeout     int64
	Environment map[string]string
}

// CreateFunctionRequest creates a Tencent SCF function from a COS package.
type CreateFunctionRequest struct {
	FunctionRef
	Runtime     string
	Handler     string
	Description string
	MemorySize  int64
	Timeout     int64
	Environment map[string]string
	COSBucket   string
	COSRegion   string
	COSObject   string
	Type        string
}

// CreateFunctionResponse describes a Tencent SCF function creation.
type CreateFunctionResponse struct {
	RequestID string
}

// DeleteFunction deletes an SCF function by its exact identity.
func (c *Client) DeleteFunction(ctx context.Context, ref FunctionRef) error {
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] delete function=%s namespace=%s region=%s", ref.FunctionName, ref.Namespace, ref.Region)
	client, err := c.newClient(ref.Region)
	if err != nil {
		return err
	}
	request := scf.NewDeleteFunctionRequest()
	request.FunctionName = common.StringPtr(ref.FunctionName)
	request.Namespace = common.StringPtr(ref.Namespace)
	_, err = client.DeleteFunctionWithContext(ctx, request)
	return err
}

// UpdateFunctionCodeRequest updates a Tencent SCF function with a local zip.
type UpdateFunctionCodeRequest struct {
	FunctionRef
	Handler   string
	ZipFile   []byte
	COSBucket string
	COSRegion string
	COSObject string
}

// UpdateFunctionCodeResponse describes a Tencent SCF code update.
type UpdateFunctionCodeResponse struct {
	RequestID string
}

type UpdateFunctionConfigurationRequest struct {
	FunctionRef
	Environment map[string]string
	MemorySize  int64
	Timeout     int64
	// ClearNativeCLS explicitly sends empty CLS ids during an update, removing
	// any historical SCF-native log destination. MooX uses the shared tRPC CLS
	// writer instead.
	ClearNativeCLS bool
}

type UpdateFunctionConfigurationResponse struct {
	RequestID string
}

type UpdateFunctionEventInvokeConfigRequest struct {
	FunctionRef
	RetryNum int64
	// MsgTTL is required by Tencent when updating async invoke config. Keep a
	// short bounded retention window because the durable retry state lives in
	// MooX EventBus, not in SCF's provider queue.
	MsgTTL int64
}

type UpdateFunctionEventInvokeConfigResponse struct {
	RequestID string
}

func (c *Client) UpdateFunctionEventInvokeConfig(ctx context.Context, req UpdateFunctionEventInvokeConfigRequest) (*UpdateFunctionEventInvokeConfigResponse, error) {
	client, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	request := scf.NewUpdateFunctionEventInvokeConfigRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	msgTTL := req.MsgTTL
	if msgTTL <= 0 {
		// Tencent currently caps this field at six hours (21600 seconds).
		msgTTL = 21600
	}
	request.AsyncTriggerConfig = &scf.AsyncTriggerConfig{RetryConfig: []*scf.RetryConfig{{RetryNum: common.Int64Ptr(req.RetryNum)}}, MsgTTL: common.Int64Ptr(msgTTL)}
	response, err := client.UpdateFunctionEventInvokeConfigWithContext(ctx, request)
	if err != nil {
		return nil, err
	}
	out := &UpdateFunctionEventInvokeConfigResponse{}
	if response.Response != nil {
		out.RequestID = deref(response.Response.RequestId)
	}
	return out, nil
}

// InvokeFunctionRequest describes a SCF invocation.
type InvokeFunctionRequest struct {
	Region       string
	FunctionName string
	Namespace    string
	Qualifier    string
	InvokeType   string
	EventData    any
}

// InvokeFunctionResponse describes a SCF invocation result.
type InvokeFunctionResponse struct {
	RequestID    string
	Code         int32
	Message      string
	ReturnResult string
	Duration     int64
	BillDuration int64
	MemoryUsage  int64
}

// New creates a Tencent SCF client.
func New(secretID string, secretKey string) *Client {
	return &Client{secretID: secretID, secretKey: secretKey}
}

// GetFunction returns Tencent SCF function metadata.
func (c *Client) GetFunction(ctx context.Context, req FunctionRef) (*FunctionInfo, error) {
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] get function=%s namespace=%s region=%s", req.FunctionName, req.Namespace, req.Region)
	scfClient, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	request := scf.NewGetFunctionRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	response, err := scfClient.GetFunctionWithContext(ctx, request)
	if err != nil {
		return nil, err
	}
	out := &FunctionInfo{}
	if response.Response == nil {
		return out, nil
	}
	out.RequestID = deref(response.Response.RequestId)
	out.Status = deref(response.Response.Status)
	out.Runtime = deref(response.Response.Runtime)
	out.ModTime = deref(response.Response.ModTime)
	if response.Response.Environment != nil {
		out.Environment = make(map[string]string, len(response.Response.Environment.Variables))
		for _, variable := range response.Response.Environment.Variables {
			if variable != nil {
				out.Environment[deref(variable.Key)] = deref(variable.Value)
			}
		}
	}
	if response.Response.CodeSize != nil {
		out.CodeSize = int64(*response.Response.CodeSize)
	}
	if response.Response.MemorySize != nil {
		out.MemorySize = int64(*response.Response.MemorySize)
	}
	if response.Response.Timeout != nil {
		out.Timeout = int64(*response.Response.Timeout)
	}
	return out, nil
}

func (c *Client) UpdateFunctionConfiguration(ctx context.Context, req UpdateFunctionConfigurationRequest) (*UpdateFunctionConfigurationResponse, error) {
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] update function configuration function=%s namespace=%s region=%s", req.FunctionName, req.Namespace, req.Region)
	scfClient, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	request := scf.NewUpdateFunctionConfigurationRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	request.Environment = &scf.Environment{Variables: environmentVariables(req.Environment)}
	if req.MemorySize > 0 {
		request.MemorySize = common.Int64Ptr(req.MemorySize)
	}
	if req.Timeout > 0 {
		request.Timeout = common.Int64Ptr(req.Timeout)
	}
	if req.ClearNativeCLS {
		request.ClsLogsetId = common.StringPtr("")
		request.ClsTopicId = common.StringPtr("")
		// Leaving both CLS IDs unset selects SCF's default delivery, which
		// auto-creates SCF_logset/SCF_logtopic resources. IgnoreSysLog is the
		// provider switch that actually disables that native path.
		request.IgnoreSysLog = common.BoolPtr(true)
	}
	response, err := scfClient.UpdateFunctionConfigurationWithContext(ctx, request)
	if err != nil {
		return nil, err
	}
	out := &UpdateFunctionConfigurationResponse{}
	if response.Response != nil {
		out.RequestID = deref(response.Response.RequestId)
	}
	return out, nil
}

// CreateFunction creates Tencent SCF function code from a COS package.
func (c *Client) CreateFunction(ctx context.Context, req CreateFunctionRequest) (*CreateFunctionResponse, error) {
	if req.COSBucket == "" || req.COSRegion == "" || req.COSObject == "" {
		return nil, fmt.Errorf("cos package is required")
	}
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] create function=%s namespace=%s region=%s cos=%s/%s", req.FunctionName, req.Namespace, req.Region, req.COSBucket, req.COSObject)
	scfClient, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	response, err := scfClient.CreateFunctionWithContext(ctx, buildCreateFunctionRequest(req))
	if err != nil {
		return nil, err
	}
	out := &CreateFunctionResponse{}
	if response.Response != nil {
		out.RequestID = deref(response.Response.RequestId)
	}
	return out, nil
}

// UpdateFunctionCode updates Tencent SCF function code from a zip payload.
func (c *Client) UpdateFunctionCode(ctx context.Context, req UpdateFunctionCodeRequest) (*UpdateFunctionCodeResponse, error) {
	if len(req.ZipFile) == 0 && (req.COSBucket == "" || req.COSRegion == "" || req.COSObject == "") {
		return nil, fmt.Errorf("zip file or cos package is required")
	}
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] update function code function=%s namespace=%s region=%s zip_bytes=%d cos=%s/%s", req.FunctionName, req.Namespace, req.Region, len(req.ZipFile), req.COSBucket, req.COSObject)
	scfClient, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	response, err := scfClient.UpdateFunctionCodeWithContext(ctx, buildUpdateFunctionCodeRequest(req))
	if err != nil {
		return nil, err
	}
	out := &UpdateFunctionCodeResponse{}
	if response.Response != nil {
		out.RequestID = deref(response.Response.RequestId)
	}
	return out, nil
}

func buildCreateFunctionRequest(req CreateFunctionRequest) *scf.CreateFunctionRequest {
	request := scf.NewCreateFunctionRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	request.CodeSource = common.StringPtr("Cos")
	request.Code = &scf.Code{
		CosBucketName:   common.StringPtr(req.COSBucket),
		CosBucketRegion: common.StringPtr(req.COSRegion),
		CosObjectName:   common.StringPtr(req.COSObject),
	}
	if req.Runtime != "" {
		request.Runtime = common.StringPtr(req.Runtime)
	}
	if req.Handler != "" {
		request.Handler = common.StringPtr(req.Handler)
	}
	if req.Description != "" {
		request.Description = common.StringPtr(req.Description)
	}
	if req.MemorySize > 0 {
		request.MemorySize = common.Int64Ptr(req.MemorySize)
	}
	if req.Timeout > 0 {
		request.Timeout = common.Int64Ptr(req.Timeout)
	}
	if len(req.Environment) > 0 {
		request.Environment = &scf.Environment{Variables: environmentVariables(req.Environment)}
	}
	// Disable SCF's creation-time default delivery. The application writer is
	// explicitly configured in trpc_go.yaml instead.
	request.AutoCreateClsTopic = common.StringPtr("FALSE")
	request.AutoDeployClsTopicIndex = common.StringPtr("FALSE")
	if req.Type != "" {
		request.Type = common.StringPtr(req.Type)
	}
	return request
}

func buildUpdateFunctionCodeRequest(req UpdateFunctionCodeRequest) *scf.UpdateFunctionCodeRequest {
	request := scf.NewUpdateFunctionCodeRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	if req.Handler != "" {
		request.Handler = common.StringPtr(req.Handler)
	}
	if len(req.ZipFile) > 0 {
		request.ZipFile = common.StringPtr(base64.StdEncoding.EncodeToString(req.ZipFile))
	} else {
		request.CosBucketName = common.StringPtr(req.COSBucket)
		request.CosBucketRegion = common.StringPtr(req.COSRegion)
		request.CosObjectName = common.StringPtr(req.COSObject)
	}
	return request
}

func environmentVariables(values map[string]string) []*scf.Variable {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*scf.Variable, 0, len(keys))
	for _, key := range keys {
		out = append(out, &scf.Variable{
			Key:   common.StringPtr(key),
			Value: common.StringPtr(values[key]),
		})
	}
	return out
}

// InvokeFunction invokes a Tencent SCF function.
func (c *Client) InvokeFunction(ctx context.Context, req InvokeFunctionRequest) (*InvokeFunctionResponse, error) {
	log.DebugContextf(ctx, "[CloudNode-TencentSCF] invoke function=%s namespace=%s region=%s", req.FunctionName, req.Namespace, req.Region)
	scfClient, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	request := scf.NewInvokeRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	if req.Qualifier != "" {
		request.Qualifier = common.StringPtr(req.Qualifier)
	}
	invokeType := req.InvokeType
	if invokeType == "" || invokeType == "sync" {
		invokeType = "RequestResponse"
	}
	request.InvocationType = common.StringPtr(invokeType)
	raw, err := json.Marshal(req.EventData)
	if err != nil {
		return nil, fmt.Errorf("marshal event data: %w", err)
	}
	request.ClientContext = common.StringPtr(string(raw))

	response, err := scfClient.InvokeWithContext(ctx, request)
	if err != nil {
		return nil, err
	}
	out := &InvokeFunctionResponse{}
	if response.Response == nil {
		return out, nil
	}
	out.RequestID = deref(response.Response.RequestId)
	if response.Response.Result == nil {
		return out, nil
	}
	result := response.Response.Result
	out.Message = deref(result.ErrMsg)
	out.ReturnResult = deref(result.RetMsg)
	if result.InvokeResult != nil {
		out.Code = int32(*result.InvokeResult)
	}
	if result.FunctionRequestId != nil {
		out.RequestID = *result.FunctionRequestId
	}
	if result.Duration != nil {
		out.Duration = int64(*result.Duration)
	}
	if result.BillDuration != nil {
		out.BillDuration = int64(*result.BillDuration)
	}
	if result.MemUsage != nil {
		out.MemoryUsage = *result.MemUsage
	}
	return out, nil
}

func (c *Client) newClient(region string) (*scf.Client, error) {
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = "scf.tencentcloudapi.com"
	clientProfile.HttpProfile.ReqTimeout = 240
	credential := common.NewCredential(c.secretID, c.secretKey)
	return scf.NewClient(credential, region, clientProfile)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
