package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

type clsPrepareOptions struct {
	ControlURL        string
	CloudAccountID    string
	ServiceAccessKey  string
	ServiceSecretKey  string
	CredentialsOutput string
}

type prepareRunner interface {
	Prepare(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error)
}

type realPrepareRunner struct{}

func (realPrepareRunner) Prepare(ctx context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
	result, err := clsprepare.Prepare(ctx, source, factory, opts)
	if err != nil {
		return clsprepare.Result{}, trustedCLSPrepareError{err: err}
	}
	return result, nil
}

// trustedCLSPrepareError marks diagnostics produced by clsprepare, whose public
// contract guarantees that collaborator errors and credentials are sanitized.
type trustedCLSPrepareError struct{ err error }

func (e trustedCLSPrepareError) Error() string { return e.err.Error() }
func (e trustedCLSPrepareError) Unwrap() error { return e.err }

var clsPrepareRunner prepareRunner = realPrepareRunner{}

func newCLSPrepareCommand() *cobra.Command {
	var opts clsPrepareOptions
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "发布前从 MooX 云账户准备固定 CLS 资源",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCLSPrepare(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.ControlURL, "control-url", "", "目标机 Admin service gateway 地址")
	f.StringVar(&opts.CloudAccountID, "cloud-account-id", "", "腾讯云账户 ID；缺省选择列表第一项")
	f.StringVar(&opts.ServiceAccessKey, "service-access-key", "", "后台服务鉴权 AccessKey")
	f.StringVar(&opts.ServiceSecretKey, "service-secret-key", "", "后台服务鉴权 SecretKey")
	f.StringVar(&opts.CredentialsOutput, "credentials-output", "", "写入 0600 cls.env 的路径")
	_ = cmd.MarkFlagRequired("control-url")
	_ = cmd.MarkFlagRequired("credentials-output")
	return cmd
}

func runCLSPrepare(cmd *cobra.Command, opts clsPrepareOptions) error {
	opts.ControlURL = strings.TrimSpace(opts.ControlURL)
	opts.CloudAccountID = strings.TrimSpace(opts.CloudAccountID)
	opts.CredentialsOutput = strings.TrimSpace(opts.CredentialsOutput)
	if opts.ControlURL == "" || opts.CredentialsOutput == "" {
		return fmt.Errorf("--control-url and --credentials-output are required")
	}

	client := newControlClient(opts.ControlURL, "", opts.ServiceAccessKey, opts.ServiceSecretKey, "")
	if client.ServiceAuth == nil {
		return fmt.Errorf("service authentication is required for CLS account reveal")
	}
	factory := func(secretID, secretKey string) (tencent.CLSAPI, error) {
		return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{
			SecretID:  secretID,
			SecretKey: secretKey,
			Region:    clsprepare.Region,
		})
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()
	result, err := clsPrepareRunner.Prepare(ctx, client, factory, clsprepare.Options{
		CloudAccountID:    opts.CloudAccountID,
		CredentialsOutput: opts.CredentialsOutput,
	})
	if err != nil {
		return sanitizeCLSPrepareCommandError(err)
	}
	return writeJSON(cmd, map[string]any{"status": "configured", "resources": result})
}

func sanitizeCLSPrepareCommandError(err error) error {
	var trusted trustedCLSPrepareError
	if errors.As(err, &trusted) {
		return trusted
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("prepare CLS resources failed: %w", sentinel)
		}
	}
	return errors.New("prepare CLS resources failed")
}
