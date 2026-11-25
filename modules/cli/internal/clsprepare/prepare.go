package clsprepare

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

const (
	Region              = "ap-guangzhou"
	LogsetName          = "moox"
	TopicName           = "moox-application"
	Host                = Region + ".cls.tencentyun.com"
	RetentionDays int64 = 2
	Partitions    int64 = 1
)

type AccountSource interface {
	ListCloudAccounts(context.Context, string) ([]*collectorpb.CloudAccount, error)
	GetSecretValue(context.Context, string) (*rpcclient.SecretMaterial, error)
}

type Factory func(secretID, secretKey string) (tencent.CLSAPI, error)

type Result struct {
	AccountID     string `json:"account_id"`
	Region        string `json:"region"`
	Host          string `json:"host"`
	LogsetID      string `json:"logset_id"`
	TopicID       string `json:"topic_id"`
	ServiceOpened bool   `json:"service_opened"`
	LogsetCreated bool   `json:"logset_created"`
	TopicCreated  bool   `json:"topic_created"`
	IndexCreated  bool   `json:"index_created"`
	// IndexFieldsChanged 表示这次补齐了采集日志字段的键值索引。
	IndexFieldsChanged bool `json:"index_fields_changed"`
}

// Provision 幂等开通 CLS（服务、日志集、主题、索引字段），不写任何凭证文件：系统初始化用它保证日志服务开箱可用。
// retentionDays 是新建日志主题时的保留天数（来自配置的 [logging] retention_days），不大于 0 时用默认值。
func Provision(ctx context.Context, source AccountSource, factory Factory, cloudAccountID string, retentionDays int64) (Result, error) {
	if retentionDays <= 0 {
		retentionDays = RetentionDays
	}
	if source == nil {
		return Result{}, fmt.Errorf("account source is required")
	}
	if factory == nil {
		return Result{}, fmt.Errorf("CLS factory is required")
	}

	accounts, err := source.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return Result{}, safeUpstreamError("list Tencent cloud accounts", err)
	}
	account, err := selectAccount(accounts, cloudAccountID)
	if err != nil {
		return Result{}, err
	}
	secret, err := source.GetSecretValue(ctx, account.GetCredentialSecretId())
	if err != nil {
		return Result{}, safeUpstreamError(fmt.Sprintf("reveal cloud account %q", account.GetAccountId()), err)
	}
	if secret == nil || account.GetCredentialSecretId() == "" {
		return Result{}, fmt.Errorf("cloud account %q returned incomplete Tencent credentials", account.GetAccountId())
	}
	if secret.Provider != "tencent" || secret.Category != "cloud" || secret.Status != "active" ||
		strings.TrimSpace(secret.KeyID) == "" || strings.TrimSpace(secret.SecretValue) == "" {
		return Result{}, fmt.Errorf("cloud account %q returned incomplete Tencent credentials", account.GetAccountId())
	}

	api, err := factory(secret.KeyID, secret.SecretValue)
	if err != nil {
		return Result{}, safeUpstreamError("create CLS client", err)
	}
	resources, err := tencent.BootstrapCLS(ctx, api, tencent.CLSBootstrapOptions{
		LogsetName: LogsetName, TopicName: TopicName, RetentionDays: retentionDays, Partitions: Partitions,
		IndexFields: tencent.MarketFetchLogIndexFields,
	})
	if err != nil {
		return Result{}, safeUpstreamError("prepare fixed CLS resources", err)
	}
	return Result{
		AccountID: account.GetAccountId(), Region: Region, Host: Host,
		LogsetID: resources.LogsetID, TopicID: resources.TopicID,
		ServiceOpened: resources.ServiceOpened, LogsetCreated: resources.LogsetCreated,
		TopicCreated: resources.TopicCreated, IndexCreated: resources.IndexCreated, IndexFieldsChanged: resources.IndexFieldsChanged,
	}, nil
}

type sanitizedUpstreamError struct {
	operation string
	cause     error
}

func (e sanitizedUpstreamError) Error() string { return e.operation + " failed" }
func (e sanitizedUpstreamError) Unwrap() error { return e.cause }

// safeUpstreamError deliberately discards collaborator error text because SDK and
// HTTP errors can contain secrets. Only standard context identity is retained.
func safeUpstreamError(operation string, err error) error {
	var cause error
	switch {
	case errors.Is(err, context.Canceled):
		cause = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		cause = context.DeadlineExceeded
	}
	return sanitizedUpstreamError{operation: operation, cause: cause}
}

func selectAccount(accounts []*collectorpb.CloudAccount, requested string) (*collectorpb.CloudAccount, error) {
	requested = strings.TrimSpace(requested)
	for _, account := range accounts {
		if account.GetProvider() != "tencent" {
			continue
		}
		if requested == "" || account.GetAccountId() == requested {
			return account, nil
		}
	}
	if requested != "" {
		return nil, fmt.Errorf("cloud account %q not found", requested)
	}
	return nil, fmt.Errorf("no Tencent cloud account is configured")
}
