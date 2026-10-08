package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/adminclient/admintest"
	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/stretchr/testify/require"
)

type prepareRunnerFunc func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error)

func (f prepareRunnerFunc) Prepare(ctx context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
	return f(ctx, source, factory, opts)
}

func TestRunCLSPrepareUsesControlClientAndExplicitAccount(t *testing.T) {
	control := useTestCLSPrepareControl(t)
	cmd := newCLSPrepareCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	credentialPath := filepath.Join(t.TempDir(), "cls.env")
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(_ context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
		require.Same(t, control, source.(*adminclient.Client))
		require.NotNil(t, factory)
		require.Equal(t, "chosen", opts.CloudAccountID)
		require.Equal(t, credentialPath, opts.CredentialsOutput)
		return clsprepare.Result{AccountID: "chosen", Region: clsprepare.Region, LogsetID: "logset-1", TopicID: "topic-1"}, nil
	})
	cmd.SetArgs([]string{"--cloud-account-id", "chosen", "--credentials-output", credentialPath})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"topic_id": "topic-1"`)
}

func TestRunCLSPreparePassesEmptyAccountForDefaultSelection(t *testing.T) {
	useTestCLSPrepareControl(t)
	cmd := newCLSPrepareCommand()
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(_ context.Context, _ clsprepare.AccountSource, _ clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
		require.Empty(t, opts.CloudAccountID)
		return clsprepare.Result{AccountID: "first", Region: clsprepare.Region}, nil
	})
	cmd.SetArgs([]string{"--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	require.NoError(t, cmd.Execute())
}

func TestCLSPrepareHasNoFixedResourceOverrideFlags(t *testing.T) {
	cmd := newCLSPrepareCommand()
	for _, name := range []string{"region", "logset-name", "topic-name"} {
		require.Nil(t, cmd.Flags().Lookup(name), "fixed CLS setting must not be configurable: %s", name)
	}
}

func TestCLSPrepareRequiresCredentialOutput(t *testing.T) {
	cmd := newCLSPrepareCommand()
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.ErrorContains(t, err, "credentials-output")
}

func TestCLSPrepareReportsControlConnectionFailure(t *testing.T) {
	old := clsPrepareOpenControl
	t.Cleanup(func() { clsPrepareOpenControl = old })
	clsPrepareOpenControl = func(string) (*adminclient.Client, func(), error) {
		return nil, nil, errors.New("创建 moox-cli 的 gatewayclient: 密钥文件不存在")
	}
	cmd := newCLSPrepareCommand()
	cmd.SetArgs([]string{"--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	require.ErrorContains(t, cmd.Execute(), "gatewayclient")
}

// useTestCLSPrepareControl 让 CLS prepare 使用测试替身客户端，返回该客户端。
func useTestCLSPrepareControl(t *testing.T) *adminclient.Client {
	t.Helper()
	control := admintest.Client("http://127.0.0.1:1")
	old := clsPrepareOpenControl
	t.Cleanup(func() { clsPrepareOpenControl = old })
	clsPrepareOpenControl = func(string) (*adminclient.Client, func(), error) { return control, func() {}, nil }
	return control
}

func TestCLSPrepareSanitizesRunnerErrors(t *testing.T) {
	const credential = "sid=account-id key=account-key Auth=signature body=credential-payload"
	useTestCLSPrepareControl(t)
	cmd := newCLSPrepareCommand()
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error) {
		return clsprepare.Result{}, errors.New("upstream included " + credential)
	})
	cmd.SetArgs([]string{"--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	err := cmd.Execute()
	require.Error(t, err)
	require.NotContains(t, err.Error(), credential)
	for _, fragment := range []string{"account-id", "account-key", "signature", "credential-payload"} {
		require.NotContains(t, err.Error(), fragment)
	}
	require.NotContains(t, err.Error(), "svc-sk")
}

func TestCLSPreparePreservesKnownSafeRunnerDiagnostics(t *testing.T) {
	useTestCLSPrepareControl(t)
	cmd := newCLSPrepareCommand()
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error) {
		return clsprepare.Result{}, trustedCLSPrepareError{err: errors.New(`cloud account "missing" not found`)}
	})
	cmd.SetArgs([]string{"--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	err := cmd.Execute()
	require.ErrorContains(t, err, `cloud account "missing" not found`)
}

func TestCLSPrepareUnknownContextErrorsKeepIdentityWithoutLeakingText(t *testing.T) {
	for name, sentinel := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			useTestCLSPrepareControl(t)
			cmd := newCLSPrepareCommand()
			oldRunner := clsPrepareRunner
			t.Cleanup(func() { clsPrepareRunner = oldRunner })
			clsPrepareRunner = prepareRunnerFunc(func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error) {
				return clsprepare.Result{}, fmt.Errorf("sid=account-id key=account-key Auth=signature body=payload: %w", sentinel)
			})
			cmd.SetArgs([]string{"--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
			err := cmd.Execute()
			require.Error(t, err)
			require.ErrorIs(t, err, sentinel)
			for _, fragment := range []string{"account-id", "account-key", "signature", "payload"} {
				require.NotContains(t, err.Error(), fragment)
			}
		})
	}
}
