package clsprepare

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
)

type fakeAccounts struct {
	items     []*collectorpb.CloudAccount
	secrets   map[string]*rpcclient.SecretMaterial
	revealed  string
	listErr   error
	revealErr error
}

func (f *fakeAccounts) ListCloudAccounts(context.Context, string) ([]*collectorpb.CloudAccount, error) {
	return f.items, f.listErr
}

func (f *fakeAccounts) GetSecretValue(_ context.Context, secretID string) (*rpcclient.SecretMaterial, error) {
	f.revealed = secretID
	return f.secrets[secretID], f.revealErr
}

type fakeCLS struct{}

func (fakeCLS) GetService(context.Context) (bool, error)    { return true, nil }
func (fakeCLS) OpenService(context.Context) (string, error) { return "", nil }
func (fakeCLS) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	return tencent.CLSLogset{ID: "logset-1", Name: LogsetName}, true, nil
}
func (fakeCLS) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (fakeCLS) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{
		ID: "topic-1", LogsetID: "logset-1", Name: TopicName, IndexEnabled: true,
	}, true, nil
}
func (fakeCLS) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (fakeCLS) EnsureIndexFields(context.Context, string, []tencent.CLSIndexField) (bool, string, error) {
	return false, "", nil
}

func (fakeCLS) CreateIndex(context.Context, string) (string, error) {
	panic("unexpected CreateIndex")
}

type failingCLS struct {
	err error
}

func (f failingCLS) GetService(context.Context) (bool, error)    { return false, f.err }
func (f failingCLS) OpenService(context.Context) (string, error) { panic("unexpected OpenService") }
func (f failingCLS) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	panic("unexpected FindLogset")
}
func (f failingCLS) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (f failingCLS) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	panic("unexpected FindTopic")
}
func (f failingCLS) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (f failingCLS) EnsureIndexFields(context.Context, string, []tencent.CLSIndexField) (bool, string, error) {
	return false, "", nil
}

func (f failingCLS) CreateIndex(context.Context, string) (string, error) {
	panic("unexpected CreateIndex")
}

func testSource() *fakeAccounts {
	return &fakeAccounts{
		items: []*collectorpb.CloudAccount{
			{AccountId: "foreign", Provider: "aws"},
			{AccountId: "newest", Provider: "tencent", CredentialSecretId: "newest"},
			{AccountId: "older", Provider: "tencent", CredentialSecretId: "older"},
		},
		secrets: map[string]*rpcclient.SecretMaterial{
			"newest": {Category: "cloud", Provider: "tencent", Status: "active", KeyID: "sid-new", SecretValue: "skey-new"},
			"older":  {Category: "cloud", Provider: "tencent", Status: "active", KeyID: "sid-old", SecretValue: "skey-old"},
		},
	}
}

func TestProvisionDefaultsToFirstActiveTencentAccount(t *testing.T) {
	source := testSource()
	result, err := Provision(context.Background(), source, func(secretID, secretKey string) (tencent.CLSAPI, error) {
		require.Equal(t, "sid-new", secretID)
		require.Equal(t, "skey-new", secretKey)
		return fakeCLS{}, nil
	}, "", 0)
	require.NoError(t, err)
	require.Equal(t, "newest", source.revealed)
	require.Equal(t, "topic-1", result.TopicID)
}

func TestProvisionUsesExplicitCloudAccountID(t *testing.T) {
	source := testSource()
	_, err := Provision(context.Background(), source, func(secretID, secretKey string) (tencent.CLSAPI, error) {
		require.Equal(t, "sid-old", secretID)
		require.Equal(t, "skey-old", secretKey)
		return fakeCLS{}, nil
	}, " older ", 0)
	require.NoError(t, err)
	require.Equal(t, "older", source.revealed)
}

func TestProvisionRejectsUnknownExplicitAccount(t *testing.T) {
	_, err := Provision(context.Background(), testSource(), func(string, string) (tencent.CLSAPI, error) {
		return fakeCLS{}, nil
	}, "missing", 0)
	require.ErrorContains(t, err, `cloud account "missing" not found`)
}

func TestProvisionRejectsIncompleteCredentialsBeforeCallingCLS(t *testing.T) {
	source := testSource()
	source.secrets["newest"] = &rpcclient.SecretMaterial{Category: "cloud", Provider: "tencent", Status: "active"}
	called := false
	_, err := Provision(context.Background(), source, func(string, string) (tencent.CLSAPI, error) {
		called = true
		return fakeCLS{}, nil
	}, "", 0)
	require.ErrorContains(t, err, "incomplete Tencent credentials")
	require.False(t, called)
}

func TestProvisionDoesNotExposeSensitiveUpstreamErrors(t *testing.T) {
	const upstream = "sid-new skey-new Authorization: TC3-HMAC-SHA256 private request-body"
	assertSanitized := func(t *testing.T, err error, context string) {
		t.Helper()
		require.ErrorContains(t, err, context)
		require.NotContains(t, err.Error(), "sid-new")
		require.NotContains(t, err.Error(), "skey-new")
		require.NotContains(t, err.Error(), "Authorization")
		require.NotContains(t, err.Error(), "request-body")
	}

	t.Run("list accounts", func(t *testing.T) {
		source := testSource()
		source.listErr = errors.New(upstream)
		_, err := Provision(context.Background(), source, func(string, string) (tencent.CLSAPI, error) {
			return fakeCLS{}, nil
		}, "", 0)
		assertSanitized(t, err, "list Tencent cloud accounts")
	})

	t.Run("reveal account", func(t *testing.T) {
		source := testSource()
		source.revealErr = errors.New(upstream)
		_, err := Provision(context.Background(), source, func(string, string) (tencent.CLSAPI, error) {
			return fakeCLS{}, nil
		}, "", 0)
		assertSanitized(t, err, `reveal cloud account "newest"`)
	})

	t.Run("create client", func(t *testing.T) {
		_, err := Provision(context.Background(), testSource(), func(string, string) (tencent.CLSAPI, error) {
			return nil, errors.New(upstream)
		}, "", 0)
		assertSanitized(t, err, "create CLS client")
	})

	t.Run("bootstrap resources", func(t *testing.T) {
		_, err := Provision(context.Background(), testSource(), func(string, string) (tencent.CLSAPI, error) {
			return failingCLS{err: errors.New(upstream)}, nil
		}, "", 0)
		assertSanitized(t, err, "prepare fixed CLS resources")
	})
}

func TestProvisionPreservesContextErrorIdentityWithoutExposingUpstreamText(t *testing.T) {
	for name, contextErr := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			upstreamErr := fmt.Errorf("sid-new skey-new Authorization: private request-body: %w", contextErr)
			_, err := Provision(context.Background(), testSource(), func(string, string) (tencent.CLSAPI, error) {
				return nil, upstreamErr
			}, "", 0)

			require.EqualError(t, err, "create CLS client failed")
			require.ErrorIs(t, err, contextErr)
			require.NotContains(t, err.Error(), "sid-new")
			require.NotContains(t, err.Error(), "skey-new")
			require.NotContains(t, err.Error(), "Authorization")
			require.NotContains(t, err.Error(), "request-body")
		})
	}
}

func TestProvisionValidatesDependencies(t *testing.T) {
	factory := func(string, string) (tencent.CLSAPI, error) { return fakeCLS{}, nil }
	_, err := Provision(context.Background(), nil, factory, "", 0)
	require.ErrorContains(t, err, "account source")
	_, err = Provision(context.Background(), testSource(), nil, "", 0)
	require.ErrorContains(t, err, "CLS factory")
}

// 记录索引字段请求的 CLS 替身：验证开通时会补齐采集日志字段。
type fieldRecordingCLS struct {
	fakeCLS
	fields *[]tencent.CLSIndexField
}

func (f fieldRecordingCLS) EnsureIndexFields(_ context.Context, _ string, fields []tencent.CLSIndexField) (bool, string, error) {
	*f.fields = fields
	return true, "request-fields", nil
}

func TestProvisionEnsuresIndexFieldsAndWritesNoCredentialFile(t *testing.T) {
	source := testSource()
	var got []tencent.CLSIndexField
	result, err := Provision(context.Background(), source, func(string, string) (tencent.CLSAPI, error) {
		return fieldRecordingCLS{fields: &got}, nil
	}, "", 0)
	require.NoError(t, err)
	require.Equal(t, "topic-1", result.TopicID)
	require.True(t, result.IndexFieldsChanged)
	require.Equal(t, tencent.MarketFetchLogIndexFields, got)
}

func TestProvisionHidesUpstreamErrorText(t *testing.T) {
	_, err := Provision(context.Background(), testSource(), func(string, string) (tencent.CLSAPI, error) {
		return failingCLS{err: errors.New("SecretId=AKIDleak")}, nil
	}, "", 0)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "AKID")
}

// newTopicCLS 模拟主题还不存在：记录建主题时用的保留天数。
type newTopicCLS struct {
	fakeCLS
	retention *int64
}

func (newTopicCLS) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{}, false, nil
}

func (f newTopicCLS) CreateTopic(_ context.Context, opts tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	*f.retention = opts.RetentionDays
	return tencent.CLSTopic{ID: "topic-new", LogsetID: opts.LogsetID, Name: opts.Name}, "request-topic", nil
}

func (newTopicCLS) CreateIndex(context.Context, string) (string, error) { return "request-index", nil }

func TestProvisionCreatesTopicWithConfiguredRetention(t *testing.T) {
	for _, tc := range []struct{ configured, want int64 }{{7, 7}, {0, RetentionDays}} {
		var got int64
		_, err := Provision(context.Background(), testSource(), func(string, string) (tencent.CLSAPI, error) {
			return newTopicCLS{retention: &got}, nil
		}, "", tc.configured)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}
