package cloud

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// 本文件给原样搬来的假腾讯云（scf_baseline_fake_test.go）补上 cloud 包多用到的能力，并提供注入故障的包装。

func init() {
	// 假腾讯云在被读 pendingReads 次后回到 Active：等待间隔与重试退避调小，测试不必真的等。
	activePollInterval = time.Millisecond
	retryBaseDelay = time.Millisecond
}

// ListFunctionInventory 列出一个地域里全部命名空间的函数（清点只读，不记写请求）。
func (f *baselineSCF) ListFunctionInventory(_ context.Context, region string) ([]tencentscf.DiscoveryFunction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tencentscf.DiscoveryFunction
	for key, function := range f.functions {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) != 3 || parts[0] != region {
			continue
		}
		out = append(out, tencentscf.DiscoveryFunction{FunctionRef: tencentscf.FunctionRef{Region: parts[0], Namespace: parts[1], FunctionName: parts[2]}, Status: function.info.Status})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].FunctionName < out[j].FunctionName
	})
	return out, nil
}

// GetFunctionEventInvokeConfig 读异步重试配置（只读，不记写请求）。
func (f *baselineSCF) GetFunctionEventInvokeConfig(_ context.Context, ref tencentscf.FunctionRef) (*tencentscf.EventInvokeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[baselineKey(ref)]
	if !ok {
		return nil, fmt.Errorf("ResourceNotFound.FunctionName")
	}
	if function.asyncRetry == nil {
		return &tencentscf.EventInvokeConfig{}, nil
	}
	return &tencentscf.EventInvokeConfig{Configured: true, RetryNum: function.asyncRetry.RetryNum, MsgTTL: function.asyncRetry.MsgTTL}, nil
}

// GetReservedConcurrencyConfig 读预留并发（只读，不记写请求）。
func (f *baselineSCF) GetReservedConcurrencyConfig(_ context.Context, ref tencentscf.FunctionRef) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[baselineKey(ref)]
	if !ok {
		return 0, fmt.Errorf("ResourceNotFound.FunctionName")
	}
	return function.reservedMem, nil
}

// scriptedSCF 包一层假腾讯云，按需注入故障、在请求前后插入动作，并检查同一个函数的写请求有没有并发。
type scriptedSCF struct {
	*baselineSCF

	mu sync.Mutex
	// createErrs、codeErrs、getErrs 依次作为对应请求的返回错误（用完后照常执行）。
	createErrs []error
	codeErrs   []error
	getErrs    []error
	// afterCreate、beforeConfig 在对应请求之后、之前调用。
	afterCreate  func()
	beforeConfig func()
	// acceptUpdateFailed 为 true 时，函数是 UpdateFailed 也接受换代码与改配置（模拟腾讯云允许对更新失败的函数重做更新）。
	acceptUpdateFailed bool
	// slowWrites 让每个写请求都停一会，好让并发的写有机会撞上。
	slowWrites time.Duration

	calls    map[string]int
	inFlight map[string]int
	overlaps []string
}

func newScriptedSCF(base *baselineSCF) *scriptedSCF {
	return &scriptedSCF{baselineSCF: base, calls: map[string]int{}, inFlight: map[string]int{}}
}

func (s *scriptedSCF) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

func (s *scriptedSCF) overlapping() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.overlaps...)
}

// begin 记一次请求并返回预设的错误；写请求在这里检查同一个函数有没有别的写请求在途。
func (s *scriptedSCF) begin(name string, ref tencentscf.FunctionRef, write bool, errs *[]error) (func(), error) {
	s.mu.Lock()
	s.calls[name]++
	var err error
	if errs != nil && len(*errs) > 0 {
		err, *errs = (*errs)[0], (*errs)[1:]
	}
	key := baselineKey(ref)
	if write {
		if s.inFlight[key] > 0 {
			s.overlaps = append(s.overlaps, name+" "+key)
		}
		s.inFlight[key]++
	}
	slow := s.slowWrites
	s.mu.Unlock()
	if write && slow > 0 {
		time.Sleep(slow)
	}
	return func() {
		if write {
			s.mu.Lock()
			s.inFlight[key]--
			s.mu.Unlock()
		}
	}, err
}

func (s *scriptedSCF) GetFunction(ctx context.Context, ref tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	done, err := s.begin("GetFunction", ref, false, &s.getErrs)
	defer done()
	if err != nil {
		return nil, err
	}
	return s.baselineSCF.GetFunction(ctx, ref)
}

func (s *scriptedSCF) CreateFunction(ctx context.Context, req tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	done, err := s.begin("CreateFunction", req.FunctionRef, true, &s.createErrs)
	defer done()
	if err != nil {
		return nil, err
	}
	rsp, err := s.baselineSCF.CreateFunction(ctx, req)
	if s.afterCreate != nil {
		s.afterCreate()
	}
	return rsp, err
}

func (s *scriptedSCF) DeleteFunction(ctx context.Context, ref tencentscf.FunctionRef) error {
	done, _ := s.begin("DeleteFunction", ref, true, nil)
	defer done()
	return s.baselineSCF.DeleteFunction(ctx, ref)
}

func (s *scriptedSCF) UpdateFunctionCode(ctx context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	done, err := s.begin("UpdateFunctionCode", req.FunctionRef, true, &s.codeErrs)
	defer done()
	if err != nil {
		return nil, err
	}
	s.reviveUpdateFailed(req.FunctionRef)
	return s.baselineSCF.UpdateFunctionCode(ctx, req)
}

func (s *scriptedSCF) UpdateFunctionConfiguration(ctx context.Context, req tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	if s.beforeConfig != nil {
		s.beforeConfig()
	}
	done, _ := s.begin("UpdateFunctionConfiguration", req.FunctionRef, true, nil)
	defer done()
	s.reviveUpdateFailed(req.FunctionRef)
	return s.baselineSCF.UpdateFunctionConfiguration(ctx, req)
}

func (s *scriptedSCF) EnsureTimerTrigger(ctx context.Context, req tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error) {
	done, _ := s.begin("EnsureTimerTrigger", req.FunctionRef, true, nil)
	defer done()
	return s.baselineSCF.EnsureTimerTrigger(ctx, req)
}

func (s *scriptedSCF) GetTimerTrigger(ctx context.Context, ref tencentscf.FunctionRef, name string) (*tencentscf.TimerTriggerInfo, error) {
	done, _ := s.begin("GetTimerTrigger", ref, false, nil)
	defer done()
	return s.baselineSCF.GetTimerTrigger(ctx, ref, name)
}

func (s *scriptedSCF) DeleteTimerTrigger(ctx context.Context, req tencentscf.TimerTriggerRequest) error {
	done, _ := s.begin("DeleteTimerTrigger", req.FunctionRef, true, nil)
	defer done()
	return s.baselineSCF.DeleteTimerTrigger(ctx, req)
}

func (s *scriptedSCF) InvokeFunction(ctx context.Context, req tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	s.mu.Lock()
	s.calls["Invoke:"+req.InvokeType]++
	s.mu.Unlock()
	return s.baselineSCF.InvokeFunction(ctx, req)
}

// reviveUpdateFailed 在 acceptUpdateFailed 时把 UpdateFailed 的函数当作可写（假腾讯云本身在非 Active 时拒绝写）。
func (s *scriptedSCF) reviveUpdateFailed(ref tencentscf.FunctionRef) {
	if !s.acceptUpdateFailed {
		return
	}
	s.baselineSCF.mu.Lock()
	defer s.baselineSCF.mu.Unlock()
	if function, ok := s.baselineSCF.functions[baselineKey(ref)]; ok && function.info.Status == "UpdateFailed" {
		function.info.Status = "Active"
	}
}

// fakeCredentials 返回固定的假密钥。
type fakeCredentials struct{}

func (fakeCredentials) Resolve(_ context.Context, account store.CloudAccount) (Credential, error) {
	if account.CredentialSecretID == "" {
		return Credential{}, fmt.Errorf("没有密钥 ID")
	}
	return Credential{AccountID: account.AccountID, SecretID: "fake-secret-id", SecretKey: "fake-secret-key"}, nil
}
