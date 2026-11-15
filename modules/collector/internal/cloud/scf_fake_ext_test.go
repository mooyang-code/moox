package cloud

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// 假腾讯云在 cloudtest 包里（对账器的测试也用它），这里起几个本包测试沿用的名字。
type (
	baselineSCF           = cloudtest.SCF
	baselineFunction      = cloudtest.Function
	baselineAsyncRetry    = cloudtest.AsyncRetry
	baselineFunctionState = cloudtest.FunctionSnapshot
	baselineState         = cloudtest.State
	scriptedSCF           = cloudtest.Scripted
)

var (
	newBaselineSCF = cloudtest.NewSCF
	newScriptedSCF = cloudtest.NewScripted
)

func baselineKey(ref tencentscf.FunctionRef) string { return cloudtest.Key(ref) }

func init() {
	// 假腾讯云在被读 PendingReads 次后回到 Active：等待间隔与重试退避调小，测试不必真的等。
	UseFastPollingForTest()
}

// fakeCredentials 返回固定的假密钥。
type fakeCredentials struct{}

func (fakeCredentials) Resolve(_ context.Context, account store.CloudAccount) (Credential, error) {
	if account.CredentialSecretID == "" {
		return Credential{}, fmt.Errorf("没有密钥 ID")
	}
	return Credential{AccountID: account.AccountID, SecretID: "fake-secret-id", SecretKey: "fake-secret-key"}, nil
}
