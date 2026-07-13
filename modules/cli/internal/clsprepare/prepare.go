package clsprepare

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/tencentcloud"
)

const (
	Region     = "ap-guangzhou"
	LogsetName = "moox"
	TopicName  = "moox-application"
	Host       = Region + ".cls.tencentyun.com"
)

type AccountSource interface {
	ListCloudAccounts(context.Context, string) ([]adminclient.CloudAccount, error)
	GetCOSAccountInfo(context.Context, string) (*adminclient.COSAccountInfo, error)
}

type Factory func(secretID, secretKey string) (tencentcloud.CLSAPI, error)

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
}

func Prepare(ctx context.Context, source AccountSource, factory Factory, opts Options) (Result, error) {
	if source == nil {
		return Result{}, fmt.Errorf("account source is required")
	}
	if factory == nil {
		return Result{}, fmt.Errorf("CLS factory is required")
	}
	if strings.TrimSpace(opts.CredentialsOutput) == "" {
		return Result{}, fmt.Errorf("credentials output is required")
	}

	accounts, err := source.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return Result{}, fmt.Errorf("list Tencent cloud accounts: %w", err)
	}
	account, err := selectAccount(accounts, opts.CloudAccountID)
	if err != nil {
		return Result{}, err
	}
	secret, err := source.GetCOSAccountInfo(ctx, account.AccountID)
	if err != nil {
		return Result{}, fmt.Errorf("reveal cloud account %q: %w", account.AccountID, err)
	}
	if secret == nil || secret.Provider != "tencent" ||
		strings.TrimSpace(secret.SecretID) == "" || strings.TrimSpace(secret.SecretKey) == "" {
		return Result{}, fmt.Errorf("cloud account %q returned incomplete Tencent credentials", account.AccountID)
	}

	api, err := factory(secret.SecretID, secret.SecretKey)
	if err != nil {
		return Result{}, fmt.Errorf("create CLS client: %w", err)
	}
	resources, err := tencentcloud.BootstrapCLS(ctx, api, tencentcloud.CLSBootstrapOptions{
		LogsetName: LogsetName, TopicName: TopicName, RetentionDays: 30, Partitions: 1,
	})
	if err != nil {
		return Result{}, fmt.Errorf("prepare fixed CLS resources: %w", err)
	}
	if err := writeCredentials(opts.CredentialsOutput, secret.SecretID, secret.SecretKey); err != nil {
		return Result{}, err
	}

	return Result{
		AccountID: account.AccountID, Region: Region, Host: Host,
		LogsetID: resources.LogsetID, TopicID: resources.TopicID,
		ServiceOpened: resources.ServiceOpened, LogsetCreated: resources.LogsetCreated,
		TopicCreated: resources.TopicCreated, IndexCreated: resources.IndexCreated,
	}, nil
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
