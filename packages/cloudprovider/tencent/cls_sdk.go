package tencent

import (
	"context"
	"fmt"
	"strings"

	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

const defaultCLSEndpoint = "cls.tencentcloudapi.com"

type CLSSDKOptions struct {
	SecretID  string
	SecretKey string
	Region    string
	Endpoint  string
}

type CLSSDKAPI struct {
	client *cls.Client
}

// NewCLSSDKAPI uses explicit credentials when provided and otherwise falls
// back to the Tencent Cloud provider chain: environment, profile, then CVM role.
func NewCLSSDKAPI(opts CLSSDKOptions) (*CLSSDKAPI, error) {
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		return nil, fmt.Errorf("CLS region is required")
	}
	var credential common.CredentialIface
	secretID := strings.TrimSpace(opts.SecretID)
	secretKey := strings.TrimSpace(opts.SecretKey)
	if secretID != "" || secretKey != "" {
		if secretID == "" || secretKey == "" {
			return nil, fmt.Errorf("CLS secret id and secret key must be provided together")
		}
		credential = common.NewCredential(secretID, secretKey)
	} else {
		var err error
		credential, err = common.DefaultProviderChain().GetCredential()
		if err != nil {
			return nil, fmt.Errorf("resolve Tencent Cloud credentials: %w", err)
		}
	}
	cp := profile.NewClientProfile()
	cp.HttpProfile.Endpoint = strings.TrimSpace(opts.Endpoint)
	if cp.HttpProfile.Endpoint == "" {
		cp.HttpProfile.Endpoint = defaultCLSEndpoint
	}
	cp.HttpProfile.Endpoint = strings.TrimPrefix(strings.TrimPrefix(cp.HttpProfile.Endpoint, "https://"), "http://")
	client, err := cls.NewClient(credential, region, cp)
	if err != nil {
		return nil, fmt.Errorf("create CLS client: %w", err)
	}
	return &CLSSDKAPI{client: client}, nil
}

func (a *CLSSDKAPI) GetService(ctx context.Context) (bool, error) {
	rsp, err := a.client.GetClsServiceWithContext(ctx, cls.NewGetClsServiceRequest())
	if err != nil {
		return false, err
	}
	return rsp != nil && rsp.Response != nil && rsp.Response.Status != nil && *rsp.Response.Status == 0, nil
}

func (a *CLSSDKAPI) OpenService(ctx context.Context) (string, error) {
	rsp, err := a.client.OpenClsServiceWithContext(ctx, cls.NewOpenClsServiceRequest())
	if err != nil {
		return "", err
	}
	if rsp == nil || rsp.Response == nil {
		return "", fmt.Errorf("CLS OpenClsService returned an empty response")
	}
	return stringValue(rsp.Response.RequestId), nil
}

func (a *CLSSDKAPI) FindLogset(ctx context.Context, name string) (CLSLogset, bool, error) {
	req := cls.NewDescribeLogsetsRequest()
	req.Filters = []*cls.Filter{{Key: common.StringPtr("logsetName"), Values: []*string{common.StringPtr(name)}}}
	req.Limit = common.Int64Ptr(100)
	rsp, err := a.client.DescribeLogsetsWithContext(ctx, req)
	if err != nil {
		return CLSLogset{}, false, err
	}
	if rsp == nil || rsp.Response == nil {
		return CLSLogset{}, false, fmt.Errorf("CLS DescribeLogsets returned an empty response")
	}
	for _, item := range rsp.Response.Logsets {
		if item != nil && stringValue(item.LogsetName) == name {
			return CLSLogset{ID: stringValue(item.LogsetId), Name: name}, true, nil
		}
	}
	return CLSLogset{}, false, nil
}

func (a *CLSSDKAPI) CreateLogset(ctx context.Context, name string) (CLSLogset, string, error) {
	req := cls.NewCreateLogsetRequest()
	req.LogsetName = common.StringPtr(name)
	rsp, err := a.client.CreateLogsetWithContext(ctx, req)
	if err != nil {
		return CLSLogset{}, "", err
	}
	if rsp == nil || rsp.Response == nil {
		return CLSLogset{}, "", fmt.Errorf("CLS CreateLogset returned an empty response")
	}
	return CLSLogset{ID: stringValue(rsp.Response.LogsetId), Name: name}, stringValue(rsp.Response.RequestId), nil
}

func (a *CLSSDKAPI) FindTopic(ctx context.Context, logsetID, name string) (CLSTopic, bool, error) {
	req := cls.NewDescribeTopicsRequest()
	req.Filters = []*cls.Filter{
		{Key: common.StringPtr("logsetId"), Values: []*string{common.StringPtr(logsetID)}},
		{Key: common.StringPtr("topicName"), Values: []*string{common.StringPtr(name)}},
	}
	req.PreciseSearch = common.Uint64Ptr(1)
	req.Limit = common.Int64Ptr(100)
	rsp, err := a.client.DescribeTopicsWithContext(ctx, req)
	if err != nil {
		return CLSTopic{}, false, err
	}
	if rsp == nil || rsp.Response == nil {
		return CLSTopic{}, false, fmt.Errorf("CLS DescribeTopics returned an empty response")
	}
	for _, item := range rsp.Response.Topics {
		if item != nil && stringValue(item.LogsetId) == logsetID && stringValue(item.TopicName) == name {
			return CLSTopic{
				ID: stringValue(item.TopicId), LogsetID: logsetID, Name: name,
				IndexEnabled: item.Index != nil && *item.Index,
			}, true, nil
		}
	}
	return CLSTopic{}, false, nil
}

func (a *CLSSDKAPI) CreateTopic(ctx context.Context, opts CLSCreateTopicOptions) (CLSTopic, string, error) {
	req := cls.NewCreateTopicRequest()
	req.LogsetId = common.StringPtr(opts.LogsetID)
	req.TopicName = common.StringPtr(opts.Name)
	req.PartitionCount = common.Int64Ptr(opts.Partitions)
	req.Period = common.Int64Ptr(opts.RetentionDays)
	req.AutoSplit = common.BoolPtr(true)
	req.MaxSplitPartitions = common.Int64Ptr(10)
	req.StorageType = common.StringPtr("hot")
	req.IsWebTracking = common.BoolPtr(false)
	rsp, err := a.client.CreateTopicWithContext(ctx, req)
	if err != nil {
		return CLSTopic{}, "", err
	}
	if rsp == nil || rsp.Response == nil {
		return CLSTopic{}, "", fmt.Errorf("CLS CreateTopic returned an empty response")
	}
	return CLSTopic{ID: stringValue(rsp.Response.TopicId), LogsetID: opts.LogsetID, Name: opts.Name}, stringValue(rsp.Response.RequestId), nil
}

func (a *CLSSDKAPI) CreateIndex(ctx context.Context, topicID string) (string, error) {
	req := cls.NewCreateIndexRequest()
	req.TopicId = common.StringPtr(topicID)
	req.Status = common.BoolPtr(true)
	req.IncludeInternalFields = common.BoolPtr(true)
	req.MetadataFlag = common.Uint64Ptr(1)
	req.Rule = &cls.RuleInfo{FullText: &cls.FullTextInfo{
		CaseSensitive: common.BoolPtr(false),
		Tokenizer:     common.StringPtr(", '" + `"` + ";=()[]{}?@&<>/\\\n\t\r"),
		ContainZH:     common.BoolPtr(true),
	}}
	rsp, err := a.client.CreateIndexWithContext(ctx, req)
	if err != nil {
		return "", err
	}
	if rsp == nil || rsp.Response == nil {
		return "", fmt.Errorf("CLS CreateIndex returned an empty response")
	}
	return stringValue(rsp.Response.RequestId), nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
