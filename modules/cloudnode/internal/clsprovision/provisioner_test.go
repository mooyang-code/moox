package clsprovision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	opened, logsetFound, topicFound bool
	calls                           []string
	fields                          []tencent.CLSIndexField
	failOpen                        error
}

func (f *fakeAPI) GetService(context.Context) (bool, error) { return f.opened, nil }
func (f *fakeAPI) OpenService(context.Context) (string, error) {
	f.calls = append(f.calls, "open")
	if f.failOpen != nil {
		return "", f.failOpen
	}
	f.opened = true
	return "", nil
}
func (f *fakeAPI) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	return tencent.CLSLogset{ID: "logset-1"}, f.logsetFound, nil
}
func (f *fakeAPI) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	f.calls = append(f.calls, "create-logset")
	return tencent.CLSLogset{ID: "logset-1"}, "", nil
}
func (f *fakeAPI) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{ID: "topic-1", LogsetID: "logset-1", IndexEnabled: true}, f.topicFound, nil
}
func (f *fakeAPI) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	f.calls = append(f.calls, "create-topic")
	return tencent.CLSTopic{ID: "topic-1", LogsetID: "logset-1", IndexEnabled: true}, "", nil
}
func (f *fakeAPI) CreateIndex(context.Context, string) (string, error) { return "", nil }
func (f *fakeAPI) EnsureIndexFields(_ context.Context, _ string, fields []tencent.CLSIndexField) (bool, string, error) {
	f.calls = append(f.calls, "index-fields")
	f.fields = fields
	return true, "", nil
}

func newTestProvisioner(api *fakeAPI, builds *int) *Provisioner {
	return New(DefaultConfig(), func(Credential, string, string) (tencent.CLSAPI, error) {
		*builds++
		return api, nil
	})
}

func TestApplyProvisionsOnceAndFillsTheFunctionEnvironment(t *testing.T) {
	api, builds := &fakeAPI{}, 0
	p := newTestProvisioner(api, &builds)
	env := map[string]string{"MOOX_SPACE_ID": "crypto"}
	require.True(t, p.Apply(context.Background(), "acct", Credential{SecretID: "id", SecretKey: "key"}, env))
	require.Equal(t, "true", env["MOOX_CLS_ENABLED"])
	require.Equal(t, "topic-1", env["MOOX_CLS_TOPIC_ID"])
	require.Equal(t, "logset-1", env["MOOX_CLS_LOGSET_ID"])
	require.Equal(t, "ap-guangzhou.cls.tencentcs.com", env["MOOX_CLS_ENDPOINT"])
	require.Equal(t, "3000", env["MOOX_CLS_TIMEOUT_MS"])
	require.Equal(t, tencent.MarketFetchLogIndexFields, api.fields)
	require.Contains(t, api.calls, "open")
	require.Contains(t, api.calls, "create-logset")

	// 第二个函数：命中缓存，不再调云接口。
	second := map[string]string{}
	require.True(t, p.Apply(context.Background(), "acct", Credential{SecretID: "id", SecretKey: "key"}, second))
	require.Equal(t, 1, builds)
}

func TestApplyKeepsAnOperatorDeclaredCLSEnvironment(t *testing.T) {
	api, builds := &fakeAPI{}, 0
	p := newTestProvisioner(api, &builds)
	env := map[string]string{"MOOX_CLS_ENABLED": "false"}
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Equal(t, "false", env["MOOX_CLS_ENABLED"])
	require.Zero(t, builds)
}

func TestApplyFailureNeverBlocksAndBacksOff(t *testing.T) {
	api, builds := &fakeAPI{failOpen: errors.New("SecretKey=leaked")}, 0
	p := newTestProvisioner(api, &builds)
	now := time.Now()
	p.now = func() time.Time { return now }
	env := map[string]string{}
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Empty(t, env)
	// 退避期内不重复调用云接口。
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Equal(t, 1, builds)
	// 退避过后重试。
	now = now.Add(failureBackoff + time.Second)
	api.failOpen = nil
	require.True(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Equal(t, 2, builds)
}

func TestDisabledProvisionerIsANoop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	p := New(cfg, nil)
	env := map[string]string{}
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Empty(t, env)
	require.NoError(t, cfg.Validate())
	cfg.Enabled = true
	cfg.TimeoutMS = 10
	require.Error(t, cfg.Validate())
}
