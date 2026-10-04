# CLS Predeploy Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Before every CLS-enabled release, select a Tencent cloud account, query or create the fixed MooX CLS resources, inject the returned Topic ID into staged tRPC configs, and install writer credentials without exposing them.

**Architecture:** A new `internal/clsprepare` package owns account selection, fixed CLS names, Tencent API orchestration, and atomic credential-file creation. A `moox-cli ops tencent cls prepare` command exposes that logic through the signed service gateway. The MooX Skill runs the target-architecture CLI on the deployment target before the deploy script stops any service, then patches only staged configs with the sanitized Topic ID.

For remote targets, the Skill may upload one temporary `moox-cli` helper to `/tmp` so the first rollout does not depend on an older installed CLI. This preflight helper is removed immediately; the release archive is not uploaded and no running service is stopped until preflight succeeds.

**Tech Stack:** Go 1.24, Cobra, existing MooX `adminclient`, Tencent Cloud CLS SDK, tRPC-Go YAML, Bash, SSH/SCP, Go tests, Bash contract tests.

---

## File Map

- Create `modules/cli/internal/clsprepare/prepare.go`: select the cloud account, call CLS with fixed resource names, and atomically write `cls.env`.
- Create `modules/cli/internal/clsprepare/prepare_test.go`: cover default/explicit account selection, idempotent cloud calls, credential validation, output secrecy, and file permissions.
- Create `modules/cli/internal/command/tencent_ops_cls_prepare.go`: register `moox-cli ops tencent cls prepare` and connect signed Admin access to `clsprepare`.
- Create `modules/cli/internal/command/tencent_ops_cls_prepare_test.go`: verify required flags, fixed settings, signed service-route use, and sanitized JSON.
- Modify `modules/cli/internal/adminclient/cloudnode_test.go`: prove credential reveal uses the signed service route and sends `reveal=true`.
- Create `skills/moox/scripts/cls-bootstrap.sh`: run prepare locally or through SSH, install `cls.env`, and patch staged tRPC configs.
- Create `skills/moox/scripts/test/contract/test-cls-bootstrap.sh`: test local orchestration, first-account selection propagation, literal Topic ID insertion, permissions, idempotence, and failure behavior.
- Modify `scripts/deploy/deploy-moox.sh`: add `--cloud-account-id`, invoke Skill preparation before sync/stop, and remove startup-time CLS provisioning.
- Create `scripts/test/contract/test-deploy-moox-cls.sh`: enforce deployment ordering and verify a failed preflight cannot stop existing services.
- Modify `scripts/test/contract/test-trpc-plugin-config.sh`: update the CLS configuration contract.
- Modify `skills/moox/SKILL.md`: document mandatory predeploy CLS initialization.
- Modify `docs/运维/tRPC插件运行基线.md`: describe account selection, fixed names, and failure behavior.

### Task 1: Build the CLS Preparation Domain

**Files:**
- Create: `modules/cli/internal/clsprepare/prepare.go`
- Create: `modules/cli/internal/clsprepare/prepare_test.go`

- [ ] **Step 1: Write the failing account-selection tests**

Create `prepare_test.go` with fakes that use the existing CloudNode response types:

```go
package clsprepare

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/tencentcloud"
	"github.com/stretchr/testify/require"
)

type fakeAccounts struct {
	items   []adminclient.CloudAccount
	secrets map[string]*adminclient.COSAccountInfo
	revealed string
}

func (f *fakeAccounts) ListCloudAccounts(context.Context, string) ([]adminclient.CloudAccount, error) {
	return f.items, nil
}

func (f *fakeAccounts) GetCOSAccountInfo(_ context.Context, accountID string) (*adminclient.COSAccountInfo, error) {
	f.revealed = accountID
	return f.secrets[accountID], nil
}

type fakeCLS struct{}

func (fakeCLS) GetService(context.Context) (bool, error) { return true, nil }
func (fakeCLS) OpenService(context.Context) (string, error) { return "", nil }
func (fakeCLS) FindLogset(context.Context, string) (tencentcloud.CLSLogset, bool, error) {
	return tencentcloud.CLSLogset{ID: "logset-1", Name: LogsetName}, true, nil
}
func (fakeCLS) CreateLogset(context.Context, string) (tencentcloud.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (fakeCLS) FindTopic(context.Context, string, string) (tencentcloud.CLSTopic, bool, error) {
	return tencentcloud.CLSTopic{ID: "topic-1", LogsetID: "logset-1", Name: TopicName, IndexEnabled: true}, true, nil
}
func (fakeCLS) CreateTopic(context.Context, tencentcloud.CLSCreateTopicOptions) (tencentcloud.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (fakeCLS) CreateIndex(context.Context, string) (string, error) { panic("unexpected CreateIndex") }

func testSource() *fakeAccounts {
	return &fakeAccounts{
		items: []adminclient.CloudAccount{
			{AccountID: "newest", Provider: "tencent"},
			{AccountID: "older", Provider: "tencent"},
		},
		secrets: map[string]*adminclient.COSAccountInfo{
			"newest": {AccountID: "newest", Provider: "tencent", SecretID: "sid-new", SecretKey: "skey-new"},
			"older":  {AccountID: "older", Provider: "tencent", SecretID: "sid-old", SecretKey: "skey-old"},
		},
	}
}

func TestPrepareDefaultsToFirstTencentAccount(t *testing.T) {
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
	}, Options{CloudAccountID: "older", CredentialsOutput: filepath.Join(t.TempDir(), "cls.env")})
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
```

- [ ] **Step 2: Run the tests and verify the missing package behavior fails**

Run:

```bash
cd modules/cli
go test -count=1 ./internal/clsprepare
```

Expected: FAIL because `Prepare`, `Options`, `LogsetName`, and `TopicName` do not exist.

- [ ] **Step 3: Implement fixed names, account selection, CLS lookup, and protected file output**

Create `prepare.go`:

```go
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
	CloudAccountID  string
	CredentialsOutput string
}

type Result struct {
	AccountID      string `json:"account_id"`
	Region         string `json:"region"`
	Host           string `json:"host"`
	LogsetID       string `json:"logset_id"`
	TopicID        string `json:"topic_id"`
	ServiceOpened  bool   `json:"service_opened"`
	LogsetCreated  bool   `json:"logset_created"`
	TopicCreated   bool   `json:"topic_created"`
	IndexCreated   bool   `json:"index_created"`
}

func Prepare(ctx context.Context, source AccountSource, factory Factory, opts Options) (Result, error) {
	if source == nil || factory == nil {
		return Result{}, fmt.Errorf("account source and CLS factory are required")
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
	if secret == nil || secret.Provider != "tencent" || secret.SecretID == "" || secret.SecretKey == "" {
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
		return Result{}, err
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
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install CLS credential file: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func quoteEnv(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
```

- [ ] **Step 4: Add credential safety and incomplete-account tests**

Append to `prepare_test.go`:

```go
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
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NotContains(t, result.AccountID+result.TopicID, "sid-new")
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
```

- [ ] **Step 5: Run the package tests**

Run:

```bash
cd modules/cli
go test -count=1 ./internal/clsprepare
```

Expected: PASS.

- [ ] **Step 6: Commit the domain layer**

```bash
git add modules/cli/internal/clsprepare
git commit -m "feat(cli): prepare fixed CLS resources from cloud accounts"
```

### Task 2: Expose `moox-cli ops tencent cls prepare`

**Files:**
- Create: `modules/cli/internal/command/tencent_ops_cls_prepare.go`
- Create: `modules/cli/internal/command/tencent_ops_cls_prepare_test.go`
- Modify: `modules/cli/internal/command/tencent_ops_cls.go`
- Modify: `modules/cli/internal/adminclient/cloudnode_test.go`

- [ ] **Step 1: Write failing command tests**

Create `tencent_ops_cls_prepare_test.go`:

```go
package command

import (
	"bytes"
	"context"
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

func TestRunCLSPrepareUsesSignedControlClientAndSanitizesOutput(t *testing.T) {
	t.Setenv("MOOX_SERVICE_AUTH_ACCESS_KEY", "svc-ak")
	t.Setenv("MOOX_SERVICE_AUTH_SECRET_KEY", "svc-sk")
	cmd := newCLSPrepareCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	credentialPath := filepath.Join(t.TempDir(), "cls.env")
	oldRunner := clsPrepareRunner
	clsPrepareRunner = prepareRunnerFunc(func(_ context.Context, source clsprepare.AccountSource, _ clsprepare.Factory, opts clsprepare.Options) (clsprepare.Result, error) {
		client := source.(*adminclient.Client)
		require.NotNil(t, client.ServiceAuth)
		require.Equal(t, "chosen", opts.CloudAccountID)
		require.Equal(t, credentialPath, opts.CredentialsOutput)
		return clsprepare.Result{AccountID: "chosen", Region: clsprepare.Region, LogsetID: "logset-1", TopicID: "topic-1"}, nil
	})
	defer func() { clsPrepareRunner = oldRunner }()
	cmd.SetArgs([]string{"--control-url", "http://127.0.0.1:11002", "--cloud-account-id", "chosen", "--credentials-output", credentialPath})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"topic_id": "topic-1"`)
	require.NotContains(t, out.String(), "svc-sk")
}

func TestCLSPrepareRequiresControlURLAndCredentialOutput(t *testing.T) {
	cmd := newCLSPrepareCommand()
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "control-url") || strings.Contains(err.Error(), "credentials-output"))
}
```

Append this regression test to `modules/cli/internal/adminclient/cloudnode_test.go`:

```go
func TestGetCOSAccountInfoUsesSignedServiceRouteAndReveal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/cloudnode/GetCOSAccountInfo", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("Auth"))
		var body struct {
			AccountID string `json:"account_id"`
			Reveal    bool   `json:"reveal"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "acct-1", body.AccountID)
		require.True(t, body.Reveal)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"account_id":"acct-1","provider":"tencent","secret_id":"sid","secret_key":"skey"}}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.HTTPClient = server.Client()
	client.ServiceAuth = &ServiceAuthConfig{Version: "moox-auth-v2", AccessKey: "ak", SecretKey: "sk", ExpireSecs: 60}
	secret, err := client.GetCOSAccountInfo(context.Background(), "acct-1")
	require.NoError(t, err)
	require.Equal(t, "sid", secret.SecretID)
}
```

Add `encoding/json` to that test file's imports.

- [ ] **Step 2: Run the tests and confirm they fail because the command does not exist**

Run:

```bash
cd modules/cli
go test -count=1 ./internal/command -run 'TestRunCLSPrepare|TestCLSPrepareRequires'
```

Expected: FAIL with undefined `newCLSPrepareCommand` and `clsPrepareRunner`.

- [ ] **Step 3: Implement the prepare command with fixed CLS settings**

Create `tencent_ops_cls_prepare.go`:

```go
package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/mooyang-code/moox/modules/cli/internal/tencentcloud"
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
	return clsprepare.Prepare(ctx, source, factory, opts)
}

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
	if strings.TrimSpace(opts.ControlURL) == "" || strings.TrimSpace(opts.CredentialsOutput) == "" {
		return fmt.Errorf("--control-url and --credentials-output are required")
	}
	client := newControlClient(opts.ControlURL, "", opts.ServiceAccessKey, opts.ServiceSecretKey, "")
	if client.ServiceAuth == nil {
		return fmt.Errorf("service authentication is required for CLS account reveal")
	}
	factory := func(secretID, secretKey string) (tencentcloud.CLSAPI, error) {
		return tencentcloud.NewCLSSDKAPI(tencentcloud.CLSSDKOptions{
			SecretID: secretID, SecretKey: secretKey, Region: clsprepare.Region,
		})
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()
	result, err := clsPrepareRunner.Prepare(ctx, client, factory, clsprepare.Options{
		CloudAccountID: opts.CloudAccountID, CredentialsOutput: opts.CredentialsOutput,
	})
	if err != nil {
		return err
	}
	return writeJSON(cmd, map[string]any{"status": "configured", "resources": result})
}
```

Register the command in `tencent_ops_cls.go` without adding flags that can override the fixed name or region:

```go
func init() {
	tencentOpsCmd.AddCommand(clsOpsCmd)
	clsOpsCmd.AddCommand(clsBootstrapCmd)
	clsOpsCmd.AddCommand(newCLSPrepareCommand())
	// existing bootstrap flags remain unchanged
}
```

- [ ] **Step 4: Run command and adminclient tests**

Run:

```bash
cd modules/cli
go test -count=1 ./internal/command ./internal/adminclient
```

Expected: PASS. The existing `adminclient` rewrites `/api/admin/cloudnode/*` to `/api/service/cloudnode/*` whenever service auth is configured.

- [ ] **Step 5: Commit the CLI surface**

```bash
git add modules/cli/internal/command/tencent_ops_cls_prepare.go \
  modules/cli/internal/command/tencent_ops_cls_prepare_test.go \
  modules/cli/internal/command/tencent_ops_cls.go \
  modules/cli/internal/adminclient/cloudnode_test.go
git commit -m "feat(cli): add CLS predeploy prepare command"
```

### Task 3: Add the MooX Skill CLS Bootstrap Script

**Files:**
- Create: `skills/moox/scripts/cls-bootstrap.sh`
- Create: `skills/moox/scripts/test/contract/test-cls-bootstrap.sh`

- [ ] **Step 1: Write the failing local orchestration test**

Create `test-cls-bootstrap.sh`. The fake CLI must record its arguments, emit sanitized JSON, and write credentials only to the requested file:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-cls-skill.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
STAGE="${TMP}/stage"
DEPLOY="${TMP}/deploy"
CALLS="${TMP}/calls"
mkdir -p "${STAGE}/bin" "${STAGE}/factor/config" "${STAGE}/storage/config" "${DEPLOY}/secrets"
cp "${ROOT}/modules/factor/config/trpc_go.yaml" "${STAGE}/factor/config/trpc_go.yaml"
cp "${ROOT}/modules/storage/config/trpc_go.yaml" "${STAGE}/storage/config/trpc_go.yaml"
printf 'MOOX_SERVICE_AUTH_ACCESS_KEY=svc-ak\nMOOX_SERVICE_AUTH_SECRET_KEY=svc-sk\n' >"${DEPLOY}/secrets/service-auth.env"
chmod 0600 "${DEPLOY}/secrets/service-auth.env"

cat >"${STAGE}/bin/moox-cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${MOOX_TEST_CALLS}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --credentials-output) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "MOOX_CLS_SECRET_ID='secret-id'\nMOOX_CLS_SECRET_KEY='secret-key'\nMOOX_CLS_REGION='ap-guangzhou'\nMOOX_CLS_HOST='ap-guangzhou.cls.tencentyun.com'\nMOOX_CLS_AUTO_BOOTSTRAP=0\n" >"${output}"
chmod 0600 "${output}"
printf '{"status":"configured","resources":{"account_id":"acct-first","topic_id":"topic-fixed"}}\n'
EOF
chmod +x "${STAGE}/bin/moox-cli"

MOOX_TEST_CALLS="${CALLS}" "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
  --admin-url http://127.0.0.1:11002

grep -q -- '--control-url http://127.0.0.1:11002' "${CALLS}"
if grep -q -- '--cloud-account-id' "${CALLS}"; then
  echo 'default invocation unexpectedly supplied cloud-account-id' >&2
  exit 1
fi
grep -q 'writer: cls' "${STAGE}/factor/config/trpc_go.yaml"
grep -q 'topic_id: topic-fixed' "${STAGE}/factor/config/trpc_go.yaml"
grep -q 'topic_id: topic-fixed' "${STAGE}/storage/config/trpc_go.yaml"
[[ $(stat -f '%Lp' "${DEPLOY}/secrets/cls.env" 2>/dev/null || stat -c '%a' "${DEPLOY}/secrets/cls.env") == 600 ]]
! grep -R -q 'secret-key' "${STAGE}"

MOOX_TEST_CALLS="${CALLS}" "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
  --target localhost --deploy-dir "${DEPLOY}" --stage-dir "${STAGE}" \
  --admin-url http://127.0.0.1:11002 --cloud-account-id explicit
[[ $(grep -c 'writer: cls' "${STAGE}/factor/config/trpc_go.yaml") == 1 ]]
grep -q -- '--cloud-account-id explicit' "${CALLS}"
[[ $(wc -l <"${CALLS}" | tr -d ' ') == 2 ]]

echo 'CLS Skill bootstrap contract passed'
```

- [ ] **Step 2: Run the script test and verify it fails because the Skill entrypoint is missing**

Run:

```bash
bash skills/moox/scripts/test/contract/test-cls-bootstrap.sh
```

Expected: FAIL with `skills/moox/scripts/cls-bootstrap.sh: No such file or directory`.

- [ ] **Step 3: Implement local and remote execution with atomic credential installation**

Create `cls-bootstrap.sh` with these concrete behaviors:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TARGET=localhost
DEPLOY_DIR="~/moox"
STAGE_DIR="${ROOT}/release/deploy-stage/moox"
ADMIN_URL=http://127.0.0.1:11002
CLOUD_ACCOUNT_ID=""

fail() { printf '[cls-bootstrap] ERROR: %s\n' "$*" >&2; exit 1; }
quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
is_local() { [[ "${TARGET}" == localhost || "${TARGET}" == 127.0.0.1 || "${TARGET}" == ::1 ]]; }
expand_local() {
  case "$1" in
    "~") printf '%s\n' "${HOME}" ;;
    "~/"*) printf '%s/%s\n' "${HOME}" "${1#~/}" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="${2:-}"; shift 2 ;;
    --deploy-dir) DEPLOY_DIR="${2:-}"; shift 2 ;;
    --stage-dir) STAGE_DIR="${2:-}"; shift 2 ;;
    --admin-url) ADMIN_URL="${2:-}"; shift 2 ;;
    --cloud-account-id) CLOUD_ACCOUNT_ID="${2:-}"; shift 2 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ -x "${STAGE_DIR}/bin/moox-cli" ]] || fail "missing staged moox-cli"
[[ -d "${STAGE_DIR}" ]] || fail "missing stage directory"
account_args=()
[[ -z "${CLOUD_ACCOUNT_ID}" ]] || account_args=(--cloud-account-id "${CLOUD_ACCOUNT_ID}")

run_local() {
  local deploy next output
  deploy=$(expand_local "${DEPLOY_DIR}")
  next="${deploy}/secrets/cls.env.next"
  [[ -r "${deploy}/secrets/service-auth.env" ]] || fail "missing service-auth.env"
  mkdir -p "${deploy}/secrets"
  set -a; source "${deploy}/secrets/service-auth.env"; set +a
  if ! output=$("${STAGE_DIR}/bin/moox-cli" ops tencent cls prepare \
    --control-url "${ADMIN_URL}" --credentials-output "${next}" "${account_args[@]}"); then
    rm -f "${next}"
    return 1
  fi
  chmod 0600 "${next}"
  mv "${next}" "${deploy}/secrets/cls.env"
  printf '%s' "${output}"
}

run_remote() {
  local token="${RANDOM}.$$" remote_cli="/tmp/moox-cli-cls-${token}" output
  scp -q "${STAGE_DIR}/bin/moox-cli" "${TARGET}:${remote_cli}"
  if ! output=$(ssh -o BatchMode=yes "${TARGET}" bash -s -- \
    "${remote_cli}" "${DEPLOY_DIR}" "${ADMIN_URL}" "${CLOUD_ACCOUNT_ID}" <<'REMOTE'
set -euo pipefail
cli=$1; deploy=$2; admin_url=$3; account_id=$4
case "${deploy}" in
  "~") deploy="${HOME}" ;;
  "~/"*) deploy="${HOME}/${deploy#~/}" ;;
esac
mkdir -p "${deploy}/secrets"
trap 'rm -f "${deploy}/secrets/cls.env.next"' EXIT
[[ -r "${deploy}/secrets/service-auth.env" ]]
set -a; source "${deploy}/secrets/service-auth.env"; set +a
chmod 0700 "${cli}"
args=()
[[ -z "${account_id}" ]] || args=(--cloud-account-id "${account_id}")
"${cli}" ops tencent cls prepare --control-url "${admin_url}" \
  --credentials-output "${deploy}/secrets/cls.env.next" "${args[@]}"
chmod 0600 "${deploy}/secrets/cls.env.next"
mv "${deploy}/secrets/cls.env.next" "${deploy}/secrets/cls.env"
REMOTE
  ); then
    ssh -o BatchMode=yes "${TARGET}" "rm -f $(quote "${remote_cli}")" >/dev/null 2>&1 || true
    return 1
  fi
  ssh -o BatchMode=yes "${TARGET}" "rm -f $(quote "${remote_cli}")" >/dev/null 2>&1 || true
  printf '%s' "${output}"
}

if is_local; then result=$(run_local); else result=$(run_remote); fi
topic_id=$(printf '%s' "${result}" | sed -n 's/.*"topic_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | tail -1)
[[ -n "${topic_id}" ]] || fail "CLS prepare returned no topic_id"

while IFS= read -r config; do
  tmp="${config}.cls.tmp"
  awk '/# BEGIN MOOX MANAGED CLS/{skip=1} !skip{print} /# END MOOX MANAGED CLS/{skip=0}' "${config}" >"${tmp}"
  mv "${tmp}" "${config}"
  if grep -q '^  log:' "${config}"; then
    cat >>"${config}" <<EOF
# BEGIN MOOX MANAGED CLS
      - writer: cls
        level: warn
        remote_config:
          topic_id: ${topic_id}
          host: \${MOOX_CLS_HOST}
          secret_id: \${MOOX_CLS_SECRET_ID}
          secret_key: \${MOOX_CLS_SECRET_KEY}
          max_block_sec: 0
# END MOOX MANAGED CLS
EOF
  else
    cat >>"${config}" <<EOF
# BEGIN MOOX MANAGED CLS
  log:
    default:
      - writer: cls
        level: warn
        remote_config:
          topic_id: ${topic_id}
          host: \${MOOX_CLS_HOST}
          secret_id: \${MOOX_CLS_SECRET_ID}
          secret_key: \${MOOX_CLS_SECRET_KEY}
          max_block_sec: 0
# END MOOX MANAGED CLS
EOF
  fi
done < <(find "${STAGE_DIR}" -path '*/config/trpc_go*.yaml' -type f -print)

printf '[cls-bootstrap] account and CLS resource verified; topic_id=%s\n' "${topic_id}"
```

During implementation, keep the heredoc delimiters quoted exactly as shown so the Skill script never expands credential placeholders into stage files.

- [ ] **Step 4: Run Bash syntax and behavior tests**

Run:

```bash
bash -n skills/moox/scripts/cls-bootstrap.sh skills/moox/scripts/test/contract/test-cls-bootstrap.sh
bash skills/moox/scripts/test/contract/test-cls-bootstrap.sh
```

Expected: both commands PASS; test output ends with `CLS Skill bootstrap contract passed`.

- [ ] **Step 5: Commit the Skill script**

```bash
git add skills/moox/scripts/cls-bootstrap.sh skills/moox/scripts/test/contract/test-cls-bootstrap.sh
git commit -m "feat(skill): prepare CLS before MooX deployment"
```

### Task 4: Integrate CLS Preflight Into Deployment

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Create: `scripts/test/contract/test-deploy-moox-cls.sh`

- [ ] **Step 1: Write a failing deployment ordering contract**

Create `test-deploy-moox-cls.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT}/scripts/deploy/deploy-moox.sh"

grep -q -- '--cloud-account-id' "${SCRIPT}"
grep -q 'skills/moox/scripts/cls-bootstrap.sh' "${SCRIPT}"
grep -q 'prepare_cls_preflight' "${SCRIPT}"
if grep -q '^bootstrap_cls()' "${SCRIPT}"; then
  echo 'runtime start script still provisions CLS' >&2
  exit 1
fi

prepare_line=$(grep -n '^prepare_cls_preflight$' "${SCRIPT}" | tail -1 | cut -d: -f1)
sync_line=$(grep -n '^if is_local_target; then$' "${SCRIPT}" | tail -1 | cut -d: -f1)
[[ -n "${prepare_line}" && -n "${sync_line}" && ${prepare_line} -lt ${sync_line} ]]

grep -q 'ops tencent cls prepare' "${ROOT}/skills/moox/scripts/cls-bootstrap.sh"
echo 'CLS deployment ordering contract passed'
```

- [ ] **Step 2: Run the contract and verify it fails**

Run:

```bash
bash scripts/test/contract/test-deploy-moox-cls.sh
```

Expected: FAIL because `--cloud-account-id` and `prepare_cls_preflight` are absent.

- [ ] **Step 3: Add the deploy flag and invoke the Skill before sync can stop services**

Modify the top-level state, usage, and argument parser in `deploy-moox.sh`:

```bash
ENABLE_CLS=0
CLOUD_ACCOUNT_ID=""

# usage
  --enable-cls                    Prepare fixed CLS resources and add production CLS writers.
  --cloud-account-id <id>         Tencent cloud account for CLS; default is the first account.

# parser
    --enable-cls) ENABLE_CLS=1; shift ;;
    --cloud-account-id) CLOUD_ACCOUNT_ID="${2:-}"; shift 2 ;;
```

Remove the entire CLS writer append block from `patch_configs`. Remove `bootstrap_cls()` and its call from the generated `start.sh`. Keep sourcing `${ROOT}/secrets/cls.env`, because tRPC expands writer credential placeholders at process start.

Add this function outside the generated runtime heredoc:

```bash
prepare_cls_preflight() {
  [[ "${ENABLE_CLS}" -eq 1 ]] || return 0
  local admin_url=http://127.0.0.1:11002
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" \
    --target "${TARGET}" \
    --deploy-dir "${DEPLOY_DIR}" \
    --stage-dir "${STAGE_DIR}" \
    --admin-url "${admin_url}" \
    ${CLOUD_ACCOUNT_ID:+--cloud-account-id "${CLOUD_ACCOUNT_ID}"}
}
```

Avoid the unsafe scalar expansion in the final implementation by building an array:

```bash
prepare_cls_preflight() {
  [[ "${ENABLE_CLS}" -eq 1 ]] || return 0
  local args=(
    --target "${TARGET}"
    --deploy-dir "${DEPLOY_DIR}"
    --stage-dir "${STAGE_DIR}"
    --admin-url http://127.0.0.1:11002
  )
  [[ -z "${CLOUD_ACCOUNT_ID}" ]] || args+=(--cloud-account-id "${CLOUD_ACCOUNT_ID}")
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh" "${args[@]}"
}
```

Call it only after stage creation and before either sync path:

```bash
build_core_binaries
build_web_host_binary
prepare_stage
prepare_cls_preflight

if is_local_target; then
  sync_local_stage
else
  sync_remote_stage
fi
```

This ordering allows build and stage creation, but a failed Admin, account, or CLS check occurs before `sync_local_stage` or `sync_remote_stage` can stop the current deployment.

- [ ] **Step 4: Run deploy syntax and focused contracts**

Run:

```bash
bash -n scripts/deploy/deploy-moox.sh
bash scripts/test/contract/test-deploy-moox-cls.sh
bash scripts/test/contract/test-deploy-moox-preserve-disabled.sh
bash scripts/test/contract/test-deploy-moox-admin-bootstrap.sh
```

Expected: PASS for all four commands.

- [ ] **Step 5: Commit deployment integration**

```bash
git add scripts/deploy/deploy-moox.sh scripts/test/contract/test-deploy-moox-cls.sh
git commit -m "feat(deploy): verify CLS before service shutdown"
```

### Task 5: Update Contracts and Operations Documentation

**Files:**
- Modify: `scripts/test/contract/test-trpc-plugin-config.sh`
- Modify: `skills/moox/SKILL.md`
- Modify: `docs/运维/tRPC插件运行基线.md`
- Modify: `docs/superpowers/specs/2026-07-13-cls-predeploy-bootstrap-design.md`

- [ ] **Step 1: Update the static plugin contract**

Replace the old runtime-bootstrap checks in `scripts/test/contract/test-trpc-plugin-config.sh`:

```bash
require_text scripts/deploy/deploy-moox.sh '--enable-cls' 'deployment must expose explicit CLS activation'
require_text scripts/deploy/deploy-moox.sh '--cloud-account-id' 'deployment must allow explicit CLS cloud account selection'
require_text scripts/deploy/deploy-moox.sh 'prepare_cls_preflight' 'deployment must prepare CLS before sync'
require_text skills/moox/scripts/cls-bootstrap.sh 'ops tencent cls prepare' 'MooX Skill must prepare CLS through moox-cli'
require_text skills/moox/scripts/cls-bootstrap.sh 'topic_id: ${topic_id}' 'staged CLS writer must contain the verified Topic ID'
require_text skills/moox/scripts/cls-bootstrap.sh 'secret_id: \${MOOX_CLS_SECRET_ID}' 'staged CLS writer must retain credential placeholders'
```

Delete the obsolete assertion that requires `ops tencent cls bootstrap` inside `deploy-moox.sh`.

- [ ] **Step 2: Document the Skill workflow**

Add a `Tencent CLS Before Deployment` section to `skills/moox/SKILL.md`:

````markdown
### Tencent CLS Before Deployment

When CLS is enabled, prepare it before uploading or stopping services:

```bash
skills/moox/scripts/cls-bootstrap.sh \
  --target user@host \
  --deploy-dir /home/user/moox \
  --stage-dir release/deploy-stage/moox \
  --admin-url http://127.0.0.1:11002
```

The script selects the first Tencent cloud account unless `--cloud-account-id` is set. It always queries the `ap-guangzhou` CLS API for Logset `moox` and Topic `moox-application`, then writes the returned Topic ID only into stage configs. It stores writer credentials only in the target's `secrets/cls.env` with mode `0600`.
````

Also update the System Initialization Deployment Flow so `scripts/deploy/deploy-moox.sh --enable-cls` requires an already-running Admin/CloudNode control plane and at least one Tencent cloud account.

- [ ] **Step 3: Update the operations baseline and design status**

In `docs/运维/tRPC插件运行基线.md`, replace startup-time auto-bootstrap text with:

```markdown
CLS-enabled releases run a predeploy check before service shutdown. The check uses the selected cloud account, or the first Tencent account when no ID is supplied, and queries fixed `ap-guangzhou` resources: Logset `moox` and Topic `moox-application`. It creates only missing resources and writes the verified Topic ID into staged `trpc_go*.yaml` files. Writer credentials remain in target `secrets/cls.env` with mode `0600`.
```

Add an implementation-status note to the design spec pointing to the new CLI command, Skill script, deployment contract, and tests. Do not change the approved security boundaries.

- [ ] **Step 4: Run documentation and configuration contracts**

Run:

```bash
bash scripts/test/contract/test-trpc-plugin-config.sh
bash skills/moox/scripts/test/contract/test-cls-bootstrap.sh
./scripts/build/package-skill.sh
tar -tzf dist/moox-skill.tar.gz | grep -E 'moox/scripts/cls-bootstrap.sh|moox/SKILL.md'
```

Expected: all commands PASS and the archive lists both required Skill files.

- [ ] **Step 5: Commit contracts and documentation**

```bash
git add scripts/test/contract/test-trpc-plugin-config.sh skills/moox/SKILL.md \
  docs/运维/tRPC插件运行基线.md \
  docs/superpowers/specs/2026-07-13-cls-predeploy-bootstrap-design.md
git commit -m "docs: require CLS preparation in MooX releases"
```

### Task 6: Complete Verification and Runtime Acceptance

**Files:**
- Verify only; do not modify source unless a test exposes a defect.

- [ ] **Step 1: Run fresh CLI tests**

```bash
cd modules/cli
go test -count=1 ./internal/clsprepare ./internal/adminclient ./internal/tencentcloud ./internal/command
```

Expected: PASS with no credential values in test output.

- [ ] **Step 2: Run all CLS and deployment contracts**

From the repository root:

```bash
bash skills/moox/scripts/test/contract/test-cls-bootstrap.sh
bash scripts/test/contract/test-deploy-moox-cls.sh
bash scripts/test/contract/test-trpc-plugin-config.sh
bash scripts/test/contract/test-deploy-moox-preserve-disabled.sh
bash scripts/test/contract/test-deploy-moox-admin-bootstrap.sh
bash -n scripts/deploy/deploy-moox.sh skills/moox/scripts/cls-bootstrap.sh
```

Expected: PASS for every command.

- [ ] **Step 3: Verify secret hygiene**

```bash
tracked_cls_env=$(git ls-files '*cls.env')
[[ -z "${tracked_cls_env}" ]]
if git grep -nE 'secret_(id|key): [^$]' -- 'modules/*/config/*.yaml'; then exit 1; fi
if rg -n 'MOOX_CLS_SECRET_(ID|KEY)=' release/deploy-stage 2>/dev/null; then exit 1; fi
git diff --check
```

Expected: no committed or packaged credential values and no whitespace errors. Remove generated `dist/moox-skill.tar.gz` if it is untracked or ignored build output.

- [ ] **Step 4: Run a real predeploy check without stopping services**

Build the target release stage, then invoke only the Skill script:

```bash
skills/moox/scripts/cls-bootstrap.sh \
  --target user@host \
  --deploy-dir /home/user/moox \
  --stage-dir release/deploy-stage/moox \
  --admin-url http://127.0.0.1:11002
```

Expected: sanitized output reports account ID, Logset ID, and Topic ID; it never prints SecretId or SecretKey. On the target, `stat -c '%a' /home/user/moox/secrets/cls.env` prints `600`.

- [ ] **Step 5: Verify Factor stage configuration**

```bash
rg -n 'writer: cls|topic_id:|secret_id:|secret_key:' release/deploy-stage/moox/factor/config/trpc_go.yaml
```

Expected: one CLS writer, a non-empty literal Topic ID, and environment placeholders for both credential fields.

- [ ] **Step 6: Deploy and verify one Factor warning in CLS**

```bash
scripts/deploy/deploy-moox.sh \
  --target user@host \
  --dir /home/user/moox \
  --public-host host.example \
  --enable-cls
```

After Factor starts, emit one approved warning without request bodies or credentials. Query Topic `moox-application` in `ap-guangzhou` and confirm the record contains the Factor service name and deployment version.

Expected: the deployment succeeds, Factor remains healthy, and the warning appears once in the fixed Topic.

- [ ] **Step 7: Commit any acceptance-note updates, then push**

If runtime acceptance adds only verified IDs or timestamps, record them without credentials in `docs/运维/tRPC插件运行基线.md` and commit:

```bash
git add docs/运维/tRPC插件运行基线.md
git commit -m "docs: record CLS deployment acceptance"
git push origin main
git status --short --branch
```

Expected: `## main...origin/main` with no worktree changes.
