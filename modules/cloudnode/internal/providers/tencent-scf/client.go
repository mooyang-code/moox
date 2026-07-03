// Package tencentscf wraps Tencent SCF APIs for CloudNode.
package tencentscf

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

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
	RequestID string
	Status    string
	Runtime   string
	ModTime   string
	CodeSize  int64
}

// UpdateFunctionCodeRequest updates a Tencent SCF function with a local zip.
type UpdateFunctionCodeRequest struct {
	FunctionRef
	ZipFile []byte
}

// UpdateFunctionCodeResponse describes a Tencent SCF code update.
type UpdateFunctionCodeResponse struct {
	RequestID string
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
	response, err := scfClient.GetFunction(request)
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
	if response.Response.CodeSize != nil {
		out.CodeSize = int64(*response.Response.CodeSize)
	}
	return out, nil
}

// UpdateFunctionCode updates Tencent SCF function code from a zip payload.
func (c *Client) UpdateFunctionCode(ctx context.Context, req UpdateFunctionCodeRequest) (*UpdateFunctionCodeResponse, error) {
	if len(req.ZipFile) == 0 {
		return nil, fmt.Errorf("zip file is empty")
	}
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] update function code function=%s namespace=%s region=%s zip_bytes=%d", req.FunctionName, req.Namespace, req.Region, len(req.ZipFile))
	scfClient, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	request := scf.NewUpdateFunctionCodeRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)
	request.ZipFile = common.StringPtr(base64.StdEncoding.EncodeToString(req.ZipFile))
	response, err := scfClient.UpdateFunctionCode(request)
	if err != nil {
		return nil, err
	}
	out := &UpdateFunctionCodeResponse{}
	if response.Response != nil {
		out.RequestID = deref(response.Response.RequestId)
	}
	return out, nil
}

// InvokeFunction invokes a Tencent SCF function.
func (c *Client) InvokeFunction(ctx context.Context, req InvokeFunctionRequest) (*InvokeFunctionResponse, error) {
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] invoke function=%s namespace=%s region=%s", req.FunctionName, req.Namespace, req.Region)
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

	response, err := scfClient.Invoke(request)
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
