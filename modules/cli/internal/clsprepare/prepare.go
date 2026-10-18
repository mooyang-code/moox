package clsprepare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
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
	ListCloudAccounts(context.Context, string) ([]adminclient.CloudAccount, error)
	GetSecretValue(context.Context, string) (*adminclient.SecretMaterial, error)
}

type Factory func(secretID, secretKey string) (tencent.CLSAPI, error)

type Options struct {
	CloudAccountID    string
	CredentialsOutput string
}

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

// Prepare 幂等开通 CLS（服务、日志集、主题、索引字段）并把云账号凭证写入 CredentialsOutput，供打包 SCF 代码时使用。
func Prepare(ctx context.Context, source AccountSource, factory Factory, opts Options) (Result, error) {
	if strings.TrimSpace(opts.CredentialsOutput) == "" {
		return Result{}, fmt.Errorf("credentials output is required")
	}
	result, secret, err := provision(ctx, source, factory, opts.CloudAccountID)
	if err != nil {
		return Result{}, err
	}
	if err := writeCredentials(opts.CredentialsOutput, secret.KeyID, secret.SecretValue); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Provision 幂等开通 CLS（服务、日志集、主题、索引字段），不写任何凭证文件：系统初始化用它保证日志服务开箱可用。
func Provision(ctx context.Context, source AccountSource, factory Factory, cloudAccountID string) (Result, error) {
	result, _, err := provision(ctx, source, factory, cloudAccountID)
	return result, err
}

func provision(ctx context.Context, source AccountSource, factory Factory, cloudAccountID string) (Result, *adminclient.SecretMaterial, error) {
	if source == nil {
		return Result{}, nil, fmt.Errorf("account source is required")
	}
	if factory == nil {
		return Result{}, nil, fmt.Errorf("CLS factory is required")
	}

	accounts, err := source.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return Result{}, nil, safeUpstreamError("list Tencent cloud accounts", err)
	}
	account, err := selectAccount(accounts, cloudAccountID)
	if err != nil {
		return Result{}, nil, err
	}
	secret, err := source.GetSecretValue(ctx, account.CredentialSecretID)
	if err != nil {
		return Result{}, nil, safeUpstreamError(fmt.Sprintf("reveal cloud account %q", account.AccountID), err)
	}
	if secret == nil || account.CredentialSecretID == "" {
		return Result{}, nil, fmt.Errorf("cloud account %q returned incomplete Tencent credentials", account.AccountID)
	}
	if secret.Provider != "tencent" || secret.Category != "cloud" || secret.Status != "active" ||
		strings.TrimSpace(secret.KeyID) == "" || strings.TrimSpace(secret.SecretValue) == "" {
		return Result{}, nil, fmt.Errorf("cloud account %q returned incomplete Tencent credentials", account.AccountID)
	}

	api, err := factory(secret.KeyID, secret.SecretValue)
	if err != nil {
		return Result{}, nil, safeUpstreamError("create CLS client", err)
	}
	resources, err := tencent.BootstrapCLS(ctx, api, tencent.CLSBootstrapOptions{
		LogsetName: LogsetName, TopicName: TopicName, RetentionDays: RetentionDays, Partitions: Partitions,
		IndexFields: tencent.MarketFetchLogIndexFields,
	})
	if err != nil {
		return Result{}, nil, safeUpstreamError("prepare fixed CLS resources", err)
	}
	return Result{
		AccountID: account.AccountID, Region: Region, Host: Host,
		LogsetID: resources.LogsetID, TopicID: resources.TopicID,
		ServiceOpened: resources.ServiceOpened, LogsetCreated: resources.LogsetCreated,
		TopicCreated: resources.TopicCreated, IndexCreated: resources.IndexCreated, IndexFieldsChanged: resources.IndexFieldsChanged,
	}, secret, nil
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

func selectAccount(accounts []adminclient.CloudAccount, requested string) (adminclient.CloudAccount, error) {
	requested = strings.TrimSpace(requested)
	for _, account := range accounts {
		if account.Provider != "tencent" || account.IsDeleted {
			continue
		}
		if requested == "" || account.AccountID == requested {
			return account, nil
		}
	}
	if requested != "" {
		return adminclient.CloudAccount{}, fmt.Errorf("cloud account %q not found", requested)
	}
	return adminclient.CloudAccount{}, fmt.Errorf("no Tencent cloud account is configured")
}

func writeCredentials(path, secretID, secretKey string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cls.env.*")
	if err != nil {
		return fmt.Errorf("create CLS credential file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	content := fmt.Sprintf(
		"MOOX_CLS_SECRET_ID=%s\nMOOX_CLS_SECRET_KEY=%s\nMOOX_CLS_REGION=%s\nMOOX_CLS_HOST=%s\nMOOX_CLS_AUTO_BOOTSTRAP=0\n",
		quoteEnv(secretID), quoteEnv(secretKey), quoteEnv(Region), quoteEnv(Host),
	)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect CLS credential file: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write CLS credential file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync CLS credential file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close CLS credential file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install CLS credential file: %w", err)
	}

	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open CLS credential directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync CLS credential directory: %w", err)
	}
	return nil
}

func quoteEnv(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
