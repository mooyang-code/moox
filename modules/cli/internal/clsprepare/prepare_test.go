package clsprepare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/tencentcloud"
	"github.com/stretchr/testify/require"
)

type fakeAccounts struct {
	items     []adminclient.CloudAccount
	secrets   map[string]*adminclient.COSAccountInfo
	revealed  string
	listErr   error
	revealErr error
}

func (f *fakeAccounts) ListCloudAccounts(context.Context, string) ([]adminclient.CloudAccount, error) {
	return f.items, f.listErr
}

func (f *fakeAccounts) GetCOSAccountInfo(_ context.Context, accountID string) (*adminclient.COSAccountInfo, error) {
	f.revealed = accountID
	return f.secrets[accountID], f.revealErr
}

type fakeCLS struct{}

func (fakeCLS) GetService(context.Context) (bool, error)    { return true, nil }
func (fakeCLS) OpenService(context.Context) (string, error) { return "", nil }
func (fakeCLS) FindLogset(context.Context, string) (tencentcloud.CLSLogset, bool, error) {
	return tencentcloud.CLSLogset{ID: "logset-1", Name: LogsetName}, true, nil
}
func (fakeCLS) CreateLogset(context.Context, string) (tencentcloud.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (fakeCLS) FindTopic(context.Context, string, string) (tencentcloud.CLSTopic, bool, error) {
	return tencentcloud.CLSTopic{
		ID: "topic-1", LogsetID: "logset-1", Name: TopicName, IndexEnabled: true,
	}, true, nil
}
func (fakeCLS) CreateTopic(context.Context, tencentcloud.CLSCreateTopicOptions) (tencentcloud.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (fakeCLS) CreateIndex(context.Context, string) (string, error) {
	panic("unexpected CreateIndex")
}

type failingCLS struct {
	err error
}

func (f failingCLS) GetService(context.Context) (bool, error)    { return false, f.err }
func (f failingCLS) OpenService(context.Context) (string, error) { panic("unexpected OpenService") }
func (f failingCLS) FindLogset(context.Context, string) (tencentcloud.CLSLogset, bool, error) {
	panic("unexpected FindLogset")
}
func (f failingCLS) CreateLogset(context.Context, string) (tencentcloud.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (f failingCLS) FindTopic(context.Context, string, string) (tencentcloud.CLSTopic, bool, error) {
	panic("unexpected FindTopic")
}
func (f failingCLS) CreateTopic(context.Context, tencentcloud.CLSCreateTopicOptions) (tencentcloud.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (f failingCLS) CreateIndex(context.Context, string) (string, error) {
	panic("unexpected CreateIndex")
}

func testSource() *fakeAccounts {
	return &fakeAccounts{
		items: []adminclient.CloudAccount{
			{AccountID: "deleted", Provider: "tencent", IsDeleted: true},
			{AccountID: "foreign", Provider: "aws"},
			{AccountID: "newest", Provider: "tencent"},
			{AccountID: "older", Provider: "tencent"},
		},
		secrets: map[string]*adminclient.COSAccountInfo{
			"newest": {AccountID: "newest", Provider: "tencent", SecretID: "sid-new", SecretKey: "skey-new"},
			"older":  {AccountID: "older", Provider: "tencent", SecretID: "sid-old", SecretKey: "skey-old"},
		},
	}
}

func TestPrepareDefaultsToFirstActiveTencentAccount(t *testing.T) {
	source := testSource()
	path := filepath.Join(t.TempDir(), "cls.env")
	result, err := Prepare(context.Background(), source, func(secretID, secretKey string) (tencentcloud.CLSAPI, error) {
		require.Equal(t, "sid-new", secretID)
		require.Equal(t, "skey-new", secretKey)
		return fakeCLS{}, nil
	}, Options{CredentialsOutput: path})
	require.NoError(t, err)
	require.Equal(t, "newest", source.revealed)
	require.Equal(t, "topic-1", result.TopicID)
}

func TestPrepareUsesExplicitCloudAccountID(t *testing.T) {
	source := testSource()
	_, err := Prepare(context.Background(), source, func(secretID, secretKey string) (tencentcloud.CLSAPI, error) {
		require.Equal(t, "sid-old", secretID)
		require.Equal(t, "skey-old", secretKey)
		return fakeCLS{}, nil
	}, Options{
		CloudAccountID: " older ", CredentialsOutput: filepath.Join(t.TempDir(), "cls.env"),
	})
	require.NoError(t, err)
	require.Equal(t, "older", source.revealed)
}

func TestPrepareRejectsUnknownExplicitAccount(t *testing.T) {
	_, err := Prepare(context.Background(), testSource(), func(string, string) (tencentcloud.CLSAPI, error) {
		return fakeCLS{}, nil
	}, Options{
		CloudAccountID: "missing", CredentialsOutput: filepath.Join(t.TempDir(), "cls.env"),
	})
	require.ErrorContains(t, err, `cloud account "missing" not found`)
}

func TestPrepareWritesProtectedCredentialsWithoutReturningThem(t *testing.T) {
	source := testSource()
	path := filepath.Join(t.TempDir(), "cls.env")
	result, err := Prepare(context.Background(), source, func(string, string) (tencentcloud.CLSAPI, error) {
		return fakeCLS{}, nil
	}, Options{CredentialsOutput: path})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "MOOX_CLS_SECRET_ID='sid-new'")
	require.Contains(t, string(data), "MOOX_CLS_SECRET_KEY='skey-new'")
	require.Contains(t, string(data), "MOOX_CLS_AUTO_BOOTSTRAP=0")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "sid-new")
	require.NotContains(t, string(serialized), "skey-new")
}

func TestPrepareAtomicallyReplacesExistingCredentialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cls.env")
	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o644))

	_, err := Prepare(context.Background(), testSource(), func(string, string) (tencentcloud.CLSAPI, error) {
		return fakeCLS{}, nil
	}, Options{CredentialsOutput: path})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "stale")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	matches, err := filepath.Glob(filepath.Join(dir, ".cls.env.*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestPrepareQuotesCredentialValuesForShellEnv(t *testing.T) {
	source := testSource()
	source.secrets["newest"].SecretKey = "key'with spaces"
	path := filepath.Join(t.TempDir(), "cls.env")

	_, err := Prepare(context.Background(), source, func(string, string) (tencentcloud.CLSAPI, error) {
		return fakeCLS{}, nil
	}, Options{CredentialsOutput: path})
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `MOOX_CLS_SECRET_KEY='key'"'"'with spaces'`)
}

func TestPrepareRejectsIncompleteCredentialsBeforeCallingCLS(t *testing.T) {
	source := testSource()
	source.secrets["newest"] = &adminclient.COSAccountInfo{AccountID: "newest", Provider: "tencent"}
	called := false
	_, err := Prepare(context.Background(), source, func(string, string) (tencentcloud.CLSAPI, error) {
		called = true
		return fakeCLS{}, nil
	}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
	require.ErrorContains(t, err, "incomplete Tencent credentials")
	require.False(t, called)
}

func TestPrepareRejectsCredentialsForDifferentAccountBeforeCallingCLS(t *testing.T) {
	for _, returnedAccountID := range []string{"", "older"} {
		t.Run("returned account "+returnedAccountID, func(t *testing.T) {
			source := testSource()
			source.secrets["newest"].AccountID = returnedAccountID
			called := false
			_, err := Prepare(context.Background(), source, func(string, string) (tencentcloud.CLSAPI, error) {
				called = true
				return fakeCLS{}, nil
			}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
			require.ErrorContains(t, err, `cloud account "newest" returned credentials for a different account`)
			require.False(t, called)
		})
	}
}

func TestPrepareDoesNotExposeSensitiveUpstreamErrors(t *testing.T) {
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
		_, err := Prepare(context.Background(), source, func(string, string) (tencentcloud.CLSAPI, error) {
			return fakeCLS{}, nil
		}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
		assertSanitized(t, err, "list Tencent cloud accounts")
	})

	t.Run("reveal account", func(t *testing.T) {
		source := testSource()
		source.revealErr = errors.New(upstream)
		_, err := Prepare(context.Background(), source, func(string, string) (tencentcloud.CLSAPI, error) {
			return fakeCLS{}, nil
		}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
		assertSanitized(t, err, `reveal cloud account "newest"`)
	})

	t.Run("create client", func(t *testing.T) {
		_, err := Prepare(context.Background(), testSource(), func(string, string) (tencentcloud.CLSAPI, error) {
			return nil, errors.New(upstream)
		}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
		assertSanitized(t, err, "create CLS client")
	})

	t.Run("bootstrap resources", func(t *testing.T) {
		_, err := Prepare(context.Background(), testSource(), func(string, string) (tencentcloud.CLSAPI, error) {
			return failingCLS{err: errors.New(upstream)}, nil
		}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
		assertSanitized(t, err, "prepare fixed CLS resources")
	})
}

func TestPreparePreservesContextErrorIdentityWithoutExposingUpstreamText(t *testing.T) {
	for name, contextErr := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			upstreamErr := fmt.Errorf("sid-new skey-new Authorization: private request-body: %w", contextErr)
			_, err := Prepare(context.Background(), testSource(), func(string, string) (tencentcloud.CLSAPI, error) {
				return nil, upstreamErr
			}, Options{CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})

			require.EqualError(t, err, "create CLS client failed")
			require.ErrorIs(t, err, contextErr)
			require.NotContains(t, err.Error(), "sid-new")
			require.NotContains(t, err.Error(), "skey-new")
			require.NotContains(t, err.Error(), "Authorization")
			require.NotContains(t, err.Error(), "request-body")
		})
	}
}

func TestPrepareValidatesDependenciesAndOutput(t *testing.T) {
	factory := func(string, string) (tencentcloud.CLSAPI, error) { return fakeCLS{}, nil }
	_, err := Prepare(context.Background(), nil, factory, Options{CredentialsOutput: "unused"})
	require.ErrorContains(t, err, "account source")
	_, err = Prepare(context.Background(), testSource(), nil, Options{CredentialsOutput: "unused"})
	require.ErrorContains(t, err, "CLS factory")
	_, err = Prepare(context.Background(), testSource(), factory, Options{})
	require.ErrorContains(t, err, "credentials output")
}

func TestPrepareDoesNotReplaceCredentialsWhenBootstrapFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cls.env")
	require.NoError(t, os.WriteFile(path, []byte("preserve-me"), 0o600))

	_, err := Prepare(context.Background(), testSource(), func(string, string) (tencentcloud.CLSAPI, error) {
		return nil, errors.New("client unavailable")
	}, Options{CredentialsOutput: path})
	require.ErrorContains(t, err, "create CLS client")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "preserve-me", string(data))
}
