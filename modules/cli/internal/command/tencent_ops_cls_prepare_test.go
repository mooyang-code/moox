package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/stretchr/testify/require"
)

type prepareRunnerFunc func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error)

func (f prepareRunnerFunc) Prepare(ctx context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
	return f(ctx, source, factory, opts)
}

func TestRunCLSPrepareUsesSignedControlClientAndExplicitAccount(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "svc-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "svc-sk")
	cmd := newCLSPrepareCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	credentialPath := filepath.Join(t.TempDir(), "cls.env")
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(_ context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
		client := source.(*adminclient.Client)
		require.NotNil(t, client.ServiceAuth)
		require.Equal(t, "svc-ak", client.ServiceAuth.AccessKey)
		require.NotNil(t, factory)
		require.Equal(t, "chosen", opts.CloudAccountID)
		require.Equal(t, credentialPath, opts.CredentialsOutput)
		return clsprepare.Result{AccountID: "chosen", Region: clsprepare.Region, LogsetID: "logset-1", TopicID: "topic-1"}, nil
	})
	cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--cloud-account-id", "chosen", "--credentials-output", credentialPath})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"topic_id": "topic-1"`)
	require.NotContains(t, out.String(), "svc-ak")
	require.NotContains(t, out.String(), "svc-sk")
}

func TestRunCLSPreparePassesEmptyAccountForDefaultSelection(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "svc-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "svc-sk")
	cmd := newCLSPrepareCommand()
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(_ context.Context, _ clsprepare.AccountSource, _ clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
		require.Empty(t, opts.CloudAccountID)
		return clsprepare.Result{AccountID: "first", Region: clsprepare.Region}, nil
	})
	cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	require.NoError(t, cmd.Execute())
}

func TestCLSPrepareHasNoFixedResourceOverrideFlags(t *testing.T) {
	cmd := newCLSPrepareCommand()
	for _, name := range []string{"region", "logset-name", "topic-name"} {
		require.Nil(t, cmd.Flags().Lookup(name), "fixed CLS setting must not be configurable: %s", name)
	}
}

func TestCLSPrepareRequiresControlURLAndCredentialOutput(t *testing.T) {
	cmd := newCLSPrepareCommand()
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "control-url") || strings.Contains(err.Error(), "credentials-output"))
}

func TestCLSPrepareRequiresSignedServiceAuthentication(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "")
	cmd := newCLSPrepareCommand()
	cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	err := cmd.Execute()
	require.ErrorContains(t, err, "service authentication is required")
}

func TestCLSPrepareSanitizesRunnerErrors(t *testing.T) {
	const credential = "sid=account-id key=account-key Auth=signature body=credential-payload"
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "svc-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "svc-sk")
	cmd := newCLSPrepareCommand()
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error) {
		return clsprepare.Result{}, errors.New("upstream included " + credential)
	})
	cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	err := cmd.Execute()
	require.Error(t, err)
	require.NotContains(t, err.Error(), credential)
	for _, fragment := range []string{"account-id", "account-key", "signature", "credential-payload"} {
		require.NotContains(t, err.Error(), fragment)
	}
	require.NotContains(t, err.Error(), "svc-sk")
}

func TestCLSPreparePreservesKnownSafeRunnerDiagnostics(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "svc-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "svc-sk")
	cmd := newCLSPrepareCommand()
	oldRunner := clsPrepareRunner
	t.Cleanup(func() { clsPrepareRunner = oldRunner })
	clsPrepareRunner = prepareRunnerFunc(func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error) {
		return clsprepare.Result{}, trustedCLSPrepareError{err: errors.New(`cloud account "missing" not found`)}
	})
	cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
	err := cmd.Execute()
	require.ErrorContains(t, err, `cloud account "missing" not found`)
}

func TestCLSPrepareUnknownContextErrorsKeepIdentityWithoutLeakingText(t *testing.T) {
	for name, sentinel := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
			t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "svc-ak")
			t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "svc-sk")
			cmd := newCLSPrepareCommand()
			oldRunner := clsPrepareRunner
			t.Cleanup(func() { clsPrepareRunner = oldRunner })
			clsPrepareRunner = prepareRunnerFunc(func(context.Context, clsprepare.AccountSource, clsprepare.Factory, clsprepare.Options) (clsprepare.Result, error) {
				return clsprepare.Result{}, fmt.Errorf("sid=account-id key=account-key Auth=signature body=payload: %w", sentinel)
			})
			cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--credentials-output", filepath.Join(t.TempDir(), "cls.env")})
			err := cmd.Execute()
			require.Error(t, err)
			require.ErrorIs(t, err, sentinel)
			for _, fragment := range []string{"account-id", "account-key", "signature", "payload"} {
				require.NotContains(t, err.Error(), fragment)
			}
		})
	}
}
