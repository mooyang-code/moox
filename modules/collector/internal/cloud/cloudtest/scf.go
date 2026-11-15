// Package cloudtest 是 cloud 包与对账器测试共用的假腾讯云：带状态、记录写请求，不发任何网络请求。
// 只给测试引用（生产代码不得引用）。
package cloudtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
)

// SCF 是一个带状态、只记录写请求的假腾讯云，给批 2 的金样用：
// 每条路径结束时导出云上的最终状态（环境、配置、定时器），并留下写请求的先后顺序。
// 它只依赖 tencentscf 的请求与返回类型。批 2 的 T4 把它连同金样文件从 CloudFunction 原样搬进 cloud 包的测试，
// T5 为了让对账器（fetchplan）的测试也能用同一个假腾讯云，把它挪成了可导入的包，状态语义没有改。
//
// 状态变化按真实客户端（tencentscf.Client）的语义模拟：
//   - UpdateFunctionConfiguration 的环境整份替换；内存、超时、并发、公网为零值时保持不变；
//     VPC 只在请求带了或要求清除时才变。
//   - EnsureTimerTrigger 没有就建、有就按请求改，读回的可用状态固定为 Available。
//   - 建函数、换代码、改配置之后，函数先停在 Creating 或 Updating，被读过 PendingReads 次后才回到 Active
//     （PendingReads 为 0 时立即 Active）。函数不是 Active 时，换代码、改配置、设定时器、预留并发、
//     异步重试这些写请求一律被拒绝：真实的腾讯云也是这样，调用方必须先等到 Active。
type SCF struct {
	mu sync.Mutex
	// PendingReads 是每次建函数、换代码、改配置之后，函数回到 Active 之前要被读的次数。
	PendingReads int
	namespaces   map[string]struct{}
	functions    map[string]*Function
	// writes 是写请求的先后顺序，每项一行。只读请求（GetFunction、GetTimerTrigger）不记。
	writes []string
	// rejected 是因为函数不是 Active 而被拒绝的写请求（不计入 writes）。
	rejected []string
	// inventories 是 ListFunctionInventory（清点整个地域）的调用次数。
	inventories int
}

// Function 是假腾讯云里一个函数的全部状态。
type Function struct {
	Info        tencentscf.FunctionInfo
	Handler     string
	Description string
	COSBucket   string
	COSRegion   string
	COSObject   string
	// NativeCLSCleared 记录 SCF 自带的日志投递是否已被清掉：清过一次就一直是清过，后面不带清除标记的改配置不会把它恢复。
	NativeCLSCleared bool
	// Pending 是函数回到 Active 之前还要被读的次数。
	Pending     int
	Timer       *tencentscf.TimerTriggerInfo
	ReservedMem int64
	AsyncRetry  *AsyncRetry
}

// AsyncRetry 是异步重试配置。
type AsyncRetry struct {
	RetryNum int64 `json:"retry_num"`
	MsgTTL   int64 `json:"msg_ttl"`
}

// FunctionSnapshot 是导出到金样文件的函数状态。
type FunctionSnapshot struct {
	Status                 string            `json:"status"`
	Runtime                string            `json:"runtime"`
	Type                   string            `json:"type"`
	Handler                string            `json:"handler"`
	Description            string            `json:"description"`
	MemorySize             int64             `json:"memory_size"`
	Timeout                int64             `json:"timeout"`
	MaxInstanceConcurrency int64             `json:"max_instance_concurrency"`
	PublicNetStatus        string            `json:"public_net_status"`
	VpcID                  string            `json:"vpc_id"`
	SubnetID               string            `json:"subnet_id"`
	Code                   Code              `json:"code"`
	NativeCLSCleared       bool              `json:"native_cls_cleared"`
	Environment            map[string]string `json:"environment"`
	Timer                  *TimerSnapshot    `json:"timer"`
	ReservedConcurrencyMem int64             `json:"reserved_concurrency_mem"`
	AsyncRetry             *AsyncRetry       `json:"async_retry"`
}

// Code 是金样文件里的一部分。
type Code struct {
	COSBucket string `json:"cos_bucket"`
	COSRegion string `json:"cos_region"`
	COSObject string `json:"cos_object"`
}

// TimerSnapshot 是金样文件里的一部分。
type TimerSnapshot struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	Cron            string `json:"cron"`
	Enabled         bool   `json:"enabled"`
	AvailableStatus string `json:"available_status"`
	Qualifier       string `json:"qualifier"`
	Message         string `json:"message"`
}

// State 是一条路径结束时假腾讯云的最终状态。
type State struct {
	Namespaces []string                    `json:"namespaces"`
	Functions  map[string]FunctionSnapshot `json:"functions"`
}

// NewSCF 返回一个空的假腾讯云。
func NewSCF() *SCF {
	return &SCF{namespaces: map[string]struct{}{}, functions: map[string]*Function{}}
}

// Key 返回函数的“地域/命名空间/函数名”。
func Key(ref tencentscf.FunctionRef) string {
	return ref.Region + "/" + ref.Namespace + "/" + ref.FunctionName
}

func copyEnvironment(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// record 记一条写请求。调用方已持有锁。
func (f *SCF) record(format string, args ...any) {
	f.writes = append(f.writes, fmt.Sprintf(format, args...))
}

// packageMarker 返回函数当前的代码包标记与实际代码对象，写进定时器请求的记录里，
// 用来检查“代码包落后时不启用定时器”。
func (fn *Function) packageMarker() string {
	return fmt.Sprintf("package_id=%s code=%s", fn.Info.Environment["MOOX_CODE_PACKAGE_ID"], fn.COSObject)
}

// Seed 直接在假腾讯云里放一个函数（不记写请求）。
func (f *SCF) Seed(ref tencentscf.FunctionRef, function *Function) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.namespaces[ref.Region+"/"+ref.Namespace] = struct{}{}
	f.functions[Key(ref)] = function
}

// Mutate 直接改假腾讯云里一个函数的状态（不记写请求），用来模拟云上的现状。
func (f *SCF) Mutate(ref tencentscf.FunctionRef, change func(*Function)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.functions[Key(ref)])
}

// Lookup 返回云上一个函数的状态副本；没有这个函数时第二个返回值为 false。
func (f *SCF) Lookup(ref tencentscf.FunctionRef) (Function, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[Key(ref)]
	if !ok {
		return Function{}, false
	}
	out := *function
	out.Info.Environment = copyEnvironment(function.Info.Environment)
	if function.Timer != nil {
		timer := *function.Timer
		out.Timer = &timer
	}
	return out, true
}

// Names 返回云上全部函数的“地域/命名空间/函数名”，已排序。
func (f *SCF) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.functions))
	for key := range f.functions {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

// Writes 返回到目前为止的写请求（不清空）。
func (f *SCF) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

// InventoryCalls 返回清点整个地域（ListFunctionInventory）的调用次数。
func (f *SCF) InventoryCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inventories
}

// ResetWrites 清空写请求记录，开始录下一条路径。
func (f *SCF) ResetWrites() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = nil
}

// Snapshot 导出写请求顺序与最终状态。
func (f *SCF) Snapshot() ([]string, State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := State{Functions: map[string]FunctionSnapshot{}}
	for namespace := range f.namespaces {
		state.Namespaces = append(state.Namespaces, namespace)
	}
	sort.Strings(state.Namespaces)
	for key, function := range f.functions {
		item := FunctionSnapshot{
			Status: function.Info.Status, Runtime: function.Info.Runtime, Type: function.Info.Type,
			Handler: function.Handler, Description: function.Description,
			MemorySize: function.Info.MemorySize, Timeout: function.Info.Timeout,
			MaxInstanceConcurrency: function.Info.MaxInstanceConcurrency, PublicNetStatus: function.Info.PublicNetStatus,
			VpcID: function.Info.VpcID, SubnetID: function.Info.SubnetID,
			Code:                   Code{COSBucket: function.COSBucket, COSRegion: function.COSRegion, COSObject: function.COSObject},
			NativeCLSCleared:       function.NativeCLSCleared,
			Environment:            copyEnvironment(function.Info.Environment),
			ReservedConcurrencyMem: function.ReservedMem, AsyncRetry: function.AsyncRetry,
		}
		if timer := function.Timer; timer != nil {
			item.Timer = &TimerSnapshot{Name: timer.Name, Type: timer.Type, Cron: timer.Cron, Enabled: timer.Enabled, AvailableStatus: timer.AvailableStatus, Qualifier: timer.Qualifier, Message: timer.Message}
		}
		state.Functions[key] = item
	}
	return append([]string(nil), f.writes...), state
}

// RejectedWrites 返回因为函数不是 Active 而被拒绝的写请求。
func (f *SCF) RejectedWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rejected...)
}

func (f *SCF) EnsureNamespace(_ context.Context, region, namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := region + "/" + namespace
	if _, ok := f.namespaces[key]; !ok {
		f.namespaces[key] = struct{}{}
		f.record("CreateNamespace %s", key)
	}
	return nil
}

func (f *SCF) GetFunction(_ context.Context, ref tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[Key(ref)]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	info := function.Info
	info.Environment = copyEnvironment(function.Info.Environment)
	if function.Pending > 0 {
		// 这一次读到的还是过渡状态；读够次数之后回到 Active。
		function.Pending--
		if function.Pending == 0 {
			function.Info.Status = "Active"
		}
	}
	return &info, nil
}

// transition 让函数进入过渡状态（调用方已持有锁）。
func (f *SCF) transition(function *Function, status string) {
	if f.PendingReads > 0 {
		function.Info.Status, function.Pending = status, f.PendingReads
	}
}

// rejectTriggerWhileCreateFailed 模拟腾讯云在函数 CreateFailed 时拒绝一切触发器操作（2026-11-14 真机实测，读写都拒绝）。
// 调用方已持有锁。
func (f *SCF) rejectTriggerWhileCreateFailed(operation, key string, function *Function) error {
	if function.Info.Status != "CreateFailed" {
		return nil
	}
	f.rejected = append(f.rejected, fmt.Sprintf("%s %s status=CreateFailed", operation, key))
	return errors.New("FailedOperation: functionInfo Status is CreateFailed, unsupport operate")
}

// requireActive 在函数不是 Active 时拒绝写请求（调用方已持有锁）。
func (f *SCF) requireActive(operation, key string, function *Function) error {
	if function.Info.Status == "Active" {
		return nil
	}
	f.rejected = append(f.rejected, fmt.Sprintf("%s %s status=%s", operation, key, function.Info.Status))
	return fmt.Errorf("FailedOperation.FunctionStatusError: %s 在函数状态为 %s 时不能执行", operation, function.Info.Status)
}

func (f *SCF) CreateFunction(_ context.Context, req tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	if _, ok := f.functions[key]; ok {
		return nil, errors.New("ResourceInUse.FunctionName")
	}
	function := &Function{
		Info: tencentscf.FunctionInfo{
			Status: "Active", Runtime: req.Runtime, Type: req.Type, MemorySize: req.MemorySize, Timeout: req.Timeout,
			MaxInstanceConcurrency: req.MaxInstanceConcurrency, PublicNetStatus: strings.ToUpper(strings.TrimSpace(req.PublicNetStatus)),
			VpcID: req.VpcID, SubnetID: req.SubnetID, Environment: copyEnvironment(req.Environment),
		},
		Handler: req.Handler, Description: req.Description,
		COSBucket: req.COSBucket, COSRegion: req.COSRegion, COSObject: req.COSObject,
	}
	f.functions[key] = function
	f.transition(function, "Creating")
	f.record("CreateFunction %s %s", key, function.packageMarker())
	return &tencentscf.CreateFunctionResponse{}, nil
}

func (f *SCF) DeleteFunction(_ context.Context, ref tencentscf.FunctionRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(ref)
	if _, ok := f.functions[key]; !ok {
		return errors.New("ResourceNotFound.FunctionName")
	}
	delete(f.functions, key)
	f.record("DeleteFunction %s", key)
	return nil
}

func (f *SCF) UpdateFunctionCode(_ context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("UpdateFunctionCode", key, function); err != nil {
		return nil, err
	}
	function.Handler, function.COSBucket, function.COSRegion, function.COSObject = req.Handler, req.COSBucket, req.COSRegion, req.COSObject
	f.transition(function, "Updating")
	f.record("UpdateFunctionCode %s %s", key, function.packageMarker())
	return &tencentscf.UpdateFunctionCodeResponse{}, nil
}

func (f *SCF) UpdateFunctionConfiguration(_ context.Context, req tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("UpdateFunctionConfiguration", key, function); err != nil {
		return nil, err
	}
	function.Info.Environment = copyEnvironment(req.Environment)
	if req.MemorySize > 0 {
		function.Info.MemorySize = req.MemorySize
	}
	if req.Timeout > 0 {
		function.Info.Timeout = req.Timeout
	}
	if req.MaxInstanceConcurrency > 0 {
		function.Info.MaxInstanceConcurrency = req.MaxInstanceConcurrency
	}
	if status := strings.ToUpper(strings.TrimSpace(req.PublicNetStatus)); status != "" {
		function.Info.PublicNetStatus = status
	}
	if req.ClearVPC {
		function.Info.VpcID, function.Info.SubnetID = "", ""
	} else if strings.TrimSpace(req.VpcID) != "" || strings.TrimSpace(req.SubnetID) != "" {
		function.Info.VpcID, function.Info.SubnetID = strings.TrimSpace(req.VpcID), strings.TrimSpace(req.SubnetID)
	}
	if req.ClearNativeCLS {
		function.NativeCLSCleared = true
	}
	f.transition(function, "Updating")
	f.record("UpdateFunctionConfiguration %s %s", key, function.packageMarker())
	return &tencentscf.UpdateFunctionConfigurationResponse{}, nil
}

func (f *SCF) EnsureTimerTrigger(_ context.Context, req tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.rejectTriggerWhileCreateFailed("EnsureTimerTrigger", key, function); err != nil {
		return nil, err
	}
	if err := f.requireActive("EnsureTimerTrigger", key, function); err != nil {
		return nil, err
	}
	function.Timer = &tencentscf.TimerTriggerInfo{Name: req.Name, Type: "timer", Cron: req.Cron, Enabled: req.Enabled, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}
	f.record("EnsureTimerTrigger %s cron=%q enabled=%t %s", key, req.Cron, req.Enabled, function.packageMarker())
	info := *function.Timer
	return &info, nil
}

func (f *SCF) GetTimerTrigger(_ context.Context, ref tencentscf.FunctionRef, _ string) (*tencentscf.TimerTriggerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[Key(ref)]
	if ok {
		if err := f.rejectTriggerWhileCreateFailed("GetTimerTrigger", Key(ref), function); err != nil {
			return nil, err
		}
	}
	if !ok || function.Timer == nil {
		return nil, errors.New("ResourceNotFound.Trigger")
	}
	info := *function.Timer
	return &info, nil
}

func (f *SCF) DeleteTimerTrigger(_ context.Context, req tencentscf.TimerTriggerRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	function, ok := f.functions[key]
	if ok {
		if err := f.rejectTriggerWhileCreateFailed("DeleteTimerTrigger", key, function); err != nil {
			return err
		}
	}
	if !ok || function.Timer == nil {
		return errors.New("ResourceNotFound.Trigger")
	}
	function.Timer = nil
	f.record("DeleteTimerTrigger %s", key)
	return nil
}

func (f *SCF) InvokeFunction(context.Context, tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	return &tencentscf.InvokeFunctionResponse{}, nil
}

// PutReservedConcurrencyConfig 是靠类型断言调用的可选方法（预留并发）。
func (f *SCF) PutReservedConcurrencyConfig(_ context.Context, req tencentscf.PutReservedConcurrencyConfigRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("PutReservedConcurrencyConfig", key, function); err != nil {
		return err
	}
	function.ReservedMem = req.ReservedConcurrencyMem
	f.record("PutReservedConcurrencyConfig %s mem=%d", key, req.ReservedConcurrencyMem)
	return nil
}

// UpdateFunctionEventInvokeConfig 是靠类型断言调用的可选方法（异步重试）。
func (f *SCF) UpdateFunctionEventInvokeConfig(_ context.Context, req tencentscf.UpdateFunctionEventInvokeConfigRequest) (*tencentscf.UpdateFunctionEventInvokeConfigResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("UpdateFunctionEventInvokeConfig", key, function); err != nil {
		return nil, err
	}
	function.AsyncRetry = &AsyncRetry{RetryNum: req.RetryNum, MsgTTL: req.MsgTTL}
	f.record("UpdateFunctionEventInvokeConfig %s retry=%d msg_ttl=%d", key, req.RetryNum, req.MsgTTL)
	return &tencentscf.UpdateFunctionEventInvokeConfigResponse{}, nil
}

// ListFunctionInventory 列出一个地域里全部命名空间的函数（清点只读，不记写请求）。
func (f *SCF) ListFunctionInventory(_ context.Context, region string) ([]tencentscf.DiscoveryFunction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventories++
	var out []tencentscf.DiscoveryFunction
	for key, function := range f.functions {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) != 3 || parts[0] != region {
			continue
		}
		out = append(out, tencentscf.DiscoveryFunction{FunctionRef: tencentscf.FunctionRef{Region: parts[0], Namespace: parts[1], FunctionName: parts[2]}, Status: function.Info.Status})
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
func (f *SCF) GetFunctionEventInvokeConfig(_ context.Context, ref tencentscf.FunctionRef) (*tencentscf.EventInvokeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[Key(ref)]
	if !ok {
		return nil, fmt.Errorf("ResourceNotFound.FunctionName")
	}
	if function.AsyncRetry == nil {
		return &tencentscf.EventInvokeConfig{}, nil
	}
	return &tencentscf.EventInvokeConfig{Configured: true, RetryNum: function.AsyncRetry.RetryNum, MsgTTL: function.AsyncRetry.MsgTTL}, nil
}

// GetReservedConcurrencyConfig 读预留并发（只读，不记写请求）。
func (f *SCF) GetReservedConcurrencyConfig(_ context.Context, ref tencentscf.FunctionRef) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[Key(ref)]
	if !ok {
		return 0, fmt.Errorf("ResourceNotFound.FunctionName")
	}
	return function.ReservedMem, nil
}

// Scripted 包一层假腾讯云，按需注入故障、在请求前后插入动作，并检查同一个函数的写请求有没有并发。
type Scripted struct {
	*SCF

	mu sync.Mutex
	// CreateErrs、CodeErrs、GetErrs 等依次作为对应请求的返回错误（用完后照常执行）。在发起操作之前设好。
	CreateErrs      []error
	CodeErrs        []error
	GetErrs         []error
	DeleteTimerErrs []error
	ReservedErrs    []error
	// AfterCreate、BeforeConfig 在对应请求之后、之前调用。
	AfterCreate  func()
	BeforeConfig func()
	// AcceptUpdateFailed 为 true 时，函数是 UpdateFailed 也接受换代码与改配置（模拟腾讯云允许对更新失败的函数重做更新）。
	AcceptUpdateFailed bool
	// AcceptTimerWhileFailed 为 true 时，函数在 Updating、UpdateFailed 时也接受改定时器（2026-11-14 真机实测如此）；
	// CreateFailed 时仍然拒绝一切触发器操作（真机实测）。
	AcceptTimerWhileFailed bool
	// configRequests 记下每一次改配置的请求。
	configRequests []tencentscf.UpdateFunctionConfigurationRequest
	// SlowWrites 让每个写请求都停一会，好让并发的写有机会撞上。
	SlowWrites time.Duration
	// BeforeWrite 在每个写请求真正执行之前调用（请求名、函数），可以在这里阻塞来模拟慢操作；返回错误时这个请求失败。
	BeforeWrite func(name string, ref tencentscf.FunctionRef) error
	// BeforeRead 在每个读请求（GetFunction、GetTimerTrigger）真正执行之前调用；返回错误时这个请求失败（模拟读不到）。
	BeforeRead func(name string, ref tencentscf.FunctionRef) error
	// Invoke 替换调用函数的结果（出口 IP 探测、补采调用）；为空时返回空结果。
	Invoke func(req tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error)

	calls    map[string]int
	inFlight map[string]int
	overlaps []string
}

// NewScripted 包一层 base。
func NewScripted(base *SCF) *Scripted {
	return &Scripted{SCF: base, calls: map[string]int{}, inFlight: map[string]int{}}
}

// Count 返回某个请求被调用的次数（InvokeFunction 记为 "Invoke:<调用类型>"）。
func (s *Scripted) Count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// Overlapping 返回同一个函数上并发的写请求。
func (s *Scripted) Overlapping() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.overlaps...)
}

// begin 记一次请求并返回预设的错误；写请求在这里检查同一个函数有没有别的写请求在途。
func (s *Scripted) begin(name string, ref tencentscf.FunctionRef, write bool, errs *[]error) (func(), error) {
	s.mu.Lock()
	s.calls[name]++
	var err error
	if errs != nil && len(*errs) > 0 {
		err, *errs = (*errs)[0], (*errs)[1:]
	}
	key := Key(ref)
	if write {
		if s.inFlight[key] > 0 {
			s.overlaps = append(s.overlaps, name+" "+key)
		}
		s.inFlight[key]++
	}
	slow, before, beforeRead := s.SlowWrites, s.BeforeWrite, s.BeforeRead
	s.mu.Unlock()
	if write && slow > 0 {
		time.Sleep(slow)
	}
	if write && before != nil && err == nil {
		err = before(name, ref)
	}
	if !write && beforeRead != nil && err == nil {
		err = beforeRead(name, ref)
	}
	return func() {
		if write {
			s.mu.Lock()
			s.inFlight[key]--
			s.mu.Unlock()
		}
	}, err
}

func (s *Scripted) GetFunction(ctx context.Context, ref tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	done, err := s.begin("GetFunction", ref, false, &s.GetErrs)
	defer done()
	if err != nil {
		return nil, err
	}
	return s.SCF.GetFunction(ctx, ref)
}

func (s *Scripted) CreateFunction(ctx context.Context, req tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	done, err := s.begin("CreateFunction", req.FunctionRef, true, &s.CreateErrs)
	defer done()
	if err != nil {
		return nil, err
	}
	rsp, err := s.SCF.CreateFunction(ctx, req)
	if s.AfterCreate != nil {
		s.AfterCreate()
	}
	return rsp, err
}

func (s *Scripted) DeleteFunction(ctx context.Context, ref tencentscf.FunctionRef) error {
	done, err := s.begin("DeleteFunction", ref, true, nil)
	defer done()
	if err != nil {
		return err
	}
	return s.SCF.DeleteFunction(ctx, ref)
}

func (s *Scripted) UpdateFunctionCode(ctx context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	done, err := s.begin("UpdateFunctionCode", req.FunctionRef, true, &s.CodeErrs)
	defer done()
	if err != nil {
		return nil, err
	}
	s.reviveUpdateFailed(req.FunctionRef)
	return s.SCF.UpdateFunctionCode(ctx, req)
}

func (s *Scripted) UpdateFunctionConfiguration(ctx context.Context, req tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	if s.BeforeConfig != nil {
		s.BeforeConfig()
	}
	done, err := s.begin("UpdateFunctionConfiguration", req.FunctionRef, true, nil)
	defer done()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.configRequests = append(s.configRequests, req)
	s.mu.Unlock()
	s.reviveUpdateFailed(req.FunctionRef)
	return s.SCF.UpdateFunctionConfiguration(ctx, req)
}

func (s *Scripted) EnsureTimerTrigger(ctx context.Context, req tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error) {
	done, err := s.begin("EnsureTimerTrigger", req.FunctionRef, true, nil)
	defer done()
	if err != nil {
		return nil, err
	}
	if s.AcceptTimerWhileFailed {
		// 暂时当作 Active 写定时器，写完恢复原来的失败状态。
		s.SCF.mu.Lock()
		function, ok := s.SCF.functions[Key(req.FunctionRef)]
		status := ""
		if ok && function.Info.Status != "CreateFailed" {
			status, function.Info.Status = function.Info.Status, "Active"
		} else {
			ok = false
		}
		s.SCF.mu.Unlock()
		if ok {
			defer func() {
				s.SCF.mu.Lock()
				function.Info.Status = status
				s.SCF.mu.Unlock()
			}()
		}
	}
	return s.SCF.EnsureTimerTrigger(ctx, req)
}

func (s *Scripted) GetTimerTrigger(ctx context.Context, ref tencentscf.FunctionRef, name string) (*tencentscf.TimerTriggerInfo, error) {
	done, err := s.begin("GetTimerTrigger", ref, false, nil)
	defer done()
	if err != nil {
		return nil, err
	}
	return s.SCF.GetTimerTrigger(ctx, ref, name)
}

func (s *Scripted) DeleteTimerTrigger(ctx context.Context, req tencentscf.TimerTriggerRequest) error {
	done, err := s.begin("DeleteTimerTrigger", req.FunctionRef, true, &s.DeleteTimerErrs)
	defer done()
	if err != nil {
		return err
	}
	return s.SCF.DeleteTimerTrigger(ctx, req)
}

func (s *Scripted) PutReservedConcurrencyConfig(ctx context.Context, req tencentscf.PutReservedConcurrencyConfigRequest) error {
	done, err := s.begin("PutReservedConcurrencyConfig", req.FunctionRef, true, &s.ReservedErrs)
	defer done()
	if err != nil {
		return err
	}
	return s.SCF.PutReservedConcurrencyConfig(ctx, req)
}

func (s *Scripted) InvokeFunction(ctx context.Context, req tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	s.mu.Lock()
	s.calls["Invoke:"+req.InvokeType]++
	invoke := s.Invoke
	s.mu.Unlock()
	if invoke != nil {
		return invoke(req)
	}
	return s.SCF.InvokeFunction(ctx, req)
}

// ConfigRequests 返回每一次改配置的请求。
func (s *Scripted) ConfigRequests() []tencentscf.UpdateFunctionConfigurationRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tencentscf.UpdateFunctionConfigurationRequest(nil), s.configRequests...)
}

// reviveUpdateFailed 在 acceptUpdateFailed 时把 UpdateFailed 的函数当作可写（假腾讯云本身在非 Active 时拒绝写）。
func (s *Scripted) reviveUpdateFailed(ref tencentscf.FunctionRef) {
	if !s.AcceptUpdateFailed {
		return
	}
	s.SCF.mu.Lock()
	defer s.SCF.mu.Unlock()
	if function, ok := s.SCF.functions[Key(ref)]; ok && function.Info.Status == "UpdateFailed" {
		function.Info.Status = "Active"
	}
}

// EnsureNamespace 经 begin 记数，可注入故障（命名空间配额超限）。
func (s *Scripted) EnsureNamespace(ctx context.Context, region, namespace string) error {
	done, err := s.begin("EnsureNamespace", tencentscf.FunctionRef{Region: region, Namespace: namespace}, true, nil)
	defer done()
	if err != nil {
		return err
	}
	return s.SCF.EnsureNamespace(ctx, region, namespace)
}

// UpdateFunctionEventInvokeConfig 经 begin 记数，可在这里阻塞（Ensure 第 4 步的一部分）。
func (s *Scripted) UpdateFunctionEventInvokeConfig(ctx context.Context, req tencentscf.UpdateFunctionEventInvokeConfigRequest) (*tencentscf.UpdateFunctionEventInvokeConfigResponse, error) {
	done, err := s.begin("UpdateFunctionEventInvokeConfig", req.FunctionRef, true, nil)
	defer done()
	if err != nil {
		return nil, err
	}
	return s.SCF.UpdateFunctionEventInvokeConfig(ctx, req)
}

// GetFunctionEventInvokeConfig 经 begin 记数，可注入读失败。
func (s *Scripted) GetFunctionEventInvokeConfig(ctx context.Context, ref tencentscf.FunctionRef) (*tencentscf.EventInvokeConfig, error) {
	done, err := s.begin("GetFunctionEventInvokeConfig", ref, false, nil)
	defer done()
	if err != nil {
		return nil, err
	}
	return s.SCF.GetFunctionEventInvokeConfig(ctx, ref)
}
