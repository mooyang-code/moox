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
	// MooX is a single-user system. Keep the topic to the configured partition
	// count instead of silently adding billable partitions under load.
	req.AutoSplit = common.BoolPtr(false)
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

// mergeIndexFields 把需要的键值索引字段并入规则，返回是否有改动：缺失的字段新增；已有但类型或分析开关不符的字段
// 改成需要的定义（否则数值统计等能力静默失效）。规则里的其他索引配置原样保留。
func mergeIndexFields(rule *cls.RuleInfo, fields []CLSIndexField) bool {
	if rule.KeyValue == nil {
		rule.KeyValue = &cls.RuleKeyValueInfo{CaseSensitive: common.BoolPtr(false)}
	}
	existing := make(map[string]*cls.KeyValueInfo, len(rule.KeyValue.KeyValues))
	for _, item := range rule.KeyValue.KeyValues {
		if item != nil && item.Key != nil {
			existing[*item.Key] = item
		}
	}
	changed := false
	for _, field := range fields {
		valueType := "text"
		if field.Numeric {
			valueType = "long"
		}
		wanted := &cls.ValueInfo{Type: common.StringPtr(valueType), Tokenizer: common.StringPtr(""), SqlFlag: common.BoolPtr(true), ContainZH: common.BoolPtr(false)}
		current, ok := existing[field.Key]
		if !ok {
			item := &cls.KeyValueInfo{Key: common.StringPtr(field.Key), Value: wanted}
			rule.KeyValue.KeyValues = append(rule.KeyValue.KeyValues, item)
			existing[field.Key] = item
			changed = true
			continue
		}
		if current.Value == nil || stringValue(current.Value.Type) != valueType || current.Value.SqlFlag == nil || !*current.Value.SqlFlag {
			current.Value = wanted
			changed = true
		}
	}
	return changed
}

// buildModifyIndex 由当前索引构造 ModifyIndex 请求：补齐字段，并带回读到的全部索引配置
// （全文、键值、元字段、动态索引与内置字段开关），没有任何字段需要改动时返回 changed=false。
// CLS 的 ModifyIndex 要求 Tag 与 DynamicIndex 非空，主题没有元字段索引时补一个占位字段（不影响检索）。
func buildModifyIndex(topicID string, current *cls.DescribeIndexResponseParams, fields []CLSIndexField) (*cls.ModifyIndexRequest, bool) {
	rule := current.Rule
	if !mergeIndexFields(rule, fields) {
		return nil, false
	}
	if rule.Tag == nil || len(rule.Tag.KeyValues) == 0 {
		rule.Tag = &cls.RuleTagInfo{CaseSensitive: common.BoolPtr(false), KeyValues: []*cls.KeyValueInfo{{
			Key:   common.StringPtr("moox_unused"),
			Value: &cls.ValueInfo{Type: common.StringPtr("text"), Tokenizer: common.StringPtr(""), SqlFlag: common.BoolPtr(true), ContainZH: common.BoolPtr(false)},
		}}}
	}
	if rule.DynamicIndex == nil {
		rule.DynamicIndex = &cls.DynamicIndex{Status: common.BoolPtr(false)}
	}
	modify := cls.NewModifyIndexRequest()
	modify.TopicId = common.StringPtr(topicID)
	modify.Status = common.BoolPtr(true)
	modify.Rule = rule
	modify.IncludeInternalFields = current.IncludeInternalFields
	modify.MetadataFlag = current.MetadataFlag
	modify.CoverageField = current.CoverageField
	return modify, true
}

// EnsureIndexFields 读取主题当前索引，补上缺失的键值索引字段后整体回写；已有的全文、键值、元字段索引原样保留。
// CLS 的 ModifyIndex 要求 Tag 与 DynamicIndex 非空，所以主题没有元字段索引时补一个占位字段（不影响检索）。
func (a *CLSSDKAPI) EnsureIndexFields(ctx context.Context, topicID string, fields []CLSIndexField) (bool, string, error) {
	describe := cls.NewDescribeIndexRequest()
	describe.TopicId = common.StringPtr(topicID)
	current, err := a.client.DescribeIndexWithContext(ctx, describe)
	if err != nil {
		return false, "", err
	}
	if current == nil || current.Response == nil || current.Response.Rule == nil {
		return false, "", fmt.Errorf("CLS DescribeIndex returned an empty response")
	}
	modify, changed := buildModifyIndex(topicID, current.Response, fields)
	if !changed {
		return false, "", nil
	}
	rsp, err := a.client.ModifyIndexWithContext(ctx, modify)
	if err != nil {
		return false, "", err
	}
	if rsp == nil || rsp.Response == nil {
		return false, "", fmt.Errorf("CLS ModifyIndex returned an empty response")
	}
	return true, stringValue(rsp.Response.RequestId), nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
