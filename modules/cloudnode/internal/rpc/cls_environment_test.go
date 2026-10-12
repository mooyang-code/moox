package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/clsprovision"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/stretchr/testify/require"
)

type recordingCLSEnvironment struct {
	calls      int
	credential clsprovision.Credential
}

func (r *recordingCLSEnvironment) Apply(_ context.Context, _ string, credential clsprovision.Credential, environment map[string]string) bool {
	r.calls++
	r.credential = credential
	environment["MOOX_CLS_ENABLED"] = "true"
	return true
}

func TestApplyCLSEnvironmentOnlyTouchesMarketFetchFunctions(t *testing.T) {
	recorder := &recordingCLSEnvironment{}
	svc := &Service{clsEnvironment: recorder, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}}
	account := store.CloudAccount{AccountID: "acct"}
	fetcher := &store.CloudNode{Metadata: `{"biz_type":"market_fetcher"}`}
	other := &store.CloudNode{Metadata: `{"biz_type":"strategy"}`}

	environment := map[string]string{}
	require.True(t, svc.applyCLSEnvironment(context.Background(), account, fetcher, environment))
	require.Equal(t, "true", environment["MOOX_CLS_ENABLED"])
	require.Equal(t, clsprovision.Credential{SecretID: "id", SecretKey: "key"}, recorder.credential)

	untouched := map[string]string{}
	require.False(t, svc.applyCLSEnvironment(context.Background(), account, other, untouched))
	require.Empty(t, untouched)
	require.Equal(t, 1, recorder.calls)
}

func TestApplyCLSEnvironmentNeverBlocksOnCredentialOrConfigurationGaps(t *testing.T) {
	fetcher := &store.CloudNode{Metadata: `{"biz_type":"market_fetcher"}`}
	environment := map[string]string{}
	// 未配置 CLS 下发：空操作。
	require.False(t, (&Service{}).applyCLSEnvironment(context.Background(), store.CloudAccount{}, fetcher, environment))
	// 凭证取不到：不下发，也不报错。
	svc := &Service{clsEnvironment: &recordingCLSEnvironment{}, credentialResolver: fakeCredentialResolver{err: errors.New("secret unavailable")}}
	require.False(t, svc.applyCLSEnvironment(context.Background(), store.CloudAccount{}, fetcher, environment))
	require.Empty(t, environment)
}
